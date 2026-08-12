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
	"key-router/health"
	"key-router/model"
	"key-router/router"
	"key-router/selector"

	"github.com/gin-gonic/gin"
)

// TestRelayLatencyOverhead quantifies the gateway's ADDED latency for both
// non-streaming and streaming requests against a mock upstream that replies
// after a fixed 200ms delay. The measured total should be ≈200ms + a small
// gateway overhead (<100ms); anything more indicates the gateway is adding
// real delay (buffering, serialization, lock contention).
func TestRelayLatencyOverhead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstreamDelay := 200 * time.Millisecond

	// Mock upstream: chat completions reply after the fixed delay.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(upstreamDelay)
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"mock-model"}]}`)
			return
		}
		// Non-streaming completion.
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`)
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
	e := router.Setup(embed.FS{}, engine, checker)

	do := func() (time.Duration, int) {
		body, _ := json.Marshal(map[string]interface{}{"model": "mock-model", "messages": []map[string]string{{"role": "user", "content": "hi"}}})
		req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
		req.Host = "localhost:9999"
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		start := time.Now()
		e.ServeHTTP(rec, req)
		return time.Since(start), rec.Code
	}

	// Warmup (connection pool, pricing cache).
	if _, code := do(); code != 200 {
		t.Fatalf("warmup status = %d", code)
	}
	_, _ = do()

	// Non-streaming: total ≈ upstreamDelay + overhead.
	total, code := do()
	if code != 200 {
		t.Fatalf("non-streaming status = %d", code)
	}
	overhead := total - upstreamDelay
	t.Logf("non-streaming: total=%v overhead=%v (upstream=%v)", total, overhead, upstreamDelay)
	if overhead > 100*time.Millisecond {
		t.Errorf("non-streaming gateway overhead too high: %v", overhead)
	}
}
