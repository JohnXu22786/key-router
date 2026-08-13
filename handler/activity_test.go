package handler_test

import (
	"embed"
	"encoding/json"
	"fmt"
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

// bootstrapActivity sets up the app with mock consumption data and returns
// the engine. It inserts two models over two days so the activity endpoint
// has something to aggregate.
func bootstrapActivity(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "data")); err != nil {
		t.Fatal(err)
	}
	db.SetSetting(model.SettingPort, "9999")

	// Provider + key + group + route (minimal, so relays aren't exercised).
	db.GetDB().Create(&model.Provider{Name: "mock", Type: "openai", BaseURL: "http://localhost:1"})
	var prov model.Provider
	db.GetDB().First(&prov)
	db.GetDB().Create(&model.Key{ProviderID: prov.ID, KeyValue: "k", Name: "k1"})
	var key model.Key
	db.GetDB().First(&key)
	db.GetDB().Create(&model.ModelGroup{GroupID: "g1", Name: "G1", Enabled: true})
	db.GetDB().Create(&model.ModelGroup{GroupID: "g2", Name: "G2", Enabled: true})
	var g1, g2 model.ModelGroup
	db.GetDB().Where("group_id = ?", "g1").First(&g1)
	db.GetDB().Where("group_id = ?", "g2").First(&g2)

	// Consumption: two days, two models, plus an "Unknown" (empty model_name) row.
	now := time.Now()
	day1 := time.Date(now.Year(), now.Month(), now.Day()-1, 12, 0, 0, 0, now.Location())
	day2 := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	db.GetDB().Create(&model.Consumption{KeyID: key.ID, HourBucket: day1, ModelName: "g1", RequestCount: 5, InputTokens: 100, OutputTokens: 20, CacheHitTokens: 10, CostUSD: 0.01})
	db.GetDB().Create(&model.Consumption{KeyID: key.ID, HourBucket: day2, ModelName: "g2", RequestCount: 3, InputTokens: 50, OutputTokens: 10, CacheHitTokens: 5, CostUSD: 0.005})
	day1late := day1.Add(time.Hour)
	db.GetDB().Create(&model.Consumption{KeyID: key.ID, HourBucket: day1late, ModelName: "", RequestCount: 1, InputTokens: 10, OutputTokens: 0, CostUSD: 0})

	// Second key with its own spend on g1 so subgroup=key splits a group.
	db.GetDB().Create(&model.Key{ProviderID: prov.ID, KeyValue: "k2", Name: "k2"})
	var key2 model.Key
	db.GetDB().Where("name = ?", "k2").First(&key2)
	db.GetDB().Create(&model.Consumption{KeyID: key2.ID, HourBucket: day1, ModelName: "g1", RequestCount: 7, InputTokens: 200, OutputTokens: 40, CacheHitTokens: 20, CostUSD: 0.02})

	engine := selector.NewEngine()
	checker := health.NewChecker()
	return router.Setup(embed.FS{}, engine, checker, events.NewHub())
}

// TestActivityEdgeCases exercises GetActivity across metrics/groupings/topN
// with mock data, asserting the JSON shape the frontend depends on (series
// buckets/groups, summary min/max/avg/sum/value/percent, buckets continuity).
func TestActivityEdgeCases(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	type tc struct {
		name   string
		qs     string
		expect int
	}
	cases := []tc{
		{"model daily spend top0", "metric=spend&group_by=model&rollup=day", 200},
		{"model daily spend top1", "metric=spend&group_by=model&rollup=day&top=1", 200},
		{"key hourly tokens top5", "metric=tokens&group_by=key&rollup=hour&top=5", 200},
		{"app daily requests", "metric=requests&group_by=app&rollup=day", 200},
		{"cache daily", "metric=cache&group_by=model&rollup=day", 200},
		{"model weekly spend", "metric=spend&group_by=model&rollup=week", 200},
		{"model monthly spend", "metric=spend&group_by=model&rollup=month", 200},
		{"bad metric", "metric=bogus", 400},
		{"bad group_by", "metric=spend&group_by=nope", 400},
		{"bad subgroup", "metric=spend&subgroup=bogus", 400},
		{"subgroup same as group_by", "metric=spend&group_by=model&subgroup=model", 400},
		{"bad rollup", "metric=spend&rollup=minute", 400},
		{"bad since", "metric=spend&since=notadate", 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/stats/activity?"+c.qs, nil)
			req.Host = "localhost:9999" // LocalOnlyMiddleware requires localhost
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != c.expect {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.expect, rec.Body.String())
			}
			if c.expect != 200 {
				return
			}
			// Decode into the shape the frontend consumes.
			var out struct {
				Metric string `json:"metric"`
				Series []struct {
					Bucket string  `json:"bucket"`
					Group  string  `json:"group"`
					Value  float64 `json:"value"`
				} `json:"series"`
				Summary []struct {
					Group   string  `json:"group"`
					Min     float64 `json:"min"`
					Max     float64 `json:"max"`
					Avg     float64 `json:"avg"`
					Sum     float64 `json:"sum"`
					Value   float64 `json:"value"`
					Percent float64 `json:"percent"`
				} `json:"summary"`
				Buckets []string `json:"buckets"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatalf("bad json: %v\n%s", err, rec.Body.String())
			}
			if out.Metric == "" {
				t.Fatalf("metric empty")
			}
			if len(out.Buckets) == 0 {
				t.Fatalf("no buckets")
			}
			for _, s := range out.Summary {
				if s.Avg < 0 || s.Min < 0 || s.Percent < 0 {
					t.Fatalf("negative stat in summary: %+v", s)
				}
			}
			// With top=1 there must be an "Other" series group.
			if c.name == "model daily spend top1" {
				groups := map[string]bool{}
				for _, s := range out.Series {
					groups[s.Group] = true
				}
				if !groups["Other"] {
					t.Fatalf("top=1 should fold into Other; groups=%v", groups)
				}
			}
		})
	}
}

// TestActivitySubgroup exercises the optional second dimension: series must
// be split per (group, subgroup), subgroups ordered by sum desc within each
// group, and "Other" stays a single aggregated stack. The fixture gives g1
// spend on day1 from k2 ($0.02) and k1 ($0.01) — a precise ordering check.
func TestActivitySubgroup(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	now := time.Now()
	// Bucket labels are year-qualified ("YYYY-MM-DD") since the time-scale
	// rework, so the subgroup fixture's day label must match that format.
	day1Label := time.Date(now.Year(), now.Month(), now.Day()-1, 12, 0, 0, 0, now.Location()).Format("2006-01-02")

	req := httptest.NewRequest("GET", "/api/stats/activity?metric=spend&group_by=model&subgroup=key&rollup=day&top=1", nil)
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Series []struct {
			Bucket   string  `json:"bucket"`
			Group    string  `json:"group"`
			Subgroup string  `json:"subgroup"`
			Value    float64 `json:"value"`
		} `json:"series"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec.Body.String())
	}
	// g1 has spend from both k1 and k2 — both must appear as subgroups, and
	// on day1 (their only day) the values must be exact and ordered sum-desc
	// (k2 0.02 before k1 0.01) so the chart stack is deterministic.
	var g1Day1 []float64
	otherWithSubgroup := 0
	for _, s := range out.Series {
		if s.Group == "g1" && s.Bucket == day1Label {
			g1Day1 = append(g1Day1, s.Value)
		}
		if s.Group == "Other" && s.Subgroup != "" {
			otherWithSubgroup++
		}
	}
	if len(g1Day1) != 2 || g1Day1[0] != 0.02 || g1Day1[1] != 0.01 {
		t.Fatalf("g1 day1 subgroup values = %v, want [0.02 0.01] (sum desc)", g1Day1)
	}
	if otherWithSubgroup != 0 {
		t.Fatalf("Other must stay unsplit, got %d subgroup points", otherWithSubgroup)
	}

	// Subgroup must also work with group_by=key and hourly rollup (smoke).
	req2 := httptest.NewRequest("GET", "/api/stats/activity?metric=tokens&group_by=key&subgroup=model&rollup=hour&top=5", nil)
	req2.Host = "localhost:9999"
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("key x model status = %d: %s", rec2.Code, rec2.Body.String())
	}
	var out2 struct {
		Series []struct {
			Subgroup string `json:"subgroup"`
		} `json:"series"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &out2); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec2.Body.String())
	}
	if len(out2.Series) == 0 {
		t.Fatalf("expected series points for group_by=key&subgroup=model")
	}
}

// TestActivityWeekRollupMondayAlignment pins the week-rollup edge case: a
// range starting on a Sunday must open on the PREVIOUS Monday (week buckets
// are labeled by their Monday start date).
func TestActivityWeekRollupMondayAlignment(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	// Aug 16 2026 is a Sunday.
	since := time.Date(2026, 8, 16, 10, 0, 0, 0, time.Local)
	until := since.Add(3 * 24 * time.Hour)
	qs := fmt.Sprintf("metric=spend&group_by=model&rollup=week&since=%s&until=%s",
		url.QueryEscape(since.Format(time.RFC3339)), url.QueryEscape(until.Format(time.RFC3339)))
	req := httptest.NewRequest("GET", "/api/stats/activity?"+qs, nil)
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Buckets []string `json:"buckets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Buckets) == 0 || out.Buckets[0] != "2026-08-10" {
		t.Fatalf("first week bucket = %v, want 2026-08-10 (Monday of the Sunday's week)", out.Buckets)
	}
}
