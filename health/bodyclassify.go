package health

import (
	"encoding/json"
	"strings"
	"unicode"
)

// Error body classification -------------------------------------------------
//
// Upstream error bodies vary wildly between gateways: JSON shapes differ
// ({"error":{"message":...}}, {"error":"plain string"}, {"message":...}
// top-level, {"errors":[...]} arrays), codes come as snake_case /
// camelCase / hyphenated, messages are English or Chinese, and a single
// body often blames the key AND the model at once. ClassifyErrorBody
// collapses that variance into three boolean signals used by the health
// probe and the relay:
//
//   - KeyInvalid: the body proves the KEY is invalid (fail-closed — this
//     signal always wins over a model mention in the same body).
//   - ModelProblem: the body names a model/deployment/resource denial; the
//     key itself authenticated.
//   - QuotaExhausted: the body carries an explicit billing-exhaustion
//     signal — never a bare "quota" word, and rate-limit signals
//     (quota_exceeded, too many requests, throttling, ...) always override
//     a quota reading.
type ErrorBodySignals struct {
	KeyInvalid     bool
	ModelProblem   bool
	QuotaExhausted bool
}

// ClassifyErrorBody extracts the semantic signals from an upstream error
// body. Every string VALUE in the JSON is collected (any shape: nested
// error object, error as a bare string, top-level message, error arrays);
// non-JSON bodies are treated as one plain-text value. Signals are matched
// WITHIN each value — never stitched across field boundaries ("the model"
// in the message + "denied" in the type must not chain), which also makes
// the result independent of JSON map iteration order. Token matching
// handles case/punctuation/camelCase variance; Chinese phrases are matched
// as contiguous substrings on the whitespace-stripped text, which preserves
// word order ("模型对应的密钥不存在" is a key problem, "密钥对应的模型不存在" is a
// model problem).
func ClassifyErrorBody(body []byte) ErrorBodySignals {
	var values []string
	var payload any
	if json.Unmarshal(body, &payload) == nil {
		collectStrings(payload, &values)
	} else {
		values = []string{string(body)} // plain-text / non-JSON body
	}

	// Signals are matched WITHIN one sentence of one value — never stitched
	// across field boundaries or across sentences ("The model ... is fine.
	// The endpoint was not found" must not chain). Accumulating every
	// signal before deciding also makes the result independent of JSON map
	// iteration order.
	signals := ErrorBodySignals{}
	keyAny := false
	for _, v := range values {
		for _, sentence := range splitSentences(v) {
			tokens := normalizeTokens(sentence)
			compact := compactText(sentence) // whitespace stripped, lowercased — for Chinese

			// Key-invalidity signals win (fail-closed): a dead key stays
			// dead even when the word "model" appears in the same body —
			// and a key signal in ANY sentence suppresses model readings.
			if matchAny(tokens, keyInvalidSignals) || matchAnyString(compact, keyInvalidChineseSignals) {
				keyAny = true
			}

			if matchAny(tokens, modelProblemSignals) || matchAnyString(compact, modelProblemChineseSignals) {
				signals.ModelProblem = true
			}

			// Quota exhaustion: explicit billing codes/phrases only, and
			// never when the same sentence also carries a rate-limit signal
			// (quota_exceeded, too many requests, throttling, per-minute
			// windows, 请求过于频繁...).
			if (matchAny(tokens, quotaExhaustedSignals) || matchAnyString(compact, quotaChineseSignals)) &&
				!matchAny(tokens, rateLimitSignals) && !matchAnyString(compact, rateLimitChineseSignals) {
				signals.QuotaExhausted = true
			}
		}
	}
	if keyAny {
		signals.KeyInvalid = true
		signals.ModelProblem = false
	}
	return signals
}

// ModelProblemInBody reports whether an error body blames the MODEL
// (unknown model, no access to the model) rather than the key. The health
// probe sends a hardcoded model that many gateways don't have, and such
// gateways often answer with 401 — reading the body is the only way to tell
// "the key is invalid" from "the key is fine, the probe's model is not".
// Explicit key-invalidity signals always win (fail-closed on ambiguity).
// Exported so the relay (handler/chat.go) applies the SAME classification to
// real traffic — otherwise a model-problem 401 disables the key on the
// relay path while the next probe pass recovers it (disable → active →
// disable flap).
func ModelProblemInBody(body []byte) bool {
	return ClassifyErrorBody(body).ModelProblem
}

// KeyInvalidInBody reports whether an error body blames the KEY (invalid /
// expired / disabled / revoked credentials). Used for statuses where
// key-invalidity is not the default reading (400 on Gemini-style gateways)
// and must be proven by the body.
func KeyInvalidInBody(body []byte) bool {
	return ClassifyErrorBody(body).KeyInvalid
}

// QuotaExhaustedInBody reports whether an error body carries a
// billing-exhaustion signal (OpenAI 429 + insufficient_quota /
// billing_hard_limit_reached, credit_balance_exhausted, ...). Rate-limit
// signals are deliberately excluded: "quota_exceeded" is a RATE-LIMIT code
// on many gateways (e.g. OpenRouter's "you've made too many requests to
// this model"), and the cost of a wrong disable (a healthy key taken out of
// rotation) outweighs the cost of a wrong cool-down (an over-quota key
// keeps failing over until an admin notices) — so ambiguous quota readings
// must cool the key down, not disable it.
func QuotaExhaustedInBody(body []byte) bool {
	return ClassifyErrorBody(body).QuotaExhausted
}

// collectStrings walks a decoded JSON value and appends every string VALUE
// (keys are never collected, so field names can't match a signal).
func collectStrings(v any, out *[]string) {
	switch t := v.(type) {
	case string:
		*out = append(*out, t)
	case map[string]any:
		for _, val := range t {
			collectStrings(val, out)
		}
	case []any:
		for _, val := range t {
			collectStrings(val, out)
		}
	}
}

// compactText strips all whitespace and lowercases, so Chinese phrases can
// be matched contiguously across mixed text ("无效的 API 密钥" -> "无效的api密钥").
func compactText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return strings.ToLower(b.String())
}

// splitSentences splits a value on sentence boundaries (English and Chinese
// punctuation, newlines). Signals never match across a boundary: "The model
// is fine. The endpoint was not found" must not read as a model problem
// even though the words would chain with a generous gap. A period BETWEEN
// DIGITS is a decimal point (model versions like "gemini-1.5-pro"), not a
// sentence boundary.
func splitSentences(s string) []string {
	var sentences []string
	var cur strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		switch r {
		case '.', '!', '?', ';', '。', '！', '？', '；', '\n', '\r':
			if r == '.' && i > 0 && i < len(runes)-1 && unicode.IsDigit(runes[i-1]) && unicode.IsDigit(runes[i+1]) {
				cur.WriteRune(r) // decimal point inside a version string
				continue
			}
			if cur.Len() > 0 {
				sentences = append(sentences, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		sentences = append(sentences, cur.String())
	}
	return sentences
}

// keyInvalidSignals are token sequences that prove the KEY is invalid,
// regardless of any model mention in the same body (fail-closed).
var keyInvalidSignals = [][]string{
	{"invalid", "key"},
	{"api", "key", "invalid"}, // API_KEY_INVALID (Gemini)
	{"incorrect", "key"},
	{"key", "invalid"},
	{"key", "not", "valid"},
	{"api", "key", "not", "valid"}, // "API key not valid. Please pass a valid API key."
	{"key", "not", "found"},
	{"key", "does", "not", "exist"},
	{"key", "not", "exist"},
	{"no", "such", "key"},
	{"key", "disabled"},
	{"key", "inactive"},
	{"key", "unauthorized"},
	{"key", "revoked"},
	{"key", "expired"},
	{"authentication"}, // gateways phrase key problems as "Invalid authentication"/"authentication_error"
	{"unauthorized", "key"},
	{"bad", "credentials"},
	{"invalid", "credential"},
	{"invalid", "credentials"},
	{"credential", "invalid"},
	{"credentials", "invalid"},
}

// modelProblemSignals are token sequences that prove the key authenticated
// and only the probe's MODEL is unavailable/denied. Kept fail-open ONLY for
// phrasings that name the model and its denial — the key signal list above
// is checked first and wins on any overlap.
var modelProblemSignals = [][]string{
	{"model", "not", "found"},
	{"model", "not", "exist"},
	{"model", "does", "not", "exist"},
	{"model", "not", "allowed"},
	{"model", "not", "available"},
	{"model", "unavailable"},
	{"model", "not", "supported"},
	{"model", "not", "entitled"},
	{"model", "not", "valid"},
	{"model", "not", "enabled"},
	{"model", "no", "access"},
	{"model", "access", "denied"},
	{"model", "denied"},
	{"model", "disabled"},
	{"model", "inactive"},
	{"models", "not", "found"},               // Gemini REST: "models/gemini-1.5-pro is not found"
	{"not", "valid", "model"},                // "gpt-4o is not a valid model"
	{"not", "have", "access", "to", "model"}, // "the key does not have access to the model"
	{"no", "access", "to", "model"},          // "no access to the model gpt-4o"
	{"deployment", "not", "found"},
	{"deployment", "not", "exist"},
	{"deployment", "does", "not", "exist"},
	{"deployment", "not", "available"},
	{"deployment", "not", "allowed"},
	{"deployment", "not", "valid"},
	{"deployment", "disabled"},
	{"deployment", "inactive"},
	{"resource", "not", "found"}, // Azure ResourceNotFound / DeploymentNotFound
	{"resource", "not", "exist"},
	{"resource", "does", "not", "exist"},
}

// keyInvalidChineseSignals are contiguous Chinese phrases (matched on the
// whitespace-stripped text) that blame the KEY.
var keyInvalidChineseSignals = []string{
	"密钥无效",
	"无效的密钥",
	"无效的api密钥",
	"api密钥无效",
	"密钥不存在",
	"密钥错误",
	"错误的密钥",
	"密钥失效",
	"密钥已失效",
	"密钥已禁用",
	"密钥被禁用",
	"密钥不正确",
	"密钥不合法",
	"认证失败",
	"鉴权失败",
}

// modelProblemChineseSignals are contiguous Chinese phrases that name a
// MODEL denial (the key itself authenticated). Order is preserved by
// contiguous matching, so "密钥对应的模型不存在" (model problem) and
// "模型对应的密钥不存在" (key problem) classify differently.
var modelProblemChineseSignals = []string{
	"模型不存在",
	"该模型不存在",
	"模型不可用",
	"模型不支持",
	"模型未开通",
	"模型无权限",
	"无权访问该模型",
	"该模型已禁用",
	"模型已被禁用",
	"模型已禁用",
}

// quotaExhaustedSignals: billing-exhaustion codes/types and message
// phrases. A bare "quota" word NEVER matches; only explicit billing
// signals do. ("quota_exceeded" is a rate-limit code — see
// rateLimitSignals.)
var quotaExhaustedSignals = [][]string{
	{"insufficient", "quota"},
	{"billing", "hard", "limit", "reached"},
	{"billing", "not", "active"},
	{"billing", "error"},
	{"card", "declined"},
	{"credit", "balance", "exhausted"},
	{"organization", "spend", "limit", "exceeded"},
	{"project", "spend", "limit", "exceeded"},
	{"organization", "usage", "limit", "exceeded"},
	{"insufficient", "credits"},
	{"credit", "limit", "reached"},
	{"exceeded", "your", "current", "quota"}, // OpenAI's billing 429 message
	{"no", "balance"},
	{"insufficient", "balance"},
	{"balance", "insufficient"},
	{"out", "of", "credit"},
	{"out", "of", "credits"},
}

// rateLimitSignals OVERRIDE a quota reading: any of these makes the body a
// transient throttle, never billing exhaustion.
var rateLimitSignals = [][]string{
	{"quota", "exceeded"}, // OpenRouter rate limit / Gemini RESOURCE_EXHAUSTED
	{"rate", "limit"},
	{"rate", "limited"},
	{"too", "many", "requests"},
	{"request", "too", "frequent"},
	{"throttling"},
	{"throttled"},
	{"per", "minute"}, // per-window throttle: "exceeded your current quota of 100 requests per minute"
	{"per", "second"},
	{"try", "again", "later"},
}

// quotaChineseSignals: explicit Chinese billing-exhaustion phrases.
var quotaChineseSignals = []string{
	"余额不足",
	"余额已用完",
	"余额已耗尽",
	"账户余额不足",
	"欠费",
	"额度不足",
}

// rateLimitChineseSignals override a quota reading (transient throttle).
var rateLimitChineseSignals = []string{
	"请求过于频繁",
	"请求频率过高",
	"限流",
	"请稍后重试",
}

// normalizeTokens lowercases a string and splits it into word tokens,
// treating any non-alphanumeric character as a separator and splitting
// camelCase boundaries. This collapses the punctuation variance gateways
// use — "model_not_found", "ModelNotFound", "model not found",
// "model gpt-4o-mini is not found" and "model: not found" all become
// comparable token sequences. (Chinese text has no ASCII tokens — it is
// handled by the contiguous-phrase channel instead.)
func normalizeTokens(s string) []string {
	var words []string
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = cur[:0]
		}
	}
	prevLower := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			cur = append(cur, c)
			prevLower = true
		case c >= 'A' && c <= 'Z':
			// camelCase boundary: "ModelNotFound" → [model, not, found]
			if prevLower && len(cur) > 0 {
				flush()
			}
			cur = append(cur, c+('a'-'A'))
			prevLower = false
		case c >= '0' && c <= '9':
			cur = append(cur, c)
			prevLower = false
		default:
			flush()
			prevLower = false
		}
	}
	flush()
	return words
}

// maxGapUnmatched bounds how many foreign tokens may sit between two signal
// words. It must be generous enough for long echoed model/deployment ids
// ("This deployment with id gpt-4o-mini-0125 is currently not found" has 8
// tokens between "deployment" and "not") while staying far below sentence
// length so denial words in unrelated clauses don't chain together.
const maxGapUnmatched = 16

func matchAny(tokens []string, signals [][]string) bool {
	for _, sig := range signals {
		if seqMatch(tokens, sig) {
			return true
		}
	}
	return false
}

func matchAnyString(text string, phrases []string) bool {
	for _, p := range phrases {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

// seqMatch reports whether the signal's words appear in tokens IN ORDER,
// ignoring up to maxGapUnmatched other tokens between consecutive signal
// words. A signal word must still appear as a whole token, so "invalid key"
// never matches inside "invalidate keys" — but "api key sk-123 is invalid"
// DOES match [key invalid] (the echoed key ID sits between the words).
func seqMatch(tokens []string, sig []string) bool {
	if len(sig) == 0 {
		return false
	}
	// For each occurrence of the first word, try to walk the rest of the
	// signal allowing up to maxGapUnmatched foreign tokens between words.
	for i := 0; i < len(tokens); i++ {
		if tokens[i] != sig[0] {
			continue
		}
		pos := i + 1
		matched := true
		for _, w := range sig[1:] {
			found := -1
			for j := pos; j < len(tokens) && j-pos <= maxGapUnmatched; j++ {
				if tokens[j] == w {
					found = j
					break
				}
			}
			if found < 0 {
				matched = false
				break
			}
			pos = found + 1
		}
		if matched {
			return true
		}
	}
	return false
}
