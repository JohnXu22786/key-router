package selector

import (
	"reflect"
	"testing"
	"time"

	"key-router/db"
	"key-router/model"
)

// recordResult helpers -----------------------------------------------------
//
// These tests pin the key status STATE MACHINE (the "2 of the same" rules
// the UI/relay are built on):
//
//   - Every request/probe outcome is recorded in order (RecordResult).
//   - A failure marks the key once, then the key is cooled so the retry
//     loop fails over to the next key.
//   - 2 consecutive failures with the SAME permanent reason (auth_failed /
//     insufficient_quota) disable the key. Transient reasons (http_429,
//     http_5xx, ...) only cool, forever.
//   - 2 consecutive successes return the key to active. A single success
//     never re-admits a cooled/disabled key (that single-success recovery
//     is what made a flaky key look "active" while traffic kept failing
//     over to the next one).
//   - An intervening success breaks the failure streak and vice versa.

func mustKey(t *testing.T, e *Engine, k *model.Key) *model.Key {
	t.Helper()
	if err := db.GetDB().Create(k).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().First(k).Error; err != nil {
		t.Fatal(err)
	}
	return k
}

func loadKey(t *testing.T, id int64) model.Key {
	t.Helper()
	var k model.Key
	if err := db.GetDB().First(&k, id).Error; err != nil {
		t.Fatal(err)
	}
	return k
}

// TestRecordResultSingleFailureCoolsKey: the first failure must mark the
// key once and take it out of rotation for failover (rate_limited + the
// reason the UI displays) — but must NOT disable it.
func TestRecordResultSingleFailureCoolsKey(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})

	var got []string
	e.SetOnStatusChanged(func(_ int64, status string) { got = append(got, status) })

	e.RecordResult(k.ID, false, "http_429", 30*time.Second)

	after := loadKey(t, k.ID)
	if after.Status != model.KeyStatusRateLimited {
		t.Fatalf("status = %q, want rate_limited after a single failure", after.Status)
	}
	if after.DisabledReason != "http_429" {
		t.Errorf("disabled_reason = %q, want http_429 (the UI shows why the key is down)", after.DisabledReason)
	}
	if after.RateLimitedUntil == nil || time.Now().After(*after.RateLimitedUntil) {
		t.Errorf("rate_limited_until = %v, want a future cooldown (failover must skip this key)", after.RateLimitedUntil)
	}
	if !reflect.DeepEqual(got, []string{model.KeyStatusRateLimited}) {
		t.Errorf("events = %v, want [rate_limited]", got)
	}
}

// TestRecordResultTwoSameFailuresDisable: two consecutive failures with the
// SAME permanent reason disable the key. This is the "must fail 2x with the
// same problem before disabling" rule — a single 401/402 in real traffic
// must no longer brick a healthy key.
func TestRecordResultTwoSameFailuresDisable(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})

	e.RecordResult(k.ID, false, model.ReasonAuthFailed, 30*time.Second)
	if after := loadKey(t, k.ID); after.Status != model.KeyStatusRateLimited {
		t.Fatalf("status after 1st failure = %q, want rate_limited (not disabled)", after.Status)
	}

	e.RecordResult(k.ID, false, model.ReasonAuthFailed, 30*time.Second)
	after := loadKey(t, k.ID)
	if after.Status != model.KeyStatusDisabled {
		t.Errorf("status after 2nd identical failure = %q, want disabled", after.Status)
	}
	if after.DisabledReason != model.ReasonAuthFailed {
		t.Errorf("disabled_reason = %q, want auth_failed", after.DisabledReason)
	}
}

// TestRecordResultTwoSameQuotaFailuresDisable: same rule for quota
// exhaustion (402 / 429 + billing code).
func TestRecordResultTwoSameQuotaFailuresDisable(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})

	e.RecordResult(k.ID, false, model.ReasonInsufficientQuota, 30*time.Second)
	e.RecordResult(k.ID, false, model.ReasonInsufficientQuota, 30*time.Second)
	if after := loadKey(t, k.ID); after.Status != model.KeyStatusDisabled || after.DisabledReason != model.ReasonInsufficientQuota {
		t.Errorf("status = %q reason = %q, want disabled/insufficient_quota", after.Status, after.DisabledReason)
	}
}

// TestRecordResultDifferentFailuresDoNotDisable: failures with DIFFERENT
// reasons never accumulate — each new reason starts a fresh streak, so
// [auth_failed, http_429, auth_failed] must not disable on the last one.
func TestRecordResultDifferentFailuresDoNotDisable(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})

	e.RecordResult(k.ID, false, model.ReasonAuthFailed, 30*time.Second)
	e.RecordResult(k.ID, false, "http_429", 30*time.Second)
	e.RecordResult(k.ID, false, model.ReasonAuthFailed, 30*time.Second)
	if after := loadKey(t, k.ID); after.Status == model.KeyStatusDisabled {
		t.Fatal("status = disabled after [auth_failed, http_429, auth_failed] — the streak was broken mid-way")
	}
}

// TestRecordResultSuccessBreaksFailureStreak: an intervening success resets
// the failure streak — [auth_failed, success, auth_failed] must not disable.
func TestRecordResultSuccessBreaksFailureStreak(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})

	e.RecordResult(k.ID, false, model.ReasonAuthFailed, 30*time.Second)
	e.RecordResult(k.ID, true, "", 0)
	e.RecordResult(k.ID, false, model.ReasonAuthFailed, 30*time.Second)
	if after := loadKey(t, k.ID); after.Status == model.KeyStatusDisabled {
		t.Fatal("status = disabled after [auth_failed, success, auth_failed] — success must break the streak")
	}
}

// TestRecordResultTransientNeverDisables: even two consecutive identical
// transient failures (HTTP 429 / 5xx) must only cool the key — a rate limit
// resolves on its own and the health checker's probes will recover the key.
func TestRecordResultTransientNeverDisables(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})

	e.RecordResult(k.ID, false, "http_429", 30*time.Second)
	e.RecordResult(k.ID, false, "http_429", 30*time.Second)
	e.RecordResult(k.ID, false, "http_429", 30*time.Second)
	after := loadKey(t, k.ID)
	if after.Status == model.KeyStatusDisabled {
		t.Fatal("status = disabled after repeated 429s — transient reasons must never disable")
	}
	if after.Status != model.KeyStatusRateLimited {
		t.Errorf("status = %q, want rate_limited", after.Status)
	}
	if after.DisabledReason != "http_429" {
		t.Errorf("disabled_reason = %q, want http_429", after.DisabledReason)
	}
}

// TestRecordResultSingleSuccessDoesNotRecover: one success on a cooled key
// must NOT flip it back to active — recovery needs 2 consecutive successes
// (the key must prove itself twice before re-entering rotation).
func TestRecordResultSingleSuccessDoesNotRecover(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})

	e.RecordResult(k.ID, false, "http_429", 0) // cooldown already expired (0s)

	var got []string
	e.SetOnStatusChanged(func(_ int64, status string) { got = append(got, status) })

	e.RecordResult(k.ID, true, "", 0)
	if after := loadKey(t, k.ID); after.Status != model.KeyStatusRateLimited {
		t.Fatalf("status = %q after one success, want rate_limited (needs 2 consecutive successes)", after.Status)
	}
	if len(got) != 0 {
		t.Errorf("events = %v, want none after a single success", got)
	}
}

// TestRecordResultTwoSuccessesRecover: after 2 consecutive successes a
// cooled key returns to active, clearing the cooldown and reason.
func TestRecordResultTwoSuccessesRecover(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})

	e.RecordResult(k.ID, false, "http_429", 0) // cooldown expired

	var got []string
	e.SetOnStatusChanged(func(_ int64, status string) { got = append(got, status) })

	e.RecordResult(k.ID, true, "", 0)
	e.RecordResult(k.ID, true, "", 0)

	after := loadKey(t, k.ID)
	if after.Status != model.KeyStatusActive {
		t.Fatalf("status = %q after 2 successes, want active", after.Status)
	}
	if after.DisabledReason != "" {
		t.Errorf("disabled_reason = %q, want cleared on recovery", after.DisabledReason)
	}
	if after.RateLimitedUntil != nil {
		t.Errorf("rate_limited_until = %v, want nil on recovery", after.RateLimitedUntil)
	}
	if !reflect.DeepEqual(got, []string{model.KeyStatusActive}) {
		t.Errorf("events = %v, want [active]", got)
	}
}

// TestRecordResultRecoversDisabledAuthFailedKey: a key disabled for
// auth_failed comes back after 2 consecutive successful observations (e.g.
// the admin fixed the key and two probes pass) — the state machine, not a
// timer, decides recovery.
func TestRecordResultRecoversDisabledAuthFailedKey(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})

	e.MarkKeyDisabled(k.ID, model.ReasonAuthFailed)

	e.RecordResult(k.ID, true, "", 0)
	if after := loadKey(t, k.ID); after.Status != model.KeyStatusDisabled {
		t.Fatalf("status after 1st success = %q, want disabled (needs 2 successes)", after.Status)
	}
	e.RecordResult(k.ID, true, "", 0)
	if after := loadKey(t, k.ID); after.Status != model.KeyStatusActive {
		t.Errorf("status after 2nd success = %q, want active", after.Status)
	}
}

// TestRecordResultNeverRecoversAdminDisabledKey: a deliberately disabled
// key (empty disabled_reason) must never be re-admitted by observations —
// only an explicit admin action can re-enable it.
func TestRecordResultNeverRecoversAdminDisabledKey(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":          model.KeyStatusDisabled,
		"disabled_reason": "",
	})
	e.Refresh() // sync the engine's memory cache

	var got []string
	e.SetOnStatusChanged(func(_ int64, status string) { got = append(got, status) })

	e.RecordResult(k.ID, true, "", 0)
	e.RecordResult(k.ID, true, "", 0)

	if after := loadKey(t, k.ID); after.Status != model.KeyStatusDisabled {
		t.Errorf("status = %q, want disabled (admin-disabled keys never auto-recover)", after.Status)
	}
	if len(got) != 0 {
		t.Errorf("events = %v, want none for an admin-disabled key", got)
	}
}

// TestRecordResultNeverRecoversBudgetCappedKey: the lifetime spend cap is an
// administrative limit — 2 successful observations must not revive a
// spend_limit_exhausted key.
func TestRecordResultNeverRecoversBudgetCappedKey(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":            model.KeyStatusDisabled,
		"disabled_reason":   model.KeyDisabledReasonSpendLimit,
		"total_spend_limit": 1000,
		"total_spent":       1000,
	})
	e.Refresh()

	var got []string
	e.SetOnStatusChanged(func(_ int64, status string) { got = append(got, status) })

	e.RecordResult(k.ID, true, "", 0)
	e.RecordResult(k.ID, true, "", 0)

	after := loadKey(t, k.ID)
	if after.Status != model.KeyStatusDisabled {
		t.Errorf("status = %q, want disabled (budget cap never auto-recovers)", after.Status)
	}
	if after.DisabledReason != model.KeyDisabledReasonSpendLimit {
		t.Errorf("disabled_reason = %q, want spend_limit_exhausted", after.DisabledReason)
	}
	if len(got) != 0 {
		t.Errorf("events = %v, want none for a budget-capped key", got)
	}
}

// TestRecordResultCooldownRunningBlocksRecovery: two successes while a
// fresh cooldown is still running must not flip the key active — the
// upstream's own wait instruction wins over observed success (the probe was
// answered before the new cooldown landed).
func TestRecordResultCooldownRunningBlocksRecovery(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})

	e.RecordResult(k.ID, false, "http_429", 10*time.Minute) // cooldown still running

	var got []string
	e.SetOnStatusChanged(func(_ int64, status string) { got = append(got, status) })

	e.RecordResult(k.ID, true, "", 0)
	e.RecordResult(k.ID, true, "", 0)

	after := loadKey(t, k.ID)
	if after.Status != model.KeyStatusRateLimited {
		t.Errorf("status = %q, want rate_limited (cooldown must not be wiped by successes)", after.Status)
	}
	if after.RateLimitedUntil == nil || time.Now().After(*after.RateLimitedUntil) {
		t.Errorf("rate_limited_until = %v, want the future cooldown preserved", after.RateLimitedUntil)
	}
	if len(got) != 0 {
		t.Errorf("events = %v, want none while the cooldown is running", got)
	}
}

// TestRecordResultNeverShrinksCooldown: a failure carrying a SHORTER
// cooldown than the one already running must not re-admit the key early —
// but the newest reason is still recorded for the UI (and that reason
// change pushes an SSE event so the UI updates in place).
func TestRecordResultNeverShrinksCooldown(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})

	var got []string
	e.SetOnStatusChanged(func(_ int64, status string) { got = append(got, status) })

	e.RecordResult(k.ID, false, "http_429", 60*time.Second) // long cooldown
	first := loadKey(t, k.ID)
	if first.RateLimitedUntil == nil {
		t.Fatal("no cooldown after first failure")
	}

	e.RecordResult(k.ID, false, "http_500", 5*time.Second) // shorter cooldown, new reason

	after := loadKey(t, k.ID)
	if after.RateLimitedUntil == nil || !after.RateLimitedUntil.Equal(*first.RateLimitedUntil) {
		t.Errorf("rate_limited_until = %v, want %v (cooldown must never shrink)", after.RateLimitedUntil, first.RateLimitedUntil)
	}
	if after.DisabledReason != "http_500" {
		t.Errorf("disabled_reason = %q, want http_500 (latest reason still recorded)", after.DisabledReason)
	}
	// Both failures published: the status flip on the first, and the
	// reason-only sync on the second (the reason change must reach the UI
	// without waiting for the next poll).
	if !reflect.DeepEqual(got, []string{model.KeyStatusRateLimited, model.KeyStatusRateLimited}) {
		t.Errorf("events = %v, want [rate_limited rate_limited]", got)
	}
}

// TestRecordResultFailureOnDisabledKeyIsNoOp: observations of an
// already-disabled key must not clobber its disable reason or fire events.
func TestRecordResultFailureOnDisabledKeyIsNoOp(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})

	e.MarkKeyDisabled(k.ID, model.ReasonAuthFailed)

	var got []string
	e.SetOnStatusChanged(func(_ int64, status string) { got = append(got, status) })

	e.RecordResult(k.ID, false, "http_429", 30*time.Second)

	after := loadKey(t, k.ID)
	if after.Status != model.KeyStatusDisabled || after.DisabledReason != model.ReasonAuthFailed {
		t.Errorf("status = %q reason = %q, want disabled/auth_failed preserved", after.Status, after.DisabledReason)
	}
	if len(got) != 0 {
		t.Errorf("events = %v, want none for an already-disabled key", got)
	}
}

// TestResetOutcomeClearsStreaks: an admin edit / reset must clear the
// half-built streaks, so the next failure starts from zero instead of
// inheriting a pre-edit strike.
func TestResetOutcomeClearsStreaks(t *testing.T) {
	e := newTestEngine(t)
	k := mustKey(t, e, &model.Key{ProviderID: 1, Status: model.KeyStatusActive})

	e.RecordResult(k.ID, false, model.ReasonAuthFailed, 30*time.Second) // strike 1
	e.ResetOutcome(k.ID)

	// One post-reset failure must NOT disable: without the reset, this
	// failure would have completed a streak of 2.
	e.RecordResult(k.ID, false, model.ReasonAuthFailed, 30*time.Second)
	if after := loadKey(t, k.ID); after.Status == model.KeyStatusDisabled {
		t.Fatal("status = disabled after [fail, reset, fail] — reset must clear the pre-edit strike")
	}

	// A second post-reset identical failure does disable (2 consecutive
	// post-reset observations).
	e.RecordResult(k.ID, false, model.ReasonAuthFailed, 30*time.Second)
	if after := loadKey(t, k.ID); after.Status != model.KeyStatusDisabled {
		t.Errorf("status = %q, want disabled after 2 consecutive post-reset failures", after.Status)
	}
}
