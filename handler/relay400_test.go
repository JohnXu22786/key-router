package handler_test

import (
	"bytes"
	"embed"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"key-router/db"
	"key-router/events"
	"key-router/health"
	"key-router/model"
	"key-router/router"
	"key-router/selector"

	"github.com/gin-gonic/gin"
)

// bootstrap400 wires the app with one openai provider + one key, the
// upstream answering 400 with the given body.
func bootstrap400(t *testing.T, body string) (*selector.Engine, *gin.Engine, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(upstream.Close)

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
	return engine, router.Setup(embed.FS{}, engine, checker, events.NewHub()), key.ID
}

func sendRelay(e *gin.Engine, inputBody string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(inputBody)))
	req.Host = "localhost:9999" // LocalOnlyMiddleware requires localhost
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestRelay400GeminiBadKeyCoolsNotDisables: gateways that report an invalid
// KEY with 400 + API_KEY_INVALID (Gemini-style) must be handled like a 401:
// mark the key once with auth_failed, fail over — and only disable after 2
// consecutive identical observations. The single key here means the request
// ends as key_exhausted after failover, not as a passthrough 400.
func TestRelay400GeminiBadKeyCoolsNotDisables(t *testing.T) {
	_, e, keyID := bootstrap400(t, `{"error":{"code":"API_KEY_INVALID","message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`)

	rec := sendRelay(e, `{"model":"mock-model","messages":[{"role":"user","content":"hi"}]}`)

	var after model.Key
	db.GetDB().First(&after, keyID)
	if after.Status != model.KeyStatusRateLimited {
		t.Fatalf("status = %q, want rate_limited (400 + API_KEY_INVALID must mark once and cool, not disable)", after.Status)
	}
	if after.DisabledReason != "auth_failed" {
		t.Errorf("disabled_reason = %q, want auth_failed for a 400 that blames the key", after.DisabledReason)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("client status = %d, want 429 key_exhausted (failover exhausted, no passthrough)", rec.Code)
	}
}

// TestRelay400ModelProblemPassesThrough: a 400 whose body names a MODEL /
// request problem is not a key problem — the upstream error passes through
// to the client untouched and the key stays active.
func TestRelay400ModelProblemPassesThrough(t *testing.T) {
	_, e, keyID := bootstrap400(t, `{"error":{"message":"The model 'gpt-4o-mini' does not exist or you do not have access to it","code":"model_not_found"}}`)

	rec := sendRelay(e, `{"model":"mock-model","messages":[{"role":"user","content":"hi"}]}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("client status = %d, want 400 passthrough", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "model_not_found") {
		t.Errorf("client body = %q, want the upstream error preserved", body)
	}
	var after model.Key
	db.GetDB().First(&after, keyID)
	if after.Status != model.KeyStatusActive || after.DisabledReason != "" {
		t.Errorf("key status = %q reason = %q, want untouched active key (model problem is not a key problem)", after.Status, after.DisabledReason)
	}
}

// TestRelay400KeyValidModelInvalidPassesThrough: a 400 whose body says the
// KEY is valid but the MODEL is invalid must NOT mark the key auth_failed —
// before the valence fix the "key ... invalid" chain classified it
// key-invalid and the relay cooled/marked the key on real traffic. Now the
// body passes through untouched and the key stays active.
func TestRelay400KeyValidModelInvalidPassesThrough(t *testing.T) {
	_, e, keyID := bootstrap400(t, `{"error":{"message":"Your API key is valid, but the model gpt-4o-mini is invalid."}}`)

	rec := sendRelay(e, `{"model":"mock-model","messages":[{"role":"user","content":"hi"}]}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("client status = %d, want 400 passthrough", rec.Code)
	}
	var after model.Key
	db.GetDB().First(&after, keyID)
	if after.Status != model.KeyStatusActive || after.DisabledReason != "" {
		t.Errorf("key status = %q reason = %q, want untouched active key (key valid, model invalid is not a key problem)", after.Status, after.DisabledReason)
	}
}
