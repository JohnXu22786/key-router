package selector

import (
	"fmt"
	"log"
	"math/rand"
	"sort"
	"sync"
	"time"

	"key-router/billing"
	"key-router/db"
	"key-router/model"
	"key-router/window"
)

// Engine handles routing selection and retry logic
type Engine struct {
	mu            sync.RWMutex
	WindowManager *window.WindowManager
	Calculator    *billing.Calculator

	// Cached routing data (refreshed periodically)
	routes    map[string][]*RouteEntry // model group ID -> routes
	providers map[int64]*model.Provider
	keys      map[int64][]*model.Key
}

// RouteEntry is a cached route with resolved provider and keys
type RouteEntry struct {
	Route    *model.Route
	Provider *model.Provider
	Keys     []*model.Key
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

	// Reload all model groups (one query, no N+1). On failure, keep the
	// existing routes cache (like providers/keys) instead of wiping it —
	// a transient DB error must not 404 every model.
	var allMGs []model.ModelGroup
	mgErr := db.GetDB().Find(&allMGs).Error
	if mgErr != nil {
		log.Printf("[selector] failed to load model groups: %v", mgErr)
		return
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

		// Check limits (uses metric type for 5h/daily/weekly/monthly windows)
		if !e.isKeyWithinLimits(k) {
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

// isKeyWithinLimits checks all rate limit windows against the key's metric type
func (e *Engine) isKeyWithinLimits(k *model.Key) bool {
	// RPM always checks request count
	if k.RPMLimit > 0 && e.WindowManager.GetCount(k.ID, model.WindowRPM) >= k.RPMLimit {
		return false
	}
	// TPM always checks token count
	if k.TPMLimit > 0 && e.WindowManager.GetTokens(k.ID, model.WindowTPM) >= k.TPMLimit {
		return false
	}
	// 5-hour: check based on configured metric
	if k.RP5hLimit > 0 && e.getMetricCount(k.ID, model.WindowRP5h, k.RP5hMetric) >= k.RP5hLimit {
		return false
	}
	// Daily
	if k.RPDLimit > 0 && e.getMetricCount(k.ID, model.WindowRPD, k.RPDMetric) >= k.RPDLimit {
		return false
	}
	// Weekly
	if k.RPWLimit > 0 && e.getMetricCount(k.ID, model.WindowRPW, k.RPWMetric) >= k.RPWLimit {
		return false
	}
	// Monthly
	if k.RPMLimitMonth > 0 && e.getMetricCount(k.ID, model.WindowRPMo, k.RPMMetric) >= k.RPMLimitMonth {
		return false
	}
	return true
}

// getMetricCount returns the count for a window using the appropriate metric (requests or tokens)
func (e *Engine) getMetricCount(keyID int64, wt model.WindowType, metric string) int64 {
	switch metric {
	case "tokens":
		return e.WindowManager.GetTokens(keyID, wt)
	default:
		return e.WindowManager.GetCount(keyID, wt)
	}
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

// RecordSuccess updates window counters and consumption after a successful relay
func (e *Engine) RecordSuccess(keyID int64, tokens int64) {
	e.WindowManager.IncrementAll(keyID, tokens)
}

// MarkKeyRateLimited marks a key as rate limited.
// A status guard prevents a stale in-flight relay response (the key was
// picked earlier, then the admin disabled it) from clobbering newer state,
// and the cooldown guard never SHRINKS an existing longer cooldown (a
// sibling relay response must not re-admit a hot key early).
func (e *Engine) MarkKeyRateLimited(keyID int64, retryAfter time.Duration) {
	until := time.Now().Add(retryAfter)
	res := db.GetDB().Model(&model.Key{}).
		Where("id = ? AND status <> ? AND (rate_limited_until IS NULL OR rate_limited_until <= ?)",
			keyID, model.KeyStatusDisabled, until).
		Updates(map[string]interface{}{
			"status":             model.KeyStatusRateLimited,
			"rate_limited_until": until,
		})
	if res.Error != nil {
		log.Printf("[selector] MarkKeyRateLimited DB error for key %d: %v", keyID, res.Error)
	}
	if res.RowsAffected == 0 {
		// Key is disabled (admin), already has a longer cooldown, or is in a
		// worse state — don't downgrade it, and don't touch the memory cache
		return
	}
	e.updateKeyStatus(keyID, model.KeyStatusRateLimited, &until)
}

// MarkKeyDisabled marks a key as disabled due to auth error.
// The status guard keeps a deliberately-disabled key from being re-marked
// by a stale in-flight relay response.
func (e *Engine) MarkKeyDisabled(keyID int64, reason string) {
	res := db.GetDB().Model(&model.Key{}).
		Where("id = ? AND status <> ?", keyID, model.KeyStatusDisabled).
		Updates(map[string]interface{}{
			"status":          model.KeyStatusDisabled,
			"disabled_reason": reason,
		})
	if res.Error != nil {
		log.Printf("[selector] MarkKeyDisabled DB error for key %d: %v", keyID, res.Error)
	}
	if res.RowsAffected == 0 {
		return // already disabled (admin) — don't clobber
	}
	e.updateKeyStatus(keyID, model.KeyStatusDisabled, nil)
	e.updateKeyDisabledReason(keyID, reason)
}

// MarkKeyActive marks a key as active (e.g., after health check recovers).
// Guarded so a stale recovery can't clobber a fresh cooldown or a deliberate
// admin disable; auth_failed-disabled keys (auto-recoverable) are allowed.
func (e *Engine) MarkKeyActive(keyID int64) {
	res := db.GetDB().Model(&model.Key{}).
		Where("id = ? AND (status <> ? OR disabled_reason = ?) AND (rate_limited_until IS NULL OR rate_limited_until <= ?)",
			keyID, model.KeyStatusDisabled, "auth_failed", time.Now()).
		Updates(map[string]interface{}{
			"status":             model.KeyStatusActive,
			"rate_limited_until": nil,
			"disabled_reason":    "",
		})
	if res.Error != nil {
		log.Printf("[selector] MarkKeyActive DB error for key %d: %v", keyID, res.Error)
	}
	if res.RowsAffected == 0 {
		return // state changed — keep memory consistent with DB
	}
	e.updateKeyStatus(keyID, model.KeyStatusActive, nil)
}

// updateKeyStatus updates the in-memory key status.
// It mutates BOTH the current keys map and any cached RouteEntry.Keys slices
// that may still reference older key objects (a failed Refresh leaves routes
// pointing at the previous generation of key objects).
func (e *Engine) updateKeyStatus(keyID int64, status string, rateLimitedUntil *time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Update the current keys map
	for providerID, keys := range e.keys {
		for _, k := range keys {
			if k.ID == keyID {
				k.Status = status
				k.RateLimitedUntil = rateLimitedUntil
				if status == model.KeyStatusActive {
					k.DisabledReason = ""
					k.RateLimitedUntil = nil
				}
				e.keys[providerID] = keys
				break
			}
		}
	}

	// Update any cached route entries still referencing older key objects
	for _, entries := range e.routes {
		for _, entry := range entries {
			for _, k := range entry.Keys {
				if k.ID == keyID {
					k.Status = status
					k.RateLimitedUntil = rateLimitedUntil
					if status == model.KeyStatusActive {
						k.DisabledReason = ""
						k.RateLimitedUntil = nil
					}
				}
			}
		}
	}
}

// updateKeyDisabledReason syncs the in-memory disabled_reason with the DB
// (both the keys map and any stale route-entry key objects)
func (e *Engine) updateKeyDisabledReason(keyID int64, reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	for _, keys := range e.keys {
		for _, k := range keys {
			if k.ID == keyID {
				k.DisabledReason = reason
			}
		}
	}
	for _, entries := range e.routes {
		for _, entry := range entries {
			for _, k := range entry.Keys {
				if k.ID == keyID {
					k.DisabledReason = reason
				}
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

// RetryLoop selects a route and key for a model group.
// It walks ALL priority tiers in order — the retry budget lives in the caller
// (handler/chat.go) — and within each tier tries every route (in
// weighted-random order) until a key is found, so a route whose keys are
// exhausted does not prevent healthy sibling routes in the same tier from
// being used, and lower-priority fallback routes stay reachable regardless
// of the configured retry count.
//
// excludedRouteIDs skips routes already proven unable to serve THIS request
// (e.g. an embeddings request hitting an anthropic-only route), so selection
// converges deterministically onto a serving route instead of re-rolling onto
// the unsupported one on every attempt.
func (e *Engine) RetryLoop(modelGroupID string, excludedRouteIDs map[int64]bool) (*RouteEntry, *model.Key, error) {
	entries := e.GetRoutes(modelGroupID)
	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("no available route or key for model %s", modelGroupID)
	}

	// Group by priority
	priorityGroups := make(map[int][]*RouteEntry)
	var priorities []int
	for _, entry := range entries {
		if excludedRouteIDs != nil && excludedRouteIDs[entry.Route.ID] {
			continue
		}
		p := entry.Route.Priority
		if _, ok := priorityGroups[p]; !ok {
			priorities = append(priorities, p)
		}
		priorityGroups[p] = append(priorityGroups[p], entry)
	}

	if len(priorities) == 0 {
		return nil, nil, fmt.Errorf("no available route or key for model %s", modelGroupID)
	}

	sort.Ints(priorities)

	for _, prio := range priorities {
		group := priorityGroups[prio]

		// Try routes in weighted-random order until one yields an available key.
		order := weightedOrder(group)

		// Pass 1: routes that have an immediate-recovery key available — lazy
		// keys are only used when no immediate keys exist anywhere in the tier.
		for _, route := range order {
			if !e.routeHasImmediateKey(route) {
				continue
			}
			if key := e.SelectKey(route); key != nil {
				return route, key, nil
			}
		}
		// Pass 2: any remaining route (lazy keys or fully immediate-exhausted)
		for _, route := range order {
			if key := e.SelectKey(route); key != nil {
				return route, key, nil
			}
		}
	}

	return nil, nil, fmt.Errorf("no available route or key for model %s", modelGroupID)
}

// routeHasImmediateKey reports whether the route has at least one available
// key whose recovery strategy is immediate.
func (e *Engine) routeHasImmediateKey(route *RouteEntry) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, k := range route.Keys {
		if k.RecoveryStrategy != model.RecoveryImmediate {
			continue
		}
		if e.isKeyAvailable(k) && e.isKeyWithinLimits(k) {
			return true
		}
	}
	return false
}

// weightedOrder returns the entries in weighted-random order (each entry used
// at most once), so routes with higher weight tend to be tried first.
func weightedOrder(entries []*RouteEntry) []*RouteEntry {
	remaining := make([]*RouteEntry, len(entries))
	copy(remaining, entries)
	order := make([]*RouteEntry, 0, len(entries))

	for len(remaining) > 0 {
		totalWeight := 0
		for _, en := range remaining {
			totalWeight += effectiveWeight(en)
		}
		roll := rand.Intn(totalWeight)
		idx := 0
		for i, en := range remaining {
			roll -= effectiveWeight(en)
			if roll < 0 {
				idx = i
				break
			}
		}
		order = append(order, remaining[idx])
		remaining = append(remaining[:idx], remaining[idx+1:]...)
	}
	return order
}

// effectiveWeight returns a route's weight, defaulting to 10 when unset/invalid
func effectiveWeight(en *RouteEntry) int {
	if en.Route.Weight <= 0 {
		return 10
	}
	return en.Route.Weight
}
