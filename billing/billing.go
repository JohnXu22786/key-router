package billing

import (
	"log"
	"sync"
	"time"

	"key-router/db"
	"key-router/model"

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
	cost += float64(uncachedPrompt) * p.PromptPer1M / 1e6

	// Output (completion) tokens
	cost += float64(usage.CompletionTokens) * p.CompletionPer1M / 1e6

	// Cache read (cache hit)
	cost += float64(usage.CacheHitTokens) * p.CacheReadPer1M / 1e6

	// Cache write
	cost += float64(usage.CacheWriteTokens) * p.CacheWritePer1M / 1e6

	return cost
}

// RecordConsumption writes a consumption record to the database.
// modelName is the model actually served (post route-target resolution); it
// powers the Activity page's by-model aggregation.
// appName is the client app detected from the provider attribution headers
// ("" when absent); it powers the Activity page's "Top Apps" panel.
// routePrice, when non-nil and non-zero, overrides the Pricing table (each
// route can carry its own per-1M rates — e.g. a cheap and a premium key for
// the same model).
func RecordConsumption(keyID int64, modelName, appName string, usage *model.TokenUsage, routePrice *model.Route) (*model.Consumption, error) {
	// Truncate to the LOCAL hour: time.Truncate aligns to UTC hours, which
	// misaligns buckets in non-whole-hour-offset zones (e.g. +05:30).
	nowT := time.Now()
	now := time.Date(nowT.Year(), nowT.Month(), nowT.Day(), nowT.Hour(), 0, 0, 0, nowT.Location())
	cost := 0.0

	if usage != nil {
		// Route-level pricing wins when any of its rates is set; otherwise
		// fall back to the Pricing table (exact model, then "*" wildcard).
		var prompt, completion, cacheRead, cacheWrite float64
		useRoutePrice := routePrice != nil &&
			(routePrice.PromptPer1M != 0 || routePrice.CompletionPer1M != 0 ||
				routePrice.CacheReadPer1M != 0 || routePrice.CacheWritePer1M != 0)
		if useRoutePrice {
			prompt, completion = routePrice.PromptPer1M, routePrice.CompletionPer1M
			cacheRead, cacheWrite = routePrice.CacheReadPer1M, routePrice.CacheWritePer1M
		} else {
			// Try to find pricing — exact model name first, then the "*" wildcard
			var p model.Pricing
			exact := db.GetDB().Where("model_name = ?", modelName).First(&p).Error
			if exact != nil {
				db.GetDB().Where("model_name = ?", "*").First(&p)
			}
			if exact == nil || p.ID > 0 {
				prompt, completion = p.PromptPer1M, p.CompletionPer1M
				cacheRead, cacheWrite = p.CacheReadPer1M, p.CacheWritePer1M
			}
		}
		if prompt != 0 || completion != 0 || cacheRead != 0 || cacheWrite != 0 {
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
			cost = float64(uncachedPrompt)*prompt/1e6 +
				float64(usage.CompletionTokens)*completion/1e6 +
				float64(usage.CacheHitTokens)*cacheRead/1e6 +
				float64(usage.CacheWriteTokens)*cacheWrite/1e6
		}
	}

	consumption := &model.Consumption{
		KeyID:        keyID,
		HourBucket:   now,
		ModelName:    modelName,
		AppName:      appName,
		RequestCount: 1,
		CostUSD:      cost,
	}
	if usage != nil {
		// Store input_tokens under ONE convention for every provider: total
		// input INCLUDING cached tokens. OpenAI's prompt_tokens already
		// includes cached_tokens; Anthropic's input_tokens excludes them, so
		// fold cache reads + writes in (same conversion the relay applies to
		// the Responses API). The UI cache-hit rate then divides by
		// input_tokens alone — storing the raw values mixed semantics and
		// double-counted OpenAI cache tokens (~98% real hit rate read ~50%).
		// Legacy rows recorded before this build are folded once at startup
		// by db.migrateAnthropicInputTokensOnce.
		inputTokens := usage.PromptTokens
		if usage.Format == "anthropic" {
			inputTokens = usage.PromptTokens + usage.CacheHitTokens + usage.CacheWriteTokens
		}
		consumption.InputTokens = inputTokens
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
	// Floor since to the LOCAL hour: hour_bucket rows are hour-truncated, so
	// a mid-hour since (e.g. a 1h window at 16:20 -> since 15:20) must still
	// match the 15:00 bucket or the window's first hour (the bucket
	// containing its start) silently disappears from the query.
	floor := time.Date(since.Year(), since.Month(), since.Day(), since.Hour(), 0, 0, 0, since.Location())
	err := db.GetDB().Where("key_id = ? AND hour_bucket >= ? AND hour_bucket <= ?",
		keyID, floor, until).Order("hour_bucket DESC").Find(&consumptions).Error
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
	TotalRequests  int64   `json:"total_requests"`
	TotalCost      float64 `json:"total_cost"`
	TotalInput     int64   `json:"total_input_tokens"`
	TotalOutput    int64   `json:"total_output_tokens"`
	ActiveKeys     int64   `json:"active_keys"`
	DisabledKeys   int64   `json:"disabled_keys"`
	TotalKeyCount  int64   `json:"total_keys"`
	TotalProviders int64   `json:"total_providers"`
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
