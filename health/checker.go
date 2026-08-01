package health

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"local-router/db"
	"local-router/model"
)

// OnKeyRecovered is a callback invoked when a key recovers from disabled/rate-limited status
type OnKeyRecovered func(keyID int64)

// Checker periodically tests disabled/rate-limited keys for availability
type Checker struct {
	mu          sync.Mutex
	interval    time.Duration
	stopChan    chan struct{}
	done        chan struct{} // closed when the loop goroutine exits
	running     bool
	disabled    bool // set by Disable(): the checker must never restart
	onRecovered OnKeyRecovered
	// failCount tracks consecutive probe failures per key so a persistently
	// failing key (e.g. a billable Anthropic inference probe) is not probed
	// every interval forever.
	failCount map[int64]int
}

// NewChecker creates a new health checker
func NewChecker() *Checker {
	return &Checker{
		interval:  120 * time.Second, // default
		failCount: make(map[int64]int),
	}
}

// SetOnKeyRecovered sets a callback for when a key recovers
func (c *Checker) SetOnKeyRecovered(cb OnKeyRecovered) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onRecovered = cb
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
	var keys []model.Key
	if err := db.GetDB().Where("status IN ?", []string{
		model.KeyStatusDisabled,
		model.KeyStatusRateLimited,
	}).Find(&keys).Error; err != nil {
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

// ResetFailCount clears the consecutive-failure latch for a key (e.g. after
// an admin edit or a status change) so probing resumes
func (c *Checker) ResetFailCount(keyID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.failCount, keyID)
}

// checkKey probes a single disabled/rate-limited key
func (c *Checker) checkKey(key *model.Key) {
	// Rate-limited keys: only check if cooldown has expired
	if key.Status == model.KeyStatusRateLimited {
		if key.RateLimitedUntil != nil && time.Now().Before(*key.RateLimitedUntil) {
			return // Still in cooldown
		}
	}

	// Disabled keys are only auto-recovered when the failure was an upstream
	// auth error; keys disabled for any other reason (e.g. deliberately by an
	// admin via the API) stay out of rotation.
	if key.Status == model.KeyStatusDisabled && key.DisabledReason != "auth_failed" {
		return
	}

	// Back off from keys that keep failing: after 6 consecutive failed probes
	// (e.g. a billable Anthropic inference probe), stop probing until the key
	// changes status or is edited (ResetFailCount) — otherwise a broken key
	// generates charges forever.
	c.mu.Lock()
	if c.failCount[key.ID] >= 6 {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	// Get provider

	// Get provider
	var provider model.Provider
	if err := db.GetDB().First(&provider, key.ProviderID).Error; err != nil {
		return
	}

	// Test by listing models (a lightweight request)
	alive := c.testKey(key.KeyValue, &provider)

	if !alive {
		// Count the consecutive failure so persistently broken keys back off
		c.mu.Lock()
		c.failCount[key.ID]++
		if c.failCount[key.ID] > 6 {
			c.failCount[key.ID] = 6
		}
		c.mu.Unlock()
		return
	}

	if alive {
		// Reset the failure counter on success
		c.mu.Lock()
		delete(c.failCount, key.ID)
		c.mu.Unlock()

		// Re-check the cooldown/status from the DB before marking active: the
		// relay may have marked this key rate-limited again (fresh cooldown)
		// or an admin may have disabled it while our probe was in flight —
		// wiping that state would immediately re-admit a hot/disabled key.
		var current model.Key
		if err := db.GetDB().First(&current, key.ID).Error; err != nil {
			return
		}
		if current.Status == model.KeyStatusRateLimited &&
			current.RateLimitedUntil != nil && time.Now().Before(*current.RateLimitedUntil) {
			return // a fresher cooldown exists
		}
		if current.Status == model.KeyStatusDisabled && current.DisabledReason != "auth_failed" {
			return // deliberately disabled while probing
		}

		log.Printf("[health] key %d (%s...) recovered, marking active",
			key.ID, truncateKey(key.KeyValue))

		// Guarded update: don't clobber a fresher state written while our
		// probe was in flight (a new cooldown or a deliberate admin disable).
		// Deliberately-disabled keys (reason != auth_failed) are excluded;
		// auth_failed-disabled keys are the ones we're recovering.
		res := db.GetDB().Model(&model.Key{}).
			Where("id = ? AND (status <> ? OR disabled_reason = ?) AND (rate_limited_until IS NULL OR rate_limited_until <= ?)",
				key.ID, model.KeyStatusDisabled, "auth_failed", time.Now()).
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
}

func (c *Checker) testKey(keyValue string, provider *model.Provider) bool {
	baseURL := strings.TrimRight(provider.BaseURL, "/")
	// Relay builds upstream URLs as baseURL + "/v1/...", so the health probe
	// must use the same convention (BaseURL without the /v1 suffix).
	testURL := baseURL + "/v1/models"

	req, err := http.NewRequest("GET", testURL, nil)
	if err != nil {
		return false
	}

	req.Header.Set("Authorization", "Bearer "+keyValue)
	req.Header.Set("Content-Type", "application/json")

	if provider.Type == "anthropic" {
		req.Header.Set("x-api-key", keyValue)
		req.Header.Set("anthropic-version", "2023-06-01")
		// Anthropic doesn't have /models, use a minimal message instead
		return c.testAnthropic(keyValue, provider)
	}

	// Apply the same extra headers the relay uses, so gateways requiring
	// them (e.g. Organization) answer the probe like real traffic
	applyExtraHeaders(req, provider)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// Auth-based acceptance (OpenAI probe): gateways that don't implement
	// GET /v1/models return 404 for ANY key — treating only auth/rate-limit
	// failures and upstream 5xx as "not alive" lets valid keys recover there.
	return testOpenAIProbe(resp)
}

func (c *Checker) testAnthropic(keyValue string, provider *model.Provider) bool {
	baseURL := strings.TrimRight(provider.BaseURL, "/")
	testURL := baseURL + "/v1/messages"

	body := `{"model":"claude-haiku-4-5","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`

	req, err := http.NewRequest("POST", testURL, strings.NewReader(body))
	if err != nil {
		return false
	}

	req.Header.Set("x-api-key", keyValue)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	applyExtraHeaders(req, provider)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// Auth-based acceptance (Anthropic probe): a 400 ("model not found") or
	// 403 (permission_error for model access — the KEY itself authenticated)
	// still proves the key is usable; only 401 (bad key), 402 (quota
	// exhausted), 429 (rate limit) and upstream 5xx (provider down) mean the
	// key is not usable.
	return resp.StatusCode < 500 &&
		resp.StatusCode != http.StatusUnauthorized &&
		resp.StatusCode != http.StatusPaymentRequired &&
		resp.StatusCode != http.StatusTooManyRequests
}

// testOpenAIProbe is the probe acceptance predicate for OpenAI-format
// providers: unlike Anthropic (where 403 = model access), an OpenAI 403
// means the key is forbidden and should stay disabled. 402 (quota
// exhausted) is likewise not alive.
func testOpenAIProbe(resp *http.Response) bool {
	return resp.StatusCode < 500 &&
		resp.StatusCode != http.StatusUnauthorized &&
		resp.StatusCode != http.StatusForbidden &&
		resp.StatusCode != http.StatusPaymentRequired &&
		resp.StatusCode != http.StatusTooManyRequests
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
	KeyID    int64  `json:"key_id"`
	Status   string `json:"status"`
	Alive    bool   `json:"alive"`
	Error    string `json:"error,omitempty"`
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


