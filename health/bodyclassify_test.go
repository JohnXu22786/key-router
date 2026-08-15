package health

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// Body-classification contract --------------------------------------------
//
// ClassifyErrorBody must be robust against the ERROR-BODY VARIANCE across
// gateways: different JSON shapes, token spellings (underscore / camelCase /
// hyphen / spaces / case), plain-text bodies, English and Chinese messages,
// and bodies that blame the key AND the model at once. The rules:
//
//   - Key-invalidity signals always win (fail-closed): a body that blames
//     the key is never also a model problem.
//   - Model problems are only recognized when a model/deployment/resource
//     denial is named — the key itself authenticated.
//   - Quota exhaustion is only recognized from explicit billing codes or
//     billing phrases; rate-limit signals (quota_exceeded, too many
//     requests, throttling, ...) always override a quota reading.

// TestClassifyJSONShapeVariance: the same key-invalidity signal through
// every JSON shape gateways actually use.
func TestClassifyJSONShapeVariance(t *testing.T) {
	bodies := []string{
		`{"error":{"message":"Invalid API key provided","code":"invalid_api_key"}}`,
		`{"error":{"type":"authentication_error","message":"Invalid API key provided"}}`,
		`{"error":"Invalid API key provided"}`,
		`{"message":"Invalid API key provided"}`,
		`{"errors":[{"message":"Invalid API key provided"}]}`,
		`{"error":{"message":"Invalid API key provided","code":401}}`, // numeric code
		`Invalid API key provided`,                                    // plain text body
		`{"error":{"code":"API_KEY_INVALID","message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`, // Gemini
	}
	for _, b := range bodies {
		sig := ClassifyErrorBody([]byte(b))
		if !sig.KeyInvalid {
			t.Errorf("body %q: KeyInvalid=false, want true", b)
		}
		if sig.ModelProblem {
			t.Errorf("body %q: ModelProblem=true, want false (key signal wins)", b)
		}
	}
}

// TestClassifyTokenVariance: the same model-problem signal in every token
// spelling — underscore codes, camelCase codes, hyphenated, spaced, long
// deployment ids interposed, and Azure-style resource phrasings.
func TestClassifyTokenVariance(t *testing.T) {
	bodies := []string{
		`{"error":{"code":"model_not_found"}}`,
		`{"error":{"code":"ModelNotFound"}}`,
		`{"error":{"code":"model-not-found"}}`,
		`{"error":{"message":"model not found"}}`,
		`{"error":{"message":"The model 'gpt-4o-mini' does not exist or you do not have access to it","code":"model_not_found"}}`,
		`{"error":{"message":"This deployment with id gpt-4o-mini-0125 is currently not found"}}`,
		`{"error":{"code":"DeploymentNotFound"}}`,
		`{"error":{"message":"The API deployment for this resource does not exist"}}`,
		`{"error":{"code":"ResourceNotFound"}}`,
		`{"error":{"message":"gpt-4o is not a valid model for this account"}}`,
		`{"error":{"message":"models/gemini-1.5-pro is not found"}}`,
	}
	for _, b := range bodies {
		sig := ClassifyErrorBody([]byte(b))
		if !sig.ModelProblem {
			t.Errorf("body %q: ModelProblem=false, want true", b)
		}
		if sig.KeyInvalid {
			t.Errorf("body %q: KeyInvalid=true, want false", b)
		}
	}
}

// TestClassifyKeySignalWins: bodies that blame the key AND mention a model
// must classify as key-invalid (fail-closed) — a dead key stays dead even
// when the word "model" appears in the same message.
func TestClassifyKeySignalWins(t *testing.T) {
	bodies := []string{
		`{"error":{"message":"api key not found for model gpt-4o-mini"}}`,
		`{"error":{"message":"The api key sk-123 is invalid. Model gpt-4o-mini is not available."}}`,
		`{"error":{"message":"Invalid credentials (OAuth session expired, disabled/invalid API key) for model gpt-4o-mini"}}`,
		`{"error":{"message":"your api key is not valid for this model"}}`,
		`{"error":{"code":"invalid_key","message":"model gpt-4o-mini not found"}}`,
		`{"error":{"message":"Authentication failed for model gpt-4o-mini"}}`,
		`{"error":{"code":"key_not_found"}}`,
	}
	for _, b := range bodies {
		sig := ClassifyErrorBody([]byte(b))
		if !sig.KeyInvalid {
			t.Errorf("body %q: KeyInvalid=false, want true (key signal wins)", b)
		}
		if sig.ModelProblem {
			t.Errorf("body %q: ModelProblem=true, want false", b)
		}
	}
}

// TestClassifyChinese: Chinese error messages from domestic gateways.
// Contiguous-phrase matching preserves word order: "密钥对应的模型不存在"
// (the model for the key is missing) is a MODEL problem, while "模型对应的
// 密钥不存在" (the key for the model is missing) is a KEY problem.
func TestClassifyChinese(t *testing.T) {
	keyBodies := []string{
		`{"error":{"message":"无效的 API 密钥"}}`,
		`{"error":{"message":"API密钥无效"}}`,
		`{"error":{"message":"密钥不存在"}}`,
		`{"error":{"message":"密钥已禁用"}}`,
		`{"error":{"message":"模型对应的密钥不存在"}}`,
		`{"error":{"message":"认证失败，请检查密钥"}}`,
	}
	for _, b := range keyBodies {
		sig := ClassifyErrorBody([]byte(b))
		if !sig.KeyInvalid {
			t.Errorf("body %q: KeyInvalid=false, want true", b)
		}
	}

	modelBodies := []string{
		`{"error":{"message":"模型不存在"}}`,
		`{"error":{"message":"该模型不存在，请检查模型名称"}}`,
		`{"error":{"message":"模型不可用"}}`,
		`{"error":{"message":"密钥对应的模型不存在"}}`, // word order matters
	}
	for _, b := range modelBodies {
		sig := ClassifyErrorBody([]byte(b))
		if !sig.ModelProblem {
			t.Errorf("body %q: ModelProblem=false, want true", b)
		}
		if sig.KeyInvalid {
			t.Errorf("body %q: KeyInvalid=true, want false", b)
		}
	}
}

// TestClassifyQuotaCodes: billing-exhaustion codes/types, case-insensitive
// and token-normalized — while "quota_exceeded" and rate-limit codes must
// never read as quota.
func TestClassifyQuotaCodes(t *testing.T) {
	quotaBodies := []string{
		`{"error":{"code":"insufficient_quota"}}`,
		`{"error":{"code":"Insufficient_Quota"}}`, // case variance
		`{"error":{"code":"billing_hard_limit_reached"}}`,
		`{"error":{"code":"billing_not_active"}}`,
		`{"error":{"code":"card_declined"}}`,
		`{"error":{"type":"billing_error"}}`,
		`{"error":{"code":"credit_balance_exhausted"}}`,
		`{"error":{"code":"organization_spend_limit_exceeded"}}`,
		`{"error":{"code":"project_spend_limit_exceeded"}}`,
		`{"error":{"code":"insufficient_credits"}}`,
		`{"error":{"message":"You exceeded your current quota, please check your plan and billing details."}}`,
		`{"error":{"message":"Insufficient Balance"}}`,
		`{"error":{"message":"您的账户余额不足，请充值"}}`,
	}
	for _, b := range quotaBodies {
		sig := ClassifyErrorBody([]byte(b))
		if !sig.QuotaExhausted {
			t.Errorf("body %q: QuotaExhausted=false, want true", b)
		}
	}

	notQuotaBodies := []string{
		`{"error":{"code":"quota_exceeded"}}`, // OpenRouter rate-limit code
		`{"error":{"message":"Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier"}}`, // Gemini rate limit
		`{"error":{"message":"You have exceeded your quota of 100 requests per minute"}}`,                                 // per-window throttle
		`{"error":{"message":"You exceeded your current quota of 100 requests per minute"}}`,                              // per-window throttle, "current" phrasing
		`{"error":{"code":"rate_limit_exceeded"}}`,
		`{"error":{"message":"too many requests, please slow down"}}`,
		`{"error":{"message":"throttled by upstream"}}`,
		`{"error":{"message":"请求过于频繁，请稍后重试"}}`,
	}
	for _, b := range notQuotaBodies {
		sig := ClassifyErrorBody([]byte(b))
		if sig.QuotaExhausted {
			t.Errorf("body %q: QuotaExhausted=true, want false (rate-limit signal)", b)
		}
	}
}

// TestClassifyEmptyAndNoise: bodies with no signal must classify as nothing
// — an empty body, an unparseable gateway page, an HTML error page.
func TestClassifyEmptyAndNoise(t *testing.T) {
	bodies := []string{
		``,
		`{}`,
		`{"error":{}}`,
		`502 Bad Gateway`,
		`<html><body><h1>503 Service Unavailable</h1></body></html>`,
		`{"error":{"message":"Internal Server Error"}}`,
	}
	for _, b := range bodies {
		sig := ClassifyErrorBody([]byte(b))
		if sig.KeyInvalid || sig.ModelProblem || sig.QuotaExhausted {
			t.Errorf("body %q: unexpected signals %+v, want all false", b, sig)
		}
	}
}

// TestClassifyOpenAIProbe400: the Gemini gap — a 400 whose body blames the
// KEY (API_KEY_INVALID) classifies the key as dead; any other 400 (model /
// request problem) proves the key authenticated and stays alive.
func TestClassifyOpenAIProbe400(t *testing.T) {
	badKey := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"code":"API_KEY_INVALID","message":"API key not valid. Please pass a valid API key.","status":"INVALID_ARGUMENT"}}`)),
	}
	if result := classifyOpenAIProbe(badKey); result.Alive || result.Reason != "auth_failed" {
		t.Errorf("400 + API_KEY_INVALID = %+v, want {alive:false reason:auth_failed}", result)
	}

	modelBody := `{"error":{"message":"The model 'gpt-4o-mini' does not exist or you do not have access to it"}}`
	if !ModelProblemInBody([]byte(modelBody)) {
		t.Error("model-problem body must classify as ModelProblem")
	}
	modelProblem := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(modelBody)),
	}
	if result := classifyOpenAIProbe(modelProblem); !result.Alive {
		t.Errorf("400 + model problem = %+v, want alive (key authenticated)", result)
	}

	azureBody := `{"error":{"message":"The API deployment for this resource does not exist","code":"DeploymentNotFound"}}`
	if !ModelProblemInBody([]byte(azureBody)) {
		t.Error("deployment-not-found body must classify as ModelProblem")
	}
	azure := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(azureBody)),
	}
	if result := classifyOpenAIProbe(azure); !result.Alive {
		t.Errorf("400 + deployment-not-found = %+v, want alive (config problem, not key)", result)
	}
}

// TestClassifyLongDeploymentIdGap: a long model/deployment id between the
// denial words must not defeat the match (the old 8-token gap failed for
// ids like ft:gpt-4o-2024-08-06:personal:...).
func TestClassifyLongDeploymentIdGap(t *testing.T) {
	bodies := []string{
		`{"error":{"message":"The deployment ft:gpt-4o-2024-08-06:personal:model:xyz-123 is currently not found"}}`,
		`{"error":{"message":"model gpt-4o-mini-2024-07-18-custom-0123456789abcdef is not available"}}`,
	}
	for _, b := range bodies {
		sig := ClassifyErrorBody([]byte(b))
		if !sig.ModelProblem {
			t.Errorf("body %q: ModelProblem=false, want true (long id between denial words)", b)
		}
	}
}

// TestClassifyPerValueNoCrossFieldChaining: signals must match WITHIN a
// single field — "the model" in the message must not chain with "denied"
// from the type to fake a model problem, and the result must not depend on
// JSON map iteration order (Go maps randomize iteration, so joined-text
// matching was flaky).
func TestClassifyPerValueNoCrossFieldChaining(t *testing.T) {
	// The key does not have access to the MODEL: the message alone names
	// the model denial ("does not have access to the model") — model
	// problem, alive. The type value carries no model word.
	sig := ClassifyErrorBody([]byte(`{"error":{"message":"Your API key does not have access to the model gpt-4o-mini","type":"access_denied_error"}}`))
	if !sig.ModelProblem || sig.KeyInvalid {
		t.Errorf("not-entitled body = %+v, want {ModelProblem:true} (denial lives in the message)", sig)
	}

	// The denial and the model word in DIFFERENT fields must NOT chain:
	// "the model" (message) + "denied" (type) is not a model problem.
	sig = ClassifyErrorBody([]byte(`{"error":{"message":"The model gpt-4o-mini is fine","type":"access_denied_error"}}`))
	if sig.ModelProblem {
		t.Errorf("cross-field chaining = %+v, want ModelProblem=false (denial is not about the model)", sig)
	}

	// A key signal in ANY field suppresses model readings from other fields.
	sig = ClassifyErrorBody([]byte(`{"error":{"message":"model gpt-4o-mini is not available","code":"invalid_api_key"}}`))
	if !sig.KeyInvalid || sig.ModelProblem {
		t.Errorf("key signal in code + model in message = %+v, want {KeyInvalid:true}", sig)
	}
}

// TestClassifySentenceBoundary: denial words in a LATER sentence must not
// chain with a model mention in an EARLIER one ("The model is fine. The
// endpoint was not found" is not a model problem) — and a genuine denial in
// one sentence still matches.
func TestClassifySentenceBoundary(t *testing.T) {
	sig := ClassifyErrorBody([]byte(`{"error":{"message":"The model gpt-4o-mini is running fine. However, the endpoint was not found in our registry."}}`))
	if sig.ModelProblem {
		t.Errorf("cross-sentence chaining = %+v, want ModelProblem=false (denial is not about the model)", sig)
	}

	sig = ClassifyErrorBody([]byte(`{"error":{"message":"Your API key does not have access to the model gpt-4o-mini. Please contact support."}}`))
	if !sig.ModelProblem {
		t.Errorf("single-sentence denial = %+v, want ModelProblem=true", sig)
	}
}

// TestClassifyOrderIndependent: signals in DIFFERENT fields must combine
// deterministically — Go maps randomize iteration order, so the result must
// not depend on which value is visited first. A key signal in one field and
// a quota signal in another must report BOTH, every time.
func TestClassifyOrderIndependent(t *testing.T) {
	body := []byte(`{"error":{"code":"invalid_api_key","message":"You exceeded your current quota, please check your plan and billing details."}}`)
	for i := 0; i < 50; i++ {
		sig := ClassifyErrorBody(body)
		if !sig.KeyInvalid || !sig.QuotaExhausted {
			t.Fatalf("iteration %d: %+v, want {KeyInvalid:true QuotaExhausted:true} (order-independent)", i, sig)
		}
		if sig.ModelProblem {
			t.Fatalf("iteration %d: ModelProblem=true, want false (key signal wins)", i)
		}
	}
}

// TestKeyInvalidInBodyAndModelProblemInBodyWrappers: the named wrappers used
// by the relay and the probe agree with the combined classifier.
func TestKeyInvalidInBodyAndModelProblemInBodyWrappers(t *testing.T) {
	keyBody := []byte(`{"error":{"message":"Invalid API key provided"}}`)
	modelBody := []byte(`{"error":{"message":"model not found"}}`)
	ambigBody := []byte(`{"error":{"message":"api key not found for model gpt-4o-mini"}}`)

	if !KeyInvalidInBody(keyBody) || ModelProblemInBody(keyBody) {
		t.Error("key body: wrappers disagree with classifier")
	}
	if KeyInvalidInBody(modelBody) || !ModelProblemInBody(modelBody) {
		t.Error("model body: wrappers disagree with classifier")
	}
	if !KeyInvalidInBody(ambigBody) || ModelProblemInBody(ambigBody) {
		t.Error("ambiguous body: key signal must win in both wrappers")
	}
}
