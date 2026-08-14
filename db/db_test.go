package db

import (
	"path/filepath"
	"testing"
	"time"

	"key-router/model"
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
