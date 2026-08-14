package billing

import (
	"path/filepath"
	"testing"

	"key-router/db"
	"key-router/model"
)

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
	if _, err := RecordConsumption(k1.ID, "m", "", &model.TokenUsage{
		PromptTokens:     198,
		CompletionTokens: 10,
		CacheHitTokens:   98,
		Format:           "openai",
	}, nil); err != nil {
		t.Fatal(err)
	}

	// Anthropic: input_tokens = 100 EXCLUDES the 98 cache reads and 10
	// cache writes; stored input_tokens must be 100+98+10 = 208 so every
	// row shares the "input includes cached" convention.
	if _, err := RecordConsumption(k2.ID, "m", "", &model.TokenUsage{
		PromptTokens:     100,
		CompletionTokens: 10,
		CacheHitTokens:   98,
		CacheWriteTokens: 10,
		Format:           "anthropic",
	}, nil); err != nil {
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
