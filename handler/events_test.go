package handler_test

import (
	"bytes"
	"context"
	"embed"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// syncRecorder is a goroutine-safe http.ResponseWriter: the SSE handler
// writes from its own goroutine while the test polls the body.
type syncRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (r *syncRecorder) Header() http.Header { return make(http.Header) }
func (r *syncRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(b)
}
func (r *syncRecorder) WriteHeader(int) {}
func (r *syncRecorder) Flush()          {}
func (r *syncRecorder) String() string  { r.mu.Lock(); defer r.mu.Unlock(); return r.buf.String() }

// bootstrapEvents sets up the app with an event hub and returns the hub and
// engine so the test can publish/flip state through the real paths. The
// engine→hub wiring mirrors main.go exactly: the SSE push only works when a
// status flip ends up on the hub.
func bootstrapEvents(t *testing.T) (*events.Hub, *selector.Engine, *gin.Engine) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "data")); err != nil {
		t.Fatal(err)
	}
	db.SetSetting(model.SettingPort, "9999")
	hub := events.NewHub()
	engine := selector.NewEngine()
	engine.SetOnStatusChanged(func(keyID int64, status string) {
		hub.Publish(events.Event{Type: events.TypeKeyStatusChanged, KeyID: keyID, Status: status})
	})
	checker := health.NewChecker()
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})
	return hub, engine, router.Setup(embed.FS{}, engine, checker, hub)
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStreamEventsPushesStatusChange: the hot-reload contract — when the
// relay/health checker flips a key's status (here via the same engine
// methods they call), an open /api/events connection must receive an SSE
// frame naming the key and its new status, so the UI can re-fetch that key
// immediately instead of waiting for its next poll.
func TestStreamEventsPushesStatusChange(t *testing.T) {
	hub, engine, e := bootstrapEvents(t)

	key := model.Key{ProviderID: 1, Status: model.KeyStatusActive}
	if err := db.GetDB().Create(&key).Error; err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx)
	req.Host = "localhost:9999" // LocalOnlyMiddleware requires localhost
	rec := &syncRecorder{}

	done := make(chan struct{})
	go func() {
		e.ServeHTTP(rec, req)
		close(done)
	}()

	// Wait until the handler's subscription is registered before flipping
	// the status — the hub has no replay, so an early event would be lost.
	waitFor(t, 5*time.Second, func() bool { return hub.Len() > 0 }, "SSE handler never subscribed to the hub")

	engine.MarkKeyDisabled(key.ID, "auth_failed")

	// Pin the frame shape the UI parses: SSE "data: {json}\n\n" with the
	// type, key id and new status.
	wantFrame := `data: {"type":"key_status_changed","key_id":` + strconv.FormatInt(key.ID, 10) + `,"status":"disabled"}` + "\n\n"
	waitFor(t, 5*time.Second, func() bool {
		return strings.Contains(rec.String(), wantFrame)
	}, "no key_status_changed SSE frame received")

	// Cancel the request; the handler must return (not hang the test) —
	// and the stream must also end when the app begins shutdown, so the
	// graceful quit path never stalls on an immortal connection.
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SSE handler did not return after request cancellation")
	}
}
