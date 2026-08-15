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

	// onStatusChanged, when set, is invoked after a key's status flips in
	// the DB (rate_limited / disabled / active). The UI hot-reload path
	// depends on it: every status write funnels through updateKeyStatus, so
	// this single callback turns "a key changed" into an SSE push.
	onStatusChanged func(keyID int64, status string)

	// outcomeMu guards the per-key observation streaks below. The streaks
	// are the raw material of the key status STATE MACHINE (RecordResult):
	// every relayed request and every health probe is an observation,
	// recorded in arrival order, and status flips happen only when a streak
	// reaches its threshold — never on a single observation and never on a
	// timer.
	outcomeMu sync.Mutex
	outcomes  map[int64]*KeyOutcome
}

// KeyOutcome tracks the ordered request/probe results for one key:
//
//   - SuccessStreak: consecutive successful observations (capped at 2).
//     2 consecutive successes are required before a non-active key may
//     return to active — a single success must not re-admit a flaky key
//     (that single-success recovery is what made a failing key look
//     "active" in the UI while traffic kept failing over to the next one).
//   - FailureStreak / LastReason: consecutive failures with the SAME
//     reason. 2 consecutive failures with the same PERMANENT reason
//     (auth_failed / insufficient_quota) disable the key; any other
//     sequence (different reasons, an intervening success) restarts the
//     streak.
type KeyOutcome struct {
	SuccessStreak int
	FailureStreak int
	LastReason    string
}

// SetOnStatusChanged registers a callback invoked whenever a key's status
// changes (via RecordResult / MarkKeyDisabled / MarkKeyActive). It is
// called after the DB write, while the engine lock is held — keep it cheap
// and non-blocking (e.g. publish to an event hub).
func (e *Engine) SetOnStatusChanged(cb func(keyID int64, status string)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onStatusChanged = cb
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
		outcomes:      make(map[int64]*KeyOutcome),
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
// Lazy keys are only used when no immediate keys are available. Within each
// strategy, keys are tried in ascending sort_order — the order the user
// arranged them in the UI (sort order = call order).
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

	// Prefer immediate keys; only use lazy keys when no immediate ones are
	// available. Within each bucket, keys are tried in sort_order (UI order).
	if len(immediateKeys) > 0 {
		return firstBySortOrder(immediateKeys)
	}
	if len(lazyKeys) > 0 {
		return firstBySortOrder(lazyKeys)
	}
	return nil
}

// firstBySortOrder returns the key with the smallest sort_order. Ties fall
// back to the smallest ID for deterministic behavior.
func firstBySortOrder(keys []*model.Key) *model.Key {
	best := keys[0]
	for _, k := range keys[1:] {
		if k.SortOrder < best.SortOrder ||
			(k.SortOrder == best.SortOrder && k.ID < best.ID) {
			best = k
		}
	}
	return best
}

// limitedWindows returns the window types whose limits are currently
// exhausted for the key — the reasons SelectKey skips it while its status
// stays "active". Empty when the key is within all limits. Cost-metric
// windows compare against the cost bucket, token-metric windows against
// tokens, everything else against the request count.
func (e *Engine) limitedWindows(k *model.Key) []string {
	var windows []string
	// RPM always checks request count
	if k.RPMLimit > 0 && e.WindowManager.GetCount(k.ID, model.WindowRPM) >= k.RPMLimit {
		windows = append(windows, string(model.WindowRPM))
	}
	// TPM always checks token count
	if k.TPMLimit > 0 && e.WindowManager.GetTokens(k.ID, model.WindowTPM) >= k.TPMLimit {
		windows = append(windows, string(model.WindowTPM))
	}
	// 5-hour: check based on configured metric
	if k.RP5hLimit > 0 && e.getMetricCount(k.ID, model.WindowRP5h, k.RP5hMetric) >= k.RP5hLimit {
		windows = append(windows, string(model.WindowRP5h))
	}
	// Daily
	if k.RPDLimit > 0 && e.getMetricCount(k.ID, model.WindowRPD, k.RPDMetric) >= k.RPDLimit {
		windows = append(windows, string(model.WindowRPD))
	}
	// Weekly
	if k.RPWLimit > 0 && e.getMetricCount(k.ID, model.WindowRPW, k.RPWMetric) >= k.RPWLimit {
		windows = append(windows, string(model.WindowRPW))
	}
	// Monthly
	if k.RPMLimitMonth > 0 && e.getMetricCount(k.ID, model.WindowRPMo, k.RPMMetric) >= k.RPMLimitMonth {
		windows = append(windows, string(model.WindowRPMo))
	}
	return windows
}

// isKeyWithinLimits checks all rate limit windows against the key's metric type
func (e *Engine) isKeyWithinLimits(k *model.Key) bool {
	return len(e.limitedWindows(k)) == 0
}

// LimitedWindows returns the window types whose limits are currently
// exhausted for the key — why SelectKey skips it even though its status is
// still "active". Exposed for the admin API so the UI can explain a key
// that is healthy but not being routed. It only touches the caller-owned
// key and the self-synchronized WindowManager, so no engine lock is needed.
func (e *Engine) LimitedWindows(k *model.Key) []string {
	return e.limitedWindows(k)
}

// getMetricCount returns the count for a window using the appropriate metric
// (requests, tokens, or cost — cost is stored in micro-USD)
func (e *Engine) getMetricCount(keyID int64, wt model.WindowType, metric string) int64 {
	switch metric {
	case "tokens":
		return e.WindowManager.GetTokens(keyID, wt)
	case "cost":
		return e.WindowManager.GetCost(keyID, wt)
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

// RecordSuccess updates window counters and consumption after a successful relay.
// costMicroUSD: cost of this request in micro-USD (1e6 per $1), for cost-metric
// rate limits.
func (e *Engine) RecordSuccess(keyID int64, tokens int64, costMicroUSD int64) {
	e.WindowManager.IncrementAllWithCost(keyID, tokens, costMicroUSD)
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

// MarkKeyActive marks a key as active (e.g. after 2 consecutive successful
// observations in RecordResult, or after the health checker recovers a
// key). The UPDATE is fully guarded so a stale recovery can never clobber
// fresher state:
//
//   - The row is only matched when something would actually change
//     (status != active, a lingering reason, or a stale cooldown), so an
//     already-active key produces no write, no memory update and no SSE
//     event — every 2nd success on a healthy key is a no-op.
//   - Deliberately-disabled keys (empty disabled_reason) are never
//     re-admitted — only an explicit admin action can re-enable them.
//   - Budget-capped keys (spend_limit_exhausted) are never re-admitted:
//     the lifetime cap is an administrative limit, not an upstream health
//     condition.
//   - A still-running cooldown blocks recovery: the upstream's own
//     wait-instruction must not be wiped (recovering early re-admits a hot
//     key and the status ping-pongs).
func (e *Engine) MarkKeyActive(keyID int64) {
	res := db.GetDB().Model(&model.Key{}).
		Where("id = ? AND (status <> ? OR (disabled_reason IS NOT NULL AND disabled_reason <> '') OR rate_limited_until IS NOT NULL) AND (status <> ? OR (disabled_reason IS NOT NULL AND disabled_reason <> '')) AND (total_spend_limit IS NULL OR total_spend_limit = 0 OR total_spent < total_spend_limit) AND (rate_limited_until IS NULL OR rate_limited_until <= ?)",
			keyID, model.KeyStatusActive, model.KeyStatusDisabled, time.Now()).
		Updates(map[string]interface{}{
			"status":             model.KeyStatusActive,
			"rate_limited_until": nil,
			"disabled_reason":    "",
		})
	if res.Error != nil {
		log.Printf("[selector] MarkKeyActive DB error for key %d: %v", keyID, res.Error)
	}
	if res.RowsAffected == 0 {
		return // state changed or nothing to do — keep memory consistent with DB
	}
	e.updateKeyStatus(keyID, model.KeyStatusActive, nil)
}

// RecordResult records one ordered observation of a key — the outcome of a
// relayed upstream request or a health probe — and drives the key status
// state machine:
//
//   - Every observation is recorded FIRST, in arrival order (the streak
//     bookkeeping below), before any state is changed.
//   - A failure (ok=false, reason=…) marks the key once, then the key is
//     immediately cooled (failKey) so the retry loop fails over to the
//     next key — the request itself is never abandoned because of a mark.
//   - After 2 consecutive failures with the SAME permanent reason
//     (auth_failed / insufficient_quota) the key is DISABLED. Transient
//     reasons (http_429, http_5xx, network_error, …) only cool, no matter
//     how often they repeat. An intervening success — or a failure with a
//     different reason — restarts the streak.
//   - After 2 consecutive successes (ok=true) the key returns to active,
//     clearing the cooldown and the displayed reason. A single success
//     never re-admits a cooled/disabled key.
func (e *Engine) RecordResult(keyID int64, ok bool, reason string, cooldown time.Duration) {
	e.outcomeMu.Lock()
	oc := e.outcomes[keyID]
	if oc == nil {
		oc = &KeyOutcome{}
		e.outcomes[keyID] = oc
	}
	if ok {
		oc.SuccessStreak++
		oc.FailureStreak = 0
		oc.LastReason = ""
		if oc.SuccessStreak > 2 {
			oc.SuccessStreak = 2
		}
		streak := oc.SuccessStreak
		e.outcomeMu.Unlock()
		if streak >= 2 {
			e.MarkKeyActive(keyID)
		}
		return
	}
	oc.SuccessStreak = 0
	if oc.LastReason == reason {
		oc.FailureStreak++
	} else {
		oc.FailureStreak = 1
		oc.LastReason = reason
	}
	if oc.FailureStreak > 2 {
		oc.FailureStreak = 2
	}
	streak := oc.FailureStreak
	e.outcomeMu.Unlock()

	// Failover now: take the key out of rotation so the retry loop picks
	// the next key instead of re-selecting this one.
	e.failKey(keyID, reason, cooldown)
	if streak >= 2 && model.DisableClassReason(reason) {
		e.MarkKeyDisabled(keyID, reason)
	}
}

// failKey marks a key rate_limited with the given cooldown and persists the
// failure reason for the UI, in one guarded UPDATE:
//
//   - Never downgrades a disabled key (its disable reason must survive).
//   - Never shrinks an existing cooldown (a sibling failure with a shorter
//     Retry-After must not re-admit a hot key early).
//   - Never touches a budget-capped key (spend_limit_exhausted must
//     survive; a capped key is out of rotation by admin decision).
//   - When the row can't be updated (longer cooldown already running),
//     the newest reason is still synced so the UI keeps showing WHY the
//     key is down.
func (e *Engine) failKey(keyID int64, reason string, cooldown time.Duration) {
	until := time.Now().Add(cooldown)
	res := db.GetDB().Model(&model.Key{}).
		Where("id = ? AND status <> ? AND (rate_limited_until IS NULL OR rate_limited_until <= ?) AND (total_spend_limit IS NULL OR total_spend_limit = 0 OR total_spent < total_spend_limit)",
			keyID, model.KeyStatusDisabled, until).
		Updates(map[string]interface{}{
			"status":             model.KeyStatusRateLimited,
			"rate_limited_until": until,
			"disabled_reason":    reason,
		})
	if res.Error != nil {
		log.Printf("[selector] failKey DB error for key %d: %v", keyID, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		// Key is disabled, budget-capped, or already running a longer
		// cooldown — sync only the displayed reason where that is safe.
		e.persistKeyReason(keyID, reason)
		return
	}
	e.updateKeyStatus(keyID, model.KeyStatusRateLimited, &until)
	e.updateKeyDisabledReason(keyID, reason)
}

// persistKeyReason records the failure reason on a COOLED key so the UI can
// show why it is down (e.g. "HTTP 429"). Disabled keys keep their disable
// reason, active keys have nothing to show, and budget-capped keys keep the
// cap reason. Fires the SSE event when the reason actually changed so the
// UI updates in place without waiting for its next poll.
func (e *Engine) persistKeyReason(keyID int64, reason string) {
	res := db.GetDB().Model(&model.Key{}).
		Where("id = ? AND status = ? AND (disabled_reason IS NULL OR disabled_reason <> ?) AND (total_spend_limit IS NULL OR total_spend_limit = 0 OR total_spent < total_spend_limit)",
			keyID, model.KeyStatusRateLimited, reason).
		Updates(map[string]interface{}{"disabled_reason": reason})
	if res.Error != nil {
		log.Printf("[selector] persistKeyReason DB error for key %d: %v", keyID, res.Error)
		return
	}
	if res.RowsAffected == 0 {
		return // reason unchanged, or not a rate_limited key — nothing to show
	}
	e.updateKeyDisabledReason(keyID, reason)
	e.notifyStatusChanged(keyID, model.KeyStatusRateLimited)
}

// notifyStatusChanged publishes the SSE event without a status write (the
// reason-only path of persistKeyReason).
func (e *Engine) notifyStatusChanged(keyID int64, status string) {
	e.mu.Lock()
	cb := e.onStatusChanged
	e.mu.Unlock()
	if cb != nil {
		cb(keyID, status)
	}
}

// ResetOutcome clears a key's recorded streak state. Called on admin
// edits / resets / key re-creation so the new state does not inherit a
// half-built failure or success streak (e.g. one prior auth failure must
// not pre-dispose an edited key).
func (e *Engine) ResetOutcome(keyID int64) {
	e.outcomeMu.Lock()
	delete(e.outcomes, keyID)
	e.outcomeMu.Unlock()
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

	// Notify the SSE push path. Called only from the Mark* methods after a
	// successful DB write (RowsAffected > 0), so no event is published for
	// a no-op change. The callback runs with the lock held: it must not
	// block (the event hub's Publish is non-blocking).
	if e.onStatusChanged != nil {
		e.onStatusChanged(keyID, status)
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
