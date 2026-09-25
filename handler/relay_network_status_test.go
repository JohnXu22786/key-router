package handler_test

import (
	"bytes"
	"embed"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"key-router/db"
	"key-router/events"
	"key-router/health"
	"key-router/model"
	"key-router/router"
	"key-router/selector"

	"github.com/gin-gonic/gin"
)

func TestRelayNetworkFailureClearsPreviousUpstreamStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var requests int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := atomic.AddInt32(&requests, 1)
		if requestNumber == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
			return
		}

		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("upstream response writer does not support hijacking")
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack second upstream request: %v", err)
			return
		}
		conn.Close()
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
	if err := db.SetSetting(model.SettingRetryTimes, "1"); err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().Create(&model.Provider{Name: "mock", Type: "openai", BaseURL: upstream.URL}).Error; err != nil {
		t.Fatal(err)
	}
	var provider model.Provider
	if err := db.GetDB().First(&provider).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"k1", "k2"} {
		if err := db.GetDB().Create(&model.Key{
			ProviderID: provider.ID, KeyValue: "sk-" + name, Name: name,
			RecoveryStrategy: model.RecoveryImmediate,
		}).Error; err != nil {
			t.Fatal(err)
		}
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

	e := router.Setup(embed.FS{}, selector.NewEngine(), health.NewChecker(), events.NewHub())
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewBufferString(
		`{"model":"mock-model","messages":[{"role":"user","content":"hi"}]}`,
	))
	req.Host = "localhost:9999"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("upstream requests = %d, want 2 (429 followed by network failure)", got)
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("client status = %d, want 502 after the last attempt fails at the network layer", rec.Code)
	}
}
