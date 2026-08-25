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

// TestRelayEmptyTargetModelInheritsIncomingName is the general pass-through
// contract for routes with an EMPTY target_model: the upstream request must
// inherit the client-provided (incoming) model name — for every provider and
// route alike. The field incident this pins: an empty target_model route was
// observed sending a stale cached upstream endpoint id instead of the
// incoming name; once that endpoint was retired upstream, the upstream
// answered not_found_error -> non-retryable 400 -> the whole calling session
// aborted. Upstream accepts the original incoming name, so the empty route
// must send exactly that. The incoming name is deliberately arbitrary (not a
// real model, not a known endpoint id), so the assertions can only pass when
// the client-provided name itself is inherited — never when a cached/derived
// value is substituted. (The model group id IS the incoming name: the relay
// resolves groups by the client-sent model string, so the pin is on the
// exact string reaching the upstream body.)
func TestRelayEmptyTargetModelInheritsIncomingName(t *testing.T) {
	cases := []struct {
		name         string
		providerType string
		clientPath   string // where the client sends the request
		wantPath     string // where the relay must forward it upstream
		body         string // client request body (carries the arbitrary incoming model)
		wantModel    string // the exact incoming model name the upstream must receive
		upstreamBody string // response the upstream answers with
	}{
		{
			name:         "chat completions same-format",
			providerType: "openai",
			clientPath:   "/v1/chat/completions",
			wantPath:     "/v1/chat/completions",
			body:         `{"model":"arb-incoming-model-1","messages":[{"role":"user","content":"hi"}],"stream":false}`,
			wantModel:    "arb-incoming-model-1",
			upstreamBody: `{"id":"cmpl-1","object":"chat.completion","model":"arb-incoming-model-1","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
		},
		{
			name:         "responses native passthrough",
			providerType: "openai",
			clientPath:   "/v1/responses",
			wantPath:     "/v1/responses",
			body:         `{"model":"arb-incoming-model-2","input":"hi","stream":false}`,
			wantModel:    "arb-incoming-model-2",
			upstreamBody: `{"id":"resp_1","object":"response","created_at":0,"status":"completed","model":"arb-incoming-model-2","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`,
		},
		{
			name:         "responses converted to anthropic",
			providerType: "anthropic",
			clientPath:   "/v1/responses",
			wantPath:     "/v1/messages",
			body:         `{"model":"arb-incoming-model-3","input":"hi","stream":false}`,
			wantModel:    "arb-incoming-model-3",
			upstreamBody: `{"id":"msg_9","type":"message","role":"assistant","model":"arb-incoming-model-3","stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":5,"output_tokens":3}}`,
		},
		{
			name:         "anthropic converted to chat completions",
			providerType: "openai",
			clientPath:   "/v1/messages",
			wantPath:     "/v1/chat/completions",
			body:         `{"model":"arb-incoming-model-4","max_tokens":8,"messages":[{"role":"user","content":"hi"}],"stream":false}`,
			wantModel:    "arb-incoming-model-4",
			upstreamBody: `{"id":"chatcmpl-1","object":"chat.completion","model":"arb-incoming-model-4","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			// Upstream asserts the INCOMING model name reached the body
			// untouched, on the path the relay must forward to.
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				body, _ := io.ReadAll(r.Body)
				var sent struct {
					Model string `json:"model"`
				}
				if err := json.Unmarshal(body, &sent); err != nil || sent.Model != tc.wantModel {
					t.Errorf("upstream received model %q, want %q (incoming name)", sent.Model, tc.wantModel)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.upstreamBody)
			}))
			defer upstream.Close()

			// The route under test: EMPTY target_model (pass-through). The
			// group id is the incoming model name (the relay resolves groups
			// by the client-sent model string).
			e := bootstrapResponses(t, tc.providerType, tc.wantModel, upstream.URL)

			req := httptest.NewRequest("POST", tc.clientPath, bytes.NewReader([]byte(tc.body)))
			req.Host = "localhost:9999"
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			if gotPath != tc.wantPath {
				t.Errorf("upstream path = %q, want %q", gotPath, tc.wantPath)
			}
		})
	}
}
