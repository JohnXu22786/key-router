package handler_test

import (
	"bytes"
	"embed"
	"errors"
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

	"gorm.io/gorm"
)

func TestRelayHandlesIndeterminateExactPricingLookup(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "non-streaming"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					f := w.(http.Flusher)
					for _, event := range []string{
						`data: {"id":"c1","choices":[{"index":0,"delta":{"content":"hi"}}]}` + "\n\n",
						`data: {"id":"c2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n",
						`data: {"id":"c3","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}` + "\n\n",
						"data: [DONE]\n\n",
					} {
						fmt.Fprint(w, event)
						f.Flush()
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"c1","object":"chat.completion","model":"mock-model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
			}))
			defer upstream.Close()

			router, engine, key := bootstrapBillingPriceLookupRelay(t, upstream.URL)
			if err := db.GetDB().Create(&model.Pricing{ModelName: "mock-model", PromptPer1M: 2.0}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.GetDB().Create(&model.Pricing{ModelName: "*", PromptPer1M: 4.0}).Error; err != nil {
				t.Fatal(err)
			}

			const callbackName = "handler_test:fail_exact_pricing_lookup"
			queryCallbacks := db.GetDB().Callback().Query()
			if err := queryCallbacks.After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if tx.Statement.Table != "pricings" || len(tx.Statement.Vars) == 0 {
					return
				}
				modelName, ok := tx.Statement.Vars[0].(string)
				if ok && modelName == "mock-model" {
					tx.AddError(errors.New("simulated exact pricing lookup failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := queryCallbacks.Remove(callbackName); err != nil {
					t.Errorf("remove pricing lookup test callback: %v", err)
				}
			})

			body := fmt.Sprintf(`{"model":"mock-model","messages":[{"role":"user","content":"hi"}],"stream":%t}`, streaming)
			req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(body))
			req.Host = "localhost:9999"
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
			}

			var rows []model.Consumption
			if err := db.GetDB().Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if len(rows) != 0 {
				t.Fatalf("persisted consumption rows = %d, want 0 after indeterminate pricing", len(rows))
			}
			if got := engine.WindowManager.GetCount(key.ID, model.WindowRPM); got != 1 {
				t.Errorf("request window count = %d, want 1 after successful upstream response", got)
			}
			if got := engine.WindowManager.GetTokens(key.ID, model.WindowTPM); got != 6 {
				t.Errorf("token window count = %d, want 6 after successful upstream response", got)
			}
			if got := engine.WindowManager.GetCost(key.ID, model.WindowRPM); got != 0 {
				t.Errorf("cost window = %d, want 0 for unresolved pricing", got)
			}
		})
	}
}

func bootstrapBillingPriceLookupRelay(t *testing.T, baseURL string) (http.Handler, *selector.Engine, model.Key) {
	t.Helper()
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
	if err := db.GetDB().Create(&model.Provider{Name: "mock", Type: "openai", BaseURL: baseURL}).Error; err != nil {
		t.Fatal(err)
	}
	var provider model.Provider
	if err := db.GetDB().First(&provider).Error; err != nil {
		t.Fatal(err)
	}
	key := model.Key{ProviderID: provider.ID, KeyValue: "sk-test", Name: "k1", RecoveryStrategy: model.RecoveryImmediate}
	if err := db.GetDB().Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Create(&model.ModelGroup{GroupID: "mock-model", Name: "Mock", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	var group model.ModelGroup
	if err := db.GetDB().First(&group).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Create(&model.Route{ModelGroupID: group.ID, ProviderID: provider.ID, Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}

	engine := selector.NewEngine()
	return router.Setup(embed.FS{}, engine, health.NewChecker(), events.NewHub()), engine, key
}
