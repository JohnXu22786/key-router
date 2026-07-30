package health

import (
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
	mu            sync.Mutex
	interval      time.Duration
	stopChan      chan struct{}
	running       bool
	onRecovered   OnKeyRecovered
}

// NewChecker creates a new health checker
func NewChecker() *Checker {
	return &Checker{
		interval: 120 * time.Second, // default
		stopChan: make(chan struct{}),
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

	if c.running {
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

	c.running = true
	go c.loop()
	log.Printf("[health] checker started (interval: %v)", c.interval)
}

// Stop stops the health check loop
func (c *Checker) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		close(c.stopChan)
		c.running = false
	}
}

// Restart re-reads interval and restarts
func (c *Checker) Restart() {
	c.Stop()
	time.Sleep(100 * time.Millisecond)
	// Recreate stop channel
	c.stopChan = make(chan struct{})
	c.Start()
}

func (c *Checker) loop() {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.checkAll()
		case <-c.stopChan:
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

	for _, key := range keys {
		c.checkKey(&key)
	}
}

func (c *Checker) checkKey(key *model.Key) {
	// For rate-limited keys, only check if cooldown has expired
	if key.Status == model.KeyStatusRateLimited {
		if key.RateLimitedUntil != nil && time.Now().Before(*key.RateLimitedUntil) {
			return // Still in cooldown
		}
	}

	// Get provider
	var provider model.Provider
	if err := db.GetDB().First(&provider, key.ProviderID).Error; err != nil {
		return
	}

	// Test by listing models (a lightweight request)
	alive := c.testKey(key.KeyValue, &provider)

	if alive {
		log.Printf("[health] key %d (%s...) recovered, marking active",
			key.ID, truncateKey(key.KeyValue))

		err := db.GetDB().Model(key).Updates(map[string]interface{}{
			"status":             model.KeyStatusActive,
			"rate_limited_until": nil,
			"disabled_reason":    "",
		}).Error
		if err != nil {
			log.Printf("[health] failed to update key %d in DB: %v", key.ID, err)
			return
		}

		// Notify engine to update in-memory cache
		if c.onRecovered != nil {
			c.onRecovered(key.ID)
		}
	}
}

func (c *Checker) testKey(keyValue string, provider *model.Provider) bool {
	baseURL := strings.TrimRight(provider.BaseURL, "/")
	testURL := baseURL + "/models"

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

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

func (c *Checker) testAnthropic(keyValue string, provider *model.Provider) bool {
	baseURL := strings.TrimRight(provider.BaseURL, "/")
	testURL := baseURL + "/v1/messages"

	body := `{"model":"claude-3-haiku-20240307","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`

	req, err := http.NewRequest("POST", testURL, strings.NewReader(body))
	if err != nil {
		return false
	}

	req.Header.Set("x-api-key", keyValue)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// 429 means the key is still rate limited
	// 200 means it's working
	// 401 means still disabled
	return resp.StatusCode == http.StatusOK
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
func GetKeyStatuses() []CheckResult {
	var keys []model.Key
	if err := db.GetDB().Find(&keys).Error; err != nil {
		log.Printf("[health] GetKeyStatuses error: %v", err)
		return nil
	}

	var results []CheckResult
	for _, k := range keys {
		results = append(results, CheckResult{
			KeyID:  k.ID,
			Status: k.Status,
			Alive:  k.Status == model.KeyStatusActive,
		})
	}
	return results
}


