package selector

import (
	"fmt"
	"log"
	"math/rand"
	"sort"
	"sync"
	"time"

	"local-router/billing"
	"local-router/db"
	"local-router/model"
	"local-router/window"
)

// Engine handles routing selection and retry logic
type Engine struct {
	mu            sync.RWMutex
	WindowManager *window.WindowManager
	Calculator    *billing.Calculator

	// Cached routing data (refreshed periodically)
	routes     map[string][]*RouteEntry // model group ID -> routes
	providers  map[int64]*model.Provider
	keys       map[int64][]*model.Key
}

// RouteEntry is a cached route with resolved provider and keys
type RouteEntry struct {
	Route       *model.Route
	Provider    *model.Provider
	Keys        []*model.Key
}

// NewEngine creates a new routing engine
func NewEngine() *Engine {
	e := &Engine{
		WindowManager: window.NewWindowManager(),
		Calculator:    billing.NewCalculator(),
		routes:        make(map[string][]*RouteEntry),
		providers:     make(map[int64]*model.Provider),
		keys:          make(map[int64][]*model.Key),
	}
	e.Refresh()
	return e
}

// Refresh reloads all routing data from database
func (e *Engine) Refresh() {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Reload providers
	var providers []model.Provider
	if err := db.GetDB().Find(&providers).Error; err == nil {
		e.providers = make(map[int64]*model.Provider)
		for i := range providers {
			e.providers[providers[i].ID] = &providers[i]
		}
	}

	// Reload keys
	var keys []model.Key
	if err := db.GetDB().Find(&keys).Error; err == nil {
		e.keys = make(map[int64][]*model.Key)
		for i := range keys {
			e.keys[keys[i].ProviderID] = append(e.keys[keys[i].ProviderID], &keys[i])
		}
	}

	// Reload all model groups (one query, no N+1)
	var allMGs []model.ModelGroup
	if err := db.GetDB().Find(&allMGs).Error; err != nil {
		log.Printf("[selector] failed to load model groups: %v", err)
	}
	mgMap := make(map[int64]model.ModelGroup)
	for i := range allMGs {
		mgMap[allMGs[i].ID] = allMGs[i]
	}

	// Reload routes
	var routes []model.Route
	if err := db.GetDB().Preload("Provider").Find(&routes).Error; err == nil {
		e.routes = make(map[string][]*RouteEntry)
		for i := range routes {
			r := &routes[i]
			if !r.Enabled {
				continue
			}
			mg, ok := mgMap[r.ModelGroupID]
			if !ok || !mg.Enabled {
				continue
			}

			provider, ok := e.providers[r.ProviderID]
			if !ok {
				continue
			}

			entry := &RouteEntry{
				Route:    r,
				Provider: provider,
				Keys:     e.keys[r.ProviderID],
			}
			e.routes[mg.GroupID] = append(e.routes[mg.GroupID], entry)
		}
	}

	// Refresh billing pricing
	e.Calculator.RefreshPricing()
}

// GetRoutes returns all routes for a model group
func (e *Engine) GetRoutes(modelGroupID string) []*RouteEntry {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.routes[modelGroupID]
}

// SelectRoute selects a route for the given model, considering retry attempts
// retry: 0 = first attempt, 1 = first retry, etc.
func (e *Engine) SelectRoute(modelGroupID string, retry int) *RouteEntry {
	entries := e.GetRoutes(modelGroupID)
	if len(entries) == 0 {
		return nil
	}

	// Group by priority
	priorityGroups := make(map[int][]*RouteEntry)
	var priorities []int
	for _, entry := range entries {
		p := entry.Route.Priority
		if _, ok := priorityGroups[p]; !ok {
			priorities = append(priorities, p)
		}
		priorityGroups[p] = append(priorityGroups[p], entry)
	}

	sort.Ints(priorities)

	// Select priority tier based on retry count
	if retry >= len(priorities) {
		retry = len(priorities) - 1
	}
	targetPriority := priorities[retry]

	entries = priorityGroups[targetPriority]
	if len(entries) == 0 {
		return nil
	}

	// Weighted random selection within priority tier
	return weightedSelect(entries)
}

// SelectKey selects an available (non-exceeded, non-disabled) key from a route.
// Keys with recovery_strategy=immediate are preferred over lazy keys.
// Lazy keys are only used when no immediate keys are available.
func (e *Engine) SelectKey(route *RouteEntry) *model.Key {
	e.mu.RLock()
	defer e.mu.RUnlock()

	keys := route.Keys
	if len(keys) == 0 {
		return nil
	}

	var immediateKeys []*model.Key
	var lazyKeys []*model.Key

	for _, k := range keys {
		if !e.isKeyAvailable(k) {
			continue
		}

		// Check limits
		limits := e.getKeyLimits(k)
		if e.WindowManager.IsAnyExceeded(k.ID, limits) {
			continue
		}

		switch k.RecoveryStrategy {
		case model.RecoveryLazy:
			lazyKeys = append(lazyKeys, k)
		default:
			immediateKeys = append(immediateKeys, k)
		}
	}

	// Prefer immediate keys; only use lazy keys when no immediate ones are available
	if len(immediateKeys) > 0 {
		return immediateKeys[rand.Intn(len(immediateKeys))]
	}
	if len(lazyKeys) > 0 {
		return lazyKeys[rand.Intn(len(lazyKeys))]
	}
	return nil
}

// isKeyAvailable checks if a key can be used
func (e *Engine) isKeyAvailable(k *model.Key) bool {
	switch k.Status {
	case model.KeyStatusActive:
		return true
	case model.KeyStatusRateLimited:
		if k.RateLimitedUntil != nil && time.Now().After(*k.RateLimitedUntil) {
			return true
		}
		return false
	case model.KeyStatusDisabled:
		return false
	default:
		return false
	}
}

// getKeyLimits returns the limit map for a key
func (e *Engine) getKeyLimits(k *model.Key) map[model.WindowType]int64 {
	limits := make(map[model.WindowType]int64)
	if k.RPMLimit > 0 {
		limits[model.WindowRPM] = k.RPMLimit
	}
	if k.TPMLimit > 0 {
		limits[model.WindowTPM] = k.TPMLimit
	}
	if k.RP5hLimit > 0 {
		limits[model.WindowRP5h] = k.RP5hLimit
	}
	if k.RPDLimit > 0 {
		limits[model.WindowRPD] = k.RPDLimit
	}
	if k.RPWLimit > 0 {
		limits[model.WindowRPW] = k.RPWLimit
	}
	if k.RPMLimitMonth > 0 {
		limits[model.WindowRPMo] = k.RPMLimitMonth
	}
	return limits
}

// RecordSuccess updates window counters and consumption after a successful relay
func (e *Engine) RecordSuccess(keyID int64, tokens int64) {
	e.WindowManager.IncrementAll(keyID, tokens)
}

// MarkKeyRateLimited marks a key as rate limited
func (e *Engine) MarkKeyRateLimited(keyID int64, retryAfter time.Duration) {
	until := time.Now().Add(retryAfter)
	if err := db.GetDB().Model(&model.Key{}).Where("id = ?", keyID).Updates(map[string]interface{}{
		"status":              model.KeyStatusRateLimited,
		"rate_limited_until":  until,
	}).Error; err == nil {
		// Update in-memory cache
		e.updateKeyStatus(keyID, model.KeyStatusRateLimited, &until)
	}
}

// MarkKeyDisabled marks a key as disabled due to auth error
func (e *Engine) MarkKeyDisabled(keyID int64, reason string) {
	if err := db.GetDB().Model(&model.Key{}).Where("id = ?", keyID).Updates(map[string]interface{}{
		"status":          model.KeyStatusDisabled,
		"disabled_reason": reason,
	}).Error; err == nil {
		e.updateKeyStatus(keyID, model.KeyStatusDisabled, nil)
	}
}

// MarkKeyActive marks a key as active (e.g., after health check recovers)
func (e *Engine) MarkKeyActive(keyID int64) {
	if err := db.GetDB().Model(&model.Key{}).Where("id = ?", keyID).Updates(map[string]interface{}{
		"status":             model.KeyStatusActive,
		"rate_limited_until": nil,
		"disabled_reason":    "",
	}).Error; err == nil {
		e.updateKeyStatus(keyID, model.KeyStatusActive, nil)
	}
}

// updateKeyStatus updates the in-memory key status
func (e *Engine) updateKeyStatus(keyID int64, status string, rateLimitedUntil *time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for providerID, keys := range e.keys {
		for _, k := range keys {
			if k.ID == keyID {
				k.Status = status
				k.RateLimitedUntil = rateLimitedUntil
				if status == model.KeyStatusActive {
					k.DisabledReason = ""
					k.RateLimitedUntil = nil
				}
				// Refresh the provider's keys list
				e.keys[providerID] = keys
				return
			}
		}
	}
}

// GetKeyStatus returns the current status of a key
func (e *Engine) GetKeyStatus(keyID int64) *model.Key {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, keys := range e.keys {
		for _, k := range keys {
			if k.ID == keyID {
				return k
			}
		}
	}
	return nil
}

// GetProvider returns a provider by ID
func (e *Engine) GetProvider(providerID int64) *model.Provider {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.providers[providerID]
}

// RetryResult holds the result of a retry cycle
type RetryResult struct {
	Route       *RouteEntry
	Key         *model.Key
	Err         error
}

// RetryLoop performs the full retry loop for a model group
func (e *Engine) RetryLoop(modelGroupID string, maxRetries int) (*RouteEntry, *model.Key, error) {
	for retry := 0; retry <= maxRetries; retry++ {
		route := e.SelectRoute(modelGroupID, retry)
		if route == nil {
			continue
		}

		key := e.SelectKey(route)
		if key == nil {
			// No available keys in this route, try next retry
			continue
		}

		return route, key, nil
	}

	return nil, nil, fmt.Errorf("no available route or key for model %s", modelGroupID)
}

// weightedSelect picks a random entry from a list using weights
func weightedSelect(entries []*RouteEntry) *RouteEntry {
	if len(entries) == 0 {
		return nil
	}
	if len(entries) == 1 {
		return entries[0]
	}

	totalWeight := 0
	for _, e := range entries {
		if e.Route.Weight <= 0 {
			totalWeight += 10 // default weight
		} else {
			totalWeight += e.Route.Weight
		}
	}

	roll := rand.Intn(totalWeight)
	for _, e := range entries {
		w := e.Route.Weight
		if w <= 0 {
			w = 10
		}
		roll -= w
		if roll < 0 {
			return e
		}
	}

	return entries[len(entries)-1]
}
