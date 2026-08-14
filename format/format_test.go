package format

import (
	"encoding/json"
	"fmt"
	"strings"
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

func TestOpenAIStreamConverter_ToolCalls(t *testing.T) {
	t.Run("tool_use blocks become tool_calls deltas", func(t *testing.T) {
		conv := NewOpenAIStreamConverter()

		// content_block_start with tool_use
		events, err := conv.Convert([]byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{}}}`))
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 chunk, got %d", len(events))
		}
		var result map[string]interface{}
		json.Unmarshal(events[0], &result)
		choices := result["choices"].([]interface{})
		delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
		toolCalls, ok := delta["tool_calls"].([]interface{})
		if !ok || len(toolCalls) != 1 {
			t.Fatalf("expected tool_calls in delta, got %v", delta["tool_calls"])
		}
		tc := toolCalls[0].(map[string]interface{})
		if tc["id"] != "toolu_1" || tc["index"] != float64(0) {
			t.Errorf("tool_call = %v, want id=toolu_1 index=0", tc)
		}
		fn := tc["function"].(map[string]interface{})
		if fn["name"] != "get_weather" {
			t.Errorf("function name = %v, want get_weather", fn["name"])
		}

		// input_json_delta fragment maps to the same tool index
		events, err = conv.Convert([]byte(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Tok"}}`))
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		json.Unmarshal(events[0], &result)
		choices = result["choices"].([]interface{})
		delta = choices[0].(map[string]interface{})["delta"].(map[string]interface{})
		toolCalls = delta["tool_calls"].([]interface{})
		tc = toolCalls[0].(map[string]interface{})
		if tc["index"] != float64(0) {
			t.Errorf("tool index = %v, want 0", tc["index"])
		}
		fn = tc["function"].(map[string]interface{})
		if fn["arguments"] != `{"city":"Tok` {
			t.Errorf("arguments = %v", fn["arguments"])
		}

		// message_delta stop_reason tool_use → finish_reason tool_calls
		events, err = conv.Convert([]byte(`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":10}}`))
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		json.Unmarshal(events[0], &result)
		choices = result["choices"].([]interface{})
		choice := choices[0].(map[string]interface{})
		if choice["finish_reason"] != "tool_calls" {
			t.Errorf("finish_reason = %v, want tool_calls", choice["finish_reason"])
		}

		// message_stop is a no-op (finish already sent)
		if _, err := conv.Convert([]byte(`{"type":"message_stop"}`)); err != ErrSkipChunk {
			t.Errorf("message_stop: expected ErrSkipChunk, got %v", err)
		}
	})

	t.Run("text block and tool block indices stay distinct", func(t *testing.T) {
		conv := NewOpenAIStreamConverter()

		// text delta at index 0
		events, err := conv.Convert([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Sure"}}`))
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		var result map[string]interface{}
		json.Unmarshal(events[0], &result)
		choices := result["choices"].([]interface{})
		delta := choices[0].(map[string]interface{})["delta"].(map[string]interface{})
		if delta["content"] != "Sure" {
			t.Errorf("content = %v, want Sure", delta["content"])
		}

		// tool block at index 1 → tool index 0
		events, err = conv.Convert([]byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t1","name":"f","input":{}}}`))
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		json.Unmarshal(events[0], &result)
		choices = result["choices"].([]interface{})
		delta = choices[0].(map[string]interface{})["delta"].(map[string]interface{})
		toolCalls := delta["tool_calls"].([]interface{})
		tc := toolCalls[0].(map[string]interface{})
		if tc["index"] != float64(0) {
			t.Errorf("tool index = %v, want 0", tc["index"])
		}

		// second tool block at index 2 → tool index 1
		events, err = conv.Convert([]byte(`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"t2","name":"g","input":{}}}`))
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		json.Unmarshal(events[0], &result)
		choices = result["choices"].([]interface{})
		delta = choices[0].(map[string]interface{})["delta"].(map[string]interface{})
		toolCalls = delta["tool_calls"].([]interface{})
		tc = toolCalls[0].(map[string]interface{})
		if tc["index"] != float64(1) {
			t.Errorf("tool index = %v, want 1", tc["index"])
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
		// system + user + assistant + tool + trailing assistant = 5
		if len(msgs) != 5 {
			t.Fatalf("expected 5 messages (system, user, assistant, tool, assistant), got %d", len(msgs))
		}

		// First message should be system
		sysMsg := msgs[0].(map[string]interface{})
		if sysMsg["role"] != "system" {
			t.Errorf("first message role = %v, want system", sysMsg["role"])
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

		// Tool result becomes a separate tool message, not a content part
		toolMsg := msgs[3].(map[string]interface{})
		if toolMsg["role"] != "tool" {
			t.Fatalf("last message role = %v, want tool", toolMsg["role"])
		}
		if toolMsg["tool_call_id"] != "toolu_123" {
			t.Errorf("tool_call_id = %v, want toolu_123", toolMsg["tool_call_id"])
		}
		if toolMsg["content"] != "22°C" {
			t.Errorf("tool content = %v, want 22°C", toolMsg["content"])
		}

		// A conversation may not end on a tool message — trailing assistant
		tailMsg := msgs[4].(map[string]interface{})
		if tailMsg["role"] != "assistant" {
			t.Errorf("last message role = %v, want assistant", tailMsg["role"])
		}
	})
}

func TestOpenAIToAnthropic_StreamComplex(t *testing.T) {
	t.Run("finish_reason length defers message_delta, terminated on usage/close", func(t *testing.T) {
		conv := NewAnthropicStreamConverter()

		// OpenAI chunk with finish_reason: length — blocks close, termination
		// is deferred
		events, err := conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":"length"}]}`), "")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		// message_start + content_block_start + content_block_delta + content_block_stop (no message_delta yet)
		if len(events) != 4 {
			t.Fatalf("expected 4 events (no message_delta yet), got %d", len(events))
		}
		var last map[string]interface{}
		json.Unmarshal(events[3], &last)
		if last["type"] != "content_block_stop" {
			t.Errorf("last event type = %v, want content_block_stop", last["type"])
		}

		// Usage-only chunk arrives → message_delta with real tokens + message_stop
		events, err = conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":42,"total_tokens":52}}`), "")
		if err != nil {
			t.Fatalf("usage conversion failed: %v", err)
		}
		if len(events) != 2 {
			t.Fatalf("expected 2 events (message_delta + message_stop), got %d", len(events))
		}
		var md map[string]interface{}
		json.Unmarshal(events[0], &md)
		if md["type"] != "message_delta" {
			t.Fatalf("event type = %v, want message_delta", md["type"])
		}
		d := md["delta"].(map[string]interface{})
		if d["stop_reason"] != "max_tokens" {
			t.Errorf("stop_reason = %v, want max_tokens", d["stop_reason"])
		}
		usage := md["usage"].(map[string]interface{})
		if usage["output_tokens"] != float64(42) {
			t.Errorf("output_tokens = %v, want 42", usage["output_tokens"])
		}
		var ms map[string]interface{}
		json.Unmarshal(events[1], &ms)
		if ms["type"] != "message_stop" {
			t.Errorf("event type = %v, want message_stop", ms["type"])
		}
	})

	t.Run("no usage chunk → CloseStream terminates with deferred reason", func(t *testing.T) {
		conv := NewAnthropicStreamConverter()

		events, err := conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`), "")
		if err != ErrSkipChunk {
			t.Fatalf("expected ErrSkipChunk for finish-without-content, got %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 event (message_start), got %d", len(events))
		}
		// No usage chunk came; CloseStream must emit message_delta + message_stop
		closeEvents := conv.CloseStream()
		if len(closeEvents) != 2 {
			t.Fatalf("expected 2 close events, got %d", len(closeEvents))
		}
		var md map[string]interface{}
		json.Unmarshal(closeEvents[0], &md)
		if md["type"] != "message_delta" {
			t.Errorf("close event[0] type = %v, want message_delta", md["type"])
		}
		d := md["delta"].(map[string]interface{})
		if d["stop_reason"] != "end_turn" {
			t.Errorf("stop_reason = %v, want end_turn", d["stop_reason"])
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

func TestOpenAIToAnthropic_ToolCallArguments(t *testing.T) {
	t.Run("arguments string becomes input object", func(t *testing.T) {
		oaiReq := `{
			"model": "gpt-4o",
			"messages": [
				{"role": "assistant", "content": "", "tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "get_weather", "arguments": "{\"city\": \"Tokyo\"}"}}
				]},
				{"role": "tool", "tool_call_id": "call_1", "content": "25°C"}
			],
			"tool_choice": {"type": "function", "function": {"name": "get_weather"}}
		}`

		anthReq, err := OpenAIRequestToAnthropic([]byte(oaiReq), "")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(anthReq, &result); err != nil {
			t.Fatalf("invalid JSON result: %v", err)
		}

		// tool_use.input must be an object, not a string
		msgs := result["messages"].([]interface{})
		asstMsg := msgs[0].(map[string]interface{})
		content := asstMsg["content"].([]interface{})
		found := false
		for _, c := range content {
			block := c.(map[string]interface{})
			if block["type"] == "tool_use" {
				found = true
				input, ok := block["input"].(map[string]interface{})
				if !ok {
					t.Fatalf("tool_use input = %v (%T), want object", block["input"], block["input"])
				}
				if input["city"] != "Tokyo" {
					t.Errorf("tool_use input city = %v, want Tokyo", input["city"])
				}
			}
		}
		if !found {
			t.Error("expected tool_use block")
		}

		// tool_choice mapped to anthropic {"type":"tool","name":...}
		tc := result["tool_choice"].(map[string]interface{})
		if tc["type"] != "tool" || tc["name"] != "get_weather" {
			t.Errorf("tool_choice = %v, want {type:tool name:get_weather}", tc)
		}
	})

	t.Run("invalid arguments JSON falls back to empty object", func(t *testing.T) {
		oaiReq := `{
			"model": "gpt-4o",
			"messages": [
				{"role": "assistant", "content": "", "tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "f", "arguments": "not-json"}}
				]}
			]
		}`

		anthReq, err := OpenAIRequestToAnthropic([]byte(oaiReq), "")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}

		var result map[string]interface{}
		json.Unmarshal(anthReq, &result)
		msgs := result["messages"].([]interface{})
		asstMsg := msgs[0].(map[string]interface{})
		content := asstMsg["content"].([]interface{})
		block := content[0].(map[string]interface{})
		input, ok := block["input"].(map[string]interface{})
		if !ok || len(input) != 0 {
			t.Errorf("input = %v, want empty object", block["input"])
		}
	})
}

func TestOpenAIToAnthropic_StopNormalization(t *testing.T) {
	t.Run("string stop becomes stop_sequences array", func(t *testing.T) {
		oaiReq := `{
			"model": "gpt-4o",
			"messages": [{"role": "user", "content": "hi"}],
			"stop": "END"
		}`

		anthReq, _ := OpenAIRequestToAnthropic([]byte(oaiReq), "")
		var result map[string]interface{}
		json.Unmarshal(anthReq, &result)

		seq, ok := result["stop_sequences"].([]interface{})
		if !ok || len(seq) != 1 || seq[0] != "END" {
			t.Errorf("stop_sequences = %v, want [END]", result["stop_sequences"])
		}
	})
}

func TestAnthropicToOpenAI_ToolCallArgumentsString(t *testing.T) {
	t.Run("tool_use input object becomes arguments JSON string", func(t *testing.T) {
		anthReq := `{
			"model": "claude-sonnet-4",
			"messages": [
				{"role": "user", "content": "weather?"},
				{"role": "assistant", "content": [
					{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "Tokyo"}}
				]}
			],
			"max_tokens": 100
		}`

		oaiReq, err := AnthropicRequestToOpenAI([]byte(anthReq), "")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}

		var result map[string]interface{}
		json.Unmarshal(oaiReq, &result)

		msgs := result["messages"].([]interface{})
		asstMsg := msgs[1].(map[string]interface{})
		toolCalls := asstMsg["tool_calls"].([]interface{})
		tc := toolCalls[0].(map[string]interface{})
		fn := tc["function"].(map[string]interface{})

		// arguments must be a JSON-encoded STRING, not the raw object
		args, ok := fn["arguments"].(string)
		if !ok {
			t.Fatalf("arguments = %v (%T), want string", fn["arguments"], fn["arguments"])
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(args), &parsed); err != nil {
			t.Fatalf("arguments is not valid JSON: %v", err)
		}
		if parsed["city"] != "Tokyo" {
			t.Errorf("arguments city = %v, want Tokyo", parsed["city"])
		}
	})

	t.Run("null input falls back to {}", func(t *testing.T) {
		anthReq := `{
			"model": "claude-sonnet-4",
			"messages": [
				{"role": "user", "content": "hi"},
				{"role": "assistant", "content": [
					{"type": "tool_use", "id": "toolu_2", "name": "f", "input": null}
				]}
			],
			"max_tokens": 100
		}`

		oaiReq, err := AnthropicRequestToOpenAI([]byte(anthReq), "")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}

		var result map[string]interface{}
		json.Unmarshal(oaiReq, &result)
		msgs := result["messages"].([]interface{})
		asstMsg := msgs[1].(map[string]interface{})
		toolCalls := asstMsg["tool_calls"].([]interface{})
		tc := toolCalls[0].(map[string]interface{})
		fn := tc["function"].(map[string]interface{})
		if fn["arguments"] != "{}" {
			t.Errorf("arguments = %v, want {}", fn["arguments"])
		}
	})
}

func TestAnthropicToOpenAI_ToolChoice(t *testing.T) {
	t.Run("tool type maps to function", func(t *testing.T) {
		anthReq := `{
			"model": "claude-sonnet-4",
			"messages": [{"role": "user", "content": "hi"}],
			"tool_choice": {"type": "tool", "name": "get_weather"}
		}`

		oaiReq, err := AnthropicRequestToOpenAI([]byte(anthReq), "")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}

		var result map[string]interface{}
		json.Unmarshal(oaiReq, &result)

		tc := result["tool_choice"].(map[string]interface{})
		if tc["type"] != "function" {
			t.Fatalf("tool_choice type = %v, want function", tc["type"])
		}
		fn := tc["function"].(map[string]interface{})
		if fn["name"] != "get_weather" {
			t.Errorf("tool_choice function name = %v, want get_weather", fn["name"])
		}
	})
}

func TestAnthropicStreamConverter_EventOrdering(t *testing.T) {
	t.Run("emits message_start, deltas, message_delta, message_stop", func(t *testing.T) {
		conv := NewAnthropicStreamConverter()

		// First content chunk
		events, err := conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`), "gpt-4o")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		if len(events) != 3 {
			t.Fatalf("expected 3 events (message_start + content_block_start + content_block_delta), got %d", len(events))
		}
		var start map[string]interface{}
		json.Unmarshal(events[0], &start)
		if start["type"] != "message_start" {
			t.Errorf("first event type = %v, want message_start", start["type"])
		}
		msg := start["message"].(map[string]interface{})
		if msg["model"] != "gpt-4o" {
			t.Errorf("message_start model = %v, want gpt-4o", msg["model"])
		}
		var cbs map[string]interface{}
		json.Unmarshal(events[1], &cbs)
		if cbs["type"] != "content_block_start" {
			t.Errorf("second event type = %v, want content_block_start", cbs["type"])
		}
		var delta map[string]interface{}
		json.Unmarshal(events[2], &delta)
		if delta["type"] != "content_block_delta" {
			t.Errorf("third event type = %v, want content_block_delta", delta["type"])
		}

		// Finish chunk → content_block_stop; message_delta/message_stop are
		// deferred until the usage chunk or CloseStream
		events, err = conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`), "gpt-4o")
		if err != nil {
			t.Fatalf("finish conversion failed: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 event (content_block_stop), got %d", len(events))
		}
		var cbsStop map[string]interface{}
		json.Unmarshal(events[0], &cbsStop)
		if cbsStop["type"] != "content_block_stop" {
			t.Errorf("event type = %v, want content_block_stop", cbsStop["type"])
		}

		// Usage chunk → message_delta + message_stop
		events, err = conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15}}`), "gpt-4o")
		if err != nil {
			t.Fatalf("usage conversion failed: %v", err)
		}
		if len(events) != 2 {
			t.Fatalf("expected 2 events (message_delta + message_stop), got %d", len(events))
		}
		var md map[string]interface{}
		json.Unmarshal(events[0], &md)
		if md["type"] != "message_delta" {
			t.Errorf("event type = %v, want message_delta", md["type"])
		}
		d := md["delta"].(map[string]interface{})
		if d["stop_reason"] != "end_turn" {
			t.Errorf("stop_reason = %v, want end_turn", d["stop_reason"])
		}
		var ms map[string]interface{}
		json.Unmarshal(events[1], &ms)
		if ms["type"] != "message_stop" {
			t.Errorf("event type = %v, want message_stop", ms["type"])
		}
	})

	t.Run("content and finish in same chunk both preserved", func(t *testing.T) {
		conv := NewAnthropicStreamConverter()

		// Some OpenAI-compatible upstreams send the final content fragment
		// together with finish_reason — both must be emitted; termination
		// is deferred to the usage chunk.
		events, err := conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":"stop"}]}`), "gpt-4o")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		// message_start + content_block_start + content_block_delta + content_block_stop
		if len(events) != 4 {
			t.Fatalf("expected 4 events, got %d", len(events))
		}
		var delta map[string]interface{}
		json.Unmarshal(events[2], &delta)
		if delta["type"] != "content_block_delta" {
			t.Fatalf("event[2] type = %v, want content_block_delta", delta["type"])
		}
		d := delta["delta"].(map[string]interface{})
		if d["text"] != "partial" {
			t.Errorf("text = %v, want partial", d["text"])
		}
		var cbsStop map[string]interface{}
		json.Unmarshal(events[3], &cbsStop)
		if cbsStop["type"] != "content_block_stop" {
			t.Errorf("event[3] type = %v, want content_block_stop", cbsStop["type"])
		}

		// Usage chunk terminates with message_delta + message_stop
		events, err = conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[],"usage":{"completion_tokens":7}}`), "gpt-4o")
		if err != nil {
			t.Fatalf("usage conversion failed: %v", err)
		}
		if len(events) != 2 {
			t.Fatalf("expected 2 events (message_delta + message_stop), got %d", len(events))
		}
		var md map[string]interface{}
		json.Unmarshal(events[0], &md)
		if md["type"] != "message_delta" {
			t.Errorf("event[0] type = %v, want message_delta", md["type"])
		}
		dr := md["delta"].(map[string]interface{})
		if dr["stop_reason"] != "end_turn" {
			t.Errorf("stop_reason = %v, want end_turn", dr["stop_reason"])
		}
		usage := md["usage"].(map[string]interface{})
		if usage["output_tokens"] != float64(7) {
			t.Errorf("output_tokens = %v, want 7", usage["output_tokens"])
		}
		var ms map[string]interface{}
		json.Unmarshal(events[1], &ms)
		if ms["type"] != "message_stop" {
			t.Errorf("event[1] type = %v, want message_stop", ms["type"])
		}
	})

	t.Run("finish chunk without delta still terminates", func(t *testing.T) {
		conv := NewAnthropicStreamConverter()

		events, err := conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"finish_reason":"stop"}]}`), "gpt-4o")
		if err != ErrSkipChunk {
			t.Fatalf("expected ErrSkipChunk, got %v", err)
		}
		// message_start only (no blocks opened; termination deferred)
		if len(events) != 1 {
			t.Fatalf("expected 1 event (message_start), got %d", len(events))
		}
		// CloseStream (no usage chunk) emits message_delta + message_stop
		closeEvents := conv.CloseStream()
		if len(closeEvents) != 2 {
			t.Fatalf("expected 2 close events, got %d", len(closeEvents))
		}
		var md map[string]interface{}
		json.Unmarshal(closeEvents[0], &md)
		if md["type"] != "message_delta" {
			t.Errorf("close event[0] type = %v, want message_delta", md["type"])
		}
		var ms map[string]interface{}
		json.Unmarshal(closeEvents[1], &ms)
		if ms["type"] != "message_stop" {
			t.Errorf("close event[1] type = %v, want message_stop", ms["type"])
		}
	})
}

func TestAnthropicStreamConverter_RoleOnlyFirstChunk(t *testing.T) {
	t.Run("role-only first chunk still emits message_start", func(t *testing.T) {
		conv := NewAnthropicStreamConverter()

		// Real OpenAI streams open with a role-only chunk (no content)
		events, err := conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`), "gpt-4o")
		if err != ErrSkipChunk {
			t.Fatalf("expected ErrSkipChunk, got %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 event (message_start), got %d", len(events))
		}
		var start map[string]interface{}
		json.Unmarshal(events[0], &start)
		if start["type"] != "message_start" {
			t.Errorf("event type = %v, want message_start", start["type"])
		}

		// Post-finish chunks are ignored (no duplicate message_stop); the
		// finish chunk itself returns ErrSkipChunk with only message_start
		// since termination is deferred.
		_, err = conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`), "gpt-4o")
		if err != ErrSkipChunk {
			t.Fatalf("expected ErrSkipChunk for finish chunk, got %v", err)
		}
		events, err = conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"late"},"finish_reason":null}]}`), "gpt-4o")
		if err != ErrSkipChunk {
			t.Fatalf("expected ErrSkipChunk after finish, got %v", err)
		}
		if len(events) != 0 {
			t.Errorf("expected no events after finish, got %d", len(events))
		}
	})

	t.Run("tool_calls become content_block_start + input_json_delta", func(t *testing.T) {
		conv := NewAnthropicStreamConverter()

		// First tool fragment (name + empty arguments)
		events, err := conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`), "gpt-4o")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		if len(events) != 2 {
			t.Fatalf("expected message_start + content_block_start, got %d events", len(events))
		}
		var cbs map[string]interface{}
		json.Unmarshal(events[1], &cbs)
		if cbs["type"] != "content_block_start" {
			t.Fatalf("event type = %v, want content_block_start", cbs["type"])
		}
		cb := cbs["content_block"].(map[string]interface{})
		if cb["type"] != "tool_use" || cb["name"] != "get_weather" || cb["id"] != "call_1" {
			t.Errorf("content_block = %v, want tool_use get_weather", cb)
		}

		// Second fragment: incremental arguments
		events, err = conv.Convert([]byte(`{"id":"cmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":\"Tok"}}]},"finish_reason":null}]}`), "gpt-4o")
		if err != nil {
			t.Fatalf("conversion failed: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected 1 input_json_delta, got %d events", len(events))
		}
		var ijd map[string]interface{}
		json.Unmarshal(events[0], &ijd)
		if ijd["type"] != "content_block_delta" {
			t.Fatalf("event type = %v, want content_block_delta", ijd["type"])
		}
		d := ijd["delta"].(map[string]interface{})
		if d["type"] != "input_json_delta" {
			t.Fatalf("delta type = %v, want input_json_delta", d["type"])
		}
		if d["partial_json"] != `{"city":"Tok` {
			t.Errorf("partial_json = %v", d["partial_json"])
		}
	})
}

// TestOpenAIStreamConverterThinking verifies Anthropic thinking_delta events
// are mapped to OpenAI reasoning_content so OpenAI-format clients (opencode)
// can render the reasoning chain through a cross-format route.
func TestOpenAIStreamConverterThinking(t *testing.T) {
	conv := NewOpenAIStreamConverter()
	conv.SetModel("deepseek-v4-flash")

	// content_block_start with a thinking block ? assistant-role chunk
	start := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"step 1"}}`)
	chunks, err := conv.Convert(start)
	if err != nil && err != ErrSkipChunk {
		t.Fatalf("start convert: %v", err)
	}
	// thinking_delta ? reasoning_content chunk
	delta := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"because X"}}`)
	chunks, err = conv.Convert(delta)
	if err != nil {
		t.Fatalf("delta convert: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("no chunks produced for thinking_delta")
	}
	var chunk struct {
		Choices []struct {
			Delta struct {
				ReasoningContent string `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(chunks[0], &chunk); err != nil {
		t.Fatalf("bad chunk json: %v", err)
	}
	if len(chunk.Choices) == 0 || chunk.Choices[0].Delta.ReasoningContent != "because X" {
		t.Fatalf("reasoning_content not mapped: %s", chunks[0])
	}
}

func TestOpenAIRequestToAnthropicSystemOnly(t *testing.T) {
	// A system-only chat request must still produce an Anthropic messages
	// array (regression: it used to be omitted, 400ing the upstream).
	out, err := OpenAIRequestToAnthropic([]byte(`{"model":"m","messages":[{"role":"system","content":"You are helpful."}]}`), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	if req["system"] != "You are helpful." {
		t.Errorf("system = %v", req["system"])
	}
	msgs, ok := req["messages"].([]interface{})
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages = %v, want 1 minimal user turn", req["messages"])
	}
	m := msgs[0].(map[string]interface{})
	if m["role"] != "user" {
		t.Errorf("message role = %v, want user", m["role"])
	}
}

func TestMergeContentPartsExplicitNull(t *testing.T) {
	// An explicit null content on the second message must not wipe the
	// first message's accumulated content.
	got := mergeContentParts("keep me", true, nil, true)
	if got != "keep me" {
		t.Errorf("mergeContentParts(null) = %v, want accumulated text preserved", got)
	}
	got = mergeContentParts(nil, true, "other", true)
	if got != "other" {
		t.Errorf("mergeContentParts(nil, other) = %v", got)
	}
}

func TestAnthropicStreamConverterDelayedToolID(t *testing.T) {
	// A gateway that delays the tool id/name to a later fragment must not
	// produce a content_block_start with null id/name (regression: the
	// start used to be emitted from the first fragment only).
	conv := NewAnthropicStreamConverter()
	evs, err := conv.Convert([]byte(`{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":"{\"a\":1}"}}]}}]}`), "m")
	if err != nil && err != ErrSkipChunk {
		t.Fatal(err)
	}
	// start must NOT be emitted yet
	for _, ev := range evs {
		var e map[string]interface{}
		json.Unmarshal(ev, &e)
		if e["type"] == "content_block_start" {
			t.Fatalf("content_block_start emitted before id/name known: %s", ev)
		}
	}
	// second fragment carries id + name; arguments continue
	evs, err = conv.Convert([]byte(`{"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_weather","arguments":"}"}}]}}]}`), "m")
	if err != nil {
		t.Fatal(err)
	}
	// event order must be: content_block_start (with id/name) → buffered
	// first-fragment delta {"a":1} → current fragment delta "}"
	var gotStart bool
	var deltaTexts []string
	for _, ev := range evs {
		var e map[string]interface{}
		json.Unmarshal(ev, &e)
		switch e["type"] {
		case "content_block_start":
			gotStart = true
			cb := e["content_block"].(map[string]interface{})
			if cb["id"] != "call_1" || cb["name"] != "get_weather" {
				t.Fatalf("content_block_start = %v, want id call_1 / name get_weather", cb)
			}
		case "content_block_delta":
			if !gotStart {
				t.Fatalf("content_block_delta emitted before content_block_start: %s", ev)
			}
			d := e["delta"].(map[string]interface{})
			if d["type"] != "input_json_delta" {
				t.Fatalf("delta = %v", d)
			}
			deltaTexts = append(deltaTexts, d["partial_json"].(string))
		}
	}
	if !gotStart {
		t.Fatalf("no content_block_start in events: %v", evs)
	}
	// both the buffered first-fragment arguments and the follow-up must be
	// replayed in order after the start
	if len(deltaTexts) != 2 || deltaTexts[0] != `{"a":1}` || deltaTexts[1] != "}" {
		t.Fatalf("delta texts = %v, want [{\"a\":1} }] (buffered args replayed in order)", deltaTexts)
	}
}

func TestAnthropicStreamConverterDeferredStartsAscendingOrder(t *testing.T) {
	// Two tools whose id/name arrive late must have their
	// content_block_start events flushed in ascending block-index order,
	// even when the fragments arrive in a different order.
	conv := NewAnthropicStreamConverter()
	// tool index 1's first fragment arrives first → block index 0
	_, err := conv.Convert([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":1,"type":"function","function":{"arguments":"{"}}]}}]}`), "m")
	if err != nil && err != ErrSkipChunk {
		t.Fatal(err)
	}
	// tool index 0's first fragment → block index 1
	_, err = conv.Convert([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":"{"}}]}}]}`), "m")
	if err != nil && err != ErrSkipChunk {
		t.Fatal(err)
	}
	// both resolve in array order [0,1] — starts must still be block 0 then block 1
	evs, err := conv.Convert([]byte(`{"choices":[{"delta":{"tool_calls":[
		{"index":0,"id":"call_0","function":{"name":"f0","arguments":"}"}},
		{"index":1,"id":"call_1","function":{"name":"f1","arguments":"}"}}
	]}}]}`), "m")
	if err != nil {
		t.Fatal(err)
	}
	var starts []int
	for _, ev := range evs {
		var e map[string]interface{}
		json.Unmarshal(ev, &e)
		if e["type"] == "content_block_start" {
			starts = append(starts, int(e["index"].(float64)))
		}
	}
	if len(starts) != 2 || starts[0] != 0 || starts[1] != 1 {
		t.Fatalf("start order = %v, want [0 1] (ascending block indexes)", starts)
	}
}

func TestAnthropicStreamConverterNamelessToolDropped(t *testing.T) {
	// A tool whose name never arrives must be dropped at stream end — no
	// malformed content_block_start, no orphan content_block_stop.
	conv := NewAnthropicStreamConverter()
	_, err := conv.Convert([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"arguments":"{\"a\":1}"}}]}}]}`), "m")
	if err != nil && err != ErrSkipChunk {
		t.Fatal(err)
	}
	evs := conv.CloseStream()
	for _, ev := range evs {
		var e map[string]interface{}
		json.Unmarshal(ev, &e)
		switch e["type"] {
		case "content_block_start":
			t.Fatalf("nameless tool emitted a start: %s", ev)
		case "content_block_stop":
			t.Fatalf("orphan content_block_stop for dropped tool: %s", ev)
		}
	}
}

func TestAnthropicStreamConverterHoldsCompleteToolWhileEarlierPending(t *testing.T) {
	// A fully-formed tool must not emit its start while an EARLIER tool
	// (lower block index) is still deferred — all starts flush together in
	// ascending block order.
	conv := NewAnthropicStreamConverter()
	// tool 0 first fragment: args only → deferred, block index 0
	_, err := conv.Convert([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":"{"}}]}}]}`), "m")
	if err != nil && err != ErrSkipChunk {
		t.Fatal(err)
	}
	// tool 1 fully formed → must be HELD, not emitted (block index 1)
	evs, err := conv.Convert([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_1","function":{"name":"f1","arguments":"{\"b\":2}"}}]}}]}`), "m")
	if err != nil && err != ErrSkipChunk {
		t.Fatal(err)
	}
	for _, ev := range evs {
		var e map[string]interface{}
		json.Unmarshal(ev, &e)
		if e["type"] == "content_block_start" {
			t.Fatalf("start emitted while earlier tool still pending: %s", ev)
		}
	}
	// tool 0 resolves → both flush in block order [0, 1]
	evs, err = conv.Convert([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_0","function":{"name":"f0","arguments":"}"}}]}}]}`), "m")
	if err != nil {
		t.Fatal(err)
	}
	var starts []int
	for _, ev := range evs {
		var e map[string]interface{}
		json.Unmarshal(ev, &e)
		if e["type"] == "content_block_start" {
			starts = append(starts, int(e["index"].(float64)))
		}
	}
	if len(starts) != 2 || starts[0] != 0 || starts[1] != 1 {
		t.Fatalf("start order = %v, want [0 1]", starts)
	}
}

func TestAnthropicStreamConverterTextHeldForAscendingOrder(t *testing.T) {
	// Text deltas arriving while a lower-indexed tool start is deferred
	// must be held — emitting the text start early would skip a block
	// index (strict Anthropic validators require ascending order).
	conv := NewAnthropicStreamConverter()
	// tool 0: args only → deferred, block index 0
	_, err := conv.Convert([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":"{"}}]}}]}`), "m")
	if err != nil && err != ErrSkipChunk {
		t.Fatal(err)
	}
	// text delta → block index 1, must be held (no start, no delta yet)
	evs, err := conv.Convert([]byte(`{"choices":[{"delta":{"content":"hello"}}]}`), "m")
	if err != nil && err != ErrSkipChunk {
		t.Fatal(err)
	}
	for _, ev := range evs {
		var e map[string]interface{}
		json.Unmarshal(ev, &e)
		if e["type"] == "content_block_start" || e["type"] == "content_block_delta" {
			t.Fatalf("text events emitted while tool start pending: %s", ev)
		}
	}
	// tool 0 resolves → tool start(0), text start(1), text delta, tool delta
	evs, err = conv.Convert([]byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_0","function":{"name":"f0","arguments":"}"}}]}}]}`), "m")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, ev := range evs {
		var e map[string]interface{}
		json.Unmarshal(ev, &e)
		switch e["type"] {
		case "content_block_start":
			cb := e["content_block"].(map[string]interface{})
			order = append(order, "start:"+cb["type"].(string)+"@"+fmt.Sprintf("%v", e["index"]))
		case "content_block_delta":
			order = append(order, "delta@"+fmt.Sprintf("%v", e["index"]))
		}
	}
	// starts must be ascending; each block's deltas follow its own start
	// (the tool's buffered delta precedes the text start, which is valid —
	// deltas may interleave across blocks, starts may not)
	want := []string{"start:tool_use@0", "delta@0", "start:text@1", "delta@1", "delta@0"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("event order = %v, want %v", order, want)
	}
}
