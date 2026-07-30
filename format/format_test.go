package format

import (
	"encoding/json"
	"testing"
)

func TestOpenAIToAnthropic(t *testing.T) {
	t.Run("simple text message", func(t *testing.T) {
		oaiReq := `{
			"model": "gpt-4o",
			"messages": [
				{"role": "user", "content": "Hello, how are you?"}
			]
		}`

		anthReq, err := OpenAIRequestToAnthropic([]byte(oaiReq), "claude-sonnet-4")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(anthReq, &result); err != nil {
			t.Fatalf("invalid JSON result: %v", err)
		}

		if result["model"] != "claude-sonnet-4" {
			t.Errorf("model = %v, want claude-sonnet-4", result["model"])
		}
		if _, ok := result["messages"]; !ok {
			t.Error("messages not found in result")
		}
		if _, ok := result["system"]; ok {
			t.Error("system should not be present for user-only messages")
		}
	})

	t.Run("system message becomes top-level system field", func(t *testing.T) {
		oaiReq := `{
			"model": "gpt-4o",
			"messages": [
				{"role": "system", "content": "You are a helpful assistant."},
				{"role": "user", "content": "Hi!"}
			]
		}`

		anthReq, err := OpenAIRequestToAnthropic([]byte(oaiReq), "")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}

		var result map[string]interface{}
		json.Unmarshal(anthReq, &result)

		if result["system"] != "You are a helpful assistant." {
			t.Errorf("system = %v, want 'You are a helpful assistant.'", result["system"])
		}
	})

	t.Run("supports max_tokens mapping", func(t *testing.T) {
		oaiReq := `{
			"model": "gpt-4o",
			"messages": [{"role": "user", "content": "hi"}],
			"max_tokens": 1000
		}`

		anthReq, _ := OpenAIRequestToAnthropic([]byte(oaiReq), "")

		var result map[string]interface{}
		json.Unmarshal(anthReq, &result)

		if result["max_tokens"] != float64(1000) {
			t.Errorf("max_tokens = %v, want 1000", result["max_tokens"])
		}
	})

	t.Run("stream option preserved", func(t *testing.T) {
		oaiReq := `{
			"model": "gpt-4o",
			"messages": [{"role": "user", "content": "hi"}],
			"stream": true
		}`

		anthReq, _ := OpenAIRequestToAnthropic([]byte(oaiReq), "")

		var result map[string]interface{}
		json.Unmarshal(anthReq, &result)

		if result["stream"] != true {
			t.Error("stream should be preserved")
		}
	})
}

func TestAnthropicToOpenAI(t *testing.T) {
	t.Run("simple text message", func(t *testing.T) {
		anthReq := `{
			"model": "claude-sonnet-4",
			"messages": [
				{"role": "user", "content": "Hello, how are you?"}
			],
			"max_tokens": 1000
		}`

		oaiReq, err := AnthropicRequestToOpenAI([]byte(anthReq), "gpt-4o")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}

		var result map[string]interface{}
		json.Unmarshal(oaiReq, &result)

		if result["model"] != "gpt-4o" {
			t.Errorf("model = %v, want gpt-4o", result["model"])
		}
		if _, ok := result["messages"]; !ok {
			t.Error("messages not found")
		}
		if _, ok := result["max_tokens"]; !ok {
			t.Error("max_tokens should be present")
		}
	})

	t.Run("system becomes first message", func(t *testing.T) {
		anthReq := `{
			"model": "claude-sonnet-4",
			"system": "You are Claude.",
			"messages": [
				{"role": "user", "content": "Hi!"}
			],
			"max_tokens": 100
		}`

		oaiReq, _ := AnthropicRequestToOpenAI([]byte(anthReq), "gpt-4o")

		var result map[string]interface{}
		json.Unmarshal(oaiReq, &result)

		messages := result["messages"].([]interface{})
		firstMsg := messages[0].(map[string]interface{})
		if firstMsg["role"] != "system" {
			t.Errorf("first message role = %v, want system", firstMsg["role"])
		}
		if firstMsg["content"] != "You are Claude." {
			t.Errorf("first message content = %v, want 'You are Claude.'", firstMsg["content"])
		}
	})
}

func TestOpenAIToAnthropic_StreamChunk(t *testing.T) {
	t.Run("content delta chunk", func(t *testing.T) {
		oaiChunk := `{"id":"chatcmpl-123","object":"chat.completion.chunk","choices":[{"delta":{"content":"Hello"},"index":0}]}`

		anthChunk, err := OpenAIStreamChunkToAnthropic([]byte(oaiChunk))
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}

		var result map[string]interface{}
		json.Unmarshal(anthChunk, &result)

		if result["type"] != "content_block_delta" {
			t.Errorf("type = %v, want content_block_delta", result["type"])
		}
	})

	t.Run("empty delta should be skipped", func(t *testing.T) {
		oaiChunk := `{"id":"chatcmpl-123","object":"chat.completion.chunk","choices":[{"delta":{},"index":0}]}`

		_, err := OpenAIStreamChunkToAnthropic([]byte(oaiChunk))
		if err != ErrSkipChunk {
			t.Errorf("expected ErrSkipChunk, got %v", err)
		}
	})
}

func TestAnthropicToOpenAI_StreamChunk(t *testing.T) {
	t.Run("content block delta", func(t *testing.T) {
		anthChunk := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`

		oaiChunk, err := AnthropicStreamEventToOpenAI([]byte(anthChunk))
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}

		var result map[string]interface{}
		json.Unmarshal(oaiChunk, &result)

		choices := result["choices"].([]interface{})
		delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
		if delta["content"] != "Hello" {
			t.Errorf("content = %v, want Hello", delta["content"])
		}
	})
}

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{"chat completions", "/v1/chat/completions", "openai"},
		{"embeddings", "/v1/embeddings", "openai"},
		{"models list", "/v1/models", "openai"},
		{"anthropic messages", "/v1/messages", "anthropic"},
		{"unknown", "/v1/somepath", "openai"}, // default to openai
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectFormat(tt.path); got != tt.expected {
				t.Errorf("DetectFormat(%q) = %q, want %q", tt.path, got, tt.expected)
			}
		})
	}
}
