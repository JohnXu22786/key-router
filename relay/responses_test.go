package relay

import (
	"encoding/json"
	"strings"
	"testing"

	"key-router/model"
)

// Response-object converters (non-stream /v1/responses bodies) — these live
// in the relay package because the existing cross-format response converters
// do.

func TestChatCompletionResponseToResponses(t *testing.T) {
	body := `{
		"id": "chatcmpl-42", "object": "chat.completion", "model": "deepseek",
		"created": 1234,
		"choices": [{
			"finish_reason": "stop",
			"message": {
				"role": "assistant",
				"content": "final answer",
				"reasoning_content": "thinking...",
				"tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "f", "arguments": "{\"a\":1}"}}]
			}
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
			"prompt_tokens_details": {"cached_tokens": 3}}
	}`
	out, err := ChatCompletionResponseToResponses([]byte(body), "deepseek")
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if resp["object"] != "response" || resp["status"] != "completed" {
		t.Errorf("object/status = %v/%v", resp["object"], resp["status"])
	}
	if resp["model"] != "deepseek" {
		t.Errorf("model = %v", resp["model"])
	}
	if !strings.HasPrefix(resp["id"].(string), "resp_") {
		t.Errorf("id = %v", resp["id"])
	}

	output := resp["output"].([]interface{})
	if len(output) != 3 {
		t.Fatalf("output = %d items, want 3 (message + function_call + reasoning): %v", len(output), output)
	}
	msg := output[0].(map[string]interface{})
	if msg["type"] != "message" || msg["role"] != "assistant" {
		t.Errorf("message item = %v", msg)
	}
	parts := msg["content"].([]interface{})
	part := parts[0].(map[string]interface{})
	if part["type"] != "output_text" || part["text"] != "final answer" {
		t.Errorf("content part = %v", part)
	}
	fc := output[1].(map[string]interface{})
	if fc["type"] != "function_call" || fc["call_id"] != "call_1" || fc["arguments"] != `{"a":1}` {
		t.Errorf("function_call item = %v", fc)
	}
	rs := output[2].(map[string]interface{})
	if rs["type"] != "reasoning" {
		t.Errorf("reasoning item = %v", rs)
	}
	summary := rs["summary"].([]interface{})[0].(map[string]interface{})
	if summary["text"] != "thinking..." {
		t.Errorf("reasoning summary = %v", summary)
	}

	u := resp["usage"].(map[string]interface{})
	if u["input_tokens"] != float64(10) || u["output_tokens"] != float64(5) || u["total_tokens"] != float64(15) {
		t.Errorf("usage = %v", u)
	}
	details := u["input_tokens_details"].(map[string]interface{})
	if details["cached_tokens"] != float64(3) {
		t.Errorf("input_tokens_details = %v", details)
	}
}

func TestChatCompletionResponseToResponsesIncomplete(t *testing.T) {
	body := `{"id":"c","choices":[{"finish_reason":"length","message":{"content":"cut off"}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`
	out, err := ChatCompletionResponseToResponses([]byte(body), "m")
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]interface{}
	json.Unmarshal(out, &resp)
	if resp["status"] != "incomplete" {
		t.Errorf("status = %v, want incomplete (finish_reason length)", resp["status"])
	}
	details := resp["incomplete_details"].(map[string]interface{})
	if details["reason"] != "max_output_tokens" {
		t.Errorf("incomplete_details = %v", details)
	}
}

func TestAnthropicResponseToResponses(t *testing.T) {
	body := `{
		"id": "msg_1", "type": "message", "model": "claude-3-7-sonnet",
		"role": "assistant", "stop_reason": "tool_use",
		"content": [
			{"type": "thinking", "thinking": "hmm..."},
			{"type": "text", "text": "Let me check."},
			{"type": "tool_use", "id": "toolu_1", "name": "search", "input": {"q": "x"}}
		],
		"usage": {"input_tokens": 20, "output_tokens": 8,
			"cache_creation_input_tokens": 2, "cache_read_input_tokens": 3}
	}`
	out, err := AnthropicResponseToResponses([]byte(body), "claude-3-7-sonnet")
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if resp["status"] != "completed" {
		t.Errorf("status = %v", resp["status"])
	}
	output := resp["output"].([]interface{})
	if len(output) != 3 {
		t.Fatalf("output = %d, want 3: %v", len(output), output)
	}
	// block order preserved: reasoning first, then message, then function_call;
	// ids carry the per-type sequence (msg_<seed>_1, rs_<seed>_1)
	rs := output[0].(map[string]interface{})
	if rs["type"] != "reasoning" || rs["id"] != "rs_msg_1_1" {
		t.Errorf("output[0] = %v, want reasoning rs_msg_1_1", rs)
	}
	msg := output[1].(map[string]interface{})
	if msg["type"] != "message" || msg["id"] != "msg_msg_1_1" {
		t.Errorf("output[1] = %v, want message msg_msg_1_1", msg)
	}
	fc := output[2].(map[string]interface{})
	if fc["type"] != "function_call" || fc["call_id"] != "toolu_1" || fc["arguments"] != `{"q":"x"}` {
		t.Errorf("function_call = %v", fc)
	}

	// input_tokens includes cache tokens; cached broken out in details
	u := resp["usage"].(map[string]interface{})
	if u["input_tokens"] != float64(25) {
		t.Errorf("input_tokens = %v, want 25 (20 + 2 + 3)", u["input_tokens"])
	}
	details := u["input_tokens_details"].(map[string]interface{})
	if details["cached_tokens"] != float64(5) {
		t.Errorf("cached_tokens = %v, want 5", details["cached_tokens"])
	}
	if u["total_tokens"] != float64(33) {
		t.Errorf("total_tokens = %v, want 33", u["total_tokens"])
	}
}

func TestAnthropicResponseToResponsesMaxTokens(t *testing.T) {
	body := `{"id":"m1","type":"message","model":"m","stop_reason":"max_tokens","content":[{"type":"text","text":"x"}]}`
	out, err := AnthropicResponseToResponses([]byte(body), "m")
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]interface{}
	json.Unmarshal(out, &resp)
	if resp["status"] != "incomplete" {
		t.Errorf("status = %v, want incomplete", resp["status"])
	}
}

// TestAnthropicResponseToResponsesUniqueItemIDs guards the Responses API
// item-id uniqueness contract: a response carrying several text blocks (split
// by tool_use/thinking) or several thinking blocks must not stamp the bare
// response seed on every item of a type — clients key output items by id, so
// two message items sharing msg_<seed> (or two reasoning items sharing
// rs_<seed>) breaks correlation. Each item of a type gets the seed plus that
// type's running sequence; tool ids keep coming from the upstream block id.
func TestAnthropicResponseToResponsesUniqueItemIDs(t *testing.T) {
	body := `{
		"id": "msg_abc", "type": "message", "model": "claude",
		"stop_reason": "tool_use",
		"content": [
			{"type": "thinking", "thinking": "first..."},
			{"type": "text", "text": "Let me check."},
			{"type": "tool_use", "id": "toolu_1", "name": "search", "input": {"q": "x"}},
			{"type": "thinking", "thinking": "second..."},
			{"type": "text", "text": "Done."}
		]
	}`
	out, err := AnthropicResponseToResponses([]byte(body), "claude")
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	output := resp["output"].([]interface{})
	if len(output) != 5 {
		t.Fatalf("output = %d items, want 5: %v", len(output), output)
	}

	// no two output items may share an id
	seen := map[string]int{}
	for i, item := range output {
		m := item.(map[string]interface{})
		id := m["id"].(string)
		if prev, dup := seen[id]; dup {
			t.Errorf("output[%d] id %q duplicates output[%d] (type %v)", i, id, prev, m["type"])
		}
		seen[id] = i
	}

	// per-type sequence disambiguates same-type items; tool id stays stable
	wantTypes := []string{"reasoning", "message", "function_call", "reasoning", "message"}
	for i, item := range output {
		if item.(map[string]interface{})["type"] != wantTypes[i] {
			t.Errorf("output[%d] type = %v, want %s", i, item.(map[string]interface{})["type"], wantTypes[i])
		}
	}
	if got := output[0].(map[string]interface{})["id"]; got != "rs_msg_abc_1" {
		t.Errorf("first reasoning id = %v, want rs_msg_abc_1", got)
	}
	if got := output[1].(map[string]interface{})["id"]; got != "msg_msg_abc_1" {
		t.Errorf("first message id = %v, want msg_msg_abc_1", got)
	}
	if got := output[2].(map[string]interface{})["id"]; got != "toolu_1" {
		t.Errorf("function_call id = %v, want toolu_1 (stable from block id)", got)
	}
	if got := output[3].(map[string]interface{})["id"]; got != "rs_msg_abc_2" {
		t.Errorf("second reasoning id = %v, want rs_msg_abc_2", got)
	}
	if got := output[4].(map[string]interface{})["id"]; got != "msg_msg_abc_2" {
		t.Errorf("second message id = %v, want msg_msg_abc_2", got)
	}
}

// ---- usage parsing for native /v1/responses upstreams ----

func TestParseTokenUsageResponses(t *testing.T) {
	body := `{"id":"resp_1","object":"response","status":"completed","output":[],
		"usage":{"input_tokens":10,"input_tokens_details":{"cached_tokens":4},
		"output_tokens":6,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":16}}`
	u := ParseTokenUsage([]byte(body), "responses")
	if u.PromptTokens != 10 || u.CompletionTokens != 6 || u.TotalTokens != 16 {
		t.Errorf("usage = %+v", u)
	}
	if u.CacheHitTokens != 4 {
		t.Errorf("CacheHitTokens = %d, want 4", u.CacheHitTokens)
	}
	// responses input_tokens include cached tokens — billing must subtract
	// them, so the record reports OpenAI semantics
	if u.Format != "openai" {
		t.Errorf("Format = %q, want openai semantics", u.Format)
	}
}

func TestExtractStreamUsageResponses(t *testing.T) {
	usage := &model.TokenUsage{}
	extractStreamUsage([]byte(`{"type":"response.in_progress","response":{"usage":null}}`), "responses", "responses", usage)
	if usage.TotalTokens != 0 {
		t.Errorf("usage parsed from in_progress: %+v", usage)
	}
	extractStreamUsage([]byte(`{"type":"response.completed","response":{"status":"completed",
		"usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":3},
		"output_tokens":7,"total_tokens":19}}}`), "responses", "responses", usage)
	if usage.PromptTokens != 12 || usage.CompletionTokens != 7 || usage.TotalTokens != 19 {
		t.Errorf("usage = %+v", usage)
	}
	if usage.CacheHitTokens != 3 {
		t.Errorf("CacheHitTokens = %d, want 3", usage.CacheHitTokens)
	}
	if usage.Format != "openai" {
		t.Errorf("Format = %q, want openai semantics", usage.Format)
	}
}

// TestChatCompletionResponseToResponsesMissingTotalTokens guards the
// total_tokens fallback: gateways that omit total_tokens must not produce a
// Responses object reporting total_tokens: 0.
func TestChatCompletionResponseToResponsesMissingTotalTokens(t *testing.T) {
	body := `{"id":"chatcmpl-7","object":"chat.completion","model":"m",
		"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"hi"}}],
		"usage":{"prompt_tokens":10,"completion_tokens":5}}`
	out, err := ChatCompletionResponseToResponses([]byte(body), "m")
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	u := resp["usage"].(map[string]interface{})
	if u["total_tokens"] != float64(15) {
		t.Errorf("total_tokens = %v, want 15 (derived from input + output)", u["total_tokens"])
	}
	if u["input_tokens"] != float64(10) || u["output_tokens"] != float64(5) {
		t.Errorf("usage = %v", u)
	}
}
