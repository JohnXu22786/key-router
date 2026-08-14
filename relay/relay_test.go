package relay

import (
	"encoding/json"
	"strings"
	"testing"

	"key-router/model"
)

// TestCompletionToStreamChunkPreservesReasoning guards the DeepSeek
// reasoning_content passthrough: when an upstream ignores stream:true and
// returns a full completion JSON, the synthesized chunk must keep the
// model's reasoning so streaming clients (e.g. opencode's
// reasoning_content interleave) still see it.
func TestCompletionToStreamChunkPreservesReasoning(t *testing.T) {
	body := `{
		"id":"chatcmpl-1",
		"object":"chat.completion",
		"model":"deepseek-v4-flash",
		"choices":[{
			"index":0,
			"finish_reason":"stop",
			"message":{
				"role":"assistant",
				"content":"final answer",
				"reasoning_content":"step 1: think; step 2: conclude"
			}
		}]
	}`

	out := completionToStreamChunk([]byte(body), "deepseek-v4-flash")
	var chunk struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &chunk); err != nil {
		t.Fatalf("bad chunk json: %v\n%s", err, out)
	}
	if len(chunk.Choices) == 0 {
		t.Fatalf("no choices in chunk: %s", out)
	}
	if chunk.Choices[0].Delta.Content != "final answer" {
		t.Errorf("content = %q, want final answer", chunk.Choices[0].Delta.Content)
	}
	if chunk.Choices[0].Delta.ReasoningContent != "step 1: think; step 2: conclude" {
		t.Errorf("reasoning_content lost: %q", chunk.Choices[0].Delta.ReasoningContent)
	}
	if !strings.Contains(string(out), "reasoning_content") {
		t.Errorf("chunk has no reasoning_content field: %s", out)
	}
}

// TestCompletionToStreamChunkDeltaForm covers the already-a-chunk input
// (choices[0].delta) — reasoning_content must survive there too.
func TestCompletionToStreamChunkDeltaForm(t *testing.T) {
	body := `{
		"id":"chatcmpl-2",
		"object":"chat.completion.chunk",
		"model":"deepseek-v4-flash",
		"choices":[{"index":0,"delta":{"role":"assistant","content":"hi","reasoning_content":"thinking"}}]
	}`
	out := completionToStreamChunk([]byte(body), "deepseek-v4-flash")
	var chunk struct {
		Choices []struct {
			Delta struct {
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &chunk); err != nil {
		t.Fatalf("bad chunk json: %v\n%s", err, out)
	}
	if chunk.Choices[0].Delta.ReasoningContent != "thinking" {
		t.Errorf("delta reasoning_content lost: %q", chunk.Choices[0].Delta.ReasoningContent)
	}
}

// TestExtractAnthropicUsageCacheTokens guards usage consistency between the
// non-stream Anthropic→OpenAI path and the streaming path: prompt_tokens
// must INCLUDE cached tokens in both (regression: the non-stream path
// reported input tokens without cache, so stream:false showed less usage
// than stream:true for the same upstream response).
func TestExtractAnthropicUsageCacheTokens(t *testing.T) {
	usage := extractAnthropicUsage(map[string]interface{}{
		"usage": map[string]interface{}{
			"input_tokens":                10.0,
			"output_tokens":               5.0,
			"cache_creation_input_tokens": 3.0,
			"cache_read_input_tokens":     2.0,
		},
	})
	if usage["prompt_tokens"] != float64(15) {
		t.Errorf("prompt_tokens = %v, want 15 (10 input + 3 create + 2 read)", usage["prompt_tokens"])
	}
	if usage["total_tokens"] != float64(20) {
		t.Errorf("total_tokens = %v, want 20", usage["total_tokens"])
	}
	details, _ := usage["prompt_tokens_details"].(map[string]interface{})
	if details["cached_tokens"] != float64(5) {
		t.Errorf("cached_tokens = %v, want 5", details["cached_tokens"])
	}
	if usage["input_tokens"] != float64(10) {
		t.Errorf("input_tokens = %v, want 10 (raw Anthropic semantics)", usage["input_tokens"])
	}
}

// TestParseTokenUsageDerivesTotalTokens guards billing when an OpenAI-
// compatible gateway omits total_tokens: the record must still count the
// request (regression: total 0 zeroed out billing and the streaming
// SetUsage gate).
func TestParseTokenUsageDerivesTotalTokens(t *testing.T) {
	u := ParseTokenUsage([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5}}`), "openai")
	if u.TotalTokens != 15 {
		t.Errorf("TotalTokens = %d, want 15 (derived)", u.TotalTokens)
	}
	u = ParseTokenUsage([]byte(`{"usage":{"input_tokens":10,"output_tokens":5}}`), "responses")
	if u.TotalTokens != 15 {
		t.Errorf("responses TotalTokens = %d, want 15 (derived)", u.TotalTokens)
	}
	// explicit total wins
	u = ParseTokenUsage([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":12}}`), "openai")
	if u.TotalTokens != 12 {
		t.Errorf("TotalTokens = %d, want 12 (explicit)", u.TotalTokens)
	}
}

// TestExtractStreamUsageDerivesTotalTokens guards the SSE-path counterpart
// of ParseTokenUsage: a streaming gateway omitting total_tokens must not
// zero the usage record (token-rate-limit windows would count nothing).
func TestExtractStreamUsageDerivesTotalTokens(t *testing.T) {
	usage := &model.TokenUsage{}
	extractStreamUsage([]byte(`{"choices":[{"delta":{"content":"x"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`), "openai", "openai", usage)
	if usage.TotalTokens != 15 {
		t.Errorf("openai TotalTokens = %d, want 15 (derived)", usage.TotalTokens)
	}
	usage = &model.TokenUsage{}
	extractStreamUsage([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":5}}}`), "responses", "responses", usage)
	if usage.TotalTokens != 15 {
		t.Errorf("responses TotalTokens = %d, want 15 (derived)", usage.TotalTokens)
	}
}

// TestConvertOpenAIResponseToAnthropicReasoning guards the non-stream
// OpenAI→Anthropic response path: the model's reasoning_content must
// survive as a thinking block (regression: it was silently dropped).
func TestConvertOpenAIResponseToAnthropicReasoning(t *testing.T) {
	body := `{
		"id":"chatcmpl-1",
		"object":"chat.completion",
		"model":"deepseek-v4-flash",
		"choices":[{
			"index":0,
			"finish_reason":"stop",
			"message":{
				"role":"assistant",
				"content":"final answer",
				"reasoning_content":"step 1: think; step 2: conclude"
			}
		}]
	}`

	out, err := ConvertOpenAIResponseToAnthropic([]byte(body))
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	var anth map[string]interface{}
	if err := json.Unmarshal(out, &anth); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	content, ok := anth["content"].([]interface{})
	if !ok {
		t.Fatalf("content = %v, want block array", anth["content"])
	}
	if len(content) != 2 {
		t.Fatalf("content = %d blocks, want 2 (thinking + text)", len(content))
	}
	first := content[0].(map[string]interface{})
	if first["type"] != "thinking" || first["thinking"] != "step 1: think; step 2: conclude" {
		t.Errorf("first block = %v, want thinking block with reasoning", first)
	}
	if _, has := first["signature"]; !has {
		t.Error("thinking block has no signature field")
	}
	second := content[1].(map[string]interface{})
	if second["type"] != "text" || second["text"] != "final answer" {
		t.Errorf("second block = %v, want text block", second)
	}
}

// TestConvertOpenAIResponseToAnthropicReasoningParts covers the o-series
// content-parts shape ({"type":"reasoning","summary":[...]}) on the same
// conversion path, including accumulation from BOTH sources (the
// reasoning_content field and a reasoning part, in that order).
func TestConvertOpenAIResponseToAnthropicReasoningParts(t *testing.T) {
	body := `{
		"id":"chatcmpl-2",
		"object":"chat.completion",
		"model":"o3",
		"choices":[{
			"index":0,
			"finish_reason":"stop",
			"message":{
				"role":"assistant",
				"reasoning_content":"field reasoning",
				"content":[
					{"type":"reasoning","summary":[{"type":"summary_text","text":"part reasoning"}]},
					{"type":"text","text":"answer"}
				]
			}
		}]
	}`

	out, err := ConvertOpenAIResponseToAnthropic([]byte(body))
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	var anth map[string]interface{}
	if err := json.Unmarshal(out, &anth); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	content := anth["content"].([]interface{})
	if len(content) != 2 {
		t.Fatalf("content = %d blocks, want 2 (thinking + text)", len(content))
	}
	first := content[0].(map[string]interface{})
	// both sources accumulate, field first, joined with "\n"
	if first["type"] != "thinking" || first["thinking"] != "field reasoning\npart reasoning" {
		t.Errorf("first block = %v, want thinking block with both reasoning sources", first)
	}
	second := content[1].(map[string]interface{})
	if second["type"] != "text" || second["text"] != "answer" {
		t.Errorf("second block = %v, want text block", second)
	}
}

// TestConvertAnthropicResponseToOpenAIThinking guards the non-stream
// Anthropic→OpenAI response path: thinking blocks must survive as
// message.reasoning_content (regression: they were silently dropped).
func TestConvertAnthropicResponseToOpenAIThinking(t *testing.T) {
	body := `{
		"id":"msg_1",
		"type":"message",
		"role":"assistant",
		"model":"claude-sonnet-4",
		"content":[
			{"type":"thinking","thinking":"let me reason","signature":"sig-1"},
			{"type":"text","text":"answer"}
		],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":5,"output_tokens":3}
	}`

	out, err := ConvertAnthropicResponseToOpenAI([]byte(body), "claude-sonnet-4")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	var oai map[string]interface{}
	if err := json.Unmarshal(out, &oai); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	choices := oai["choices"].([]interface{})
	choice := choices[0].(map[string]interface{})
	msg := choice["message"].(map[string]interface{})
	if rc, ok := msg["reasoning_content"].(string); !ok || rc != "let me reason" {
		t.Errorf("reasoning_content = %v, want 'let me reason'", msg["reasoning_content"])
	}
	if msg["content"] != "answer" {
		t.Errorf("content = %v, want answer", msg["content"])
	}
}

// TestConvertAnthropicResponseToOpenAIThinkingMultipleBlocks pins the "\n"
// join across multiple thinking blocks (boundary words must not merge).
func TestConvertAnthropicResponseToOpenAIThinkingMultipleBlocks(t *testing.T) {
	body := `{
		"id":"msg_2",
		"type":"message",
		"role":"assistant",
		"model":"claude-sonnet-4",
		"content":[
			{"type":"thinking","thinking":"think A","signature":"s1"},
			{"type":"thinking","thinking":"think B","signature":"s2"},
			{"type":"text","text":"answer"}
		],
		"stop_reason":"end_turn",
		"usage":{"input_tokens":5,"output_tokens":3}
	}`

	out, err := ConvertAnthropicResponseToOpenAI([]byte(body), "claude-sonnet-4")
	if err != nil {
		t.Fatalf("conversion failed: %v", err)
	}
	var oai map[string]interface{}
	if err := json.Unmarshal(out, &oai); err != nil {
		t.Fatalf("invalid JSON result: %v", err)
	}
	choices := oai["choices"].([]interface{})
	choice := choices[0].(map[string]interface{})
	msg := choice["message"].(map[string]interface{})
	if rc, ok := msg["reasoning_content"].(string); !ok || rc != "think A\nthink B" {
		t.Errorf("reasoning_content = %q, want 'think A\\nthink B'", msg["reasoning_content"])
	}
}
