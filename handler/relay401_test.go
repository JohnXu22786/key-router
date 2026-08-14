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
	// The end state is deterministic: single key, and MarkKeyRateLimited's
	// cooldown guard (never shrink) keeps the final DB row rate_limited.
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

// TestRelay401BadKeyDisables: a 401 whose body blames the KEY must still
// permanently disable the key — the auth_failed classification the relay
// had before the model-problem fix must be preserved.
func TestRelay401BadKeyDisables(t *testing.T) {
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
	_ = rec

	var after model.Key
	db.GetDB().First(&after, key.ID)
	if after.Status != model.KeyStatusDisabled || after.DisabledReason != "auth_failed" {
		t.Errorf("key status = %q reason = %q, want disabled/auth_failed for a genuine key 401", after.Status, after.DisabledReason)
	}
}
