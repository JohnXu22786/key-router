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

// OnKeyResult is invoked after every health probe of a key: ok=true on a
// successful probe, ok=false with the classified reason on a failed one.
// main.go wires it to the engine's RecordResult — the single key status
// state machine that BOTH real traffic (relay) and probes feed, so a probe
// and a request count toward the same 2-consecutive-successes /
// 2-consecutive-same-failures thresholds.
type OnKeyResult func(keyID int64, ok bool, reason string)

// Checker periodically tests keys for availability. Disabled/rate-limited
// keys are probed every interval (recovery happens only via the engine
// state machine: 2 consecutive successful observations); ACTIVE keys are
// probed on a slower cadence so an expired/over-quota key with no traffic
// is still detected and auto-disabled instead of sitting in rotation
// forever.
type Checker struct {
	mu          sync.Mutex
	interval    time.Duration
	stopChan    chan struct{}
	done        chan struct{} // closed when the loop goroutine exits
	running     bool
	disabled    bool // set by Disable(): the checker must never restart
	onResult    OnKeyResult
	// failCount tracks consecutive probe failures per key so a persistently
	// failing key (e.g. a billable Anthropic inference probe) is not probed
	// every interval forever.
	failCount map[int64]int
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
		lastActiveProbe: make(map[int64]time.Time),
	}
}

// SetOnKeyResult sets the callback invoked after every probe of a key
// (main.go wires it to selector.Engine.RecordResult).
func (c *Checker) SetOnKeyResult(cb OnKeyResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onResult = cb
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

// ResetFailCount clears the consecutive-failure latch for a key (e.g.
// after an admin edit or a status change) so probing resumes
func (c *Checker) ResetFailCount(keyID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.failCount, keyID)
}

// ProbeResult classifies the outcome of a health probe. The classification
// feeds the engine's key status state machine (via OnKeyResult), which
// drives both the key's disabled_reason (user-visible feedback) and the
// status flips: recovery happens ONLY after 2 consecutive non-failing
// observations, never on a timer. "Not failing" includes a 403 (model
// access denied — the key itself authenticated), which is exactly the proof
// a disabled auth_failed key needs to re-enter rotation.
type ProbeResult struct {
	Alive  bool
	Reason string // "" when alive; otherwise a display reason:
	// semantic for classified problems ("auth_failed", "insufficient_quota",
	// "network_error"), the bare HTTP status ("http_429", "http_500", ...)
	// for unambiguous upstream responses.
}

// checkKey probes a single key and reports the outcome to the engine's key
// status state machine (via the onResult callback):
//
//   - Probe failure: reported with the classified reason; the engine marks
//     the key once, cools it for failover, and disables it only after 2
//     consecutive failures with the same permanent reason.
//   - Probe success: reported as ok; the engine returns the key to active
//     only after 2 consecutive successes.
//
// All status writes live in the engine — this function never touches the
// DB's status columns itself, so the DB and the engine's in-memory cache
// (and the SSE hot-reload events) can never diverge.
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

	// Disabled keys are only ever auto-recovered by the engine's state
	// machine when the disabled_reason was set by the system
	// (auth_failed / insufficient_quota / ...). A key disabled deliberately
	// by an admin has an empty reason (UpdateKey clears it) and stays out
	// of rotation — it must not even be probed (each probe is billable).
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
		c.mu.Lock()
		if c.failCount[key.ID] < 6 {
			c.failCount[key.ID]++
		}
		c.mu.Unlock()
		if c.onResult != nil {
			c.onResult(key.ID, false, result.Reason)
		}
		return
	}

	// Successful probe: reset the probe backoff latch and report the
	// success. The engine state machine decides whether the key now has 2
	// consecutive successes and may return to active.
	c.mu.Lock()
	delete(c.failCount, key.ID)
	c.mu.Unlock()
	if c.onResult != nil {
		c.onResult(key.ID, true, "")
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
		return ProbeResult{Alive: false, Reason: model.ReasonNetworkError}
	}

	req.Header.Set("Authorization", "Bearer "+keyValue)
	req.Header.Set("Content-Type", "application/json")

	// Apply the same extra headers the relay uses, so gateways requiring
	// them (e.g. Organization) answer the probe like real traffic
	applyExtraHeaders(req, provider)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{Alive: false, Reason: model.ReasonNetworkError}
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
		return ProbeResult{Alive: false, Reason: model.ReasonNetworkError}
	}

	req.Header.Set("x-api-key", keyValue)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	applyExtraHeaders(req, provider)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{Alive: false, Reason: model.ReasonNetworkError}
	}
	defer resp.Body.Close()

	return classifyAnthropicProbe(resp)
}

// classifyOpenAIProbe classifies an OpenAI-format probe response.
// Only a genuine key-invalidity signal (401 naming the KEY, 402, or a 429
// carrying a billing-exhaustion code) means the key is unusable. A 400/404
// ("model not found", endpoint not supported) AND a 403 (model/endpoint
// access denied — the KEY itself authenticated) still prove the key works,
// mirroring the relay's own 403 handling (a 30s cooldown, never a disable —
// 403 is often model access, not key invalidity). A 401 whose body names a
// MODEL problem (unknown model, key not entitled to the probe's hardcoded
// model) also proves the key authenticated: the probe's model choice is
// wrong, not the key — disabling on it takes every usable key out of
// rotation. A 429 whose body carries a billing-exhaustion code is classified
// as insufficient_quota (disabled), not a transient rate limit — an
// over-quota key never recovers on its own. "quota_exceeded" is NOT a
// billing code: on many gateways (e.g. OpenRouter) it is the RATE-LIMIT
// code ("you've made too many requests to this model"), so it cools the key
// down instead of disabling it.
//
// Failed probes carry a display reason: semantic for classified problems
// (auth_failed / insufficient_quota), the bare HTTP status (http_429,
// http_5xx, ...) for unambiguous upstream responses — the UI renders those
// as "HTTP 429" etc. instead of guessing a category.
func classifyOpenAIProbe(resp *http.Response) ProbeResult {
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		if ModelProblemInBody(body) {
			// The gateway named a model/access problem: the key itself
			// authenticated, only the probe's hardcoded model is unavailable
			// to it. Alive — never disable on the probe's model choice.
			return ProbeResult{Alive: true}
		}
		// Bare 401, or a body that blames the key: fail-closed.
		return ProbeResult{Alive: false, Reason: model.ReasonAuthFailed}
	case resp.StatusCode == http.StatusPaymentRequired:
		return ProbeResult{Alive: false, Reason: model.ReasonInsufficientQuota}
	case resp.StatusCode == http.StatusTooManyRequests:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		if quotaErrorInBody(body) {
			return ProbeResult{Alive: false, Reason: model.ReasonInsufficientQuota}
		}
		return ProbeResult{Alive: false, Reason: model.HTTPStatusReason(resp.StatusCode)}
	case resp.StatusCode >= 500:
		return ProbeResult{Alive: false, Reason: model.HTTPStatusReason(resp.StatusCode)}
	default:
		return ProbeResult{Alive: true}
	}
}

// ModelProblemInBody reports whether a 401 error body blames the MODEL
// (unknown model, no access to the model) rather than the key. The health
// probe sends a hardcoded model that many gateways don't have, and such
// gateways often answer with 401 — reading the body is the only way to tell
// "the key is invalid" from "the key is fine, the probe's model is not".
// Explicit key-invalidity signals always win (fail-closed on ambiguity).
// Exported so the relay (handler/chat.go) applies the SAME classification to
// real traffic — otherwise a model-problem 401 disables the key on the
// relay path while the next probe pass recovers it (disable → active →
// disable flap).
func ModelProblemInBody(body []byte) bool {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	tokens := normalizeTokens(payload.Error.Message + " " + payload.Error.Code + " " + payload.Error.Type)

	// The gateway explicitly blamed the key — the key is invalid even if a
	// model is mentioned too. Key-scoped denial phrasings ("key does not
	// exist", "key disabled", ...) must NOT be outvoted by a model mention
	// in the same body: a dead key stays dead.
	for _, sig := range keyInvalidSignals {
		if seqMatch(tokens, sig) {
			return false
		}
	}

	// Otherwise the body must point at a MODEL (or Azure-style deployment)
	// and deny it in some way.
	for _, sig := range modelProblemSignals {
		if seqMatch(tokens, sig) {
			return true
		}
	}
	return false
}

// normalizeTokens lowercases a body and splits it into word tokens, treating
// any non-alphanumeric character as a separator and splitting camelCase
// boundaries. This collapses the punctuation variance gateways use —
// "model_not_found", "ModelNotFound", "model not found",
// "model gpt-4o-mini is not found" and "model: not found" all become
// comparable token sequences.
func normalizeTokens(s string) []string {
	var words []string
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = cur[:0]
		}
	}
	prevLower := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			cur = append(cur, c)
			prevLower = true
		case c >= 'A' && c <= 'Z':
			// camelCase boundary: "ModelNotFound" → [model, not, found]
			if prevLower && len(cur) > 0 {
				flush()
			}
			cur = append(cur, c+('a'-'A'))
			prevLower = false
		case c >= '0' && c <= '9':
			cur = append(cur, c)
			prevLower = false
		default:
			flush()
			prevLower = false
		}
	}
	flush()
	return words
}

// seqMatch reports whether the signal's words appear in tokens IN ORDER,
// ignoring up to maxGapUnmatched other tokens between consecutive signal
// words. A signal word must still appear as a whole token, so "invalid key"
// never matches inside "invalidate keys" — but "api key sk-123 is invalid"
// DOES match [key invalid] (the echoed key ID sits between the words).
const maxGapUnmatched = 8

func seqMatch(tokens []string, sig []string) bool {
	if len(sig) == 0 {
		return false
	}
	// For each occurrence of the first word, try to walk the rest of the
	// signal allowing up to maxGapUnmatched foreign tokens between words.
	for i := 0; i < len(tokens); i++ {
		if tokens[i] != sig[0] {
			continue
		}
		pos := i + 1
		matched := true
		for _, w := range sig[1:] {
			found := -1
			for j := pos; j < len(tokens) && j-pos <= maxGapUnmatched; j++ {
				if tokens[j] == w {
					found = j
					break
				}
			}
			if found < 0 {
				matched = false
				break
			}
			pos = found + 1
		}
		if matched {
			return true
		}
	}
	return false
}

// keyInvalidSignals are token sequences that prove the KEY is invalid,
// regardless of any model mention in the same body (fail-closed).
var keyInvalidSignals = [][]string{
	{"invalid", "key"},
	{"incorrect", "key"},
	{"key", "invalid"},
	{"key", "not", "found"},
	{"key", "does", "not", "exist"},
	{"key", "not", "exist"},
	{"no", "such", "key"},
	{"key", "disabled"},
	{"key", "inactive"},
	{"key", "unauthorized"},
	{"key", "revoked"},
	{"key", "expired"},
	{"authentication"},
	{"unauthorized", "key"},
	{"bad", "credentials"},
	{"invalid", "credentials"},
	{"credentials", "invalid"},
}

// modelProblemSignals are token sequences that prove the key authenticated
// and only the probe's MODEL is unavailable/denied. Kept fail-open ONLY for
// phrasings that name the model and its denial — the key signal list above
// is checked first and wins on any overlap.
var modelProblemSignals = [][]string{
	{"model", "not", "found"},
	{"model", "not", "exist"},
	{"model", "does", "not", "exist"},
	{"model", "not", "allowed"},
	{"model", "not", "available"},
	{"model", "unavailable"},
	{"model", "not", "supported"},
	{"model", "not", "entitled"},
	{"model", "no", "access"},
	{"model", "access", "denied"},
	{"model", "denied"},
	{"model", "disabled"},
	{"model", "inactive"},
	{"deployment", "not", "found"},
	{"deployment", "not", "exist"},
	{"deployment", "does", "not", "exist"},
	{"deployment", "not", "available"},
	{"deployment", "not", "allowed"},
	{"deployment", "disabled"},
	{"deployment", "inactive"},
}

// quotaErrorInBody reports whether an error body carries a billing-exhaustion
// code (OpenAI 429 + insufficient_quota / billing_hard_limit_reached, or a
// gateway using error.type). "quota_exceeded" is deliberately NOT matched:
// gateways use it for request/model rate-limit throttles as well as billing
// exhaustion, and the cost of a wrong disable (a healthy key taken out of
// rotation) outweighs the cost of a wrong cool-down (an over-quota key keeps
// failing over until an admin notices) — so it must cool the key down, not
// disable it.
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
	case "insufficient_quota", "billing_hard_limit_reached", "billing_not_active", "card_declined":
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
// key is not usable. Reasons follow the same convention as the OpenAI
// classifier: semantic for classified problems, bare HTTP statuses
// (http_429 / http_5xx) for unambiguous upstream responses.
func classifyAnthropicProbe(resp *http.Response) ProbeResult {
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ProbeResult{Alive: false, Reason: model.ReasonAuthFailed}
	case resp.StatusCode == http.StatusPaymentRequired:
		return ProbeResult{Alive: false, Reason: model.ReasonInsufficientQuota}
	case resp.StatusCode == http.StatusTooManyRequests:
		return ProbeResult{Alive: false, Reason: model.HTTPStatusReason(resp.StatusCode)}
	case resp.StatusCode >= 500:
		return ProbeResult{Alive: false, Reason: model.HTTPStatusReason(resp.StatusCode)}
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
