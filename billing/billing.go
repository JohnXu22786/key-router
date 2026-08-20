package billing

import (
	"errors"
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

// lookupPricing fetches the pricing rule for a model name. It returns
// (nil, nil) when no rule exists for the name (a definitive "not found")
// and (nil, err) when the query itself fails, so callers can tell the
// legitimate unpriced-model case apart from a real query error such as a
// transient SQLite locked/busy read.
func lookupPricing(modelName string) (*model.Pricing, error) {
	var p model.Pricing
	err := db.GetDB().Where("model_name = ?", modelName).First(&p).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

// resolvePricing returns the effective pricing rule for priceModel: its exact
// Pricing-table rule, else the "*" wildcard rule, else nil.
//
// A genuine query error is NOT treated as an absence of pricing. The exact
// lookup and the wildcard lookup both used to discard their error, so when
// both failed — e.g. a brief SQLite locked/busy read during a relay response
// — the code fell through to zeroed rates and RecordConsumption wrote the row
// at $0, silently under-billing an otherwise-priced request. When the answer
// is unknown (a lookup errored instead of returning a definitive result), the
// error is logged and the cached Calculator price is used as the best
// available estimate. Only a definitive no-rule (no exact rule and no
// wildcard) yields nil, which makes the caller charge zero — the intended
// price for an unpriced model.
func resolvePricing(calc *Calculator, priceModel string) *model.Pricing {
	exact, exactErr := lookupPricing(priceModel)
	if exact != nil {
		return exact
	}

	// No exact rule (or its lookup failed): fall back to the "*" wildcard.
	wildcard, wildcardErr := lookupPricing("*")
	if wildcard != nil {
		if exactErr != nil {
			log.Printf("[billing] exact pricing lookup failed for %q: %v; using wildcard price", priceModel, exactErr)
		}
		return wildcard
	}

	// Neither lookup produced its rule. If one of them errored, the absence
	// is unproven — fall back to the cached price instead of billing $0.
	if exactErr != nil || wildcardErr != nil {
		log.Printf("[billing] pricing lookup unavailable for %q (exact: %v, wildcard: %v); falling back to cached price", priceModel, exactErr, wildcardErr)
		if calc != nil {
			if p := calc.GetPricing(priceModel); p != nil {
				return p
			}
		}
	}
	return nil
}

// RecordConsumption writes a consumption record to the database.
// modelName is the model the CLIENT requested (the model group id) — it is
// what the Activity page groups by. priceModel is the model actually served
// upstream (post route-target resolution) and keys the Pricing-table fallback,
// so billing follows the upstream price while the activity page shows the
// ingress model. For pass-through routes the two are identical.
// appName is the client app detected from the provider attribution headers
// ("" when absent); it powers the Activity page's "Top Apps" panel.
// routePrice, when non-nil and non-zero, overrides the Pricing table (each
// route can carry its own per-1M rates — e.g. a cheap and a premium key for
// the same model).
// calc, when non-nil, is the in-memory pricing cache (the engine's
// Calculator). It backs the Pricing-table lookup: if that query genuinely
// errors, the cached price is used so a transient DB hiccup does not bill an
// otherwise-priced request at $0.
func RecordConsumption(keyID int64, modelName, priceModel, appName string, usage *model.TokenUsage, routePrice *model.Route, calc *Calculator) (*model.Consumption, error) {
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
			// Keyed on priceModel (the upstream model actually served): the
			// provider bills at that price even though the activity page
			// shows the client-requested model name. nil means no rule (or a
			// priced model the fallback could not resolve) — rates stay 0.
			if p := resolvePricing(calc, priceModel); p != nil {
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

	// Atomic upsert: INSERT or increment the hourly row. The row key is
	// (key, hour, model, app) — a single provider key legitimately serves
	// several model groups, and each must keep its own hourly row (the
	// Activity page aggregates by model_name and app_name). Conflating every
	// model a key served within an hour into whichever row was written first
	// would fold the second model's usage (and cost, and cache tokens) into
	// the first model's row while keeping the first model's name.
	// Using a real ON CONFLICT avoids the FirstOrCreate SELECT+INSERT race
	// where concurrent requests for the same (key, hour, model, app) could
	// both INSERT and one would hit the unique index error and be lost.
	consumption.ID = 0
	err := db.GetDB().Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key_id"}, {Name: "hour_bucket"}, {Name: "model_name"}, {Name: "app_name"}},
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
