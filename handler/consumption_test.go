package handler_test

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
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

// TestRelayRecordsIngressModel is the end-to-end contract for the Activity
// page's model dimension: a request for model group "client-model" whose
// route targets an upstream model "up-real-model" must be recorded with
// "client-model" (what the client connected with) — NOT the target model the
// relay sent upstream. Regression: chat.go used to pass the resolved target
// model to RecordConsumption, so the Activity page grouped by upstream names.
func TestRelayRecordsIngressModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Upstream asserts the substituted target model reached the body.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var sent struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &sent); err != nil || sent.Model != "up-real-model" {
			t.Errorf("upstream received model %q, want %q", sent.Model, "up-real-model")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"cmpl-1","object":"chat.completion","model":"up-real-model",
			"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hi"}}],
			"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
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
	db.GetDB().Create(&model.ModelGroup{GroupID: "client-model", Name: "Client", Enabled: true})
	var g model.ModelGroup
	db.GetDB().First(&g)
	db.GetDB().Create(&model.Route{
		ModelGroupID: g.ID,
		ProviderID:   prov.ID,
		TargetModel:  "up-real-model",
		Enabled:      true,
		Priority:     0,
	})

	e := router.Setup(embed.FS{}, selector.NewEngine(), health.NewChecker(), events.NewHub())

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"client-model","messages":[{"role":"user","content":"hi"}],"stream":false}`)))
	req.Host = "localhost:9999"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var rows []model.Consumption
	if err := db.GetDB().Where("key_id = ?", key.ID).Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("consumption rows = %d, want 1", len(rows))
	}
	if rows[0].ModelName != "client-model" {
		t.Errorf("recorded ModelName = %q, want %q (ingress, not the upstream target)", rows[0].ModelName, "client-model")
	}
}
