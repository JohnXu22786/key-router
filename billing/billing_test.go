package billing

import (
	"path/filepath"
	"testing"
	"time"

	"key-router/db"
	"key-router/model"
)

// TestGetConsumptionSummaryMidHourSince pins the hour-bucket boundary: the
// summary is queried with an exact-minute `since`, but hour_bucket rows are
// truncated to the local hour. A since in the middle of the hour (e.g. a
// 1h rolling window at 16:20 -> since 15:20) must still match the bucket
// containing it, or the most recent hour's usage silently disappears.
func TestGetConsumptionSummaryMidHourSince(t *testing.T) {
	if err := db.Init(filepath.Join(t.TempDir(), "data")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	prov := &model.Provider{Name: "mock", Type: "openai", BaseURL: "http://localhost:1"}
	if err := db.GetDB().Create(prov).Error; err != nil {
		t.Fatal(err)
	}
	key := &model.Key{ProviderID: prov.ID, KeyValue: "k", Name: "k1"}
	if err := db.GetDB().Create(key).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	hour := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
	c := &model.Consumption{
		KeyID: key.ID, HourBucket: hour, ModelName: "g1",
		RequestCount: 3, InputTokens: 50, OutputTokens: 10, CostUSD: 0.005,
	}
	if err := db.GetDB().Create(c).Error; err != nil {
		t.Fatal(err)
	}

	// The row's bucket is `hour`, but the query range starts 20 minutes in.
	since := hour.Add(20 * time.Minute)
	rows, err := GetConsumptionSummary(key.ID, since, since.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1 (mid-hour since must match the truncated hour bucket)", len(rows))
	}
	if rows[0].RequestCount != 3 || rows[0].CostUSD != 0.005 {
		t.Fatalf("row = %+v, want the %s-bucket values", rows[0], hour.Format("15:04"))
	}
}
