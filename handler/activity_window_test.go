package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"key-router/db"
)

// TestActivityShortRangeMidHour pins the 15m/30m preset regression: the
// frontend queries with since/until at exact-minute precision (e.g. a 15m
// window at 16:20 -> since 16:05), but hour_bucket rows are truncated to the
// LOCAL hour. The query must include the bucket CONTAINING since (16:00),
// or a short window that spans only the current hour returns nothing for
// most of the hour. Querying a mid-hour window must yield the current-hour
// bucket with its data.
func TestActivityShortRangeMidHour(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	now := time.Now()
	// bootstrapActivity's g1 spend rows live at yesterday 12:00 (k1 $0.01,
	// k2 $0.02). A 15m-style window at 12:10..12:30 must still return them.
	hour := time.Date(now.Year(), now.Month(), now.Day()-1, 12, 0, 0, 0, now.Location())
	qs := fmt.Sprintf("metric=spend&group_by=model&rollup=hour&since=%s&until=%s",
		url.QueryEscape(hour.Add(10*time.Minute).Format(time.RFC3339)),
		url.QueryEscape(hour.Add(30*time.Minute).Format(time.RFC3339)))
	req := httptest.NewRequest("GET", "/api/stats/activity?"+qs, nil)
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Buckets []string `json:"buckets"`
		Series  []struct {
			Bucket string  `json:"bucket"`
			Group  string  `json:"group"`
			Value  float64 `json:"value"`
		} `json:"series"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	wantBucket := hour.Format("2006-01-02 15:00")
	if len(out.Buckets) != 1 || out.Buckets[0] != wantBucket {
		t.Fatalf("buckets = %v, want [%s] (the hour containing since)", out.Buckets, wantBucket)
	}
	// g1's two rows at 12:00 (k1 $0.01 + k2 $0.02) must land in that bucket;
	// the "Unknown" row at 13:00 stays outside the window.
	g1 := 0.0
	for _, s := range out.Series {
		if s.Group == "g1" {
			g1 += s.Value
		}
	}
	if g1 != 0.03 {
		t.Fatalf("g1 spend in %s = %v, want 0.03 (mid-hour window must include the bucket containing since)", wantBucket, g1)
	}
}

// TestActivityDayRollupBoundaryBuckets: for day/week/month rollups the axis
// spans the buckets CONTAINING since and until (the boundary days are shown
// whole), so the query must return all rows of those days — not just the
// rows inside [since, until]. A 20-minute window on day1 must aggregate the
// full day1 bucket.
func TestActivityDayRollupBoundaryBuckets(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	now := time.Now()
	day1 := time.Date(now.Year(), now.Month(), now.Day()-1, 12, 0, 0, 0, now.Location())
	// Requests metric: day1 has g1 5+7 and an Unknown 1 (at 13:00, AFTER the
	// window's until — the day bucket must still include it).
	qs := fmt.Sprintf("metric=requests&group_by=model&rollup=day&since=%s&until=%s",
		url.QueryEscape(day1.Add(10*time.Minute).Format(time.RFC3339)),
		url.QueryEscape(day1.Add(30*time.Minute).Format(time.RFC3339)))
	req := httptest.NewRequest("GET", "/api/stats/activity?"+qs, nil)
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Buckets []string `json:"buckets"`
		Series  []struct {
			Bucket string  `json:"bucket"`
			Group  string  `json:"group"`
			Value  float64 `json:"value"`
		} `json:"series"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	wantDay := day1.Format("2006-01-02")
	if len(out.Buckets) != 1 || out.Buckets[0] != wantDay {
		t.Fatalf("buckets = %v, want [%s]", out.Buckets, wantDay)
	}
	byGroup := map[string]float64{}
	for _, s := range out.Series {
		if s.Bucket == wantDay {
			byGroup[s.Group] = s.Value
		}
	}
	if byGroup["g1"] != 12 || byGroup["Unknown"] != 1 {
		t.Fatalf("day %s requests = %v, want g1:12 Unknown:1 (boundary day buckets must be complete)", wantDay, byGroup)
	}
}

// TestStatsConsumptionsMidHourSince: /api/stats/consumptions filters
// hour_bucket >= since, so a since in the middle of the hour must still
// match the truncated current-hour bucket.
func TestStatsConsumptionsMidHourSince(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	now := time.Now()
	hour := time.Date(now.Year(), now.Month(), now.Day()-1, 12, 0, 0, 0, now.Location())
	qs := fmt.Sprintf("since=%s&until=%s",
		url.QueryEscape(hour.Add(10*time.Minute).Format(time.RFC3339)),
		url.QueryEscape(hour.Add(30*time.Minute).Format(time.RFC3339)))
	req := httptest.NewRequest("GET", "/api/stats/consumptions?"+qs, nil)
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var out []struct {
		HourBucket time.Time `json:"hour_bucket"`
		CostUSD    float64   `json:"cost_usd"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("consumptions = %d rows, want 2 (the 12:00 bucket's rows, k1+k2)", len(out))
	}
	for _, r := range out {
		if !r.HourBucket.Equal(hour) {
			t.Fatalf("hour_bucket = %v, want %v", r.HourBucket, hour)
		}
	}
}
