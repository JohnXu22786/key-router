package selector

import (
	"path/filepath"
	"reflect"
	"testing"

	"key-router/db"
	"key-router/model"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	if err := db.Init(filepath.Join(t.TempDir(), "data")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})
	return NewEngine()
}

// TestSelectKeySkipsLimitExceededKey pins the routing contract behind the
// UI's "limit hit" indicator: a key whose window limit (RPM here) is
// exhausted is skipped by SelectKey even though its status is still
// "active" — traffic falls through to the next key — and LimitedWindows
// reports which window is blocking it.
func TestSelectKeySkipsLimitExceededKey(t *testing.T) {
	e := newTestEngine(t)
	k1 := &model.Key{ID: 1, ProviderID: 1, Status: model.KeyStatusActive, SortOrder: 0, RPMLimit: 10}
	k2 := &model.Key{ID: 2, ProviderID: 1, Status: model.KeyStatusActive, SortOrder: 1}
	route := &RouteEntry{Keys: []*model.Key{k1, k2}}

	// k1 is within limits -> picked first (sort order).
	if got := e.SelectKey(route); got != k1 {
		t.Fatalf("SelectKey = key %d, want key 1 (within limits)", got.ID)
	}

	// Exhaust k1's RPM window; traffic must fall through to k2.
	for i := 0; i < 10; i++ {
		e.WindowManager.IncrementRequest(k1.ID, model.WindowRPM)
	}
	if got := e.SelectKey(route); got != k2 {
		t.Fatalf("SelectKey = key %d, want key 2 (key 1 over RPM limit)", got.ID)
	}
	if got := e.LimitedWindows(k1); !reflect.DeepEqual(got, []string{"rpm"}) {
		t.Errorf("LimitedWindows(k1) = %v, want [rpm]", got)
	}
	if got := e.LimitedWindows(k2); len(got) != 0 {
		t.Errorf("LimitedWindows(k2) = %v, want []", got)
	}

	// Window rolls over -> key 1 is selected again.
	e.WindowManager.Reset(k1.ID)
	if got := e.SelectKey(route); got != k1 {
		t.Fatalf("SelectKey = key %d, want key 1 (window reset)", got.ID)
	}
}

// TestLimitedWindowsRespectsMetricType: cost-metric windows compare against
// the cost bucket, token-metric windows against tokens, request windows
// against the request count.
func TestLimitedWindowsRespectsMetricType(t *testing.T) {
	e := newTestEngine(t)
	k := &model.Key{
		ID: 1, ProviderID: 1, Status: model.KeyStatusActive,
		TPMLimit:   100, // token metric
		RP5hLimit:  1,
		RP5hMetric: "cost", // cost metric (micro-USD)
		RPDLimit:   1,
		RPDMetric:  "requests", // request metric
	}
	// 5 requests, 200 tokens, cost 2 micro-USD across all windows.
	e.WindowManager.IncrementAllWithCost(k.ID, 200, 2)

	got := e.LimitedWindows(k)
	want := []string{"tpm", "rp5h", "rpd"} // rpm has no limit configured
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LimitedWindows = %v, want %v", got, want)
	}
}
