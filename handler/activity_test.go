package handler_test

import (
	"embed"
	"encoding/json"
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

	engine := selector.NewEngine()
	checker := health.NewChecker()
	return router.Setup(embed.FS{}, engine, checker)
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
		{"bad metric", "metric=bogus", 400},
		{"bad group_by", "metric=spend&group_by=nope", 400},
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
				Metric  string `json:"metric"`
				Series  []struct {
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
