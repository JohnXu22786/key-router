package health

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"key-router/db"
	"key-router/model"
)

// OnKeyRecovered is a callback invoked when a key recovers from disabled/rate-limited status
type OnKeyRecovered func(keyID int64)

// OnKeyFailed is a callback invoked when a probe classifies a key as
// permanently failing (auth_failed / insufficient_quota), so the engine can
// take it out of rotation in memory as well as in the DB.
type OnKeyFailed func(keyID int64, reason string)

// Checker periodically tests keys for availability. Disabled/rate-limited
// keys are probed every interval (recovery happens only on a successful
// probe); ACTIVE keys are probed on a slower cadence so an expired/over-quota
// key with no traffic is still detected and auto-disabled instead of sitting
// in rotation forever.
type Checker struct {
	mu          sync.Mutex
	interval    time.Duration
	stopChan    chan struct{}
	done        chan struct{} // closed when the loop goroutine exits
	running     bool
	disabled    bool // set by Disable(): the checker must never restart
	onRecovered OnKeyRecovered
	onFailed    OnKeyFailed
	// failCount tracks consecutive probe failures per key so a persistently
	// failing key (e.g. a billable Anthropic inference probe) is not probed
	// every interval forever.
	failCount map[int64]int
	// authFailCount tracks consecutive AUTH/quota probe failures (auth_failed
	// / insufficient_quota) separately from failCount: the auto-disable
	// decision requires a REPEAT auth failure. A single transient auth blip
	// that happens to follow unrelated failures (rate_limited,
	// upstream_error) must not take the key out of rotation — that status
	// flip would recover on the next successful probe, which is exactly the
	// auth_failed flash users see.
	authFailCount map[int64]int
	// lastActiveProbe throttles probes of ACTIVE keys: checking every key
	// every interval would burn billable Anthropic probes and hammer the
	// upstreams. Active keys are probed at most once per activeProbeInterval.
	lastActiveProbe map[int64]time.Time
}

// activeProbeInterval is how often ACTIVE keys are health-probed. Expired
// keys are therefore detected within this window even with zero traffic.
const activeProbeInterval = 1 * time.Hour

// NewChecker creates a new health checker
func NewChecker() *Checker {
	return &Checker{
		interval:        120 * time.Second, // default
		failCount:       make(map[int64]int),
		authFailCount:   make(map[int64]int),
		lastActiveProbe: make(map[int64]time.Time),
	}
}

// SetOnKeyRecovered sets a callback for when a key recovers
func (c *Checker) SetOnKeyRecovered(cb OnKeyRecovered) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onRecovered = cb
}

// SetOnKeyFailed sets a callback for when a key is classified as permanently
// failing (auth_failed / insufficient_quota)
func (c *Checker) SetOnKeyFailed(cb OnKeyFailed) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onFailed = cb
}

// Start begins the periodic health check loop
func (c *Checker) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.running || c.disabled {
		return
	}

	// Read interval from settings
	intervalStr := db.GetSetting(model.SettingHealthCheck)
	if intervalStr != "" {
		var sec int
		if _, err := fmt.Sscanf(intervalStr, "%d", &sec); err == nil && sec > 0 {
			c.interval = time.Duration(sec) * time.Second
		}
	}

	c.stopChan = make(chan struct{})
	c.done = make(chan struct{})
	c.running = true
	// Capture the generation's channels AND interval in the closure so a
	// concurrent Stop/Start pair can never make the loop observe another
	// generation's (open) channels or a racing interval write.
	stop, done := c.stopChan, c.done
	interval := c.interval
	go func() {
		defer close(done)
		c.loop(stop, interval)
	}()
	log.Printf("[health] checker started (interval: %v)", c.interval)
}

// Stop stops the health check loop and waits for it to exit.
// Uses a per-generation done channel (not a WaitGroup) so that concurrent
// Stop/Start pairs — e.g. the async Restart from UpdateSettings — can never
// trigger "WaitGroup misuse: Add called concurrently with Wait".
func (c *Checker) Stop() {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	close(c.stopChan)
	c.running = false
	done := c.done
	c.mu.Unlock()

	<-done
}

// Disable permanently prevents the checker from (re)starting — used at
// shutdown so an async Restart from UpdateSettings can't relaunch a loop
// after Stop() has run.
func (c *Checker) Disable() {
	c.mu.Lock()
	c.disabled = true
	c.mu.Unlock()
	c.Stop()
}

// Restart re-reads interval and restarts
func (c *Checker) Restart() {
	c.Stop()
	c.Start()
}

func (c *Checker) loop(stop chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.checkAll()
		case <-stop:
			return
		}
	}
}

func (c *Checker) checkAll() {
	// Include ACTIVE keys: an expired/over-quota key with no traffic must
	// still be detected and auto-disabled. The throttle in shouldProbeKey
	// keeps active-key probing at one check per activeProbeInterval.
	var keys []model.Key
	if err := db.GetDB().Find(&keys).Error; err != nil {
		log.Printf("[health] failed to query keys: %v", err)
		return
	}

	// Probe concurrently (bounded) so one hung upstream (10s timeout) doesn't
	// stall recovery of every other key; Stop() waits for the whole pass.
	const maxConcurrent = 5
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for i := range keys {
		key := &keys[i]
		if !c.shouldProbeKey(key) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			c.checkKey(key)
		}()
	}
	wg.Wait()
}

// budgetExhausted reports whether the key has consumed its lifetime spend
// budget. The cap is an administrative limit, not an upstream health
// condition: once exhausted, the key must never be probed (each probe is a
// billable chat completion) or revived by the checker — only an admin reset
// (POST /api/keys/:id/reset-spend) re-admits it. Guarding on the budget
// itself (not on the disabled_reason) also covers keys whose reason was
// overwritten by a concurrent failure path.
func budgetExhausted(key *model.Key) bool {
	return key.TotalSpendLimit > 0 && key.TotalSpent >= key.TotalSpendLimit
}

// shouldProbeKey decides whether a key is due for a probe this pass.
//   - Keys whose lifetime spend budget is exhausted are admin-capped — never
//     probed.
//   - Disabled keys with an empty reason are admin-disabled — never probed.
//   - Disabled (system) / rate-limited keys: every pass, EXCEPT a
//     rate-limited key whose cooldown is still running — probing it can't
//     change its state (recovery is blocked during the cooldown) and each
//     probe is a billable chat completion.
//   - Active keys: at most once per activeProbeInterval (throttled).
func (c *Checker) shouldProbeKey(key *model.Key) bool {
	if budgetExhausted(key) {
		return false // lifetime budget cap reached — never probe nor recover
	}
	if key.Status == model.KeyStatusDisabled && key.DisabledReason == "" {
		return false // deliberately disabled by an admin
	}
	if key.Status == model.KeyStatusRateLimited &&
		key.RateLimitedUntil != nil && time.Now().Before(*key.RateLimitedUntil) {
		return false // cooldown still running — probing can't recover it
	}
	if key.Status != model.KeyStatusActive {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	last, ok := c.lastActiveProbe[key.ID]
	if !ok || time.Since(last) >= activeProbeInterval {
		return true
	}
	return false
}

// ResetFailCount clears the consecutive-failure latches for a key (e.g.
// after an admin edit or a status change) so probing resumes
func (c *Checker) ResetFailCount(keyID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.failCount, keyID)
	delete(c.authFailCount, keyID)
}

// ProbeResult classifies the outcome of a health probe. The classification
// drives both the key's disabled_reason (user-visible feedback: 欠费, auth
// failure, ...) and the recovery decision — recovery happens ONLY when a
// probe does not classify the key as failing, never on a timer. "Not
// failing" includes a 403 (model access denied — the key itself
// authenticated), which is exactly the proof a disabled auth_failed key
// needs to re-enter rotation.
type ProbeResult struct {
	Alive  bool
	Reason string // "" when alive; otherwise one of:
	// "auth_failed", "insufficient_quota", "rate_limited", "upstream_error"
}

// checkKey probes a single key.
//   - Active key: probe validates it is still usable (quota/auth); on
//     auth_failed/insufficient_quota the key is auto-disabled. A probe that
//     does not classify the key as failing leaves it active.
//   - Disabled/rate-limited key: recovery happens ONLY when a probe does not
//     classify the key as failing (never on a timer).
func (c *Checker) checkKey(key *model.Key) {
	// Refresh the pass snapshot before probing: the key was loaded seconds
	// ago, and since then the relay may have exhausted its lifetime budget
	// or an admin may have disabled it — a probe is a billable chat
	// completion, so the fresh state decides whether one is still allowed.
	var fresh model.Key
	if err := db.GetDB().First(&fresh, key.ID).Error; err != nil {
		return
	}
	key = &fresh

	// Disabled keys are only auto-recovered when the disabled_reason was set
	// by the system (auth_failed / insufficient_quota / ...). A key disabled
	// deliberately by an admin has an empty reason (UpdateKey clears it) and
	// stays out of rotation.
	if key.Status == model.KeyStatusDisabled && key.DisabledReason == "" {
		return
	}

	// A key whose lifetime spend budget is exhausted must stay out of
	// rotation: the cap is an administrative limit, not an upstream health
	// condition, so a successful probe must never revive it (that would let
	// traffic keep overspending the budget on every health pass). Only an
	// admin reset (POST /api/keys/:id/reset-spend) re-admits the key.
	if budgetExhausted(key) {
		return
	}

	// Back off from keys that keep failing: after 6 consecutive failed probes
	// (each probe is now a billable chat completion), stop probing until the
	// key is edited (ResetFailCount) — otherwise a broken key generates
	// charges forever.
	c.mu.Lock()
	if c.failCount[key.ID] >= 6 {
		c.mu.Unlock()
		return
	}
	// Mark this probe as the key's active-probe time (covers the active-key
	// throttle; harmless for non-active keys).
	c.lastActiveProbe[key.ID] = time.Now()
	c.mu.Unlock()

	// Get provider
	var provider model.Provider
	if err := db.GetDB().First(&provider, key.ProviderID).Error; err != nil {
		return
	}

	// Probe with the smallest real request that proves the key works (a
	// max_tokens=1 chat completion / minimal message). Rate-limit cooldowns
	// are NOT waited out: the probe decides, not the timer.
	result := c.testKey(key.KeyValue, &provider)

	if !result.Alive {
		c.recordFailure(key, result.Reason)
		return
	}

	// Reset the failure counters on success
	c.mu.Lock()
	delete(c.failCount, key.ID)
	delete(c.authFailCount, key.ID)
	c.mu.Unlock()

	// Active keys that probe fine stay active — but clear any failure reason
	// a transient earlier probe failure persisted (it is invisible while
	// the key is active and would otherwise linger in the DB forever).
	if key.Status == model.KeyStatusActive {
		if key.DisabledReason != "" {
			db.GetDB().Model(&model.Key{}).
				Where("id = ? AND status = ? AND disabled_reason <> ''", key.ID, model.KeyStatusActive).
				Updates(map[string]interface{}{"disabled_reason": ""})
		}
		return
	}

	// Re-check the status from the DB before marking active: the relay may
	// have marked this key rate-limited again (fresh cooldown), exhausted
	// its budget, or an admin may have disabled it while our probe was in
	// flight — wiping that state would immediately re-admit a hot/capped
	// key.
	var current model.Key
	if err := db.GetDB().First(&current, key.ID).Error; err != nil {
		return
	}
	if budgetExhausted(&current) {
		return // budget exhausted while the probe was in flight
	}
	if current.Status == model.KeyStatusDisabled && current.DisabledReason == "" {
		return // deliberately disabled while probing
	}
	// The relay may have re-cooled this key while the probe was in flight,
	// or the upstream's Retry-After window is still running. The probe
	// proves the key authenticates, but the cooldown is the upstream's own
	// instruction to wait: recovering early re-admits a hot key, the next
	// request re-triggers the 429, and the status ping-pongs between
	// rate_limited and active.
	if current.Status == model.KeyStatusRateLimited &&
		current.RateLimitedUntil != nil && time.Now().Before(*current.RateLimitedUntil) {
		return
	}

	log.Printf("[health] key %d (%s...) recovered, marking active",
		key.ID, truncateKey(key.KeyValue))

	// Guarded update: don't clobber a fresher state written while our probe
	// was in flight. Deliberately-disabled keys (empty reason) and
	// budget-exhausted keys are excluded (the cap is an admin limit, never
	// an upstream health condition); system-disabled and rate-limited keys
	// are the ones we recover — but a rate_limited_until still in the future
	// (the relay re-cooled this key between the re-check above and this
	// write) must NOT be wiped.
	res := db.GetDB().Model(&model.Key{}).
		Where("id = ? AND (status <> ? OR disabled_reason <> ?) AND (total_spend_limit IS NULL OR total_spend_limit = 0 OR total_spent < total_spend_limit) AND (rate_limited_until IS NULL OR rate_limited_until <= ?)",
			key.ID, model.KeyStatusDisabled, "", time.Now()).
		Updates(map[string]interface{}{
			"status":             model.KeyStatusActive,
			"rate_limited_until": nil,
			"disabled_reason":    "",
		})
	if res.Error != nil {
		log.Printf("[health] failed to update key %d in DB: %v", key.ID, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		return // state changed while probing — don't touch memory
	}

	// Notify engine to update in-memory cache
	if c.onRecovered != nil {
		c.onRecovered(key.ID)
	}
}

// recordFailure classifies a failed probe into a user-visible reason and
// persists it as the key's disabled_reason (the UI shows 欠费/鉴权失败/...).
// Auth/quota failures take the key out of rotation entirely (via the
// onFailed callback so the engine's in-memory cache stays consistent);
// rate limits and upstream errors leave the status to the relay, which
// already handles failover.
func (c *Checker) recordFailure(key *model.Key, reason string) {
	if reason == "" {
		reason = "upstream_error"
	}
	// Persist the reason so the UI can show WHY the key is down. Skip the
	// write when the reason is unchanged (avoids pointless DB churn on every
	// interval for a key that stays broken) — and never overwrite a budget
	// cap already recorded in the DB: the relay may have exhausted the
	// key's budget while this probe was in flight, and that state must
	// survive (otherwise the key would be probed again next pass and the
	// budget bypassed).
	if key.DisabledReason != reason {
		res := db.GetDB().Model(&model.Key{}).
			Where("id = ? AND (total_spend_limit IS NULL OR total_spend_limit = 0 OR total_spent < total_spend_limit)", key.ID).
			Updates(map[string]interface{}{"disabled_reason": reason})
		if res.Error != nil {
			log.Printf("[health] failed to record failure reason for key %d: %v", key.ID, res.Error)
		} else if res.RowsAffected > 0 {
			key.DisabledReason = reason // keep the in-memory copy in sync
		}
	}
	// Count the consecutive failures first so persistently broken keys back
	// off AND the disable decision below can require a repeat: the counts
	// are read before this probe is added. Auth/quota failures are counted
	// separately (authFailCount) so the disable gate only trips on repeated
	// auth failures, not on an auth blip after unrelated probe failures.
	isAuth := reason == "auth_failed" || reason == "insufficient_quota"
	c.mu.Lock()
	c.failCount[key.ID]++
	if c.failCount[key.ID] > 6 {
		c.failCount[key.ID] = 6
	}
	consecutiveAuthFailures := c.authFailCount[key.ID]
	if isAuth {
		c.authFailCount[key.ID]++
	} else {
		// A non-auth failure breaks the auth-failure streak: the disable
		// gate below must only trip on REPEATED auth failures, not on a
		// single stale auth blip that happened earlier.
		c.authFailCount[key.ID] = 0
	}
	c.mu.Unlock()

	// Auth/quota failures permanently take the key out of rotation (no point
	// routing traffic to it) — but only after the key has failed probes with
	// AUTH failures CONSECUTIVELY. A single transient probe failure (upstream
	// hiccup) must not yank a healthy key: the status would flip to disabled
	// and back to active on the very next successful probe, which is exactly
	// the auth_failed/rate_limited flash users see. Rate limits and upstream
	// errors stay as-is so the relay's own retry/failover handles them and
	// this probe keeps checking.
	if isAuth &&
		key.Status != model.KeyStatusDisabled && consecutiveAuthFailures >= 1 {
		if c.onFailed != nil {
			c.onFailed(key.ID, reason)
		} else {
			// No callback wired (e.g. tests): fall back to a direct DB update.
			res := db.GetDB().Model(&model.Key{}).
				Where("id = ? AND status <> ?", key.ID, model.KeyStatusDisabled).
				Updates(map[string]interface{}{
					"status":          model.KeyStatusDisabled,
					"disabled_reason": reason,
				})
			if res.Error != nil {
				log.Printf("[health] failed to disable key %d: %v", key.ID, res.Error)
			}
		}
	}
}

func (c *Checker) testKey(keyValue string, provider *model.Provider) ProbeResult {
	baseURL := strings.TrimRight(provider.BaseURL, "/")

	if provider.Type == "anthropic" {
		return c.testAnthropic(keyValue, provider)
	}

	// OpenAI-format providers: probe with the smallest REAL request (a
	// max_tokens=1 chat completion). GET /v1/models is not a valid probe:
	// many OpenAI-compatible gateways answer it with 200 even for an INVALID
	// key, so a key the relay disabled for auth_failed would pass the models
	// probe, get recovered to "active", and be disabled again by the very
	// next request — the auth_failed → active → auth_failed ping-pong users
	// see. A chat completion authenticates exactly like real traffic, so
	// recovery is gated on the key being genuinely usable. (Relay builds
	// upstream URLs as baseURL + "/v1/...", so the probe follows the same
	// convention.)
	testURL := baseURL + "/v1/chat/completions"
	body := `{"model":"gpt-4o-mini","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`

	req, err := http.NewRequest("POST", testURL, strings.NewReader(body))
	if err != nil {
		return ProbeResult{Alive: false, Reason: "upstream_error"}
	}

	req.Header.Set("Authorization", "Bearer "+keyValue)
	req.Header.Set("Content-Type", "application/json")

	// Apply the same extra headers the relay uses, so gateways requiring
	// them (e.g. Organization) answer the probe like real traffic
	applyExtraHeaders(req, provider)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{Alive: false, Reason: "upstream_error"}
	}
	defer resp.Body.Close()

	return classifyOpenAIProbe(resp)
}

func (c *Checker) testAnthropic(keyValue string, provider *model.Provider) ProbeResult {
	baseURL := strings.TrimRight(provider.BaseURL, "/")
	testURL := baseURL + "/v1/messages"

	body := `{"model":"claude-haiku-4-5","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`

	req, err := http.NewRequest("POST", testURL, strings.NewReader(body))
	if err != nil {
		return ProbeResult{Alive: false, Reason: "upstream_error"}
	}

	req.Header.Set("x-api-key", keyValue)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	applyExtraHeaders(req, provider)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{Alive: false, Reason: "upstream_error"}
	}
	defer resp.Body.Close()

	return classifyAnthropicProbe(resp)
}

// classifyOpenAIProbe classifies an OpenAI-format probe response.
// Only 401 (bad key) and quota signals (402, 429 + quota error code) mean
// the key is unusable. A 400/404 ("model not found", endpoint not
// supported) AND a 403 (model/endpoint access denied — the KEY itself
// authenticated, e.g. a key not entitled to the probe's model) still prove
// the key works, mirroring the relay's own 403 handling (a 30s cooldown,
// never a disable — 403 is often model access, not key invalidity). A 429
// whose body carries a quota error code is classified as insufficient_quota
// (disabled), not a transient rate limit — an over-quota key never recovers
// on its own.
func classifyOpenAIProbe(resp *http.Response) ProbeResult {
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ProbeResult{Alive: false, Reason: "auth_failed"}
	case resp.StatusCode == http.StatusPaymentRequired:
		return ProbeResult{Alive: false, Reason: "insufficient_quota"}
	case resp.StatusCode == http.StatusTooManyRequests:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		if quotaErrorInBody(body) {
			return ProbeResult{Alive: false, Reason: "insufficient_quota"}
		}
		return ProbeResult{Alive: false, Reason: "rate_limited"}
	case resp.StatusCode >= 500:
		return ProbeResult{Alive: false, Reason: "upstream_error"}
	default:
		return ProbeResult{Alive: true}
	}
}

// quotaErrorInBody reports whether an error body carries a quota-exhaustion
// code (OpenAI 429 + insufficient_quota / billing_hard_limit_reached, or a
// gateway using error.type).
func quotaErrorInBody(body []byte) bool {
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Error) == 0 {
		return false
	}
	var inner struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	if json.Unmarshal(payload.Error, &inner) != nil {
		return false
	}
	switch inner.Code {
	case "insufficient_quota", "billing_hard_limit_reached", "billing_not_active", "card_declined", "quota_exceeded":
		return true
	}
	switch inner.Type {
	case "insufficient_quota", "billing_error", "billing_not_active":
		return true
	}
	return false
}

// classifyAnthropicProbe classifies an Anthropic probe response.
// A 400 ("model not found") or 403 (permission_error for model access — the
// KEY itself authenticated) still proves the key is usable; only 401 (bad
// key), 402 (quota exhausted), 429 (rate limit) and upstream 5xx mean the
// key is not usable.
func classifyAnthropicProbe(resp *http.Response) ProbeResult {
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ProbeResult{Alive: false, Reason: "auth_failed"}
	case resp.StatusCode == http.StatusPaymentRequired:
		return ProbeResult{Alive: false, Reason: "insufficient_quota"}
	case resp.StatusCode == http.StatusTooManyRequests:
		return ProbeResult{Alive: false, Reason: "rate_limited"}
	case resp.StatusCode >= 500:
		return ProbeResult{Alive: false, Reason: "upstream_error"}
	default:
		return ProbeResult{Alive: true}
	}
}

// applyExtraHeaders sets the provider's configured extra headers on a request
// (same as relay.ForwardRequest). Auth headers are never overwritten.
func applyExtraHeaders(req *http.Request, provider *model.Provider) {
	if provider.ExtraHeaders == "" {
		return
	}
	var extra map[string]string
	if err := json.Unmarshal([]byte(provider.ExtraHeaders), &extra); err != nil {
		return
	}
	for k, v := range extra {
		// Header names are case-insensitive
		if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "X-Api-Key") {
			continue
		}
		req.Header.Set(k, v)
	}
}

func truncateKey(key string) string {
	if len(key) <= 8 {
		return key
	}
	return key[:8] + "..."
}

// CheckResult holds the result of a health check for a specific key
type CheckResult struct {
	KeyID  int64  `json:"key_id"`
	Status string `json:"status"`
	Alive  bool   `json:"alive"`
	Error  string `json:"error,omitempty"`
}

// GetKeyStatuses returns current status of all keys
func GetKeyStatuses() ([]CheckResult, error) {
	var keys []model.Key
	if err := db.GetDB().Find(&keys).Error; err != nil {
		log.Printf("[health] GetKeyStatuses error: %v", err)
		return nil, err
	}

	var results []CheckResult
	for _, k := range keys {
		results = append(results, CheckResult{
			KeyID:  k.ID,
			Status: k.Status,
			Alive:  k.Status == model.KeyStatusActive,
		})
	}
	return results, nil
}
