package billing

import (
	"log"
	"sync"
	"time"

	"local-router/db"
	"local-router/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Calculator handles token cost calculations
type Calculator struct {
	mu       sync.RWMutex
	pricing  map[string]*model.Pricing
	lastLoad time.Time
}

// NewCalculator creates a new billing calculator
func NewCalculator() *Calculator {
	c := &Calculator{
		pricing: make(map[string]*model.Pricing),
	}
	c.loadPricing()
	return c
}

// loadPricing loads all pricing rules from the database
func (c *Calculator) loadPricing() {
	var pricings []model.Pricing
	if err := db.GetDB().Find(&pricings).Error; err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Rebuild from scratch so deleted rows don't linger in the map
	c.pricing = make(map[string]*model.Pricing, len(pricings))
	for _, p := range pricings {
		cp := p
		c.pricing[p.ModelName] = &cp
	}
	c.lastLoad = time.Now()
}

// GetPricing returns pricing for a model
func (c *Calculator) GetPricing(modelName string) *model.Pricing {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if p, ok := c.pricing[modelName]; ok {
		return p
	}
	// If model not found, try wildcard
	if p, ok := c.pricing["*"]; ok {
		return p
	}
	return nil
}

// RefreshPricing reloads pricing from database
func (c *Calculator) RefreshPricing() {
	c.loadPricing()
}

// CalculateCost computes the cost for given token usage and model
func (c *Calculator) CalculateCost(modelName string, usage *model.TokenUsage) float64 {
	if usage == nil {
		return 0
	}

	p := c.GetPricing(modelName)
	if p == nil {
		return 0
	}

	// OpenAI's prompt_tokens includes cached_tokens — bill cached tokens at
	// the cache-read rate only, not also at the prompt rate. Anthropic's
	// input_tokens excludes cache tokens (no subtraction).
	uncachedPrompt := usage.PromptTokens
	if usage.Format == "openai" {
		uncachedPrompt = usage.PromptTokens - usage.CacheHitTokens
		if uncachedPrompt < 0 {
			uncachedPrompt = 0
		}
	}

	cost := 0.0

	// Input (prompt) tokens
	cost += float64(uncachedPrompt) * p.PromptPer1K / 1000.0

	// Output (completion) tokens
	cost += float64(usage.CompletionTokens) * p.CompletionPer1K / 1000.0

	// Cache read (cache hit)
	cost += float64(usage.CacheHitTokens) * p.CacheReadPer1K / 1000.0

	// Cache write
	cost += float64(usage.CacheWriteTokens) * p.CacheWritePer1K / 1000.0

	return cost
}

// RecordConsumption writes a consumption record to the database
func RecordConsumption(keyID int64, modelName string, usage *model.TokenUsage) (*model.Consumption, error) {
	// Truncate to the LOCAL hour: time.Truncate aligns to UTC hours, which
	// misaligns buckets in non-whole-hour-offset zones (e.g. +05:30).
	nowT := time.Now()
	now := time.Date(nowT.Year(), nowT.Month(), nowT.Day(), nowT.Hour(), 0, 0, 0, nowT.Location())
	cost := 0.0

	if usage != nil {
		// Try to find pricing — exact model name first, then the "*" wildcard
		var p model.Pricing
		exact := db.GetDB().Where("model_name = ?", modelName).First(&p).Error
		if exact != nil {
			db.GetDB().Where("model_name = ?", "*").First(&p)
		}
		if exact == nil || p.ID > 0 {
			// OpenAI's prompt_tokens INCLUDES prompt_tokens_details.cached_tokens,
			// so cached tokens are billed at the cache-read rate only. Anthropic's
			// input_tokens EXCLUDES cache tokens — subtracting there would
			// under-bill real prompt tokens.
			uncachedPrompt := usage.PromptTokens
			if usage.Format == "openai" {
				uncachedPrompt = usage.PromptTokens - usage.CacheHitTokens
				if uncachedPrompt < 0 {
					uncachedPrompt = 0
				}
			}
			cost = float64(uncachedPrompt)*p.PromptPer1K/1000.0 +
				float64(usage.CompletionTokens)*p.CompletionPer1K/1000.0 +
				float64(usage.CacheHitTokens)*p.CacheReadPer1K/1000.0 +
				float64(usage.CacheWriteTokens)*p.CacheWritePer1K/1000.0
		}
	}

	consumption := &model.Consumption{
		KeyID:      keyID,
		HourBucket: now,
		RequestCount: 1,
		CostUSD:    cost,
	}
	if usage != nil {
		consumption.InputTokens = usage.PromptTokens
		consumption.OutputTokens = usage.CompletionTokens
		consumption.CacheHitTokens = usage.CacheHitTokens
		consumption.CacheWriteTokens = usage.CacheWriteTokens
	}

	// Atomic upsert: INSERT or increment the hourly row.
	// Using a real ON CONFLICT avoids the FirstOrCreate SELECT+INSERT race
	// where concurrent requests for the same (key, hour) could both INSERT
	// and one would hit the unique index error and be lost.
	consumption.ID = 0
	err := db.GetDB().Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key_id"}, {Name: "hour_bucket"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"request_count":      gorm.Expr("request_count + 1"),
			"input_tokens":       gorm.Expr("input_tokens + ?", consumption.InputTokens),
			"output_tokens":      gorm.Expr("output_tokens + ?", consumption.OutputTokens),
			"cache_hit_tokens":   gorm.Expr("cache_hit_tokens + ?", consumption.CacheHitTokens),
			"cache_write_tokens": gorm.Expr("cache_write_tokens + ?", consumption.CacheWriteTokens),
			"cost_usd":           gorm.Expr("cost_usd + ?", consumption.CostUSD),
		}),
	}).Create(consumption).Error

	return consumption, err
}

// GetConsumptionSummary returns consumption statistics for a time range
func GetConsumptionSummary(keyID int64, since, until time.Time) ([]model.Consumption, error) {
	var consumptions []model.Consumption
	err := db.GetDB().Where("key_id = ? AND hour_bucket >= ? AND hour_bucket <= ?",
		keyID, since, until).Order("hour_bucket DESC").Find(&consumptions).Error
	return consumptions, err
}

// GetTotalCost returns total cost for a key
func GetTotalCost(keyID int64) (float64, error) {
	var result struct {
		Total float64
	}
	err := db.GetDB().Model(&model.Consumption{}).
		Select("COALESCE(SUM(cost_usd), 0) as total").
		Where("key_id = ?", keyID).Scan(&result).Error
	return result.Total, err
}

// OverviewStats holds aggregate statistics
type OverviewStats struct {
	TotalRequests   int64   `json:"total_requests"`
	TotalCost       float64 `json:"total_cost"`
	TotalInput      int64   `json:"total_input_tokens"`
	TotalOutput     int64   `json:"total_output_tokens"`
	ActiveKeys      int64   `json:"active_keys"`
	DisabledKeys    int64   `json:"disabled_keys"`
	TotalKeyCount   int64   `json:"total_keys"`
	TotalProviders  int64   `json:"total_providers"`
}

// GetOverview returns overall statistics
func GetOverview() (*OverviewStats, error) {
	stats := &OverviewStats{}

	// Each query is best-effort; errors are logged but don't fail the request
	if err := db.GetDB().Model(&model.Consumption{}).Select("COALESCE(SUM(request_count), 0)").Scan(&stats.TotalRequests).Error; err != nil {
		log.Printf("[billing] GetOverview TotalRequests: %v", err)
	}
	if err := db.GetDB().Model(&model.Consumption{}).Select("COALESCE(SUM(cost_usd), 0)").Scan(&stats.TotalCost).Error; err != nil {
		log.Printf("[billing] GetOverview TotalCost: %v", err)
	}
	if err := db.GetDB().Model(&model.Consumption{}).Select("COALESCE(SUM(input_tokens), 0)").Scan(&stats.TotalInput).Error; err != nil {
		log.Printf("[billing] GetOverview TotalInput: %v", err)
	}
	if err := db.GetDB().Model(&model.Consumption{}).Select("COALESCE(SUM(output_tokens), 0)").Scan(&stats.TotalOutput).Error; err != nil {
		log.Printf("[billing] GetOverview TotalOutput: %v", err)
	}
	if err := db.GetDB().Model(&model.Key{}).Where("status = ?", model.KeyStatusActive).Count(&stats.ActiveKeys).Error; err != nil {
		log.Printf("[billing] GetOverview ActiveKeys: %v", err)
	}
	if err := db.GetDB().Model(&model.Key{}).Where("status = ?", model.KeyStatusDisabled).Count(&stats.DisabledKeys).Error; err != nil {
		log.Printf("[billing] GetOverview DisabledKeys: %v", err)
	}
	if err := db.GetDB().Model(&model.Key{}).Count(&stats.TotalKeyCount).Error; err != nil {
		log.Printf("[billing] GetOverview TotalKeyCount: %v", err)
	}
	if err := db.GetDB().Model(&model.Provider{}).Count(&stats.TotalProviders).Error; err != nil {
		log.Printf("[billing] GetOverview TotalProviders: %v", err)
	}

	return stats, nil
}
