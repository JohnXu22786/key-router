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

// bootstrapResponses sets up the app with one provider/group/route and
// returns the router plus the created provider (whose BaseURL the test's
// upstream must replace before wiring — simplest is to point BaseURL at the
// httptest server directly).
func bootstrapResponses(t *testing.T, providerType, baseURL string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
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
	db.GetDB().Create(&model.Provider{Name: "mock", Type: providerType, BaseURL: baseURL})
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
	return router.Setup(embed.FS{}, engine, checker, events.NewHub())
}

func postResponses(t *testing.T, e *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader([]byte(body)))
	req.Host = "localhost:9999"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// TestResponsesNativePassthrough: an OpenAI-format provider that implements
// /v1/responses gets the request verbatim and the response is relayed
// without conversion.
func TestResponsesNativePassthrough(t *testing.T) {
	upstreamBody := `{"id":"resp_1","object":"response","status":"completed","model":"mock-model",
		"output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant",
		"content":[{"type":"output_text","text":"native!","annotations":[]}]}],
		"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`

	var gotPath string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, upstreamBody)
	}))
	defer upstream.Close()

	e := bootstrapResponses(t, "openai", upstream.URL)
	rec := postResponses(t, e, `{"model":"mock-model","input":"hi","stream":false}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/responses" {
		t.Errorf("upstream path = %q, want /v1/responses", gotPath)
	}
	var sent map[string]interface{}
	json.Unmarshal(gotBody, &sent)
	if sent["model"] != "mock-model" || sent["input"] != "hi" {
		t.Errorf("upstream body = %s", gotBody)
	}
	// The responses body must reach the client untouched
	if strings.TrimSpace(rec.Body.String()) != upstreamBody {
		t.Errorf("relayed body = %s\nwant %s", rec.Body.String(), upstreamBody)
	}
}

// TestResponsesFallbackToChatCompletions: an OpenAI-compatible gateway
// without /v1/responses (404) must be retried as chat completions and the
// response converted back to the Responses API shape.
func TestResponsesFallbackToChatCompletions(t *testing.T) {
	var chatPathCalled bool
	var chatBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			http.Error(w, `{"error":{"message":"not found","type":"invalid_request_error"}}`, http.StatusNotFound)
		case "/v1/chat/completions":
			chatPathCalled = true
			chatBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"chatcmpl-1","object":"chat.completion","model":"mock-model",
				"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"from chat"}}],
				"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	e := bootstrapResponses(t, "openai", upstream.URL)
	rec := postResponses(t, e, `{"model":"mock-model","input":"hello","max_output_tokens":64,"stream":false}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !chatPathCalled {
		t.Fatal("chat completions fallback path was never called")
	}
	// The upstream must have received a converted chat request
	var sent map[string]interface{}
	if err := json.Unmarshal(chatBody, &sent); err != nil {
		t.Fatalf("upstream chat body invalid: %v\n%s", err, chatBody)
	}
	if sent["max_tokens"] != float64(64) {
		t.Errorf("upstream max_tokens = %v, want 64 (from max_output_tokens)", sent["max_tokens"])
	}
	msgs, _ := sent["messages"].([]interface{})
	if len(msgs) != 1 || msgs[0].(map[string]interface{})["role"] != "user" {
		t.Errorf("upstream messages = %v", sent["messages"])
	}

	// The client must receive a Responses-API body
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("client body not JSON: %v\n%s", err, rec.Body.String())
	}
	if resp["object"] != "response" {
		t.Fatalf("client body = %s, want a response object", rec.Body.String())
	}
	if resp["status"] != "completed" {
		t.Errorf("status = %v", resp["status"])
	}
	output := resp["output"].([]interface{})
	if len(output) != 1 {
		t.Fatalf("output = %v", output)
	}
	msg := output[0].(map[string]interface{})
	parts := msg["content"].([]interface{})
	if parts[0].(map[string]interface{})["text"] != "from chat" {
		t.Errorf("converted text = %v", parts)
	}
	u := resp["usage"].(map[string]interface{})
	if u["input_tokens"] != float64(4) || u["output_tokens"] != float64(2) {
		t.Errorf("usage = %v", u)
	}
}

// TestResponsesToAnthropicProvider: a /v1/responses request routed to an
// Anthropic provider is converted to /v1/messages and back.
func TestResponsesToAnthropicProvider(t *testing.T) {
	var anthPathCalled bool
	var anthBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthPathCalled = true
		anthBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"msg_9","type":"message","role":"assistant","model":"claude-sonnet","stop_reason":"end_turn",
			"content":[{"type":"text","text":"from claude"}],
			"usage":{"input_tokens":5,"output_tokens":3}}`)
	}))
	defer upstream.Close()

	e := bootstrapResponses(t, "anthropic", upstream.URL)
	rec := postResponses(t, e, `{"model":"mock-model","input":"hello","stream":false}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !anthPathCalled {
		t.Fatal("/v1/messages was never called")
	}
	var sent map[string]interface{}
	json.Unmarshal(anthBody, &sent)
	if sent["max_tokens"] != float64(4096) {
		t.Errorf("upstream max_tokens = %v, want default 4096", sent["max_tokens"])
	}

	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["object"] != "response" || resp["status"] != "completed" {
		t.Fatalf("client body = %s", rec.Body.String())
	}
	output := resp["output"].([]interface{})
	msg := output[0].(map[string]interface{})
	parts := msg["content"].([]interface{})
	if parts[0].(map[string]interface{})["text"] != "from claude" {
		t.Errorf("converted text = %v", parts)
	}
}

// TestResponsesStreamFallback: streaming chat completions from a fallback
// gateway are converted into Responses API events, ending with a
// response.completed that carries usage.
func TestResponsesStreamFallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			http.Error(w, `{"error":{"message":"not found"}}`, http.StatusNotFound)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			f := w.(http.Flusher)
			fmt.Fprint(w, `data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`+"\n\n")
			f.Flush()
			fmt.Fprint(w, `data: {"id":"c2","choices":[{"index":0,"delta":{"content":"lo"}}]}`+"\n\n")
			f.Flush()
			fmt.Fprint(w, `data: {"id":"c3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
			f.Flush()
			fmt.Fprint(w, `data: {"id":"c4","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}`+"\n\n")
			f.Flush()
			fmt.Fprint(w, "data: [DONE]\n\n")
			f.Flush()
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	e := bootstrapResponses(t, "openai", upstream.URL)
	rec := postResponses(t, e, `{"model":"mock-model","input":"hi","stream":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("content type = %q", ct)
	}

	var types []string
	var completed *map[string]interface{}
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "[DONE]" {
			t.Error("[DONE] leaked into a Responses stream")
			continue
		}
		var ev map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatalf("bad event %q: %v", payload, err)
		}
		typ, _ := ev["type"].(string)
		types = append(types, typ)
		if typ == "response.completed" {
			m := ev
			completed = &m
		}
	}

	want := []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added", "response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.completed",
	}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("event types = %v\nwant %v", types, want)
	}
	if completed == nil {
		t.Fatal("no response.completed event")
	}
	respObj := (*completed)["response"].(map[string]interface{})
	u := respObj["usage"].(map[string]interface{})
	if u["input_tokens"] != float64(2) || u["output_tokens"] != float64(2) {
		t.Errorf("completed usage = %v", u)
	}
	output := respObj["output"].([]interface{})
	if len(output) != 1 {
		t.Fatalf("completed output = %v", output)
	}
	parts := output[0].(map[string]interface{})["content"].([]interface{})
	if parts[0].(map[string]interface{})["text"] != "Hel"+"lo" {
		t.Errorf("accumulated text = %v", parts)
	}
}

// TestResponsesStreamFallbackNonSSE: a fallback gateway that IGNORES
// stream:true and returns a plain chat completions JSON body must still
// deliver a complete Responses event sequence to the stream client.
func TestResponsesStreamFallbackNonSSE(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/responses":
			http.Error(w, `{"error":{"message":"not found"}}`, http.StatusNotFound)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"chatcmpl-1","object":"chat.completion","model":"mock-model",
				"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"plain json"}}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	e := bootstrapResponses(t, "openai", upstream.URL)
	rec := postResponses(t, e, `{"model":"mock-model","input":"hi","stream":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var types []string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("bad event %q: %v", line, err)
		}
		types = append(types, ev.Type)
	}
	want := []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added", "response.output_text.delta",
		"response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.completed",
	}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("event types = %v\nwant %v", types, want)
	}
}

// TestResponsesNativeFailedIsTerminal: a native Responses stream ending with
// response.failed must not get a synthesized response.completed after it
// (SDKs treat completed as success — a failed response must stay a failure).
func TestResponsesNativeFailedIsTerminal(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, `data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"mock-model","output":[]}}`+"\n\n")
		f.Flush()
		fmt.Fprint(w, `data: {"type":"response.failed","response":{"id":"resp_1","object":"response","status":"failed","model":"mock-model","output":[],"error":{"code":"server_error","message":"overloaded"}}}`+"\n\n")
		f.Flush()
	}))
	defer upstream.Close()

	e := bootstrapResponses(t, "openai", upstream.URL)
	rec := postResponses(t, e, `{"model":"mock-model","input":"hi","stream":true}`)

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"response.failed"`) {
		t.Fatalf("missing response.failed frame: %s", body)
	}
	if strings.Contains(body, "response.completed") {
		t.Errorf("synthesized response.completed after response.failed: %s", body)
	}
	if strings.Contains(body, "[DONE]") {
		t.Errorf("synthesized [DONE] after response.failed: %s", body)
	}
}

// TestResponsesNativeStreamPassthrough: a native /v1/responses SSE stream is
// relayed verbatim (event: lines and data frames), with no synthesized
// tail after response.completed.
func TestResponsesNativeStreamPassthrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, "event: response.created\n")
		fmt.Fprint(w, `data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"mock-model","output":[]}}`+"\n\n")
		f.Flush()
		fmt.Fprint(w, "event: response.output_text.delta\n")
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"native stream"}`+"\n\n")
		f.Flush()
		fmt.Fprint(w, "event: response.completed\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"mock-model","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
		f.Flush()
	}))
	defer upstream.Close()

	e := bootstrapResponses(t, "openai", upstream.URL)
	rec := postResponses(t, e, `{"model":"mock-model","input":"hi","stream":true}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"response.output_text.delta"`) {
		t.Errorf("missing forwarded delta frame: %s", body)
	}
	// event: lines survive native passthrough
	if !strings.Contains(body, "event: response.created") {
		t.Errorf("missing event: line: %s", body)
	}
	// nothing may be synthesized after response.completed (no [DONE], no
	// duplicate completed)
	if strings.Contains(body, "[DONE]") {
		t.Errorf("synthesized [DONE] after native completion: %s", body)
	}
	if n := strings.Count(body, `"type":"response.completed"`); n != 1 {
		t.Errorf("response.completed count = %d, want 1: %s", n, body)
	}
}
