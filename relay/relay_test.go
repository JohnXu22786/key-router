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
