package handler_test

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"key-router/db"
	"key-router/model"
)

func chathamSpringFixture(t *testing.T) (time.Time, time.Time, time.Time) {
	t.Helper()
	loc, err := time.LoadLocation("Pacific/Chatham")
	if err != nil {
		t.Fatal(err)
	}
	oldLocal := time.Local
	t.Cleanup(func() { time.Local = oldLocal })
	time.Local = loc

	// RecordConsumption's time.Date(03:00) normalizes to the persisted 04:00
	// timestamp on Chatham's spring-forward transition. The row still covers
	// the real 03:45..05:00 run reconstructed by the Activity helpers.
	normalized := time.Date(2024, 9, 29, 3, 0, 0, 0, loc)
	if normalized.Hour() != 4 || normalized.Minute() != 0 {
		t.Fatalf("Chatham normalized bucket = %v, want 04:00", normalized)
	}
	persisted := normalized.In(time.FixedZone("CHADT", 13*60*60+45*60))
	since := time.Date(2024, 9, 29, 3, 45, 0, 0, loc)
	until := time.Date(2024, 9, 29, 3, 50, 0, 0, loc)
	return persisted, since, until
}

func TestActivityPreciseChathamSpringRowDoesNotPanic(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})
	persisted, since, until := chathamSpringFixture(t)
	if err := db.GetDB().Create(&model.Consumption{
		KeyID: 1, HourBucket: persisted, ModelName: "chatham", RequestCount: 75,
	}).Error; err != nil {
		t.Fatal(err)
	}

	qs := fmt.Sprintf("metric=requests&group_by=model&rollup=hour&precise=true&since=%s&until=%s",
		url.QueryEscape(since.Format(time.RFC3339)), url.QueryEscape(until.Format(time.RFC3339)))
	req := httptest.NewRequest("GET", "/api/stats/activity?"+qs, nil)
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Buckets []string           `json:"buckets"`
		Totals  map[string]float64 `json:"totals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Buckets) != 1 || out.Buckets[0] != "2024-09-29 04:00" {
		t.Fatalf("buckets = %v, want the normalized 04:00 bucket", out.Buckets)
	}
	if got := out.Totals["requests"]; got != 5 {
		t.Fatalf("precise requests = %v, want 5 minutes of the 75-minute row", got)
	}
}

func TestStatsConsumptionsIncludesChathamSpringBoundaryRow(t *testing.T) {
	e := bootstrapActivity(t)
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})
	persisted, _, until := chathamSpringFixture(t)
	if err := db.GetDB().Create(&model.Consumption{
		KeyID: 1, HourBucket: persisted, ModelName: "chatham", RequestCount: 75,
	}).Error; err != nil {
		t.Fatal(err)
	}

	since := time.Date(2024, 9, 29, 2, 0, 0, 0, time.Local)
	qs := fmt.Sprintf("since=%s&until=%s",
		url.QueryEscape(since.Format(time.RFC3339)), url.QueryEscape(until.Format(time.RFC3339)))
	req := httptest.NewRequest("GET", "/api/stats/consumptions?"+qs, nil)
	req.Host = "localhost:9999"
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var rows []model.Consumption
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ModelName == "chatham" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("raw consumptions omitted the normalized Chatham boundary row: %+v", rows)
	}
}
