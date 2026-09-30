package handler_test

import (
	"embed"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func bootstrapTruncatedErrorBody(t *testing.T, status int) (*selector.Engine, *gin.Engine, int64) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := `{"error":`
		w.Header().Set("Content-Length", fmt.Sprint(len(body)+1))
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(upstream.Close)

	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "data")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.SetSetting(model.SettingPort, "9999"); err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Create(&model.Provider{Name: "mock", Type: "openai", BaseURL: upstream.URL}).Error; err != nil {
		t.Fatal(err)
	}
	var provider model.Provider
	if err := db.GetDB().First(&provider).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Create(&model.Key{
		ProviderID: provider.ID, KeyValue: "sk-test", Name: "k1", RecoveryStrategy: model.RecoveryImmediate,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var key model.Key
	if err := db.GetDB().First(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Create(&model.ModelGroup{GroupID: "mock-model", Name: "Mock", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	var group model.ModelGroup
	if err := db.GetDB().First(&group).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Create(&model.Route{
		ModelGroupID: group.ID, ProviderID: provider.ID, Enabled: true, Priority: 0,
	}).Error; err != nil {
		t.Fatal(err)
	}

	engine := selector.NewEngine()
	e := router.Setup(embed.FS{}, engine, health.NewChecker(), events.NewHub())
	return engine, e, key.ID
}

func TestRelayTruncatedErrorBodiesReturnBadGatewayWithoutClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		stream bool
	}{
		{name: "400 passthrough", status: http.StatusBadRequest},
		{name: "401 key classification", status: http.StatusUnauthorized},
		{name: "429 quota classification", status: http.StatusTooManyRequests},
		{name: "streaming non-2xx passthrough", status: http.StatusUnprocessableEntity, stream: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, e, keyID := bootstrapTruncatedErrorBody(t, tc.status)
			if tc.stream {
				// Preserve one prior success while the key is cooling. If the
				// truncated body is counted as another success, it is reactivated.
				engine.RecordResult(keyID, true, "", 0)
				expired := time.Now().Add(-time.Minute)
				if err := db.GetDB().Model(&model.Key{}).Where("id = ?", keyID).Updates(map[string]interface{}{
					"status": model.KeyStatusRateLimited, "rate_limited_until": expired, "disabled_reason": "http_429",
				}).Error; err != nil {
					t.Fatal(err)
				}
				engine.Refresh()
			}
			body := `{"model":"mock-model","messages":[{"role":"user","content":"hi"}]}`
			if tc.stream {
				body = `{"model":"mock-model","messages":[{"role":"user","content":"hi"}],"stream":true}`
			}
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
			req.Host = "localhost:9999"
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadGateway {
				t.Fatalf("client status = %d, want 502 for truncated upstream error body: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "upstream_read_failed") {
				t.Errorf("client body = %q, want upstream_read_failed", rec.Body.String())
			}
			var after model.Key
			if err := db.GetDB().First(&after, keyID).Error; err != nil {
				t.Fatal(err)
			}
			if tc.stream {
				if after.Status != model.KeyStatusRateLimited || after.DisabledReason != "http_429" {
					t.Errorf("key status = %q reason = %q, want cooling state unchanged after truncated body", after.Status, after.DisabledReason)
				}
			} else if after.Status != model.KeyStatusActive || after.DisabledReason != "" {
				t.Errorf("key status = %q reason = %q, want active and unclassified", after.Status, after.DisabledReason)
			}
		})
	}
}
