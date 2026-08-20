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
	key := setupBillingDB(t)

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

// setupBillingDB opens a fresh DB (db.Init also runs the startup migrations)
// and returns the first key created, so tests can record consumption against
// a real row like production does.
func setupBillingDB(t *testing.T) model.Key {
	t.Helper()
	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "data")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	db.GetDB().Create(&model.Provider{Name: "mock", Type: "openai", BaseURL: "http://localhost:1"})
	var prov model.Provider
	db.GetDB().First(&prov)
	db.GetDB().Create(&model.Key{ProviderID: prov.ID, KeyValue: "k", Name: "k1"})
	var key model.Key
	db.GetDB().First(&key)
	return key
}

// TestRecordConsumptionStoresIngressModel pins the activity-model contract:
// ModelName must be the model the CLIENT requested (the model group id), not
// the upstream target the route resolved to. Regression: the relay used to
// pass the target model, so the Activity page grouped by upstream names.
func TestRecordConsumptionStoresIngressModel(t *testing.T) {
	key := setupBillingDB(t)
	usage := &model.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, Format: "openai"}

	consumption, err := RecordConsumption(key.ID, "client-model", "upstream-real", "app", usage, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if consumption.ModelName != "client-model" {
		t.Errorf("ModelName = %q, want %q (ingress, not the upstream target)", consumption.ModelName, "client-model")
	}
	var saved model.Consumption
	if err := db.GetDB().First(&saved, consumption.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.ModelName != "client-model" {
		t.Errorf("DB ModelName = %q, want %q", saved.ModelName, "client-model")
	}
}

// TestRecordConsumptionPricesByTargetModel: billing must keep following the
// upstream model (the price the provider actually charges). A Pricing row for
// the target model must apply even though the stored model name is the
// ingress one — if the lookup wrongly switched to the ingress name, cost
// would drop to 0 (no pricing row) and this test fails.
func TestRecordConsumptionPricesByTargetModel(t *testing.T) {
	key := setupBillingDB(t)
	db.GetDB().Create(&model.Pricing{ModelName: "upstream-real", PromptPer1M: 1.0, CompletionPer1M: 2.0})
	usage := &model.TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 500_000, TotalTokens: 1_500_000, Format: "openai"}

	consumption, err := RecordConsumption(key.ID, "client-model", "upstream-real", "app", usage, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 1M prompt × $1 + 0.5M completion × $2 = $2.00
	if consumption.CostUSD != 2.0 {
		t.Errorf("CostUSD = %v, want 2 (priced by the target model)", consumption.CostUSD)
	}
	if consumption.ModelName != "client-model" {
		t.Errorf("ModelName = %q, want %q", consumption.ModelName, "client-model")
	}
}

// TestRecordConsumptionRoutePriceOverrides: per-route pricing must still win
// over the Pricing table, and the stored name must still be the ingress one.
func TestRecordConsumptionRoutePriceOverrides(t *testing.T) {
	key := setupBillingDB(t)
	db.GetDB().Create(&model.Pricing{ModelName: "upstream-real", PromptPer1M: 1.0})
	route := &model.Route{PromptPer1M: 4.0}
	usage := &model.TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 0, TotalTokens: 1_000_000, Format: "openai"}

	consumption, err := RecordConsumption(key.ID, "client-model", "upstream-real", "app", usage, route, nil)
	if err != nil {
		t.Fatal(err)
	}
	if consumption.CostUSD != 4.0 {
		t.Errorf("CostUSD = %v, want 4 (route price)", consumption.CostUSD)
	}
	if consumption.ModelName != "client-model" {
		t.Errorf("ModelName = %q, want %q", consumption.ModelName, "client-model")
	}
}

// TestRecordConsumptionSeparatesModelsInSameHour pins the per-model row key:
// one key serving SEVERAL models (and apps) within the same hour must yield
// separate hourly rows, each accumulating its own request_count / tokens /
// cost and keeping its own model_name and app_name. Regression: the unique
// index used to cover only (key_id, hour_bucket), so whichever model wrote the
// row first owned it — a second model's usage was added onto the first model's
// row while its name stayed that of the first request. The Activity page
// groups by model_name, so all of gpt-4o-mini's usage (and cost, and its share
// of the cache-hit rate) silently appeared under gpt-4o.
func TestRecordConsumptionSeparatesModelsInSameHour(t *testing.T) {
	key := setupBillingDB(t)
	// Distinct price rows so each model also carries a distinguishable cost.
	db.GetDB().Create(&model.Pricing{ModelName: "up-4o", PromptPer1M: 1.0})
	db.GetDB().Create(&model.Pricing{ModelName: "up-4o-mini", PromptPer1M: 0.25})

	// 1M prompt tokens each: $1.00 for gpt-4o, $0.25 for gpt-4o-mini.
	usage := &model.TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 0, TotalTokens: 1_000_000, Format: "openai"}
	if _, err := RecordConsumption(key.ID, "gpt-4o", "up-4o", "app-a", usage, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := RecordConsumption(key.ID, "gpt-4o-mini", "up-4o-mini", "app-b", usage, nil, nil); err != nil {
		t.Fatal(err)
	}

	var rows []model.Consumption
	if err := db.GetDB().Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (one per model/app in the same hour)", len(rows))
	}
	if rows[0].ModelName != "gpt-4o" || rows[0].AppName != "app-a" || rows[0].RequestCount != 1 || rows[0].CostUSD != 1.0 {
		t.Errorf("row 0 = %+v, want gpt-4o/app-a, 1 request, $1.00", rows[0])
	}
	if rows[1].ModelName != "gpt-4o-mini" || rows[1].AppName != "app-b" || rows[1].RequestCount != 1 || rows[1].CostUSD != 0.25 {
		t.Errorf("row 1 = %+v, want gpt-4o-mini/app-b, 1 request, $0.25", rows[1])
	}

	// One more request to EACH model: each row must grow on its own, never
	// folding the other model's usage into it.
	for i := 0; i < 2; i++ {
		if _, err := RecordConsumption(key.ID, "gpt-4o", "up-4o", "app-a", usage, nil, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := RecordConsumption(key.ID, "gpt-4o-mini", "up-4o-mini", "app-b", usage, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.GetDB().Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 after repeats", len(rows))
	}
	if rows[0].RequestCount != 3 || rows[0].CostUSD != 3.0 {
		t.Errorf("gpt-4o row = %d requests, $%v; want 3 requests, $3.00 (own usage only)", rows[0].RequestCount, rows[0].CostUSD)
	}
	if rows[1].RequestCount != 3 || rows[1].CostUSD != 0.75 {
		t.Errorf("gpt-4o-mini row = %d requests, $%v; want 3 requests, $0.75 (own usage only)", rows[1].RequestCount, rows[1].CostUSD)
	}
}

// TestRecordConsumptionCacheTokenSemantics guards the input_tokens storage
// convention the frontend's cache-hit rate depends on. OpenAI-format
// prompt_tokens ALREADY includes cached tokens (store as-is), while
// Anthropic-format input_tokens EXCLUDES cache tokens (must fold cache
// reads + writes in). Storing them inconsistently made the UI rate
// double-count OpenAI cache tokens and collapse ~98% real hit rates to ~50%.
func TestRecordConsumptionCacheTokenSemantics(t *testing.T) {
	tmp := t.TempDir()
	if err := db.Init(filepath.Join(tmp, "data")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	db.GetDB().Create(&model.Provider{Name: "mock", Type: "openai", BaseURL: "http://localhost:1"})
	var prov model.Provider
	db.GetDB().First(&prov)
	db.GetDB().Create(&model.Key{ProviderID: prov.ID, KeyValue: "k1", Name: "k1"})
	db.GetDB().Create(&model.Key{ProviderID: prov.ID, KeyValue: "k2", Name: "k2"})
	var k1, k2 model.Key
	db.GetDB().Where("name = ?", "k1").First(&k1)
	db.GetDB().Where("name = ?", "k2").First(&k2)

	// OpenAI: prompt_tokens = 198 already includes the 98 cached tokens.
	// Stored input_tokens must stay 198 (adding cache again would double-count).
	if _, err := RecordConsumption(k1.ID, "m", "m", "", &model.TokenUsage{
		PromptTokens:     198,
		CompletionTokens: 10,
		CacheHitTokens:   98,
		Format:           "openai",
	}, nil, nil); err != nil {
		t.Fatal(err)
	}

	// Anthropic: input_tokens = 100 EXCLUDES the 98 cache reads and 10
	// cache writes; stored input_tokens must be 100+98+10 = 208 so every
	// row shares the "input includes cached" convention.
	if _, err := RecordConsumption(k2.ID, "m", "m", "", &model.TokenUsage{
		PromptTokens:     100,
		CompletionTokens: 10,
		CacheHitTokens:   98,
		CacheWriteTokens: 10,
		Format:           "anthropic",
	}, nil, nil); err != nil {
		t.Fatal(err)
	}

	var rows []model.Consumption
	if err := db.GetDB().Order("key_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 consumption rows, got %d", len(rows))
	}
	if rows[0].InputTokens != 198 || rows[0].CacheHitTokens != 98 {
		t.Errorf("openai row: input_tokens = %d (want 198), cache_hit_tokens = %d (want 98)", rows[0].InputTokens, rows[0].CacheHitTokens)
	}
	if rows[1].InputTokens != 208 || rows[1].CacheHitTokens != 98 || rows[1].CacheWriteTokens != 10 {
		t.Errorf("anthropic row: input_tokens = %d (want 208), cache_hit_tokens = %d (want 98), cache_write_tokens = %d (want 10)", rows[1].InputTokens, rows[1].CacheHitTokens, rows[1].CacheWriteTokens)
	}
}

// TestRecordConsumptionPricingLookupErrorFallsBackToCache guards the billing
// default when the Pricing-table lookup itself fails: a genuine query error
// must not be indistinguishable from "no pricing rule". Before the fix the
// exact-model lookup AND the "*" wildcard lookup both discarded their error,
// so when both failed — e.g. a transient SQLite locked/busy read during a
// relay response — the rates fell through to zero and RecordConsumption wrote
// the row at $0, silently under-billing an otherwise-priced request. The fix
// logs the error and falls back to the price already in the in-memory
// Calculator, so the recorded cost stays correct even while the DB read
// hiccups.
func TestRecordConsumptionPricingLookupErrorFallsBackToCache(t *testing.T) {
	key := setupBillingDB(t)
	db.GetDB().Create(&model.Pricing{ModelName: "upstream-real", PromptPer1M: 2.0})

	// Seed the in-memory cache while the DB is healthy, then take the DB
	// down: both pricing lookups now fail with a genuine error ("sql:
	// database is closed") instead of a definitive not-found. The remaining
	// tests each re-initialize the global db.DB via setupBillingDB/db.Init,
	// so the closed handle never leaks into them.
	calc := NewCalculator()
	sqlDB, err := db.GetDB().DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.Close()

	usage := &model.TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 0, TotalTokens: 1_000_000, Format: "openai"}
	consumption, recErr := RecordConsumption(key.ID, "client-model", "upstream-real", "app", usage, nil, calc)
	if recErr == nil {
		t.Fatalf("RecordConsumption returned no error with the DB closed, want one")
	}
	// 1M prompt × $2 — from the cache, not the unreachable DB — must not be
	// silently billed at $0.
	if consumption.CostUSD != 2.0 {
		t.Fatalf("CostUSD = %v, want 2 (cached-price fallback, not 0)", consumption.CostUSD)
	}
}

// TestRecordConsumptionWildcardRulesFallback: a priceModel with no exact
// Pricing row but a "*" wildcard row must bill at the wildcard rate. This is
// the fallback the error-handling fix must preserve, not replace.
func TestRecordConsumptionWildcardRulesFallback(t *testing.T) {
	key := setupBillingDB(t)
	db.GetDB().Create(&model.Pricing{ModelName: "*", PromptPer1M: 4.0})
	usage := &model.TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 0, TotalTokens: 1_000_000, Format: "openai"}

	consumption, err := RecordConsumption(key.ID, "client-model", "no-exact-rule", "app", usage, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// No exact rule for "no-exact-rule" -> the "*" wildcard's $4 applies.
	if consumption.CostUSD != 4.0 {
		t.Fatalf("CostUSD = %v, want 4 (wildcard rate for a model without an exact rule)", consumption.CostUSD)
	}
	var saved model.Consumption
	if err := db.GetDB().First(&saved, consumption.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.CostUSD != 4.0 {
		t.Errorf("DB CostUSD = %v, want 4 (computed cost persisted)", saved.CostUSD)
	}
}

// TestRecordConsumptionUnpricedModelStaysZero: a model with no pricing rule
// (no exact rule, no "*" wildcard) must still record at $0, even when the
// cache happens to hold a stale price for it. Zero rates is the intended
// price for an unpriced model; the cache fallback applies only when the
// lookup ERRORS, never when it definitively finds nothing.
func TestRecordConsumptionUnpricedModelStaysZero(t *testing.T) {
	key := setupBillingDB(t)
	calc := &Calculator{
		pricing: map[string]*model.Pricing{
			// Stale cache entry: the rule was deleted from the Pricing table
			// but the engine's cache has not been refreshed yet.
			"no-rule-anywhere": {PromptPer1M: 9.0},
		},
	}
	usage := &model.TokenUsage{PromptTokens: 1_000_000, CompletionTokens: 0, TotalTokens: 1_000_000, Format: "openai"}

	consumption, err := RecordConsumption(key.ID, "client-model", "no-rule-anywhere", "app", usage, nil, calc)
	if err != nil {
		t.Fatal(err)
	}
	if consumption.CostUSD != 0.0 {
		t.Fatalf("CostUSD = %v, want 0 (definitive no-rule must not charge the stale cached price)", consumption.CostUSD)
	}
}
