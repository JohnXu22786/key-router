package handler_test

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"key-router/db"
	"key-router/events"
	"key-router/health"
	"key-router/model"
	"key-router/router"
	"key-router/selector"

	"github.com/gin-gonic/gin"
)

// TestRelay401ModelProblemCoolsNotDisables: a gateway that answers 401 with
// a MODEL problem (unknown / not-entitled model) must NOT have its key
// permanently disabled — the health probe classifies such 401s as alive,
// and the relay must use the same classification or the key flaps (disabled
// by real traffic, recovered by the next probe, disabled again by the next
// request). This is the regression the probe-side fix would otherwise be
// incomplete without.
func TestRelay401ModelProblemCoolsNotDisables(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"The model 'gpt-4o-mini' does not exist or you do not have access to it","code":"model_not_found"}}`)
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "data")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})
	db.SetSetting(model.SettingPort, "9999")
	db.GetDB().Create(&model.Provider{Name: "mock", Type: "openai", BaseURL: upstream.URL})
	var prov model.Provider
	db.GetDB().First(&prov)
	db.GetDB().Create(&model.Key{ProviderID: prov.ID, KeyValue: "sk-test", Name: "k1", RecoveryStrategy: model.RecoveryImmediate})
	var key model.Key
	db.GetDB().First(&key)
	db.GetDB().Create(&model.ModelGroup{GroupID: "mock-model", Name: "Mock", Enabled: true})
	var g model.ModelGroup
	db.GetDB().First(&g)
	db.GetDB().Create(&model.Route{ModelGroupID: g.ID, ProviderID: prov.ID, Enabled: true, Priority: 0})

	engine := selector.NewEngine()
	checker := health.NewChecker()
	e := router.Setup(embed.FS{}, engine, checker, events.NewHub())

	body, _ := json.Marshal(map[string]interface{}{"model": "mock-model", "messages": []map[string]string{{"role": "user", "content": "hi"}}})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Host = "localhost:9999"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// The gateway model-401s; the relay must fail over (retry), not disable.
	// The end state is deterministic: single key, and failKey's cooldown
	// guard (never shrink) keeps the final DB row rate_limited.
	var after model.Key
	db.GetDB().First(&after, key.ID)
	if after.Status == model.KeyStatusDisabled {
		t.Fatalf("key %d disabled by a model-problem 401 — must cool, not disable (disable -> recover -> disable flap)", key.ID)
	}
	if after.DisabledReason == "auth_failed" {
		t.Errorf("disabled_reason = %q, want anything but auth_failed for a model-problem 401", after.DisabledReason)
	}
	if after.Status != model.KeyStatusRateLimited {
		t.Errorf("key status = %q, want rate_limited (model-problem 401 must cool the key, 403-style)", after.Status)
	}
}

// TestRelay401KeyValidModelInvalidCoolsNotDisables: a gateway that answers
// 401 with "Your API key is valid, but the model gpt-4o-mini is invalid"
// (the KEY is fine; the requested model is not) must cool the key, never
// disable it — the same classification as the health probe, so no
// disable -> recover -> disable flap. Before the valence fix the "key ...
// invalid" chain marked the key auth_failed and a second hit disabled it.
func TestRelay401KeyValidModelInvalidCoolsNotDisables(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Your API key is valid, but the model gpt-4o-mini is invalid."}}`)
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "data")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})
	db.SetSetting(model.SettingPort, "9999")
	db.GetDB().Create(&model.Provider{Name: "mock", Type: "openai", BaseURL: upstream.URL})
	var prov model.Provider
	db.GetDB().First(&prov)
	db.GetDB().Create(&model.Key{ProviderID: prov.ID, KeyValue: "sk-test", Name: "k1", RecoveryStrategy: model.RecoveryImmediate})
	var key model.Key
	db.GetDB().First(&key)
	db.GetDB().Create(&model.ModelGroup{GroupID: "mock-model", Name: "Mock", Enabled: true})
	var g model.ModelGroup
	db.GetDB().First(&g)
	db.GetDB().Create(&model.Route{ModelGroupID: g.ID, ProviderID: prov.ID, Enabled: true, Priority: 0})

	engine := selector.NewEngine()
	checker := health.NewChecker()
	e := router.Setup(embed.FS{}, engine, checker, events.NewHub())

	body, _ := json.Marshal(map[string]interface{}{"model": "mock-model", "messages": []map[string]string{{"role": "user", "content": "hi"}}})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Host = "localhost:9999"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// The gateway 401s naming a model problem (the key is valid); the relay
	// must cool the key (model-problem 401, 403-style), NOT disable it.
	var after model.Key
	db.GetDB().First(&after, key.ID)
	if after.Status == model.KeyStatusDisabled {
		t.Fatalf("key %d disabled by a key-valid/model-invalid 401 — must cool, not disable (valid key taken out of rotation)", key.ID)
	}
	if after.DisabledReason == "auth_failed" {
		t.Errorf("disabled_reason = %q, want anything but auth_failed for a key-valid/model-invalid 401", after.DisabledReason)
	}
	if after.Status != model.KeyStatusRateLimited {
		t.Errorf("key status = %q, want rate_limited (key-valid/model-invalid 401 must cool the key)", after.Status)
	}
}

// TestRelay401BadKeyCoolsThenDisables: a 401 whose body blames the KEY must
// mark the key once and fail over (rate_limited + auth_failed reason), and
// only a SECOND consecutive 401 disables it — the "2 consecutive failures
// with the same problem" rule. A single bad response in real traffic must
// no longer brick a healthy key on the spot.
func TestRelay401BadKeyCoolsThenDisables(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided: sk-***.","type":"invalid_request_error","code":"invalid_api_key"}}`)
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "data")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})
	db.SetSetting(model.SettingPort, "9999")
	db.GetDB().Create(&model.Provider{Name: "mock", Type: "openai", BaseURL: upstream.URL})
	var prov model.Provider
	db.GetDB().First(&prov)
	db.GetDB().Create(&model.Key{ProviderID: prov.ID, KeyValue: "sk-test", Name: "k1", RecoveryStrategy: model.RecoveryImmediate})
	var key model.Key
	db.GetDB().First(&key)
	db.GetDB().Create(&model.ModelGroup{GroupID: "mock-model", Name: "Mock", Enabled: true})
	var g model.ModelGroup
	db.GetDB().First(&g)
	db.GetDB().Create(&model.Route{ModelGroupID: g.ID, ProviderID: prov.ID, Enabled: true, Priority: 0})

	engine := selector.NewEngine()
	checker := health.NewChecker()
	e := router.Setup(embed.FS{}, engine, checker, events.NewHub())

	body, _ := json.Marshal(map[string]interface{}{"model": "mock-model", "messages": []map[string]string{{"role": "user", "content": "hi"}}})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Host = "localhost:9999"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// FIRST 401: the key must be marked once and cooled, NOT disabled.
	var after model.Key
	db.GetDB().First(&after, key.ID)
	if after.Status != model.KeyStatusRateLimited {
		t.Fatalf("status after 1st 401 = %q, want rate_limited (mark once, fail over, don't disable)", after.Status)
	}
	if after.DisabledReason != "auth_failed" {
		t.Errorf("disabled_reason = %q, want auth_failed for a genuine key 401", after.DisabledReason)
	}

	// Let the cooldown pass so the key is selectable again, then a SECOND
	// 401 with the same problem disables it.
	db.GetDB().Model(&model.Key{}).Where("id = ?", key.ID).Update("rate_limited_until", time.Now().Add(-time.Minute))
	engine.Refresh() // reload the expired cooldown into the engine cache

	req2 := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req2.Host = "localhost:9999" // LocalOnlyMiddleware requires localhost
	req2.Header.Set("Content-Type", "application/json")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	db.GetDB().First(&after, key.ID)
	if after.Status != model.KeyStatusDisabled || after.DisabledReason != "auth_failed" {
		t.Errorf("status = %q reason = %q after 2 consecutive 401s, want disabled/auth_failed", after.Status, after.DisabledReason)
	}
}

// TestRelayRecoversKeyAfterTwoSuccessfulRequests pins the RELAY side of the
// recovery state machine: a cooled key returns to active only after 2
// consecutive successful real requests (the success-streak wiring in
// handleRelay — if the RecordResult(true) calls were dropped, this test
// fails even though the engine-level machine is correct). A single success
// must leave the key rate_limited.
func TestRelayRecoversKeyAfterTwoSuccessfulRequests(t *testing.T) {
	gin.SetMode(gin.TestMode)
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"id":"cmpl-1","object":"chat.completion","model":"mock-model","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer upstream.Close()

	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "data")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})
	db.SetSetting(model.SettingPort, "9999")
	db.GetDB().Create(&model.Provider{Name: "mock", Type: "openai", BaseURL: upstream.URL})
	var prov model.Provider
	db.GetDB().First(&prov)
	db.GetDB().Create(&model.Key{ProviderID: prov.ID, KeyValue: "sk-test", Name: "k1", RecoveryStrategy: model.RecoveryImmediate})
	var key model.Key
	db.GetDB().First(&key)
	db.GetDB().Create(&model.ModelGroup{GroupID: "mock-model", Name: "Mock", Enabled: true})
	var g model.ModelGroup
	db.GetDB().First(&g)
	db.GetDB().Create(&model.Route{ModelGroupID: g.ID, ProviderID: prov.ID, Enabled: true, Priority: 0})

	engine := selector.NewEngine()
	checker := health.NewChecker()
	e := router.Setup(embed.FS{}, engine, checker, events.NewHub())

	body, _ := json.Marshal(map[string]interface{}{"model": "mock-model", "messages": []map[string]string{{"role": "user", "content": "hi"}}})
	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
		req.Host = "localhost:9999" // LocalOnlyMiddleware requires localhost
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	// Fresh dest per read (reusing one dest across reads with gorm's
	// First(&dest, pk) builds a redundant PK condition and can return a
	// stale row).
	loadKey := func() model.Key {
		var k model.Key
		db.GetDB().Where("id = ?", key.ID).First(&k)
		return k
	}

	// Request 1: 429 cools the key (mark once, fail over) — streak 1.
	send()
	if after := loadKey(); after.Status != model.KeyStatusRateLimited {
		t.Fatalf("status after 429 = %q, want rate_limited", after.Status)
	}

	// Let the cooldown pass so the key is selectable again.
	db.GetDB().Model(&model.Key{}).Where("id = ?", key.ID).Update("rate_limited_until", time.Now().Add(-time.Minute))
	engine.Refresh()

	// Request 2: first success — streak 1, must NOT recover yet.
	send()
	if after := loadKey(); after.Status != model.KeyStatusRateLimited {
		t.Fatalf("status after 1st success = %q, want rate_limited (recovery needs 2 consecutive successes)", after.Status)
	}

	// Request 3: second consecutive success — the key returns to active.
	send()
	after := loadKey()
	if after.Status != model.KeyStatusActive {
		t.Errorf("status after 2nd success = %q, want active", after.Status)
	}
	if after.DisabledReason != "" {
		t.Errorf("disabled_reason = %q, want cleared after recovery", after.DisabledReason)
	}
	if after.RateLimitedUntil != nil {
		t.Errorf("rate_limited_until = %v, want nil after recovery", after.RateLimitedUntil)
	}
}
