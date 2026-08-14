package handler_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"key-router/events"
	"key-router/handler"

	"github.com/gin-gonic/gin"
)

// restartContext builds a gin test context for the Restart endpoint.
func restartContext(t *testing.T) (*handler.AdminHandler, *httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := handler.NewAdminHandler(nil, nil, events.NewHub())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/api/restart", nil)
	return h, rec, c
}

// TestRestartOrdering guards the core ordering of the restart flow: the
// fresh instance must be scheduled BEFORE the response (a scheduling
// failure must be reportable as 500), and the 200 must be written AND
// flushed BEFORE the quit hook runs — the process may exit as soon as the
// drain completes, and an unflushed response would make the caller report
// a failed restart that actually succeeded. Both hooks run exactly once.
func TestRestartOrdering(t *testing.T) {
	h, rec, c := restartContext(t)
	var order []string
	h.RestartSchedule = func() error {
		order = append(order, "schedule")
		if rec.Body.Len() != 0 {
			t.Error("schedule ran after the response was written")
		}
		return nil
	}
	h.RestartQuit = func() {
		order = append(order, "quit")
		if rec.Body.Len() == 0 {
			t.Error("quit ran before the response was written")
		}
		if !rec.Flushed {
			t.Error("quit ran before the response was flushed")
		}
	}

	h.Restart(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if len(order) != 2 || order[0] != "schedule" || order[1] != "quit" {
		t.Errorf("hook order = %v, want [schedule quit]", order)
	}
}

// TestRestartWithoutHook: a build where the restart hooks were never wired
// must fail loudly instead of accepting the request and silently never
// restarting.
func TestRestartWithoutHook(t *testing.T) {
	h, rec, c := restartContext(t)

	h.Restart(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestRestartScheduleFailure: when the fresh instance cannot be scheduled,
// the request must fail with 500, the quit hook must NOT run, and the
// endpoint must not stay locked — a later attempt must succeed (a
// permanent 409 after one failure would require killing the process to
// recover).
func TestRestartScheduleFailure(t *testing.T) {
	h, rec, c := restartContext(t)
	quitCalls := 0
	h.RestartQuit = func() { quitCalls++ }
	h.RestartSchedule = func() error { return errors.New("temp dir unwritable") }

	h.Restart(c)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body: %s)", rec.Code, rec.Body.String())
	}
	if quitCalls != 0 {
		t.Errorf("quit hook calls = %d, want 0 on scheduling failure", quitCalls)
	}

	// The next attempt must be able to restart.
	h.RestartSchedule = func() error { return nil }
	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest("POST", "/api/restart", nil)
	h.Restart(c2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200 (body: %s)", rec2.Code, rec2.Body.String())
	}
	if quitCalls != 1 {
		t.Errorf("quit hook calls = %d, want 1 after a successful retry", quitCalls)
	}
}

// TestRestartRejectsConcurrentCalls: two restart requests arriving at once
// must not both schedule a relaunch — two fresh instances would fight over
// the server port. The mutex is held through the schedule, so exactly one
// call wins: it gets 200 and runs the hooks once; the loser gets 409.
func TestRestartRejectsConcurrentCalls(t *testing.T) {
	h, rec, c := restartContext(t)
	scheduleCalls, quitCalls := 0, 0
	h.RestartSchedule = func() error { scheduleCalls++; return nil }
	h.RestartQuit = func() { quitCalls++ }

	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	c2.Request = httptest.NewRequest("POST", "/api/restart", nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); h.Restart(c) }()
	go func() { defer wg.Done(); h.Restart(c2) }()
	wg.Wait()

	if !((rec.Code == http.StatusOK) != (rec2.Code == http.StatusOK)) {
		t.Errorf("exactly one call must get 200: got %d and %d", rec.Code, rec2.Code)
	}
	loser := rec.Code
	if loser == http.StatusOK {
		loser = rec2.Code
	}
	if loser != http.StatusConflict {
		t.Errorf("second call status = %d, want 409", loser)
	}
	if scheduleCalls != 1 || quitCalls != 1 {
		t.Errorf("hook calls = schedule %d quit %d, want 1 1", scheduleCalls, quitCalls)
	}
}
