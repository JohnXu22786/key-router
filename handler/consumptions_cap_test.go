package handler_test

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
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

// bootstrapConsumptionsFixture sets up the app with a provider and three
// keys (consumption rows carry a key_id FK) and returns the engine plus the
// keys, for tests that exercise /api/stats/consumptions directly.
func bootstrapConsumptionsFixture(t *testing.T) (*gin.Engine, []model.Key) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "data")); err != nil {
		t.Fatal(err)
	}
	db.SetSetting(model.SettingPort, "9999")

	db.GetDB().Create(&model.Provider{Name: "mock", Type: "openai", BaseURL: "http://localhost:1"})
	var prov model.Provider
	db.GetDB().First(&prov)
	for i := 0; i < 3; i++ {
		db.GetDB().Create(&model.Key{ProviderID: prov.ID, KeyValue: fmt.Sprintf("k%d", i), Name: fmt.Sprintf("k%d", i)})
	}
	var keys []model.Key
	db.GetDB().Where("provider_id = ?", prov.ID).Find(&keys)

	engine := selector.NewEngine()
	checker := health.NewChecker()
	return router.Setup(embed.FS{}, engine, checker, events.NewHub()), keys
}

// consumptionsGet runs one GET /api/stats/consumptions request and returns
// the status code, the response headers, and the decoded rows.
func consumptionsGet(t *testing.T, e *gin.Engine, qs string) (int, http.Header, []model.Consumption) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/stats/consumptions?"+qs, nil)
	req.Host = "localhost:9999" // LocalOnlyMiddleware requires localhost
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		return rec.Code, rec.Header(), nil
	}
	var rows []model.Consumption
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec.Body.String())
	}
	return rec.Code, rec.Header(), rows
}

// TestStatsConsumptionsYearWindowComplete is the regression pin for the
// RANGE-AWARE cap on /api/stats/consumptions (the Overview tab's data
// source: ActivityOverview fetches the full range unfiltered and sums the
// returned rows into its KPI totals). The endpoint used to apply a FIXED
// Limit(100000) with ORDER BY hour_bucket DESC, sized for the Stats page's
// short windows (24h × 7d = 168 rows per (key, model, app)). Consumption
// rows are hourly per (key_id, hour_bucket, model_name, app_name) — 8,760
// rows per active combo per year — so the Overview page's 1y preset with
// 3 keys × 3 models × 2 apps = 18 combos holds 157,680 rows: the fixed cap
// kept only the newest 100,000, silently amputating the oldest ~4 months
// (the first monthly buckets rendered 0 and the KPI totals understated the
// year, with nothing flagging the loss). The cap must be range-aware: a
// window that fits the response contract is returned COMPLETE — every row,
// the oldest hour bucket included, full totals.
func TestStatsConsumptionsYearWindowComplete(t *testing.T) {
	e, keys := bootstrapConsumptionsFixture(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	// One full year of hourly buckets × 18 combos (3 keys × 3 models ×
	// 2 apps) = 157,680 rows — just over the old fixed 100k cap, well
	// within a range-aware contract.
	const hours = 8760
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	start = start.Add(-hours * time.Hour)
	const total = hours * 3 * 3 * 2 // 157,680

	rows := make([]model.Consumption, 0, total)
	for h := 0; h < hours; h++ {
		bucket := start.Add(time.Duration(h) * time.Hour)
		for _, k := range keys {
			for mi := 0; mi < 3; mi++ {
				for ai := 0; ai < 2; ai++ {
					rows = append(rows, model.Consumption{
						KeyID:        k.ID,
						HourBucket:   bucket,
						ModelName:    fmt.Sprintf("m%d", mi),
						AppName:      fmt.Sprintf("a%d", ai),
						RequestCount: 1,
					})
				}
			}
		}
	}
	// Batch inserts below SQLite's variable limit (10 columns per row).
	if err := db.GetDB().CreateInBatches(rows, 2000).Error; err != nil {
		t.Fatal(err)
	}

	// The 1y window: since = the oldest bucket, until = one real hour past
	// the newest bucket (its hour-floored value is the last bucket, so the
	// range covers every inserted row in any DST layout).
	lastBucket := start.Add(time.Duration(hours-1) * time.Hour)
	until := lastBucket.Add(time.Hour)
	qs := fmt.Sprintf("since=%s&until=%s",
		url.QueryEscape(start.Format(time.RFC3339)),
		url.QueryEscape(until.Format(time.RFC3339)))

	code, hdr, got := consumptionsGet(t, e, qs)
	if code != 200 {
		t.Fatalf("status = %d: wanted 200", code)
	}
	if hdr.Get("X-Consumptions-Truncated") != "" {
		t.Fatalf("complete window must not set X-Consumptions-Truncated, got header %q", hdr.Get("X-Consumptions-Truncated"))
	}
	if len(got) != total {
		t.Fatalf("rows = %d, want %d (the whole year: the old fixed 100k cap returned only the newest 100k)", len(got), total)
	}
	if !got[len(got)-1].HourBucket.Equal(start) {
		t.Fatalf("oldest returned hour_bucket = %v, want %v (the earliest ~4 months must reach the client)", got[len(got)-1].HourBucket, start)
	}
	if !got[0].HourBucket.Equal(lastBucket) {
		t.Fatalf("newest returned hour_bucket = %v, want %v", got[0].HourBucket, lastBucket)
	}
	var requestSum int64
	for i := range got {
		requestSum += got[i].RequestCount
	}
	if requestSum != total {
		t.Fatalf("sum of request_count = %d, want %d (KPI totals must cover the full range)", requestSum, total)
	}
}

// TestStatsConsumptionsRunawayWindowCapped pins the cap's other half: a
// genuinely runaway window (multi-year × many combos) must stay BOUNDED and
// must never truncate SILENTLY. Windows whose maximum possible rows
// (hour slots × distinct (key, model, app) groups) exceed the response
// contract are limited to the newest rows AND flagged with the
// X-Consumptions-Truncated header — an explicit contract. A filter that
// shrinks the window's group count back under the cap restores a complete,
// unflagged response (the cap is range- AND filter-aware).
func TestStatsConsumptionsRunawayWindowCapped(t *testing.T) {
	e, keys := bootstrapConsumptionsFixture(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	// 24 combos (1 key × 8 models × 3 apps), one hourly row each — the
	// request window, not the data volume, is what makes this runaway.
	now := time.Now()
	bucket := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location()).Add(-time.Hour)
	var rows []model.Consumption
	for mi := 0; mi < 8; mi++ {
		for ai := 0; ai < 3; ai++ {
			rows = append(rows, model.Consumption{
				KeyID:        keys[0].ID,
				HourBucket:   bucket,
				ModelName:    fmt.Sprintf("m%d", mi),
				AppName:      fmt.Sprintf("a%d", ai),
				RequestCount: 1,
			})
		}
	}
	if err := db.GetDB().Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	// A 10-year window: ~87,660 hour slots × 24 groups ≈ 2.1M possible
	// rows — beyond the 1,000,000-row contract cap → capped + flagged.
	since := bucket.AddDate(-10, 0, 0)
	until := bucket.Add(time.Hour)
	qs := fmt.Sprintf("since=%s&until=%s",
		url.QueryEscape(since.Format(time.RFC3339)),
		url.QueryEscape(until.Format(time.RFC3339)))

	code, hdr, got := consumptionsGet(t, e, qs)
	if code != 200 {
		t.Fatalf("status = %d: wanted 200", code)
	}
	if hdr.Get("X-Consumptions-Truncated") != "true" {
		t.Fatalf("runaway window must set X-Consumptions-Truncated: true (truncation is an explicit contract, never silent), got %q", hdr.Get("X-Consumptions-Truncated"))
	}
	if len(got) != len(rows) {
		t.Fatalf("rows = %d, want %d (all actual rows still returned: they fit under the cap)", len(got), len(rows))
	}

	// An unbounded request (no since/until) is by definition beyond the
	// contract: capped branch, flagged.
	code, hdr, got = consumptionsGet(t, e, "")
	if code != 200 {
		t.Fatalf("unbounded status = %d: wanted 200", code)
	}
	if hdr.Get("X-Consumptions-Truncated") != "true" {
		t.Fatalf("unbounded request must set X-Consumptions-Truncated: true, got %q", hdr.Get("X-Consumptions-Truncated"))
	}
	if len(got) != len(rows) {
		t.Fatalf("unbounded rows = %d, want %d", len(got), len(rows))
	}

	// Same 10-year window filtered to one model: 1 key × 1 model × 3 apps
	// = 3 groups ≈ 263k possible rows — within the contract → complete,
	// unflagged.
	qs = fmt.Sprintf("filter_type=model&filter_value=m0&since=%s&until=%s",
		url.QueryEscape(since.Format(time.RFC3339)),
		url.QueryEscape(until.Format(time.RFC3339)))
	code, hdr, got = consumptionsGet(t, e, qs)
	if code != 200 {
		t.Fatalf("filtered status = %d: wanted 200", code)
	}
	if hdr.Get("X-Consumptions-Truncated") != "" {
		t.Fatalf("filtered window must not set X-Consumptions-Truncated (the filter shrank max rows under the cap), got %q", hdr.Get("X-Consumptions-Truncated"))
	}
	if len(got) != 3 {
		t.Fatalf("filtered rows = %d, want 3 (the m0 combos)", len(got))
	}
}
