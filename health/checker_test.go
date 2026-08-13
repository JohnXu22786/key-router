package health

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"key-router/db"
	"key-router/model"
)

// newTestEnv wires a temp DB with one provider (pointing at the given
// upstream handler) and one key, and returns the checker + the persisted
// key. The upstream handler lets tests simulate successful or failing
// health probes without network access.
func newTestEnv(t *testing.T, upstream http.HandlerFunc) (*Checker, *model.Key) {
	t.Helper()
	if err := db.Init(filepath.Join(t.TempDir(), "data")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.GetDB().DB(); err == nil {
			sqlDB.Close()
		}
	})

	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)

	prov := &model.Provider{Name: "mock", Type: "openai", BaseURL: server.URL}
	if err := db.GetDB().Create(prov).Error; err != nil {
		t.Fatal(err)
	}
	k := &model.Key{ProviderID: prov.ID, KeyValue: "sk-test", Name: "k1"}
	if err := db.GetDB().Create(k).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.GetDB().First(k).Error; err != nil {
		t.Fatal(err)
	}
	return NewChecker(), k
}

func okUpstream(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{}`))
}

// TestCheckKeyDoesNotRecoverDuringCooldown guards the relay's rate-limit
// failover: a rate-limited key with a still-running cooldown must NOT be
// flipped back to active by a successful health probe. Recovering early
// re-admits a hot key, the next request re-triggers the 429, and the
// status ping-pongs (rate_limited -> active -> rate_limited).
func TestCheckKeyDoesNotRecoverDuringCooldown(t *testing.T) {
	c, k := newTestEnv(t, okUpstream)
	until := time.Now().Add(10 * time.Minute)
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":             model.KeyStatusRateLimited,
		"rate_limited_until": until,
	})
	// Reload so checkKey sees the persisted state
	db.GetDB().First(k)

	recovered := false
	c.SetOnKeyRecovered(func(keyID int64) { recovered = true })

	c.checkKey(k)

	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusRateLimited {
		t.Errorf("status = %q, want rate_limited (cooldown still running)", after.Status)
	}
	if after.RateLimitedUntil == nil || !after.RateLimitedUntil.Equal(until) {
		t.Errorf("rate_limited_until = %v, want %v (cooldown must not be wiped)", after.RateLimitedUntil, until)
	}
	if recovered {
		t.Error("onRecovered fired while the cooldown was still running")
	}
}

// TestCheckKeyRecoversAfterCooldownExpiry: once the cooldown has passed, a
// successful probe must recover the key (rate_limited -> active).
func TestCheckKeyRecoversAfterCooldownExpiry(t *testing.T) {
	c, k := newTestEnv(t, okUpstream)
	expired := time.Now().Add(-1 * time.Minute)
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":             model.KeyStatusRateLimited,
		"rate_limited_until": expired,
	})
	db.GetDB().First(k)

	recovered := false
	c.SetOnKeyRecovered(func(keyID int64) { recovered = true })

	c.checkKey(k)

	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusActive {
		t.Errorf("status = %q, want active (cooldown expired, probe ok)", after.Status)
	}
	if !recovered {
		t.Error("onRecovered not fired after cooldown expiry")
	}
}

// TestRecordFailureRequiresConsecutiveFailures guards against the
// auth_failed/rate_limited status flash: a SINGLE transient probe failure
// (upstream hiccup) must not yank a healthy key out of rotation — the
// auto-disable (via the onFailed callback, which main.go wires to
// engine.MarkKeyDisabled) only happens once probes fail consecutively.
func TestRecordFailureRequiresConsecutiveFailures(t *testing.T) {
	c, k := newTestEnv(t, okUpstream)
	var failed []string
	c.SetOnKeyFailed(func(keyID int64, reason string) { failed = append(failed, reason) })

	c.recordFailure(k, "auth_failed")
	if len(failed) != 0 {
		t.Fatalf("disabled after a single probe failure: %v", failed)
	}
	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusActive {
		t.Errorf("status = %q, want active after one transient failure", after.Status)
	}
	// The failure reason is still persisted (visible in the UI) — but the
	// key stays in rotation.
	if after.DisabledReason != "auth_failed" {
		t.Errorf("disabled_reason = %q, want auth_failed (reason should still be recorded)", after.DisabledReason)
	}

	c.recordFailure(k, "auth_failed")
	if len(failed) != 1 || failed[0] != "auth_failed" {
		t.Fatalf("onFailed = %v, want [auth_failed] after two consecutive failures", failed)
	}
}

// TestRecordFailureFallbackDisablesConsecutively covers the no-callback
// path (no main.go wiring, e.g. embedded use): two consecutive auth
// failures must end with the key disabled in the DB.
func TestRecordFailureFallbackDisablesConsecutively(t *testing.T) {
	c, k := newTestEnv(t, okUpstream) // no onFailed wired

	c.recordFailure(k, "auth_failed")
	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusActive {
		t.Fatalf("status = %q, want active after one transient failure (fallback path)", after.Status)
	}

	c.recordFailure(k, "auth_failed")
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusDisabled {
		t.Errorf("status = %q, want disabled after consecutive failures (fallback path)", after.Status)
	}
	if after.DisabledReason != "auth_failed" {
		t.Errorf("disabled_reason = %q, want auth_failed", after.DisabledReason)
	}
}

// TestRecordFailureMixedFailureSequence: an auth failure that follows
// UNRELATED failures (rate_limited/upstream_error) is not a repeated auth
// failure — the key must stay in rotation until auth fails again. Rate
// limits and upstream errors also keep the general backoff counter busy,
// which must not trip the auth-disable gate. An intervening non-auth
// failure also BREAKS an auth streak: [auth, rate_limited, auth] must not
// disable on the last probe.
func TestRecordFailureMixedFailureSequence(t *testing.T) {
	c, k := newTestEnv(t, okUpstream)
	var failed []string
	c.SetOnKeyFailed(func(keyID int64, reason string) { failed = append(failed, reason) })

	c.recordFailure(k, "rate_limited") // unrelated failure
	c.recordFailure(k, "auth_failed")  // first auth failure — must NOT disable
	if len(failed) != 0 {
		t.Fatalf("disabled after [rate_limited, auth_failed]: %v", failed)
	}
	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusActive {
		t.Fatalf("status = %q, want active after [rate_limited, auth_failed]", after.Status)
	}

	c.recordFailure(k, "auth_failed") // second auth failure — disable
	if len(failed) != 1 || failed[0] != "auth_failed" {
		t.Fatalf("onFailed = %v, want [auth_failed] after repeated auth failures", failed)
	}
}

// TestRecordFailureNonAuthBreaksAuthStreak: an auth failure, then an
// unrelated failure, then another auth failure must NOT disable — the
// streak was broken in the middle, so the final auth failure is the first
// of a new streak.
func TestRecordFailureNonAuthBreaksAuthStreak(t *testing.T) {
	c, k := newTestEnv(t, okUpstream)
	var failed []string
	c.SetOnKeyFailed(func(keyID int64, reason string) { failed = append(failed, reason) })

	c.recordFailure(k, "auth_failed")
	c.recordFailure(k, "rate_limited") // breaks the auth streak
	c.recordFailure(k, "auth_failed")  // first auth failure of a NEW streak
	if len(failed) != 0 {
		t.Fatalf("disabled after [auth_failed, rate_limited, auth_failed]: %v", failed)
	}
}
