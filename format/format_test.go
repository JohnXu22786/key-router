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

func TestOpenAIToAnthropic_Complex(t *testing.T) {
	t.Run("tool_calls and vision", func(t *testing.T) {
		oaiReq := `{
			"model": "gpt-4o",
			"messages": [
				{"role": "system", "content": "You are a helpful assistant."},
				{"role": "user", "content": [
					{"type": "text", "text": "What's in this image?"},
					{"type": "image_url", "image_url": {"url": "data:image/png;base64,iVBORw0KGgo="}}
				]},
				{"role": "assistant", "content": "", "tool_calls": [
					{"id": "call_123", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\": \"Tokyo\"}"}}
				]},
				{"role": "tool", "tool_call_id": "call_123", "content": "25°C"}
			],
			"max_tokens": 1000,
			"stream": true
		}`

		anthReq, err := OpenAIRequestToAnthropic([]byte(oaiReq), "claude-sonnet-4")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}

		var result map[string]interface{}
		json.Unmarshal(anthReq, &result)

		// Check system prompt
		if result["system"] != "You are a helpful assistant." {
			t.Errorf("system = %v", result["system"])
		}
		// Check model
		if result["model"] != "claude-sonnet-4" {
			t.Errorf("model = %v", result["model"])
		}
		// Check max_tokens
		if result["max_tokens"] != float64(1000) {
			t.Errorf("max_tokens = %v", result["max_tokens"])
		}
		// Check stream
		if result["stream"] != true {
			t.Error("stream should be true")
		}

		// System prompt is extracted to top-level "system", so messages has 3: user, assistant, user(tool_result)
		msgs := result["messages"].([]interface{})
		if len(msgs) != 3 {
			t.Fatalf("expected 3 messages (user, assistant, user[tool_result]), got %d", len(msgs))
		}

		// User message should have content array with text and image
		userMsg := msgs[0].(map[string]interface{})
		userContent := userMsg["content"].([]interface{})
		if len(userContent) != 2 {
			t.Fatalf("expected 2 content blocks in user message, got %d", len(userContent))
		}

		// Assistant message (index 1) should have tool_use
		asstMsg := msgs[1].(map[string]interface{})
		asstContent := asstMsg["content"].([]interface{})
		foundToolUse := false
		for _, c := range asstContent {
			block := c.(map[string]interface{})
			if block["type"] == "tool_use" {
				foundToolUse = true
				if block["name"] != "get_weather" {
					t.Errorf("tool name = %v", block["name"])
				}
			}
		}
		if !foundToolUse {
			t.Error("expected tool_use in assistant message")
		}

		// Tool result should be converted to user/tool_result
		toolMsg := msgs[2].(map[string]interface{})
		toolContent := toolMsg["content"].([]interface{})
		if len(toolContent) > 0 {
			tr := toolContent[0].(map[string]interface{})
			if tr["type"] != "tool_result" {
				t.Errorf("expected tool_result, got %s", tr["type"])
			}
		}
	})
}

func TestAnthropicToOpenAI_Complex(t *testing.T) {
	t.Run("tool_use and image messages", func(t *testing.T) {
		anthReq := `{
			"model": "claude-sonnet-4",
			"system": "You are Claude.",
			"messages": [
				{"role": "user", "content": [
					{"type": "text", "text": "Describe this image"},
					{"type": "image", "source": {"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo="}}
				]},
				{"role": "assistant", "content": [
					{"type": "text", "text": "I see an image"},
					{"type": "tool_use", "id": "toolu_123", "name": "get_weather", "input": {"city": "Paris"}}
				]},
				{"role": "user", "content": [
					{"type": "tool_result", "tool_use_id": "toolu_123", "content": "22°C"}
				]}
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
			t.Errorf("model = %v", result["model"])
		}

		msgs := result["messages"].([]interface{})
		// system + user + assistant + tool = 4
		if len(msgs) != 4 {
			t.Fatalf("expected 4 messages (system, user, assistant, user), got %d", len(msgs))
		}

		// First message should be system
		sysMsg := msgs[0].(map[string]interface{})
		if sysMsg["role"] != "system" {
			t.Errorf("first message role = %v", sysMsg["role"])
		}

		// Assistant should have tool_calls
		asstMsg := msgs[2].(map[string]interface{})
		toolCalls, ok := asstMsg["tool_calls"].([]interface{})
		if !ok || len(toolCalls) == 0 {
			t.Error("expected tool_calls in assistant message")
		} else {
			tc := toolCalls[0].(map[string]interface{})
			if tc["type"] != "function" {
				t.Errorf("tool_call type = %v", tc["type"])
			}
		}
	})
}

func TestOpenAIToAnthropic_StreamComplex(t *testing.T) {
	t.Run("finish_reason length and message_delta", func(t *testing.T) {
		// OpenAI chunk with finish_reason: length
		oaiChunk := `{"id":"cmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":"length"}]}`
		anthChunk, err := OpenAIStreamChunkToAnthropic([]byte(oaiChunk))
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal(anthChunk, &result)
		if result["type"] != "message_stop" {
			t.Errorf("type = %v, want message_stop for length finish_reason", result["type"])
		}
	})

	t.Run("anthropic message_delta with stop_reason", func(t *testing.T) {
		anthEvent := `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":42}}`
		oaiChunk, err := AnthropicStreamEventToOpenAI([]byte(anthEvent))
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal(oaiChunk, &result)
		choices := result["choices"].([]interface{})
		choice := choices[0].(map[string]interface{})
		if choice["finish_reason"] != "stop" {
			t.Errorf("finish_reason = %v, want stop", choice["finish_reason"])
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
