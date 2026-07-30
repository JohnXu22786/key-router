package format

import (
	"encoding/json"
	"errors"
	"strings"
)

var (
	ErrUnsupportedFormat = errors.New("unsupported format")
	ErrSkipChunk         = errors.New("skip chunk")
	ErrNotDeltaChunk     = errors.New("not a delta chunk")
)

// DetectFormat determines the request format from the URL path
func DetectFormat(path string) string {
	if idx := strings.IndexByte(path, '?'); idx >= 0 {
		path = path[:idx]
	}
	switch {
	case strings.HasSuffix(path, "/v1/messages"):
		return "anthropic"
	default:
		return "openai"
	}
}

// NeedConvert determines if format conversion is needed
func NeedConvert(inputFormat, targetFormat string) bool {
	return inputFormat != targetFormat
}

// safeMap safely converts an interface{} to map[string]interface{}
func safeMap(v interface{}) (map[string]interface{}, bool) {
	m, ok := v.(map[string]interface{})
	return m, ok
}

// safeString extracts a string value from a map with safe type check
func safeString(m map[string]interface{}, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// safeStringArr extracts []interface{} from a map
func safeArr(m map[string]interface{}, key string) ([]interface{}, bool) {
	v, ok := m[key]
	if !ok {
		return nil, false
	}
	a, ok := v.([]interface{})
	return a, ok
}

// safeStringOrDefault extracts a string with a default fallback
func safeStringOrDefault(m map[string]interface{}, key string, defaultVal string) string {
	if s, ok := safeString(m, key); ok {
		return s
	}
	return defaultVal
}

// OpenAIRequestToAnthropic converts OpenAI format to Anthropic format
func OpenAIRequestToAnthropic(body []byte, modelOverride string) ([]byte, error) {
	var oaiReq map[string]interface{}
	if err := json.Unmarshal(body, &oaiReq); err != nil {
		return nil, err
	}

	anthReq := make(map[string]interface{})

	if modelOverride != "" {
		anthReq["model"] = modelOverride
	} else if m, ok := oaiReq["model"]; ok {
		anthReq["model"] = m
	}

	// Copy known fields
	copyFields := []string{"max_tokens", "stream", "stop_sequences", "temperature", "top_p", "metadata"}
	for _, f := range copyFields {
		if v, ok := oaiReq[f]; ok {
			anthReq[f] = v
		}
	}

	// Convert messages
	if msgs, ok := safeArr(oaiReq, "messages"); ok {
		var systemContent string
		var anthMessages []interface{}

		for _, msg := range msgs {
			m, ok := safeMap(msg)
			if !ok {
				continue
			}
			role := safeStringOrDefault(m, "role", "")

			switch role {
			case "system":
				if content, ok := safeString(m, "content"); ok {
					systemContent = content
				}
			case "user":
				anthMessages = append(anthMessages, convertOpenAIUserMessage(m))
			case "assistant":
				anthMessages = append(anthMessages, convertOpenAIAssistantMessage(m))
			case "tool":
				anthMessages = append(anthMessages, convertOpenAIToolMessage(m))
			}
		}

		if systemContent != "" {
			anthReq["system"] = systemContent
		}
		if len(anthMessages) > 0 {
			anthReq["messages"] = anthMessages
		}
	}

	// Tools
	if tools, ok := safeArr(oaiReq, "tools"); ok {
		anthReq["tools"] = convertOpenAITools(tools)
	}

	return json.Marshal(anthReq)
}

// AnthropicRequestToOpenAI converts Anthropic format to OpenAI format
func AnthropicRequestToOpenAI(body []byte, modelOverride string) ([]byte, error) {
	var anthReq map[string]interface{}
	if err := json.Unmarshal(body, &anthReq); err != nil {
		return nil, err
	}

	oaiReq := make(map[string]interface{})

	if modelOverride != "" {
		oaiReq["model"] = modelOverride
	} else if m, ok := anthReq["model"]; ok {
		oaiReq["model"] = m
	}

	if v, ok := anthReq["max_tokens"]; ok {
		oaiReq["max_tokens"] = v
	}
	if v, ok := anthReq["stream"]; ok {
		oaiReq["stream"] = v
	}
	if v, ok := anthReq["stop_sequences"]; ok {
		oaiReq["stop"] = v
	}
	if v, ok := anthReq["temperature"]; ok {
		oaiReq["temperature"] = v
	}
	if v, ok := anthReq["top_p"]; ok {
		oaiReq["top_p"] = v
	}

	var oaiMessages []interface{}

	// System prompt becomes first message
	if system, ok := safeString(anthReq, "system"); ok && system != "" {
		oaiMessages = append(oaiMessages, map[string]interface{}{
			"role":    "system",
			"content": system,
		})
	}

	if msgs, ok := safeArr(anthReq, "messages"); ok {
		for _, msg := range msgs {
			m, ok := safeMap(msg)
			if !ok {
				continue
			}
			role := safeStringOrDefault(m, "role", "")
			switch role {
			case "user":
				oaiMessages = append(oaiMessages, convertAnthropicUserMessage(m))
			case "assistant":
				oaiMessages = append(oaiMessages, convertAnthropicAssistantMessage(m))
			}
		}
	}

	if len(oaiMessages) > 0 {
		oaiReq["messages"] = oaiMessages
	}

	if tools, ok := safeArr(anthReq, "tools"); ok {
		oaiReq["tools"] = convertAnthropicTools(tools)
	}

	return json.Marshal(oaiReq)
}

// OpenAIStreamChunkToAnthropic converts a single OpenAI stream chunk to Anthropic format
func OpenAIStreamChunkToAnthropic(chunk []byte) ([]byte, error) {
	var oai map[string]interface{}
	if err := json.Unmarshal(chunk, &oai); err != nil {
		return nil, err
	}

	choices, ok := safeArr(oai, "choices")
	if !ok || len(choices) == 0 {
		return nil, ErrSkipChunk
	}

	choice, ok := safeMap(choices[0])
	if !ok {
		return nil, ErrSkipChunk
	}

	delta, ok := safeMap(choice["delta"])
	if !ok {
		return nil, ErrSkipChunk
	}

	finishReason := safeStringOrDefault(choice, "finish_reason", "")

	// Handle finish reasons: "stop" → message_stop, "length" → also message_stop
	if finishReason == "stop" || finishReason == "length" {
		return json.Marshal(map[string]interface{}{
			"type": "message_stop",
		})
	}

	// Content delta
	if content, ok := safeString(delta, "content"); ok && content != "" {
		return json.Marshal(map[string]interface{}{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]interface{}{
				"type": "text_delta",
				"text": content,
			},
		})
	}

	return nil, ErrSkipChunk
}

// AnthropicStreamEventToOpenAI converts an Anthropic stream event to OpenAI format
func AnthropicStreamEventToOpenAI(event []byte) ([]byte, error) {
	var anth map[string]interface{}
	if err := json.Unmarshal(event, &anth); err != nil {
		return nil, err
	}

	eventType := safeStringOrDefault(anth, "type", "")

	switch eventType {
	case "content_block_delta":
		delta, ok := safeMap(anth["delta"])
		if !ok {
			return nil, ErrSkipChunk
		}
		if text, ok := safeString(delta, "text"); ok {
			return json.Marshal(openAIStreamChunk(text, ""))
		}

	case "message_delta":
		// Contains stop_reason and usage info
		delta, ok := safeMap(anth["delta"])
		if !ok {
			return nil, ErrSkipChunk
		}
		stopReason := safeStringOrDefault(delta, "stop_reason", "")
		switch stopReason {
		case "end_turn":
			return json.Marshal(openAIStreamChunk("", "stop"))
		case "max_tokens":
			return json.Marshal(openAIStreamChunk("", "length"))
		default:
			return nil, ErrSkipChunk
		}

	case "message_stop":
		return json.Marshal(openAIStreamChunk("", "stop"))

	case "content_block_start", "content_block_stop", "message_start", "ping":
		return nil, ErrSkipChunk
	}

	return nil, ErrSkipChunk
}

// Internal helpers

func openAIStreamChunk(content, finishReason string) map[string]interface{} {
	return map[string]interface{}{
		"id":      "chatcmpl-local",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "",
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"content": content,
					"role":    "assistant",
				},
				"finish_reason": finishReason,
			},
		},
	}
}

func convertOpenAIUserMessage(m map[string]interface{}) map[string]interface{} {
	content := m["content"]
	result := map[string]interface{}{
		"role": "user",
	}

	switch c := content.(type) {
	case string:
		result["content"] = []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": c,
			},
		}
	case []interface{}:
		result["content"] = convertOpenAIContentArray(c)
	default:
		result["content"] = content
	}

	return result
}

func convertOpenAIContentArray(content []interface{}) []interface{} {
	var anthContent []interface{}
	for _, part := range content {
		p, ok := safeMap(part)
		if !ok {
			continue
		}
		pType := safeStringOrDefault(p, "type", "")

		switch pType {
		case "text":
			anthContent = append(anthContent, map[string]interface{}{
				"type": "text",
				"text": p["text"],
			})
		case "image_url":
			imageURL, ok := safeMap(p["image_url"])
			if !ok {
				continue
			}
			url := safeStringOrDefault(imageURL, "url", "")
			anthContent = append(anthContent, map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": "image/jpeg",
					"data":       url,
				},
			})
		}
	}
	return anthContent
}

func convertOpenAIAssistantMessage(m map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{
		"role": "assistant",
	}

	content := safeStringOrDefault(m, "content", "")
	var anthContent []interface{}
	if content != "" {
		anthContent = append(anthContent, map[string]interface{}{
			"type": "text",
			"text": content,
		})
	}

	if toolCalls, ok := safeArr(m, "tool_calls"); ok {
		for _, tc := range toolCalls {
			tcMap, ok := safeMap(tc)
			if !ok {
				continue
			}
			fn, ok := safeMap(tcMap["function"])
			if !ok {
				continue
			}
			anthContent = append(anthContent, map[string]interface{}{
				"type":  "tool_use",
				"id":    tcMap["id"],
				"name":  fn["name"],
				"input": fn["arguments"],
			})
		}
	}

	result["content"] = anthContent
	return result
}

func convertOpenAIToolMessage(m map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"role": "user",
		"content": []interface{}{
			map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": m["tool_call_id"],
				"content":     m["content"],
			},
		},
	}
}

func convertOpenAITools(tools []interface{}) []interface{} {
	var anthTools []interface{}
	for _, tool := range tools {
		t, ok := safeMap(tool)
		if !ok {
			continue
		}
		fn, ok := safeMap(t["function"])
		if !ok {
			continue
		}
		anthTools = append(anthTools, map[string]interface{}{
			"name":         fn["name"],
			"description":  fn["description"],
			"input_schema": fn["parameters"],
			"type":         "custom",
		})
	}
	return anthTools
}

func convertAnthropicUserMessage(m map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{
		"role": "user",
	}

	content := m["content"]
	switch c := content.(type) {
	case string:
		result["content"] = c
	case []interface{}:
		result["content"] = convertAnthropicContentArray(c)
	default:
		result["content"] = content
	}

	return result
}

func convertAnthropicContentArray(content []interface{}) []interface{} {
	var oaiContent []interface{}
	for _, part := range content {
		p, ok := safeMap(part)
		if !ok {
			continue
		}
		pType := safeStringOrDefault(p, "type", "")

		switch pType {
		case "text":
			oaiContent = append(oaiContent, map[string]interface{}{
				"type": "text",
				"text": p["text"],
			})
		case "image":
			source, ok := safeMap(p["source"])
			if ok {
				oaiContent = append(oaiContent, map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]interface{}{
						"url": source["data"],
					},
				})
			}
		case "tool_result":
			oaiContent = append(oaiContent, map[string]interface{}{
				"type":         "tool_result",
				"tool_call_id": p["tool_use_id"],
				"content":      p["content"],
			})
		}
	}
	return oaiContent
}

func convertAnthropicAssistantMessage(m map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{
		"role": "assistant",
	}

	content, ok := safeArr(m, "content")
	if !ok {
		return result
	}

	var textParts []string
	var toolCalls []interface{}

	for _, part := range content {
		p, ok := safeMap(part)
		if !ok {
			continue
		}
		pType := safeStringOrDefault(p, "type", "")

		switch pType {
		case "text":
			if t, ok := safeString(p, "text"); ok {
				textParts = append(textParts, t)
			}
		case "tool_use":
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   p["id"],
				"type": "function",
				"function": map[string]interface{}{
					"name":      p["name"],
					"arguments": p["input"],
				},
			})
		}
	}

	result["content"] = strings.Join(textParts, "")
	if len(toolCalls) > 0 {
		result["tool_calls"] = toolCalls
	}

	return result
}

func convertAnthropicTools(tools []interface{}) []interface{} {
	var oaiTools []interface{}
	for _, tool := range tools {
		t, ok := safeMap(tool)
		if !ok {
			continue
		}
		oaiTools = append(oaiTools, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        t["name"],
				"description": t["description"],
				"parameters":  t["input_schema"],
			},
		})
	}
	return oaiTools
}
