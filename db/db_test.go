package db

import (
	"path/filepath"
	"testing"
	"time"

	"key-router/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestMigrateAnthropicInputTokensOnce verifies the one-time fold of cached
// tokens into legacy consumption rows: rows under anthropic-type providers
// gain cache_hit + cache_write in input_tokens (their stored input EXCLUDED
// cache), rows under openai providers stay untouched (their prompt_tokens
// already included cache), and a second run must not double-count (the
// settings flag gates it).
func TestMigrateAnthropicInputTokensOnce(t *testing.T) {
	tmp := t.TempDir()
	if err := Init(filepath.Join(tmp, "data")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	// Init ran the migration on an empty DB and set the flag; re-arm it so
	// the fixtures below are folded.
	if err := DB.Delete(&model.Setting{}, "key = ?", migrationInputTokensInclCache).Error; err != nil {
		t.Fatal(err)
	}

	anth := &model.Provider{Name: "anth", Type: model.ProviderTypeAnthropic, BaseURL: "http://localhost:2"}
	oa := &model.Provider{Name: "oa", Type: model.ProviderTypeOpenAI, BaseURL: "http://localhost:1"}
	if err := DB.Create(anth).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Create(oa).Error; err != nil {
		t.Fatal(err)
	}
	k1 := &model.Key{ProviderID: anth.ID, KeyValue: "k1", Name: "k1"}
	k2 := &model.Key{ProviderID: oa.ID, KeyValue: "k2", Name: "k2"}
	if err := DB.Create(k1).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Create(k2).Error; err != nil {
		t.Fatal(err)
	}

	// Legacy anthropic row: input EXCLUDED cache (100 uncached, 98 read, 10 written).
	// Legacy openai row: prompt_tokens already INCLUDED cache (198 = 100 uncached + 98 cached).
	now := time.Now().Truncate(time.Hour)
	if err := DB.Create(&model.Consumption{KeyID: k1.ID, HourBucket: now, InputTokens: 100, CacheHitTokens: 98, CacheWriteTokens: 10}).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Create(&model.Consumption{KeyID: k2.ID, HourBucket: now, InputTokens: 198, CacheHitTokens: 98}).Error; err != nil {
		t.Fatal(err)
	}

	if err := migrateAnthropicInputTokensOnce(DB); err != nil {
		t.Fatal(err)
	}

	var rows []model.Consumption
	if err := DB.Order("key_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 consumption rows, got %d", len(rows))
	}
	if rows[0].InputTokens != 208 || rows[0].CacheHitTokens != 98 || rows[0].CacheWriteTokens != 10 {
		t.Errorf("anthropic row: input_tokens = %d (want 208), cache_hit = %d (want 98), cache_write = %d (want 10)", rows[0].InputTokens, rows[0].CacheHitTokens, rows[0].CacheWriteTokens)
	}
	if rows[1].InputTokens != 198 || rows[1].CacheHitTokens != 98 {
		t.Errorf("openai row: input_tokens = %d (want 198, untouched), cache_hit = %d (want 98)", rows[1].InputTokens, rows[1].CacheHitTokens)
	}

	// Second run: flag set, must be a no-op (no double fold).
	if err := migrateAnthropicInputTokensOnce(DB); err != nil {
		t.Fatal(err)
	}
	if err := DB.Order("key_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows[0].InputTokens != 208 || rows[1].InputTokens != 198 {
		t.Errorf("second run double-folded: input_tokens = %d / %d, want 208 / 198", rows[0].InputTokens, rows[1].InputTokens)
	}
}

// TestMigrateAnthropicInputTokensOnceFailureKeepsFlagUnset pins the
// atomicity of the migration: when the fold fails, the flag must NOT be
// written, so the next launch retries without having double-counted any
// rows (a flag write outside the transaction would break this).
func TestMigrateAnthropicInputTokensOnceFailureKeepsFlagUnset(t *testing.T) {
	tmp := t.TempDir()
	if err := Init(filepath.Join(tmp, "data")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})
	// Init ran the migration on the empty DB and set the flag; re-arm it.
	if err := DB.Delete(&model.Setting{}, "key = ?", migrationInputTokensInclCache).Error; err != nil {
		t.Fatal(err)
	}
	// Break the UPDATE (missing column), then run the migration.
	if err := DB.Exec("ALTER TABLE consumptions DROP COLUMN cache_write_tokens").Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateAnthropicInputTokensOnce(DB); err == nil {
		t.Fatal("expected the migration to fail with a broken table")
	}
	var n int64
	if err := DB.Model(&model.Setting{}).Where("key = ?", migrationInputTokensInclCache).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("flag set despite failed fold — a retry would double-count legacy rows")
	}
}

// migrateTestDB opens a bare sqlite DB with the tables the consumption
// migration touches, WITHOUT going through db.Init (which would already run
// the migration on the empty DB and set the completion marker). The test then
// seeds legacy rows and runs migrateConsumptionModelToIngress directly.
func migrateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dbc, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := dbc.DB(); err == nil {
			sqlDB.Close()
		}
	})
	if err := dbc.AutoMigrate(
		&model.Provider{},
		&model.Key{},
		&model.ModelGroup{},
		&model.Route{},
		&model.Consumption{},
		&model.Setting{},
	); err != nil {
		t.Fatal(err)
	}
	return dbc
}

// TestMigrateConsumptionModelToIngress exercises the legacy-data migration
// matrix. Legacy rows store the upstream TARGET model (the old behavior); the
// migration must remap them to the model group id the client actually
// requested — but ONLY when the reverse mapping is unambiguous:
//
//   - per key: restricted to the key's provider (a target served by two
//     providers still resolves per provider; globally-ambiguous targets are
//     remapped per provider but never for orphan rows)
//   - ambiguous targets (two groups on the same provider) are left untouched
//   - orphan rows (deleted keys) fall back to the globally-unique mapping
//   - a target that equals ANY live group id (a pass-through group's id, or a
//     chained target) is never rewritten: those rows may already be correct
//   - pass-through/unknown/empty names are never rewritten
func TestMigrateConsumptionModelToIngress(t *testing.T) {
	dbc := migrateTestDB(t)

	p1 := model.Provider{Name: "p1", Type: "openai", BaseURL: "http://p1"}
	p2 := model.Provider{Name: "p2", Type: "openai", BaseURL: "http://p2"}
	dbc.Create(&p1)
	dbc.Create(&p2)

	k1 := model.Key{ProviderID: p1.ID, KeyValue: "k1"}
	k2 := model.Key{ProviderID: p1.ID, KeyValue: "k2"}
	k3 := model.Key{ProviderID: p2.ID, KeyValue: "k3"}
	dbc.Create(&k1)
	dbc.Create(&k2)
	dbc.Create(&k3)

	alpha := model.ModelGroup{GroupID: "alpha", Name: "A", Enabled: true}
	beta := model.ModelGroup{GroupID: "beta", Name: "B", Enabled: true}
	gamma := model.ModelGroup{GroupID: "gamma", Name: "C", Enabled: true}
	delta := model.ModelGroup{GroupID: "delta", Name: "D", Enabled: true}
	epsilon := model.ModelGroup{GroupID: "epsilon", Name: "E", Enabled: true}
	tgt := model.ModelGroup{GroupID: "tgt", Name: "T", Enabled: true}
	passthru := model.ModelGroup{GroupID: "passthru", Name: "Pass", Enabled: true}
	chainA := model.ModelGroup{GroupID: "chain-a", Name: "CA", Enabled: true}
	chainB := model.ModelGroup{GroupID: "chain-x", Name: "CB", Enabled: true}
	for _, g := range []*model.ModelGroup{&alpha, &beta, &gamma, &delta, &epsilon, &tgt, &passthru, &chainA, &chainB} {
		if err := dbc.Create(g).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Routes: up-model-1 is targeted by alpha on BOTH providers (per-provider
	// unique -> remappable); up-model-2 by beta AND gamma on p1 (ambiguous on
	// p1 -> untouched, even though gamma targets it only there); up-model-3 by
	// gamma on p2 only (unique both per-provider and globally).
	//
	// shared-x is targeted by delta on p1 and epsilon on p2 — unique PER
	// provider but ambiguous globally, so orphan rows must NOT follow it.
	//
	// "passthru" is both a live group id (pass-through route, TargetModel "")
	// and tgt's target: rows named "passthru" may already be correct, so the
	// target is never rewritten (live-group-id rule).
	//
	// chain-b ("chain-x") targets "chain-y" while chain-a targets "chain-x":
	// "chain-x" is a live group id, so rows named "chain-x" are never
	// rewritten (the chain would otherwise relabel chain-b's migrated rows).
	dbc.Create(&model.Route{ModelGroupID: alpha.ID, ProviderID: p1.ID, TargetModel: "up-model-1", Enabled: true})
	dbc.Create(&model.Route{ModelGroupID: alpha.ID, ProviderID: p2.ID, TargetModel: "up-model-1", Enabled: true})
	dbc.Create(&model.Route{ModelGroupID: beta.ID, ProviderID: p1.ID, TargetModel: "up-model-2", Enabled: true})
	dbc.Create(&model.Route{ModelGroupID: gamma.ID, ProviderID: p1.ID, TargetModel: "up-model-2", Enabled: true})
	dbc.Create(&model.Route{ModelGroupID: gamma.ID, ProviderID: p2.ID, TargetModel: "up-model-3", Enabled: true})
	dbc.Create(&model.Route{ModelGroupID: delta.ID, ProviderID: p1.ID, TargetModel: "shared-x", Enabled: true})
	dbc.Create(&model.Route{ModelGroupID: epsilon.ID, ProviderID: p2.ID, TargetModel: "shared-x", Enabled: true})
	dbc.Create(&model.Route{ModelGroupID: passthru.ID, ProviderID: p1.ID, TargetModel: "", Enabled: true})
	dbc.Create(&model.Route{ModelGroupID: tgt.ID, ProviderID: p1.ID, TargetModel: "passthru", Enabled: true})
	dbc.Create(&model.Route{ModelGroupID: chainA.ID, ProviderID: p1.ID, TargetModel: "chain-x", Enabled: true})
	dbc.Create(&model.Route{ModelGroupID: chainB.ID, ProviderID: p1.ID, TargetModel: "chain-y", Enabled: true})

	rows := []model.Consumption{
		// 0: k1 (p1) -> up-model-1 unique on p1 (alpha).
		{KeyID: k1.ID, HourBucket: h(1), ModelName: "up-model-1", RequestCount: 1},
		// 1: k3 (p2) -> up-model-1 unique on p2 (alpha).
		{KeyID: k3.ID, HourBucket: h(2), ModelName: "up-model-1", RequestCount: 1},
		// 2: k1 (p1) -> up-model-2 ambiguous on p1 (beta|gamma): untouched.
		{KeyID: k1.ID, HourBucket: h(3), ModelName: "up-model-2", RequestCount: 1},
		// 3: k2 (p1) -> up-model-2 ambiguous on p1: untouched.
		{KeyID: k2.ID, HourBucket: h(4), ModelName: "up-model-2", RequestCount: 1},
		// 4: k3 (p2) -> up-model-2 not routed on p2: untouched.
		{KeyID: k3.ID, HourBucket: h(5), ModelName: "up-model-2", RequestCount: 1},
		// 5: k3 (p2) -> up-model-3 unique on p2 (gamma).
		{KeyID: k3.ID, HourBucket: h(6), ModelName: "up-model-3", RequestCount: 1},
		// 6: deleted key 999 -> up-model-3 globally unique (gamma).
		{KeyID: 999, HourBucket: h(7), ModelName: "up-model-3", RequestCount: 1},
		// 7: deleted key 999 -> up-model-2 globally ambiguous (beta|gamma): untouched.
		{KeyID: 999, HourBucket: h(8), ModelName: "up-model-2", RequestCount: 1},
		// 8: pass-through name (never a route target): untouched.
		{KeyID: k1.ID, HourBucket: h(9), ModelName: "pass-model", RequestCount: 1},
		// 9: empty name ("Unknown" rows): untouched.
		{KeyID: k1.ID, HourBucket: h(10), ModelName: "", RequestCount: 1},
		// 10: k1 (p1) -> shared-x unique on p1 (delta) even though p2's epsilon
		// also targets it: per-provider resolution.
		{KeyID: k1.ID, HourBucket: h(11), ModelName: "shared-x", RequestCount: 1},
		// 11: k3 (p2) -> shared-x unique on p2 (epsilon).
		{KeyID: k3.ID, HourBucket: h(12), ModelName: "shared-x", RequestCount: 1},
		// 12: deleted key 999 -> shared-x globally ambiguous (delta|epsilon): untouched.
		{KeyID: 999, HourBucket: h(13), ModelName: "shared-x", RequestCount: 1},
		// 13: "passthru" is a live group id (pass-through group) AND tgt's
		// target: rows named "passthru" may already be correct -> never rewritten.
		{KeyID: k1.ID, HourBucket: h(14), ModelName: "passthru", RequestCount: 1},
		// 14: same rule for orphan rows.
		{KeyID: 999, HourBucket: h(15), ModelName: "passthru", RequestCount: 1},
		// 15: k1 (p1) -> chain-y unique on p1 (chain-x): chain-b's legacy rows.
		{KeyID: k1.ID, HourBucket: h(16), ModelName: "chain-y", RequestCount: 1},
		// 16: "chain-x" is a live group id (chain-b) and chain-a's target:
		// rows named "chain-x" may be chain-b's already-correct rows -> untouched.
		{KeyID: k1.ID, HourBucket: h(17), ModelName: "chain-x", RequestCount: 1},
	}
	for i := range rows {
		if err := dbc.Create(&rows[i]).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := migrateConsumptionModelToIngress(dbc); err != nil {
		t.Fatal(err)
	}

	var got []model.Consumption
	dbc.Order("id ASC").Find(&got)
	want := []string{
		"alpha", "alpha", // 0,1
		"up-model-2", "up-model-2", "up-model-2", // 2,3,4 untouched
		"gamma",      // 5
		"gamma",      // 6 (orphan, globally unique)
		"up-model-2", // 7 (orphan, ambiguous)
		"pass-model", // 8
		"",           // 9
		"delta",      // 10 (per-provider unique, despite global ambiguity)
		"epsilon",    // 11
		"shared-x",   // 12 (orphan, globally ambiguous)
		"passthru",   // 13 (target == live group id)
		"passthru",   // 14 (orphan, same rule)
		"chain-x",    // 15 (chain-b's legacy rows remap to its own id)
		"chain-x",    // 16 (target == live group id)
	}
	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].ModelName != want[i] {
			t.Errorf("row %d ModelName = %q, want %q", i, got[i].ModelName, want[i])
		}
	}

	// The completion marker must be set so the migration never re-runs.
	var marker model.Setting
	if err := dbc.Where("key = ?", model.SettingConsumptionModelSource).First(&marker).Error; err != nil {
		t.Fatalf("migration marker missing: %v", err)
	}
	if marker.Value != "ingress" {
		t.Errorf("marker value = %q, want %q", marker.Value, "ingress")
	}

	// Re-running must be a no-op even if a target name reappears (rows written
	// after the feature flip store ingress names; remapping them would corrupt
	// the new data).
	dbc.Model(&model.Consumption{}).Where("id = ?", got[5].ID).Update("model_name", "up-model-3")
	if err := migrateConsumptionModelToIngress(dbc); err != nil {
		t.Fatal(err)
	}
	var back model.Consumption
	dbc.First(&back, got[5].ID)
	if back.ModelName != "up-model-3" {
		t.Errorf("re-run rewrote a post-migration row to %q, want it untouched", back.ModelName)
	}
}

// h builds a distinct hour bucket per index (deterministic, no real clock).
func h(idx int) time.Time {
	return time.Date(2026, 8, 1, idx%24, 0, 0, 0, time.Local)
}
