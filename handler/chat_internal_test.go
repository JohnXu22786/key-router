package handler

import "testing"

// TestIsQuotaExhaustedErrorExcludesRateLimitCodes: "quota_exceeded" is a
// RATE-LIMIT signal on many gateways (e.g. OpenRouter's "you've made too
// many requests to this model"), not billing exhaustion. The relay must not
// disable a key on it — real traffic hitting a transient throttle would
// otherwise permanently take a healthy key out of rotation. Genuine
// billing-exhaustion codes must still be detected.
func TestIsQuotaExhaustedErrorExcludesRateLimitCodes(t *testing.T) {
	if isQuotaExhaustedError([]byte(`{"error":{"code":"quota_exceeded"}}`)) {
		t.Error("quota_exceeded is a rate-limit code, must NOT be treated as quota exhaustion")
	}
	if !isQuotaExhaustedError([]byte(`{"error":{"code":"insufficient_quota"}}`)) {
		t.Error("insufficient_quota must be treated as quota exhaustion")
	}
	if !isQuotaExhaustedError([]byte(`{"error":{"code":"billing_hard_limit_reached"}}`)) {
		t.Error("billing_hard_limit_reached must be treated as quota exhaustion")
	}
	if !isQuotaExhaustedError([]byte(`{"error":{"type":"billing_error"}}`)) {
		t.Error("billing_error must be treated as quota exhaustion")
	}
}
