package health

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"key-router/db"
	"key-router/model"
	"key-router/selector"
)

// newTestEnv wires a temp DB with one provider (pointing at the given
// upstream handler) and one key, and returns the checker + the persisted
// key + a routing engine wired to the checker EXACTLY like main.go wires
// them (checker -> engine.RecordResult). The upstream handler lets tests
// simulate successful or failing health probes without network access, and
// the engine lets them observe the resulting status flips through
// SetOnStatusChanged.
func newTestEnv(t *testing.T, upstream http.HandlerFunc) (*Checker, *model.Key, *selector.Engine) {
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

	engine := selector.NewEngine()
	c := NewChecker()
	c.SetOnKeyResult(func(keyID int64, ok bool, reason string) {
		engine.RecordResult(keyID, ok, reason, 30*time.Second)
	})
	return c, k, engine
}

func okUpstream(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{}`))
}

// TestCheckKeyDoesNotRecoverDuringCooldown guards the relay's rate-limit
// failover: a rate-limited key with a still-running cooldown must NOT be
// flipped back to active by successful health probes. Recovering early
// re-admits a hot key, the next request re-triggers the 429, and the
// status ping-pongs (rate_limited -> active -> rate_limited).
func TestCheckKeyDoesNotRecoverDuringCooldown(t *testing.T) {
	c, k, engine := newTestEnv(t, okUpstream)
	until := time.Now().Add(10 * time.Minute)
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":             model.KeyStatusRateLimited,
		"rate_limited_until": until,
	})
	// Reload so checkKey sees the persisted state
	db.GetDB().First(k)

	var flips []string
	engine.SetOnStatusChanged(func(_ int64, status string) { flips = append(flips, status) })

	c.checkKey(k)
	c.checkKey(k) // even a second successful probe must not recover early

	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusRateLimited {
		t.Errorf("status = %q, want rate_limited (cooldown still running)", after.Status)
	}
	if after.RateLimitedUntil == nil || !after.RateLimitedUntil.Equal(until) {
		t.Errorf("rate_limited_until = %v, want %v (cooldown must not be wiped)", after.RateLimitedUntil, until)
	}
	if len(flips) != 0 {
		t.Errorf("status events = %v, want none while the cooldown is still running", flips)
	}
}

// TestCheckKeyRecoversAfterCooldownExpiry: once the cooldown has passed,
// successful probes must recover the key (rate_limited -> active) — but
// only after TWO consecutive successes, the state machine's enable rule.
func TestCheckKeyRecoversAfterCooldownExpiry(t *testing.T) {
	c, k, engine := newTestEnv(t, okUpstream)
	expired := time.Now().Add(-1 * time.Minute)
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":             model.KeyStatusRateLimited,
		"rate_limited_until": expired,
	})
	db.GetDB().First(k)

	var flips []string
	engine.SetOnStatusChanged(func(_ int64, status string) { flips = append(flips, status) })

	// One success is not enough — the key must prove itself twice.
	c.checkKey(k)
	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusRateLimited {
		t.Fatalf("status after 1st probe = %q, want rate_limited (recovery needs 2 consecutive successes)", after.Status)
	}

	c.checkKey(k)
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusActive {
		t.Errorf("status = %q, want active (cooldown expired, 2 probes ok)", after.Status)
	}
	if !reflect.DeepEqual(flips, []string{model.KeyStatusActive}) {
		t.Errorf("status events = %v, want [active]", flips)
	}
}

// TestCheckKeyDoesNotRecoverAuthFailedKey guards the auth_failed -> active
// -> auth_failed ping-pong: many OpenAI-compatible gateways answer GET
// /v1/models with 200 even for an INVALID key, so a models-listing probe
// cannot prove the key is usable. A key the relay disabled for auth failure
// must stay disabled until a REAL request (a chat completion) succeeds —
// otherwise the checker recovers it on the next pass and the very next
// client request disables it again (the exact flapping users saw).
func TestCheckKeyDoesNotRecoverAuthFailedKey(t *testing.T) {
	// The gateway's models endpoint does not authenticate (200 for any key)
	// but the chat endpoint rejects the key with 401.
	c, k, engine := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"data":[]}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":          model.KeyStatusDisabled,
		"disabled_reason": "auth_failed",
	})
	db.GetDB().First(k)

	var flips []string
	engine.SetOnStatusChanged(func(_ int64, status string) { flips = append(flips, status) })

	// Probe TWICE: a single probe could not distinguish "still failing"
	// from "fail-open misclassification" (recovery needs 2 successes), so
	// two failing probes are the minimal sequence that would flip the key
	// active if the classifier regressed to alive for a bare 401.
	c.checkKey(k)
	c.checkKey(k)

	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusDisabled {
		t.Errorf("status = %q, want disabled (key still fails auth on the chat endpoint)", after.Status)
	}
	if after.DisabledReason != "auth_failed" {
		t.Errorf("disabled_reason = %q, want auth_failed", after.DisabledReason)
	}
	if len(flips) != 0 {
		t.Errorf("status events = %v, want none (a single failure must not change a disabled key)", flips)
	}
}

// TestCheckKeyRecoversAuthFailedKeyWhenUsable: recovery is gated on the key
// being genuinely usable, not blocked forever. Once the chat-completion
// probe succeeds (e.g. the admin fixed the key), a disabled auth_failed key
// returns to active.
func TestCheckKeyRecoversAuthFailedKeyWhenUsable(t *testing.T) {
	c, k, engine := newTestEnv(t, okUpstream)
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":          model.KeyStatusDisabled,
		"disabled_reason": "auth_failed",
	})
	db.GetDB().First(k)

	var flips []string
	engine.SetOnStatusChanged(func(_ int64, status string) { flips = append(flips, status) })

	c.checkKey(k)
	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusDisabled {
		t.Fatalf("status after 1st probe = %q, want disabled (one success is not enough)", after.Status)
	}

	c.checkKey(k)
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusActive {
		t.Errorf("status = %q, want active (key usable again)", after.Status)
	}
	if after.DisabledReason != "" {
		t.Errorf("disabled_reason = %q, want cleared after recovery", after.DisabledReason)
	}
	if !reflect.DeepEqual(flips, []string{model.KeyStatusActive}) {
		t.Errorf("status events = %v, want [active]", flips)
	}
}

// TestCheckKeyDoesNotRecoverSpendLimitExhausted guards the lifetime spend
// cap: the budget is an administrative limit, not an upstream health
// condition, so a successful probe must NOT resurrect a key whose budget is
// exhausted — it stays disabled until an admin resets the spend
// (POST /api/keys/:id/reset-spend). Without this guard the checker
// re-enables the key on the next pass and traffic keeps overspending the
// budget. The key must not even be probed (each probe is a billable chat
// completion), so the upstream handler must see zero requests.
func TestCheckKeyDoesNotRecoverSpendLimitExhausted(t *testing.T) {
	hits := 0
	c, k, _ := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		okUpstream(w, r)
	})
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":            model.KeyStatusDisabled,
		"disabled_reason":   model.KeyDisabledReasonSpendLimit,
		"total_spend_limit": 1000,
		"total_spent":       1000,
	})
	db.GetDB().First(k)

	if c.shouldProbeKey(k) {
		t.Error("shouldProbeKey returned true for a spend-capped key (it would be probed every pass)")
	}

	c.checkKey(k)

	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusDisabled {
		t.Errorf("status = %q, want disabled (budget exhausted must not auto-recover)", after.Status)
	}
	if after.DisabledReason != model.KeyDisabledReasonSpendLimit {
		t.Errorf("disabled_reason = %q, want spend_limit_exhausted", after.DisabledReason)
	}
	if hits != 0 {
		t.Errorf("upstream probed %d time(s), want 0 (spend-capped keys must not be probed)", hits)
	}
}

// TestCheckKeyDoesNotRecoverSpendLimitSetMidProbe covers the race the
// pre-pass guard can't see: the pass loads a healthy-looking key, and while
// the probe is in flight the relay crosses the budget and disables it with
// spend_limit_exhausted. A successful probe must still not revive it (the
// mid-probe DB re-check and the guarded recovery update).
func TestCheckKeyDoesNotRecoverSpendLimitSetMidProbe(t *testing.T) {
	// The handler runs during the probe, after newTestEnv returns — bind the
	// key id through a variable in the closure's scope instead of k itself.
	var keyID int64
	c, k, engine := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		// Simulate the relay's applySpendLimit landing while the probe is in
		// flight: the key's budget crosses the limit and it is disabled.
		db.GetDB().Model(&model.Key{}).Where("id = ?", keyID).Updates(map[string]interface{}{
			"status":          model.KeyStatusDisabled,
			"disabled_reason": model.KeyDisabledReasonSpendLimit,
			"total_spent":     1000,
		})
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})
	keyID = k.ID
	// The key starts selectable (rate-limited with an expired cooldown) so
	// the probe actually runs, and it has a budget for the cap to trip.
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":             model.KeyStatusRateLimited,
		"rate_limited_until": time.Now().Add(-1 * time.Minute),
		"total_spend_limit":  1000,
		"total_spent":        0,
		"disabled_reason":    "",
	})
	db.GetDB().First(k)

	var flips []string
	engine.SetOnStatusChanged(func(_ int64, status string) { flips = append(flips, status) })

	c.checkKey(k)

	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusDisabled {
		t.Errorf("status = %q, want disabled (budget crossed mid-probe must not auto-recover)", after.Status)
	}
	if after.DisabledReason != model.KeyDisabledReasonSpendLimit {
		t.Errorf("disabled_reason = %q, want spend_limit_exhausted", after.DisabledReason)
	}
	if len(flips) != 0 {
		t.Errorf("status events = %v, want none for a budget-capped key", flips)
	}
}

// TestCheckKeyDoesNotOverwriteSpendLimitReasonMidProbe: when the probe
// FAILS after the relay disabled the key for an exhausted budget, the
// failure reason must not clobber spend_limit_exhausted — otherwise the
// next pass would probe the key again (its reason no longer matches the
// cap) and a later success could revive it.
func TestCheckKeyDoesNotOverwriteSpendLimitReasonMidProbe(t *testing.T) {
	var keyID int64
	c, k, _ := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		db.GetDB().Model(&model.Key{}).Where("id = ?", keyID).Updates(map[string]interface{}{
			"status":          model.KeyStatusDisabled,
			"disabled_reason": model.KeyDisabledReasonSpendLimit,
			"total_spent":     1000,
		})
		w.WriteHeader(http.StatusInternalServerError)
	})
	keyID = k.ID
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":             model.KeyStatusRateLimited,
		"rate_limited_until": time.Now().Add(-1 * time.Minute),
		"total_spend_limit":  1000,
		"total_spent":        0,
		"disabled_reason":    "",
	})
	db.GetDB().First(k)

	c.checkKey(k)

	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.DisabledReason != model.KeyDisabledReasonSpendLimit {
		t.Errorf("disabled_reason = %q, want spend_limit_exhausted (failure reason must not clobber the cap)", after.DisabledReason)
	}
	if after.Status != model.KeyStatusDisabled {
		t.Errorf("status = %q, want disabled", after.Status)
	}
}

// TestCheckKeyStaleSnapshotBudgetExhausted pins the entry re-read: the pass
// loaded the key BEFORE the budget was exhausted, so checkKey's stale copy
// looks healthy. The fresh DB state must stop the probe before it fires —
// otherwise a billable chat completion runs on a capped key.
func TestCheckKeyStaleSnapshotBudgetExhausted(t *testing.T) {
	hits := 0
	c, k, _ := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		okUpstream(w, r)
	})
	// Exhaust the budget in the DB but deliberately do NOT reload k: k is
	// the stale pass snapshot (status active, spent 0).
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"total_spend_limit": 1000,
		"total_spent":       1000,
	})

	c.checkKey(k)

	if hits != 0 {
		t.Errorf("upstream probed %d time(s), want 0 (stale snapshot must be re-checked against the DB)", hits)
	}
	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusActive {
		t.Errorf("status = %q, want active (no probe ran, state untouched)", after.Status)
	}
}

// TestCheckKeyActiveStartBudgetCrossedMidProbe pins the ACTIVE-key success
// path: an active key whose budget is exhausted while its probe is in flight
// must stay disabled in the DB — the active branch never writes status, so
// the relay's spend-limit disable must survive untouched.
func TestCheckKeyActiveStartBudgetCrossedMidProbe(t *testing.T) {
	var keyID int64
	c, k, engine := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		db.GetDB().Model(&model.Key{}).Where("id = ?", keyID).Updates(map[string]interface{}{
			"status":          model.KeyStatusDisabled,
			"disabled_reason": model.KeyDisabledReasonSpendLimit,
			"total_spent":     1000,
		})
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	})
	keyID = k.ID
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"total_spend_limit": 1000,
		"total_spent":       0,
	})
	db.GetDB().First(k)

	var flips []string
	engine.SetOnStatusChanged(func(_ int64, status string) { flips = append(flips, status) })

	c.checkKey(k)

	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusDisabled {
		t.Errorf("status = %q, want disabled (active-key probe must not wipe the relay's spend disable)", after.Status)
	}
	if after.DisabledReason != model.KeyDisabledReasonSpendLimit {
		t.Errorf("disabled_reason = %q, want spend_limit_exhausted", after.DisabledReason)
	}
	if len(flips) != 0 {
		t.Errorf("status events = %v, want none for a budget-capped key", flips)
	}
}

// TestOpenAIProbeUsesChatCompletions pins the OpenAI health probe to a real
// authenticated chat-completion request. A GET /v1/models probe returns 200
// even for an invalid key on many gateways and would recover disabled
// auth_failed keys (the auth_failed -> active -> auth_failed ping-pong);
// only an auth failure on the chat endpoint proves the key is still broken.
func TestOpenAIProbeUsesChatCompletions(t *testing.T) {
	var probePath, probeMethod, probeAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probePath = r.URL.Path
		probeMethod = r.Method
		probeAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	c := NewChecker()
	result := c.testKey("sk-test", &model.Provider{Type: "openai", BaseURL: server.URL})

	if probePath != "/v1/chat/completions" {
		t.Errorf("probe path = %q, want /v1/chat/completions (models listing can't prove key usability)", probePath)
	}
	if probeMethod != http.MethodPost {
		t.Errorf("probe method = %q, want POST (a GET can't be a chat completion)", probeMethod)
	}
	if probeAuth != "Bearer sk-test" {
		t.Errorf("probe auth = %q, want the key presented like real traffic", probeAuth)
	}
	if !result.Alive {
		t.Errorf("alive = false, want true for a 200 chat probe")
	}
}

// TestShouldProbeKeySkipsRateLimitedCooldown bounds probe spend: a
// rate-limited key whose cooldown is still running must not be probed — the
// probe cannot change its state (recovery is blocked during the cooldown)
// and every probe is now a billable chat completion. Once the cooldown
// expires, probing resumes.
func TestShouldProbeKeySkipsRateLimitedCooldown(t *testing.T) {
	c, k, _ := newTestEnv(t, okUpstream)
	until := time.Now().Add(10 * time.Minute)
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":             model.KeyStatusRateLimited,
		"rate_limited_until": until,
	})
	db.GetDB().First(k)

	if c.shouldProbeKey(k) {
		t.Error("shouldProbeKey = true, want false (cooldown still running)")
	}

	expired := time.Now().Add(-1 * time.Minute)
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"rate_limited_until": expired,
	})
	db.GetDB().First(k)

	if !c.shouldProbeKey(k) {
		t.Error("shouldProbeKey = false, want true (cooldown expired)")
	}
}

// TestClassifyOpenAIProbe403IsNotAuthFailure pins 403 as model/endpoint
// access (the KEY itself authenticated), matching the relay's own 403
// handling (a 30s cooldown, never a disable — see handler/chat.go). The
// chat probe uses a hardcoded model a key may not be entitled to, so a 403
// must not classify the key as auth_failed and get it disabled.
func TestClassifyOpenAIProbe403IsNotAuthFailure(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"The model gpt-4o-mini is not allowed for this key"}}`)),
	}
	result := classifyOpenAIProbe(resp)
	if !result.Alive {
		t.Errorf("403 probe = %+v, want alive (key authenticated, model access denied)", result)
	}
}

// TestCheckKeyRecoversAuthFailedKeyOn403 pins the recovery semantics for a
// 403 probe: a key disabled for auth_failed is recovered by 403 responses,
// because a 403 proves the key itself authenticates (the model is denied,
// not the key) — the same proof the relay accepts (403 -> 30s cooldown,
// never a disable). Recovery is gated on the state machine: 2 consecutive
// successful probes; 403 is deliberately not "failing".
func TestCheckKeyRecoversAuthFailedKeyOn403(t *testing.T) {
	c, k, engine := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":          model.KeyStatusDisabled,
		"disabled_reason": "auth_failed",
	})
	db.GetDB().First(k)

	var flips []string
	engine.SetOnStatusChanged(func(_ int64, status string) { flips = append(flips, status) })

	c.checkKey(k)
	c.checkKey(k)

	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusActive {
		t.Errorf("status = %q, want active (403 proves the key authenticates)", after.Status)
	}
	if !reflect.DeepEqual(flips, []string{model.KeyStatusActive}) {
		t.Errorf("status events = %v, want [active]", flips)
	}
}

// TestCheckKeySingleProbeFailureCoolsNotDisables: a single failed probe of
// an ACTIVE key must mark the key once and cool it for failover — but must
// NOT disable it. Disabling requires 2 consecutive failures with the SAME
// permanent reason (auth_failed / insufficient_quota); a transient probe
// failure (here an HTTP 500) only ever cools, no matter how often it
// repeats.
func TestCheckKeySingleProbeFailureCoolsNotDisables(t *testing.T) {
	c, k, engine := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	var flips []string
	engine.SetOnStatusChanged(func(_ int64, status string) { flips = append(flips, status) })

	c.checkKey(k)
	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusRateLimited {
		t.Fatalf("status = %q after one failed probe, want rate_limited (mark once, fail over, don't disable)", after.Status)
	}
	// The reason the UI shows for the cooled key is the HTTP status itself.
	if after.DisabledReason != "http_500" {
		t.Errorf("disabled_reason = %q, want http_500", after.DisabledReason)
	}
	if !reflect.DeepEqual(flips, []string{model.KeyStatusRateLimited}) {
		t.Errorf("status events = %v, want [rate_limited]", flips)
	}

	// Repeated identical TRANSIENT failures must never disable.
	c.checkKey(k)
	c.checkKey(k)
	db.GetDB().First(&after, k.ID)
	if after.Status == model.KeyStatusDisabled {
		t.Fatal("status = disabled after repeated 500 probes — transient failures never disable")
	}
}

// TestClassifyOpenAIProbe401ModelProblemIsAlive: many OpenAI-compatible
// gateways answer a request for an UNKNOWN or NOT-ENTITLED model with 401
// (not 400/404), even though the key itself is perfectly valid. The health
// probe uses a hardcoded model (gpt-4o-mini), so a 401 whose body names a
// model/access problem must classify the key as ALIVE — the key
// authenticated; only the probe's model choice is wrong. Disabling on this
// takes every usable key out of rotation (the regression users saw after
// the probe switched to chat completions).
func TestClassifyOpenAIProbe401ModelProblemIsAlive(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"message":"The model 'gpt-4o-mini' does not exist or you do not have access to it","code":"model_not_found"}}`)),
	}
	result := classifyOpenAIProbe(resp)
	if !result.Alive {
		t.Errorf("401 + model-not-found body = %+v, want alive (key authenticated, probe model unavailable)", result)
	}
}

// TestClassifyOpenAIProbe401NotEntitledModelIsAlive: same as above for a
// gateway that returns 401 with a permission-flavored message when the key
// is not entitled to the probe's model.
func TestClassifyOpenAIProbe401NotEntitledModelIsAlive(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"message":"Your API key does not have access to the model gpt-4o-mini","type":"access_denied_error"}}`)),
	}
	result := classifyOpenAIProbe(resp)
	if !result.Alive {
		t.Errorf("401 + not-entitled body = %+v, want alive (key authenticated, model denied)", result)
	}
}

// TestClassifyOpenAIProbe401InvalidKeyIsAuthFailed: a 401 whose body clearly
// says the KEY is invalid (invalid api key / authentication error) must
// still classify as auth_failed — this is the exact case the chat-probe
// switch (health #69) exists to catch.
func TestClassifyOpenAIProbe401InvalidKeyIsAuthFailed(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"message":"Incorrect API key provided: sk-***.","type":"invalid_request_error","code":"invalid_api_key"}}`)),
	}
	result := classifyOpenAIProbe(resp)
	if result.Alive || result.Reason != "auth_failed" {
		t.Errorf("401 + invalid-api-key body = %+v, want {alive:false reason:auth_failed}", result)
	}
}

// TestClassifyOpenAIProbeKeyValidModelInvalidIsAlive: a 401 (or 400) whose
// body says the KEY is valid but the MODEL is invalid ("Your API key is
// valid, but the model gpt-4o-mini is invalid") proves the key
// authenticated — only the probe's model / the requested model is the
// problem, so the key must be ALIVE. Before the valence fix the "key ...
// invalid" chain matched and classified these bodies as key-invalid,
// disabling every usable key whose account merely lacks the model.
func TestClassifyOpenAIProbeKeyValidModelInvalidIsAlive(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusUnauthorized, `{"error":{"message":"Your API key is valid, but the model gpt-4o-mini is invalid."}}`},
		{http.StatusBadRequest, `{"error":{"message":"Your API key is valid, but the model gpt-4o-mini is invalid."}}`},
		{http.StatusUnauthorized, `{"error":{"message":"The API key is fine, but the model is invalid."}}`},
	} {
		resp := &http.Response{
			StatusCode: tc.status,
			Body:       io.NopCloser(strings.NewReader(tc.body)),
		}
		if result := classifyOpenAIProbe(resp); !result.Alive {
			t.Errorf("%d + key-valid/model-invalid body = %+v, want alive (the KEY is valid, the model is the problem)", tc.status, result)
		}
	}
}

// TestClassifyOpenAIProbe401EmptyBodyIsAuthFailed: a bare 401 (no body, or
// an unparseable body) stays fail-closed: an ambiguous 401 is treated as an
// auth failure, since a gateway with a real model problem normally includes
// an error body naming the model.
func TestClassifyOpenAIProbe401EmptyBodyIsAuthFailed(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader(``)),
	}
	result := classifyOpenAIProbe(resp)
	if result.Alive || result.Reason != "auth_failed" {
		t.Errorf("bare 401 = %+v, want {alive:false reason:auth_failed}", result)
	}
}

// TestClassifyOpenAIProbe429QuotaExceededIsRateLimited: "quota_exceeded" is
// a RATE-LIMIT signal on many gateways (e.g. OpenRouter's "you've made too
// many requests to this model"), NOT a billing/quota exhaustion. Classifying
// it as insufficient_quota disables a healthy key on a transient throttle.
// It must classify as http_429 (cooldown, no disable).
func TestClassifyOpenAIProbe429QuotaExceededIsRateLimited(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"code":"quota_exceeded","message":"You've made too many requests to this model within a short time window."}}`)),
	}
	result := classifyOpenAIProbe(resp)
	if result.Alive || result.Reason != "http_429" {
		t.Errorf("429 + quota_exceeded = %+v, want {alive:false reason:http_429}", result)
	}
}

// TestQuotaExhaustedInBodyKeepsBillingCodes: QuotaExhaustedInBody must
// still report genuine billing-exhaustion codes as quota errors
// (insufficient_quota / billing_hard_limit_reached), while excluding the
// rate-limit code quota_exceeded.
func TestQuotaExhaustedInBodyKeepsBillingCodes(t *testing.T) {
	if !QuotaExhaustedInBody([]byte(`{"error":{"code":"insufficient_quota"}}`)) {
		t.Error("insufficient_quota must be a quota error")
	}
	if !QuotaExhaustedInBody([]byte(`{"error":{"type":"billing_error"}}`)) {
		t.Error("billing_error must be a quota error")
	}
	if QuotaExhaustedInBody([]byte(`{"error":{"code":"quota_exceeded"}}`)) {
		t.Error("quota_exceeded is a rate-limit code, must NOT be a quota error")
	}
}

// TestCheckKeyKeepsActiveKeyOnModelProblem401 is the end-to-end regression:
// an ACTIVE key whose probe comes back 401 + model-not-found (the probe's
// hardcoded model is unavailable on this gateway) must survive repeated
// probe passes WITHOUT being disabled. The classifier treats the 401 as a
// SUCCESS (the key authenticated), so no failure streak can ever build.
func TestCheckKeyKeepsActiveKeyOnModelProblem401(t *testing.T) {
	c, k, engine := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"The model 'gpt-4o-mini' does not exist","code":"model_not_found"}}`))
	})

	var flips []string
	engine.SetOnStatusChanged(func(_ int64, status string) { flips = append(flips, status) })

	c.checkKey(k)
	c.checkKey(k) // second consecutive probe must not disable either

	var after model.Key
	db.GetDB().First(&after, k.ID)
	if after.Status != model.KeyStatusActive {
		t.Errorf("status = %q, want active (model-problem 401 must not disable the key)", after.Status)
	}
	if len(flips) != 0 {
		t.Errorf("status events = %v, want none for an active key whose probe stays healthy", flips)
	}
}

// TestModelProblemInBodyKeySignalWins: when the body blames BOTH the key
// and the model (e.g. "api key not found for model gpt-4o-mini"), the key
// signal must win — fail-closed, the key is invalid. The "key not found"
// signal closes the hole where a body naming both would otherwise classify
// as a model problem and wrongly keep (or recover) a dead key.
func TestModelProblemInBodyKeySignalWins(t *testing.T) {
	body := []byte(`{"error":{"message":"api key not found for model gpt-4o-mini"}}`)
	if ModelProblemInBody(body) {
		t.Errorf("body %q = model problem, want false (key signal wins)", body)
	}
	resp := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	result := classifyOpenAIProbe(resp)
	if result.Alive || result.Reason != "auth_failed" {
		t.Errorf("401 + key-not-found-for-model body = %+v, want {alive:false reason:auth_failed}", result)
	}
}

// TestModelProblemInBodyKeyScopedDenialWins covers the other key-blaming
// phrasings that mention a model in the same body: "the key does not exist
// for model gpt-4o-mini", "your api key is disabled and the model
// gpt-4o-mini is unavailable", "you have access to the model gpt-4o-mini
// but the api key is invalid" — a genuinely dead key must NEVER be
// classified as a model problem just because the word "model" appears.
// Interposed words (an echoed key ID like "sk-123", qualifiers) must not
// defeat the key signal either: "The api key sk-123 is invalid. Model
// gpt-4o-mini is not available." still blames the KEY.
func TestModelProblemInBodyKeyScopedDenialWins(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"the key does not exist for model gpt-4o-mini"}}`,
		`{"error":{"message":"your api key is disabled and the model gpt-4o-mini is unavailable"}}`,
		`{"error":{"message":"you have access to the model gpt-4o-mini but the api key is invalid"}}`,
		`{"error":{"message":"The api key sk-123 is invalid. Model gpt-4o-mini is not available."}}`,
		`{"error":{"message":"The key you provided is invalid. Model gpt-4o-mini does not exist."}}`,
		`{"error":{"message":"key abc is not found. model gpt-4o-mini is not found."}}`,
		`{"error":{"message":"the api key sk-123 is disabled. the model gpt-4o-mini is not found."}}`,
		`{"error":{"message":"the key is unauthorized. model gpt-4o-mini not found"}}`,
		`{"error":{"code":"invalid_key","message":"model gpt-4o-mini not found"}}`,
		// OpenRouter's documented 401 message is "Invalid credentials
		// (OAuth session expired, disabled/invalid API key)": the credential
		// phrasing must count as a key signal even when a model is named.
		`{"error":{"message":"Invalid credentials. Model gpt-4o-mini not found."}}`,
		`{"error":{"message":"Invalid credentials (OAuth session expired, disabled/invalid API key) for model gpt-4o-mini"}}`,
	} {
		if ModelProblemInBody([]byte(body)) {
			t.Errorf("body %q = model problem, want false (key-blaming clause must win)", body)
		}
		resp := &http.Response{
			StatusCode: http.StatusUnauthorized,
			Body:       io.NopCloser(strings.NewReader(body)),
		}
		if result := classifyOpenAIProbe(resp); result.Alive || result.Reason != "auth_failed" {
			t.Errorf("401 + %q = %+v, want {alive:false reason:auth_failed}", body, result)
		}
	}
}

// TestModelProblemInBodyTerseCodeMatches: gateways that return a terse
// 401 with ONLY an error code ({"code":"model_not_found"}, no message)
// still name a model problem — the underscore code form must match, or a
// valid key on such a gateway gets disabled (fail-closed on the wrong
// side). The underscore form is shared by all denial phrasings
// (model_not_available, model_not_supported, ...), not just not_found.
func TestModelProblemInBodyTerseCodeMatches(t *testing.T) {
	for _, body := range []string{
		`{"error":{"code":"model_not_found"}}`,
		`{"error":{"code":"model_not_allowed"}}`,
		`{"error":{"code":"model_not_available"}}`,
		`{"error":{"code":"model_not_supported"}}`,
		`{"error":{"code":"model_not_entitled"}}`,
		`{"error":{"code":"deployment_not_found"}}`,
		`{"error":{"code":"deployment_not_available"}}`,
		// camelCase codes collapse the same way ("ModelNotFound" →
		// [model, not, found]); a code-only camelCase 401 must not disable.
		`{"error":{"code":"ModelNotFound"}}`,
		`{"error":{"code":"DeploymentNotFound"}}`,
	} {
		if !ModelProblemInBody([]byte(body)) {
			t.Errorf("body %q = not a model problem, want true (terse code names the model)", body)
		}
	}
	// The key-side underscore code must NOT be swallowed by the model gate:
	// a dead key stays dead.
	if ModelProblemInBody([]byte(`{"error":{"code":"key_not_found"}}`)) {
		t.Error(`{"code":"key_not_found"} = model problem, want false`)
	}
}

// TestModelProblemInBodyLongFormGap: long-winded model-denial sentences
// with several words between "model"/"deployment" and the denial must still
// match — a tight gap budget would classify them auth_failed and disable a
// valid key (the original regression, in its long-form variant).
func TestModelProblemInBodyLongFormGap(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"This deployment with id gpt-4o-mini-0125 is currently not found"}}`,
		`{"error":{"message":"The model that was requested by this account is not available"}}`,
	} {
		if !ModelProblemInBody([]byte(body)) {
			t.Errorf("body %q = not a model problem, want true (long-form model denial)", body)
		}
	}
}

// TestCheckKeyRecoversAuthFailedKeyOnModelProblem401: a disabled auth_failed
// key whose 401 body names a MODEL problem must be recovered (the key
// authenticates; only the probe's hardcoded model is unavailable to it) —
// after the state machine's 2 consecutive successful observations. This is
// the recovery side of the flap the relay fix prevents: with the same
// classification on both paths, a model-problem 401 recovers the key and
// the next real request ALSO cools (not disables) it — no ping-pong.
func TestCheckKeyRecoversAuthFailedKeyOnModelProblem401(t *testing.T) {
	c, k, engine := newTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"The model 'gpt-4o-mini' does not exist","code":"model_not_found"}}`))
	})
	db.GetDB().Model(&model.Key{}).Where("id = ?", k.ID).Updates(map[string]interface{}{
		"status":          model.KeyStatusDisabled,
		"disabled_reason": "auth_failed",
	})
	db.GetDB().First(k)

	var flips []string
	engine.SetOnStatusChanged(func(_ int64, status string) { flips = append(flips, status) })

	c.checkKey(k)
	if after := loadTestKey(t, k.ID); after.Status != model.KeyStatusDisabled {
		t.Fatalf("status after 1st probe = %q, want disabled (one success is not enough)", after.Status)
	}
	c.checkKey(k)

	after := loadTestKey(t, k.ID)
	if after.Status != model.KeyStatusActive {
		t.Errorf("status = %q, want active (model-problem 401 proves the key authenticates)", after.Status)
	}
	if !reflect.DeepEqual(flips, []string{model.KeyStatusActive}) {
		t.Errorf("status events = %v, want [active]", flips)
	}
}

func loadTestKey(t *testing.T, id int64) model.Key {
	t.Helper()
	var k model.Key
	if err := db.GetDB().First(&k, id).Error; err != nil {
		t.Fatal(err)
	}
	return k
}

// TestCheckKeySingleProbeFailureCoolsNotDisables above covers the old
// recordFailure semantics (mark once + failover, never disable on one
// failure); mixed-streak and streak-breaking rules are engine-level and
// pinned in selector/outcome_test.go (RecordResult).
