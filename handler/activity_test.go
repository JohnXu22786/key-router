package handler_test

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
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
	// It also carries an app name so app filters discriminate against the
	// empty-app_name rows.
	db.GetDB().Create(&model.Key{ProviderID: prov.ID, KeyValue: "k2", Name: "k2"})
	var key2 model.Key
	db.GetDB().Where("name = ?", "k2").First(&key2)
	db.GetDB().Create(&model.Consumption{KeyID: key2.ID, HourBucket: day1, ModelName: "g1", AppName: "testapp", RequestCount: 7, InputTokens: 200, OutputTokens: 40, CacheHitTokens: 20, CostUSD: 0.02})

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
		{"blended daily", "metric=blended&group_by=model&rollup=day", 200},
		{"blended hourly top5", "metric=blended&group_by=model&rollup=hour&top=5", 200},
		{"blended ranked by blended", "metric=blended&group_by=model&rollup=day&rank_by=blended", 200},
		{"blended ranked by spend", "metric=blended&group_by=model&rollup=day&rank_by=spend", 200},
		{"spend ranked by blended", "metric=spend&group_by=model&rollup=day&rank_by=blended", 200},
		{"model weekly spend", "metric=spend&group_by=model&rollup=week", 200},
		{"model monthly spend", "metric=spend&group_by=model&rollup=month", 200},
		{"model total spend", "metric=spend&group_by=model&rollup=total", 200},
		{"rank by requests", "metric=spend&group_by=model&rollup=day&rank_by=requests", 200},
		{"rank by cache", "metric=spend&group_by=model&rollup=day&rank_by=cache", 200},
		{"total rollup with rank_by", "metric=spend&group_by=model&rollup=total&rank_by=tokens", 200},
		{"total rollup with subgroup", "metric=spend&group_by=model&rollup=total&subgroup=key", 200},
		{"bad metric", "metric=bogus", 400},
		{"bad group_by", "metric=spend&group_by=nope", 400},
		{"bad subgroup", "metric=spend&subgroup=bogus", 400},
		{"subgroup same as group_by", "metric=spend&group_by=model&subgroup=model", 400},
		{"bad rollup", "metric=spend&rollup=minute", 400},
		{"bad rank_by", "metric=spend&rank_by=bogus", 400},
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

// TestActivityTotalRollup pins the rollup=total behavior: the whole range
// collapses into a single "Total" bucket, every group's series value equals
// its range total, and the summary's Value (last bucket) equals its Sum.
// since/until are pinned so the fixture rows (yesterday + today noon) are
// always in range regardless of the wall-clock time the test runs at.
func TestActivityTotalRollup(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})
	qs := "metric=spend&group_by=model&rollup=total&" + rangeQuery(t)

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
		Summary []summaryRow `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec.Body.String())
	}
	if len(out.Buckets) != 1 || out.Buckets[0] != "Total" {
		t.Fatalf("buckets = %v, want [Total]", out.Buckets)
	}
	got := map[string]float64{}
	for _, s := range out.Series {
		if s.Bucket != "Total" {
			t.Fatalf("series bucket = %q, want Total", s.Bucket)
		}
		got[s.Group] += s.Value
	}
	if got["g1"] != 0.03 || got["g2"] != 0.005 || got["Unknown"] != 0 {
		t.Fatalf("total-bucket series values = %v, want g1=0.03 g2=0.005 Unknown=0", got)
	}
	for _, s := range out.Summary {
		if s.Value != s.Sum {
			t.Fatalf("group %s: Value %v != Sum %v (single bucket)", s.Group, s.Value, s.Sum)
		}
	}

	// With top=1 the runner-up groups fold into Other (single bucket).
	req2 := httptest.NewRequest("GET", "/api/stats/activity?metric=spend&group_by=model&rollup=total&top=1&"+rangeQuery(t), nil)
	req2.Host = "localhost:9999"
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("top=1 status = %d: %s", rec2.Code, rec2.Body.String())
	}
	var out2 struct {
		Series []struct {
			Group  string  `json:"group"`
			Bucket string  `json:"bucket"`
			Value  float64 `json:"value"`
		} `json:"series"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &out2); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec2.Body.String())
	}
	other := 0.0
	topSeen := ""
	for _, s := range out2.Series {
		if s.Bucket != "Total" {
			t.Fatalf("series bucket = %q, want Total", s.Bucket)
		}
		if s.Group == "Other" {
			other += s.Value
		} else if topSeen == "" {
			topSeen = s.Group
		}
	}
	if topSeen != "g1" {
		t.Fatalf("top group = %q, want g1 (spend rank)", topSeen)
	}
	if other != 0.005 {
		t.Fatalf("Other value = %v, want 0.005 (g2 + Unknown)", other)
	}
}

// TestActivityTotalRollupExactRange pins that rollup=total aggregates ONLY
// the requested since..until (widened to the endpoint local-hour bucket
// boundaries) into the single "Total" bucket: consumption rows inside the
// same calendar month but OUTSIDE the range must NOT leak in. Regression
// for activityWindow treating total as the month rollup, which widened the
// query to the whole containing month(s) and inflated the Total bucket with
// out-of-range usage. The window is pinned to 2020-01 so no wall-clock
// fixture row (yesterday/today noon) can land in it — assertions are exact.
func TestActivityTotalRollupExactRange(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})
	loc := time.Local
	since := time.Date(2020, 1, 13, 0, 0, 0, 0, loc)
	until := time.Date(2020, 1, 20, 23, 59, 59, 0, loc)
	var key model.Key
	if err := db.GetDB().Where("name = ?", "k1").First(&key).Error; err != nil {
		t.Fatal(err)
	}
	// In-range rows (distinct hours satisfy the (key_id, hour_bucket) key).
	db.GetDB().Create(&model.Consumption{KeyID: key.ID, HourBucket: time.Date(2020, 1, 15, 12, 0, 0, 0, loc), ModelName: "g1", RequestCount: 1, CostUSD: 0.10})
	db.GetDB().Create(&model.Consumption{KeyID: key.ID, HourBucket: time.Date(2020, 1, 16, 12, 0, 0, 0, loc), ModelName: "g3", RequestCount: 1, CostUSD: 0.20})
	// OUT-OF-RANGE rows in the SAME month: the buggy month-widened window
	// (Jan 1..Feb 1) would pull these into the Total bucket too.
	db.GetDB().Create(&model.Consumption{KeyID: key.ID, HourBucket: time.Date(2020, 1, 5, 12, 0, 0, 0, loc), ModelName: "g1", RequestCount: 1, CostUSD: 0.50})
	db.GetDB().Create(&model.Consumption{KeyID: key.ID, HourBucket: time.Date(2020, 1, 25, 12, 0, 0, 0, loc), ModelName: "g1", RequestCount: 1, CostUSD: 1.00})

	qs := fmt.Sprintf("metric=spend&group_by=model&rollup=total&since=%s&until=%s",
		url.QueryEscape(since.Format(time.RFC3339)), url.QueryEscape(until.Format(time.RFC3339)))
	req := httptest.NewRequest("GET", "/api/stats/activity?"+qs, nil)
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Totals  map[string]float64 `json:"totals"`
		Buckets []string           `json:"buckets"`
		Series  []struct {
			Group string  `json:"group"`
			Value float64 `json:"value"`
		} `json:"series"`
		Summary []struct {
			Group string  `json:"group"`
			Sum   float64 `json:"sum"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec.Body.String())
	}
	if len(out.Buckets) != 1 || out.Buckets[0] != "Total" {
		t.Fatalf("buckets = %v, want [Total]", out.Buckets)
	}
	// Only the in-range rows (0.10 + 0.20) may contribute — the out-of-range
	// January rows (0.50 + 1.00) must stay out even though the buggy
	// month-widened window contained them.
	if math.Abs(out.Totals["spend"]-0.30) > 0.000001 {
		t.Fatalf("totals.spend = %v, want 0.30 (in-range rows only, not 1.80)", out.Totals["spend"])
	}
	series := map[string]float64{}
	for _, s := range out.Series {
		series[s.Group] += s.Value
	}
	if series["g1"] != 0.10 || series["g3"] != 0.20 {
		t.Fatalf("total-bucket series = %v, want g1=0.10 g3=0.20", series)
	}
	for _, s := range out.Summary {
		if s.Group == "g1" && s.Sum != 0.10 {
			t.Fatalf("g1 summary Sum = %v, want 0.10", s.Sum)
		}
		if s.Group == "g3" && s.Sum != 0.20 {
			t.Fatalf("g3 summary Sum = %v, want 0.20", s.Sum)
		}
	}
}

// TestActivityRankBy pins the rank_by param: series (top-N + Other folding)
// and the summary's default order are ranked by the requested METRIC, which
// may differ from the charted metric. An extra high-request/low-spend g2 row
// makes the metric ranks diverge from the spend rank.
func TestActivityRankBy(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	// g2 gains 100 requests at negligible cost on day1 (a distinct hour so
	// the (key_id, hour_bucket, model_name, app_name) unique index is
	// satisfied), so request rank (g2 >= 100 > g1 12) opposes spend rank
	// (g1 0.03 > g2 ~0.0051).
	var k1 model.Key
	db.GetDB().Where("name = ?", "k1").First(&k1)
	now := time.Now()
	day1 := time.Date(now.Year(), now.Month(), now.Day()-1, 12, 0, 0, 0, now.Location())
	db.GetDB().Create(&model.Consumption{KeyID: k1.ID, HourBucket: day1.Add(2 * time.Hour), ModelName: "g2", RequestCount: 100, InputTokens: 0, OutputTokens: 0, CostUSD: 0.0001})

	req := httptest.NewRequest("GET", "/api/stats/activity?metric=spend&group_by=model&rollup=day&rank_by=requests&top=1&"+rangeQuery(t), nil)
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Summary []summaryRow `json:"summary"`
		Series  []struct {
			Group string  `json:"group"`
			Value float64 `json:"value"`
		} `json:"series"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec.Body.String())
	}
	if len(out.Summary) != 3 || out.Summary[0].Group != "g2" || out.Summary[1].Group != "g1" {
		t.Fatalf("summary order = %v, want [g2 g1 Unknown] (request rank)", summaryGroups(out.Summary))
	}
	// The chart metric is still spend: series values must be spend sums.
	// The top group's series value must equal its summary Sum (checked
	// self-consistently, no float-literal trap).
	var g2Series, g2Sum float64
	firstGroup := out.Series[0].Group
	for _, s := range out.Series {
		if s.Group == firstGroup {
			g2Series += s.Value
		}
	}
	for _, s := range out.Summary {
		if s.Group == firstGroup {
			g2Sum = s.Sum
		}
	}
	if g2Series != g2Sum {
		t.Fatalf("top series %q value %v != its summary Sum %v (series must carry the chart metric)", firstGroup, g2Series, g2Sum)
	}
	otherSum := 0.0
	for _, s := range out.Series {
		if s.Group == "Other" {
			otherSum += s.Value
		}
	}
	if firstGroup != "g2" {
		t.Fatalf("first series group = %q, want g2 (top-1 by requests)", firstGroup)
	}
	if otherSum != 0.03 {
		t.Fatalf("Other spend = %v, want 0.03 (g1 + Unknown folded)", otherSum)
	}

	// Without rank_by the default stays the chart metric's sum (g1 first).
	req2 := httptest.NewRequest("GET", "/api/stats/activity?metric=spend&group_by=model&rollup=day&"+rangeQuery(t), nil)
	req2.Host = "localhost:9999"
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("status = %d: %s", rec2.Code, rec2.Body.String())
	}
	var out2 struct {
		Summary []summaryRow `json:"summary"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &out2); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec2.Body.String())
	}
	if out2.Summary[0].Group != "g1" {
		t.Fatalf("default summary order starts with %q, want g1 (spend sum)", out2.Summary[0].Group)
	}

	// rank_by=tokens with the same fixture ranks by token totals (g1 first).
	req3 := httptest.NewRequest("GET", "/api/stats/activity?metric=spend&group_by=model&rollup=day&rank_by=tokens&"+rangeQuery(t), nil)
	req3.Host = "localhost:9999"
	rec3 := httptest.NewRecorder()
	e.ServeHTTP(rec3, req3)
	if rec3.Code != 200 {
		t.Fatalf("status = %d: %s", rec3.Code, rec3.Body.String())
	}
	var out3 struct {
		Summary []summaryRow `json:"summary"`
	}
	if err := json.Unmarshal(rec3.Body.Bytes(), &out3); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec3.Body.String())
	}
	if out3.Summary[0].Group != "g1" || out3.Summary[1].Group != "g2" {
		t.Fatalf("rank_by=tokens order = %v, want [g1 g2 Unknown]", summaryGroups(out3.Summary))
	}
}

// summaryRow is the summary slice shape the activity endpoint returns.
type summaryRow struct {
	Group   string  `json:"group"`
	Sum     float64 `json:"sum"`
	Value   float64 `json:"value"`
	Percent float64 `json:"percent"`
}

// TestActivityBlended pins the blended $/1M metric (the Overview "Blended
// $/1M" KPI's Explore target): series values are per-bucket RATES
// (spend/tokens*1e6 — never a sum of row rates), the summary Sum is the
// group's overall rate over the whole range (which the fixture forces to
// DIFFER from the sum of its per-bucket rates), Min/Max/Avg span the
// per-bucket rates, Value is the last bucket's rate, Percent is the group's
// share of total SPEND (rates don't add up to a total), and the "Other"
// fold keeps a combined rate of the folded groups.
//
// Fixture on top of bootstrapActivity: g2 gains a pricier day2 row
// ($0.006/60 tok, day1's g1 rows are $0.03/360 tok) and g1 gains a cheap
// day2 row ($0.003/30 tok = rate 100) plus a pricey day1 row on k2
// ($0.04/100 tok) so the per-bucket and per-subgroup rates diverge:
//
//	day1: g1 = 0.07/460 = 152.17, g2 = 0.012/60 = 200
//	day2: g1 = 0.003/30 = 100,  g2 = 0.011/120 = 91.67
//	g1 overall = 0.073/490 = 148.98 (sum of its bucket rates = 252.17)
//	g2 overall = 0.023/180 = 127.78 (sum of its bucket rates = 291.67)
//	k2 subgroup = 0.06/340 = 176.47 (sum of its row rates = 483.33)
//
// g2's tiny high-rate day1 row flips the ranking under a buggy
// "sum of per-bucket rates" rank: that would order [g2 g1] (291.67 >
// 252.17), while the correct overall-rate ranking is [g1 g2] (148.98 >
// 127.78) — so the summary ORDER assertion pins the rank semantics too.
func TestActivityBlended(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	var k1, k2 model.Key
	db.GetDB().Where("name = ?", "k1").First(&k1)
	db.GetDB().Where("name = ?", "k2").First(&k2)
	now := time.Now()
	day1 := time.Date(now.Year(), now.Month(), now.Day()-1, 12, 0, 0, 0, now.Location())
	day2 := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	day1Label := day1.Format("2006-01-02")
	day2Label := day2.Format("2006-01-02")
	// Distinct hours satisfy the (key_id, hour_bucket, model_name, app_name)
	// unique index.
	db.GetDB().Create(&model.Consumption{KeyID: k1.ID, HourBucket: day2.Add(2 * time.Hour), ModelName: "g2", RequestCount: 1, InputTokens: 50, OutputTokens: 10, CacheHitTokens: 0, CostUSD: 0.006})
	db.GetDB().Create(&model.Consumption{KeyID: k1.ID, HourBucket: day2.Add(3 * time.Hour), ModelName: "g1", RequestCount: 1, InputTokens: 25, OutputTokens: 5, CacheHitTokens: 0, CostUSD: 0.003})
	db.GetDB().Create(&model.Consumption{KeyID: k2.ID, HourBucket: day1.Add(4 * time.Hour), ModelName: "g1", RequestCount: 2, InputTokens: 80, OutputTokens: 20, CacheHitTokens: 0, CostUSD: 0.04})
	db.GetDB().Create(&model.Consumption{KeyID: k1.ID, HourBucket: day1.Add(2 * time.Hour), ModelName: "g2", RequestCount: 1, InputTokens: 50, OutputTokens: 10, CacheHitTokens: 0, CostUSD: 0.012})

	req := httptest.NewRequest("GET", "/api/stats/activity?metric=blended&group_by=model&rollup=day&"+rangeQuery(t), nil)
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
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
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec.Body.String())
	}

	eps := 0.01
	seriesBy := map[string]map[string]float64{} // group -> bucket -> value
	for _, s := range out.Series {
		if seriesBy[s.Group] == nil {
			seriesBy[s.Group] = map[string]float64{}
		}
		seriesBy[s.Group][s.Bucket] = s.Value
	}
	if v := seriesBy["g1"][day1Label]; math.Abs(v-152.173913) > eps {
		t.Fatalf("g1 %s blended = %v, want 152.17 (0.07/460)", day1Label, v)
	}
	if v := seriesBy["g1"][day2Label]; math.Abs(v-100) > eps {
		t.Fatalf("g1 %s blended = %v, want 100 (0.003/30)", day2Label, v)
	}
	if v := seriesBy["g2"][day1Label]; math.Abs(v-200) > eps {
		t.Fatalf("g2 %s blended = %v, want 200 (0.012/60)", day1Label, v)
	}
	if v := seriesBy["g2"][day2Label]; math.Abs(v-91.666667) > eps {
		t.Fatalf("g2 %s blended = %v, want 91.67 (0.011/120)", day2Label, v)
	}
	if v := seriesBy["Unknown"][day1Label]; v != 0 {
		t.Fatalf("Unknown blended = %v, want 0 (zero spend)", v)
	}

	// Summary: ranked by OVERALL rate desc — g1 148.98 beats g2 127.78. A
	// buggy sum-of-per-bucket-rates rank would order [g2 g1] (291.67 >
	// 252.17), so this order assertion pins the rank semantics. Sum must be
	// the overall rate, not 252.17/291.67.
	got := map[string]struct{ min, max, avg, sum, value, pct float64 }{}
	var order []string
	for _, s := range out.Summary {
		got[s.Group] = struct{ min, max, avg, sum, value, pct float64 }{s.Min, s.Max, s.Avg, s.Sum, s.Value, s.Percent}
		order = append(order, s.Group)
	}
	if len(order) != 3 || order[0] != "g1" || order[1] != "g2" || order[2] != "Unknown" {
		t.Fatalf("summary order = %v, want [g1 g2 Unknown] (overall-rate rank)", order)
	}
	g1 := got["g1"]
	if math.Abs(g1.sum-148.979592) > eps {
		t.Fatalf("g1 Sum = %v, want 148.98 (overall rate 0.073/490, NOT 252.17)", g1.sum)
	}
	if math.Abs(g1.avg-126.086957) > eps {
		t.Fatalf("g1 Avg = %v, want 126.09 (mean of bucket rates)", g1.avg)
	}
	if math.Abs(g1.min-100) > eps || math.Abs(g1.max-152.173913) > eps {
		t.Fatalf("g1 Min/Max = %v/%v, want 100/152.17 (per-bucket rates)", g1.min, g1.max)
	}
	if math.Abs(g1.value-100) > eps {
		t.Fatalf("g1 Value = %v, want 100 (last bucket's rate)", g1.value)
	}
	if math.Abs(g1.pct-76.041667) > eps {
		t.Fatalf("g1 Percent = %v, want 76.04 (share of total spend)", g1.pct)
	}
	g2 := got["g2"]
	if math.Abs(g2.sum-127.777778) > eps {
		t.Fatalf("g2 Sum = %v, want 127.78 (overall rate 0.023/180, NOT 291.67)", g2.sum)
	}
	if math.Abs(g2.avg-145.833333) > eps {
		t.Fatalf("g2 Avg = %v, want 145.83 (mean of bucket rates)", g2.avg)
	}
	if math.Abs(g2.min-91.666667) > eps || math.Abs(g2.max-200) > eps {
		t.Fatalf("g2 Min/Max = %v/%v, want 91.67/200 (per-bucket rates)", g2.min, g2.max)
	}
	if math.Abs(g2.value-91.666667) > eps {
		t.Fatalf("g2 Value = %v, want 91.67 (last bucket's rate)", g2.value)
	}
	if math.Abs(g2.pct-23.958333) > eps {
		t.Fatalf("g2 Percent = %v, want 23.96 (share of total spend)", g2.pct)
	}
	if got["Unknown"].sum != 0 || got["Unknown"].pct != 0 {
		t.Fatalf("Unknown stats = %+v, want zeros", got["Unknown"])
	}

	// top=1: the folded Other series keeps a per-bucket COMBINED rate of the
	// folded groups (spend and tokens summed first, rate derived last) —
	// never a sum of row rates. day1 folds g2 + Unknown: (0.012+0)/(60+10)
	// = 171.43 (a naive sum of g2's row rates would read 200); day2 folds g2
	// alone: 0.011/120 = 91.67 (naive: 83.33 + 100 = 183.33).
	req2 := httptest.NewRequest("GET", "/api/stats/activity?metric=blended&group_by=model&rollup=day&top=1&"+rangeQuery(t), nil)
	req2.Host = "localhost:9999"
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("top=1 status = %d: %s", rec2.Code, rec2.Body.String())
	}
	var out2 struct {
		Series []struct {
			Group  string  `json:"group"`
			Bucket string  `json:"bucket"`
			Value  float64 `json:"value"`
		} `json:"series"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &out2); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec2.Body.String())
	}
	other := map[string]float64{}
	for _, s := range out2.Series {
		if s.Group == "Other" {
			other[s.Bucket] = s.Value
		}
	}
	if len(other) == 0 {
		t.Fatalf("no Other series with top=1")
	}
	if v := other[day2Label]; math.Abs(v-91.666667) > eps {
		t.Fatalf("Other %s blended = %v, want 91.67 (0.011/120 combined)", day2Label, v)
	}
	if v := other[day1Label]; math.Abs(v-171.428571) > eps {
		t.Fatalf("Other %s blended = %v, want 171.43 (0.012/70 combined)", day1Label, v)
	}

	// subgroup=key: each subgroup's rate from ITS spend/tokens only. k2's
	// combined day1 rate (0.06/340 = 176.47) must not be the sum of its row
	// rates (83.33 + 400 = 483.33).
	req3 := httptest.NewRequest("GET", "/api/stats/activity?metric=blended&group_by=model&subgroup=key&rollup=day&"+rangeQuery(t), nil)
	req3.Host = "localhost:9999"
	rec3 := httptest.NewRecorder()
	e.ServeHTTP(rec3, req3)
	if rec3.Code != 200 {
		t.Fatalf("subgroup status = %d: %s", rec3.Code, rec3.Body.String())
	}
	var out3 struct {
		Series []struct {
			Group    string  `json:"group"`
			Subgroup string  `json:"subgroup"`
			Bucket   string  `json:"bucket"`
			Value    float64 `json:"value"`
		} `json:"series"`
	}
	if err := json.Unmarshal(rec3.Body.Bytes(), &out3); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec3.Body.String())
	}
	subBy := map[string]float64{}
	for _, s := range out3.Series {
		if s.Group == "g1" && s.Bucket == day1Label {
			subBy[s.Subgroup] = s.Value
		}
	}
	if math.Abs(subBy["k1"]-83.333333) > eps || math.Abs(subBy["k2"]-176.470588) > eps {
		t.Fatalf("g1 day1 subgroup rates = %v, want k1=83.33 k2=176.47", subBy)
	}
}

func summaryGroups(s []summaryRow) []string {
	out := make([]string, len(s))
	for i := range s {
		out[i] = s[i].Group
	}
	return out
}

// rangeQuery pins since/until to the last two days (start of day minus one
// day through end of today), so the fixture's yesterday-noon and today-noon
// rows are always in range no matter what wall-clock time the suite runs at.
func rangeQuery(t *testing.T) string {
	t.Helper()
	now := time.Now()
	since := time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, now.Location())
	until := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location())
	return fmt.Sprintf("since=%s&until=%s",
		url.QueryEscape(since.Format(time.RFC3339)), url.QueryEscape(until.Format(time.RFC3339)))
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

// activityGet issues an activity request and decodes totals + summary rows.
func activityGet(t *testing.T, e *gin.Engine, qs string) (int, map[string]float64, []struct {
	Group string  `json:"group"`
	Sum   float64 `json:"sum"`
}) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/stats/activity?"+qs, nil)
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		return rec.Code, nil, nil
	}
	var out struct {
		Totals  map[string]float64 `json:"totals"`
		Summary []struct {
			Group string  `json:"group"`
			Sum   float64 `json:"sum"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad json: %v\n%s", err, rec.Body.String())
	}
	return rec.Code, out.Totals, out.Summary
}

// summarySumOf returns a summary row's Sum by group name (0 when absent).
func summarySumOf(rows []struct {
	Group string  `json:"group"`
	Sum   float64 `json:"sum"`
}, group string) float64 {
	for _, r := range rows {
		if r.Group == group {
			return r.Sum
		}
	}
	return 0
}

// TestActivityFilter exercises the entity filter (filter_type/filter_value)
// on the activity endpoint: rows outside the filter must not contribute to
// totals or summary, and "Unknown" matches rows with an empty name.
// Fixture (bootstrapActivity): g1 spend $0.03 via k1 ($0.01) + k2 ($0.02,
// app "testapp"); g2 spend $0.005 via k1; one empty-model_name row ($0).
func TestActivityFilter(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	var key2 model.Key
	if err := db.GetDB().Where("name = ?", "k2").First(&key2).Error; err != nil {
		t.Fatal(err)
	}

	// Explicit range covering the fixture rows (yesterday + today noon): the
	// default since..until window ("now-7d .. now") would drop today's row
	// when the suite runs before noon.
	now := time.Now()
	since := time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, now.Location())
	until := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	rangeQS := fmt.Sprintf("since=%s&until=%s",
		url.QueryEscape(since.Format(time.RFC3339)), url.QueryEscape(until.Format(time.RFC3339)))

	// filter_type=model&filter_value=g1 -> only the two g1 rows ($0.01 + $0.02).
	code, totals, rows := activityGet(t, e, "metric=spend&group_by=key&rollup=day&filter_type=model&filter_value=g1&"+rangeQS)
	if code != 200 {
		t.Fatalf("model filter status = %d", code)
	}
	if totals["spend"] != 0.03 {
		t.Fatalf("model=g1 totals.spend = %v, want 0.03", totals["spend"])
	}
	if len(rows) != 2 || summarySumOf(rows, "k1") != 0.01 || summarySumOf(rows, "k2") != 0.02 {
		t.Fatalf("model=g1 summary = %+v, want k1 0.01 + k2 0.02 only", rows)
	}

	// filter_type=key&filter_value=<k2 id> -> only k2's row ($0.02).
	code, totals, rows = activityGet(t, e, fmt.Sprintf("metric=spend&group_by=model&rollup=day&filter_type=key&filter_value=%d&%s", key2.ID, rangeQS))
	if code != 200 {
		t.Fatalf("key filter status = %d", code)
	}
	if totals["spend"] != 0.02 || summarySumOf(rows, "g1") != 0.02 {
		t.Fatalf("key=k2 totals=%v rows=%+v, want spend 0.02 on g1 only", totals, rows)
	}

	// filter_type=app&filter_value=testapp -> only k2's row.
	code, totals, rows = activityGet(t, e, "metric=spend&group_by=model&rollup=day&filter_type=app&filter_value=testapp&"+rangeQS)
	if code != 200 {
		t.Fatalf("app filter status = %d", code)
	}
	if totals["spend"] != 0.02 || len(rows) != 1 || rows[0].Group != "g1" {
		t.Fatalf("app=testapp totals=%v rows=%+v, want the k2 row only", totals, rows)
	}

	// filter_type=app&filter_value=Unknown -> all rows with an EMPTY app name
	// (k1's g1 row + the g2 row + the empty-model row): 5+3+1 = 9 requests.
	code, totals, rows = activityGet(t, e, "metric=requests&group_by=model&rollup=day&filter_type=app&filter_value=Unknown&"+rangeQS)
	if code != 200 {
		t.Fatalf("app=Unknown status = %d", code)
	}
	if totals["requests"] != 9 {
		t.Fatalf("app=Unknown totals.requests = %v, want 9 (empty app_name rows only)", totals["requests"])
	}
	if summarySumOf(rows, "Unknown") != 1 || summarySumOf(rows, "g2") != 3 {
		t.Fatalf("app=Unknown summary = %+v, want g2 3 + Unknown 1", rows)
	}

	// filter_type=model&filter_value=Unknown -> the empty-model_name row only.
	code, totals, rows = activityGet(t, e, "metric=requests&group_by=model&rollup=day&filter_type=model&filter_value=Unknown&"+rangeQS)
	if code != 200 {
		t.Fatalf("model=Unknown status = %d", code)
	}
	if totals["requests"] != 1 || len(rows) != 1 || rows[0].Group != "Unknown" {
		t.Fatalf("model=Unknown totals=%v rows=%+v, want the empty-model_name row only", totals, rows)
	}

	// Bad filters -> 400.
	for _, qs := range []string{
		"metric=spend&filter_type=bogus&filter_value=x",
		"metric=spend&filter_type=model",
		"metric=spend&filter_type=model&filter_value=",
		"metric=spend&filter_type=key&filter_value=abc",
		"metric=spend&filter_value=x",
	} {
		if code, _, _ := activityGet(t, e, qs); code != 400 {
			t.Fatalf("qs %q status = %d, want 400", qs, code)
		}
	}
}

// TestConsumptionsFilter pins the same filter on the raw-consumption
// endpoint (the Overview tab's data source).
func TestConsumptionsFilter(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	get := func(qs string) (int, []model.Consumption) {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/stats/consumptions?"+qs, nil)
		req.Host = "localhost:9999"
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != 200 {
			return rec.Code, nil
		}
		var rows []model.Consumption
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatalf("bad json: %v\n%s", err, rec.Body.String())
		}
		return rec.Code, rows
	}

	// Explicit range covering the fixture rows (see TestActivityFilter): the
	// default since..until window drops today's noon row before noon.
	now := time.Now()
	since := time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, now.Location())
	until := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	rangeQS := fmt.Sprintf("since=%s&until=%s",
		url.QueryEscape(since.Format(time.RFC3339)), url.QueryEscape(until.Format(time.RFC3339)))

	code, rows := get("filter_type=model&filter_value=g2&" + rangeQS)
	if code != 200 || len(rows) != 1 || rows[0].ModelName != "g2" {
		t.Fatalf("model=g2: code=%d rows=%d, want exactly the g2 row", code, len(rows))
	}
	code, rows = get("filter_type=app&filter_value=testapp&" + rangeQS)
	if code != 200 || len(rows) != 1 || rows[0].ModelName != "g1" {
		t.Fatalf("app=testapp: code=%d rows=%d, want exactly the k2 g1 row", code, len(rows))
	}
	if code, _ := get("filter_type=nope"); code != 400 {
		t.Fatalf("bad filter_type status = %d, want 400", code)
	}
}
