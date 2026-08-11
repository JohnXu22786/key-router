package relay

import (
	"encoding/json"
	"strings"
	"testing"
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
