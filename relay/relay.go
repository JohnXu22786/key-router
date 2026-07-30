package relay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"local-router/format"
	"local-router/model"
)

// ForwardRequest forwards an API request to the upstream provider
// Returns the response with proper streaming support
func ForwardRequest(meta *model.RequestMetadata, key *model.Key, provider *model.Provider) (*http.Response, error) {
	// Build upstream URL
	upstreamURL := strings.TrimRight(provider.BaseURL, "/") + meta.RequestPath

	// Determine target format
	targetFormat := provider.Type

	// Convert body format if needed
	var bodyToSend []byte
	var err error
	if format.NeedConvert(meta.Format, targetFormat) {
		if meta.Format == "openai" && targetFormat == "anthropic" {
			bodyToSend, err = format.OpenAIRequestToAnthropic(meta.RequestBody, meta.TargetModel)
		} else if meta.Format == "anthropic" && targetFormat == "openai" {
			bodyToSend, err = format.AnthropicRequestToOpenAI(meta.RequestBody, meta.TargetModel)
		} else {
			return nil, fmt.Errorf("unsupported format conversion: %s -> %s", meta.Format, targetFormat)
		}
		if err != nil {
			return nil, fmt.Errorf("format conversion error: %w", err)
		}
	} else {
		// Same format, just possibly replace model name
		bodyToSend, err = replaceModelName(meta.RequestBody, meta.TargetModel)
		if err != nil {
			return nil, fmt.Errorf("model replacement error: %w", err)
		}
	}

	// Create upstream request
	req, err := http.NewRequest("POST", upstreamURL, bytes.NewReader(bodyToSend))
	if err != nil {
		return nil, err
	}

	// Set headers (discard incoming headers, use provider type)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Set auth based on provider type
	if targetFormat == "anthropic" {
		req.Header.Set("x-api-key", key.KeyValue)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+key.KeyValue)
	}

	// Apply extra headers from provider config
	if provider.ExtraHeaders != "" {
		var extraHeaders map[string]string
		if err := json.Unmarshal([]byte(provider.ExtraHeaders), &extraHeaders); err == nil {
			for k, v := range extraHeaders {
				req.Header.Set(k, v)
			}
		}
	}

	// Use a timeout client
	client := &http.Client{
		Timeout: 300 * time.Second,
	}

	// For streaming, don't use timeout on the client level
	if meta.Stream {
		client.Timeout = 0 // No timeout for streaming
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream request failed: %w", err)
	}

	return resp, nil
}

// StreamResponse streams an SSE response from upstream to the client response writer.
// Returns captured token usage if available from the stream end events.
func StreamResponse(w http.ResponseWriter, resp *http.Response, inputFormat, targetFormat string) (*model.TokenUsage, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}

	usage := &model.TokenUsage{}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*64), 1024*1024) // 64KB initial, 1MB max line

	for scanner.Scan() {
		line := scanner.Text()

		// Forward SSE non-data lines
		if !strings.HasPrefix(line, "data: ") {
			_, err := fmt.Fprintf(w, "%s\n", line)
			if err != nil {
				resp.Body.Close()
				return usage, err
			}
			flusher.Flush()
			continue
		}

		// Handle "[DONE]" message
		if strings.TrimSpace(line) == "data: [DONE]" {
			_, err := fmt.Fprintf(w, "%s\n", line)
			if err != nil {
				resp.Body.Close()
				return usage, err
			}
			flusher.Flush()
			continue
		}

		// Extract JSON data
		jsonStr := strings.TrimPrefix(line, "data: ")

		// Try to extract token usage from stream events
		extractStreamUsage([]byte(jsonStr), inputFormat, targetFormat, usage)

		// Convert format if needed
		var converted []byte
		var err error

		if format.NeedConvert(targetFormat, inputFormat) {
			if inputFormat == "openai" && targetFormat == "anthropic" {
				converted, err = format.AnthropicStreamEventToOpenAI([]byte(jsonStr))
			} else if inputFormat == "anthropic" && targetFormat == "openai" {
				converted, err = format.OpenAIStreamChunkToAnthropic([]byte(jsonStr))
			} else {
				converted = []byte(jsonStr)
			}
		} else {
			converted = []byte(jsonStr)
		}

		if err != nil {
			if err == format.ErrSkipChunk {
				continue
			}
			continue
		}

		// Write converted chunk to client
		out := append([]byte("data: "), converted...)
		out = append(out, '\n', '\n')
		_, err = w.Write(out)
		if err != nil {
			resp.Body.Close()
			return usage, err
		}
		flusher.Flush()
	}

	return usage, scanner.Err()
}

// extractStreamUsage tries to parse token usage from streaming events
func extractStreamUsage(data []byte, inputFormat, targetFormat string, usage *model.TokenUsage) {
	// OpenAI final chunk may include usage
	if inputFormat == "openai" || targetFormat == "openai" {
		var chunk struct {
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
				TotalTokens      int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(data, &chunk); err == nil && chunk.Usage != nil {
			usage.PromptTokens = chunk.Usage.PromptTokens
			usage.CompletionTokens = chunk.Usage.CompletionTokens
			usage.TotalTokens = chunk.Usage.TotalTokens
		}
	}

	// Anthropic message_delta includes usage
	if inputFormat == "anthropic" || targetFormat == "anthropic" {
		var event struct {
			Type  string `json:"type"`
			Usage *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(data, &event); err == nil && event.Type == "message_delta" && event.Usage != nil {
			usage.PromptTokens = event.Usage.InputTokens
			usage.CompletionTokens = event.Usage.OutputTokens
			usage.TotalTokens = event.Usage.InputTokens + event.Usage.OutputTokens
		}
	}
}

// WriteStreamError sends an error message to the downstream client in stream format (JSON-safe)
func WriteStreamError(w http.ResponseWriter, inputFormat string, errMsg string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	var errPayload []byte
	if inputFormat == "openai" {
		errPayload, _ = json.Marshal(map[string]interface{}{
			"error": map[string]string{
				"message": errMsg,
				"type":    "stream_error",
			},
		})
	} else {
		errPayload, _ = json.Marshal(map[string]interface{}{
			"type": "error",
			"error": map[string]string{
				"message": errMsg,
			},
		})
	}
	// Best-effort write; client may already be disconnected
	fmt.Fprintf(w, "data: %s\n\n", string(errPayload))
	flusher.Flush()
}

// replaceModelName replaces the "model" field in a JSON request body if targetModel is set
func replaceModelName(body []byte, targetModel string) ([]byte, error) {
	if targetModel == "" {
		return body, nil
	}

	var req map[string]interface{}
	if err := json.Unmarshal(body, &req); err != nil {
		return body, nil // Return original on parse error
	}

	req["model"] = targetModel
	return json.Marshal(req)
}

// ConvertAnthropicResponseToOpenAI converts an Anthropic response to OpenAI format
func ConvertAnthropicResponseToOpenAI(body []byte, model string) ([]byte, error) {
	var anthResp map[string]interface{}
	if err := json.Unmarshal(body, &anthResp); err != nil {
		return body, nil
	}

	oaiResp := map[string]interface{}{
		"id":      anthResp["id"],
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []interface{}{
			map[string]interface{}{
				"index":         0,
				"finish_reason": "stop",
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": extractAnthropicContent(anthResp),
				},
			},
		},
		"usage": extractAnthropicUsage(anthResp),
	}

	return json.Marshal(oaiResp)
}

// ConvertOpenAIResponseToAnthropic converts an OpenAI response to Anthropic format
func ConvertOpenAIResponseToAnthropic(body []byte) ([]byte, error) {
	var oaiResp map[string]interface{}
	if err := json.Unmarshal(body, &oaiResp); err != nil {
		return body, nil
	}

	usage, usageOk := oaiResp["usage"].(map[string]interface{})
	if !usageOk {
		usage = nil
	}
	anthResp := map[string]interface{}{
		"id":          oaiResp["id"],
		"type":        "message",
		"role":        "assistant",
		"content":     []interface{}{},
		"model":       oaiResp["model"],
		"stop_reason": "end_turn",
	}
	if usage != nil {
		anthResp["usage"] = map[string]interface{}{
			"input_tokens":  usage["prompt_tokens"],
			"output_tokens": usage["completion_tokens"],
		}
	}

	choices, ok := oaiResp["choices"].([]interface{})
	if ok && len(choices) > 0 {
		choice, ok := choices[0].(map[string]interface{})
		if !ok {
			return json.Marshal(anthResp)
		}
		msg, _ := choice["message"].(map[string]interface{})
		if content, ok := msg["content"].(string); ok && content != "" {
			anthResp["content"] = []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": content,
				},
			}
		}
		if reason, ok := choice["finish_reason"].(string); ok {
			switch reason {
			case "stop":
				anthResp["stop_reason"] = "end_turn"
			case "length":
				anthResp["stop_reason"] = "max_tokens"
			}
		}
	}

	return json.Marshal(anthResp)
}

// extractAnthropicUsage safely extracts usage info from an Anthropic response
func extractAnthropicUsage(anthResp map[string]interface{}) map[string]interface{} {
	usageRaw, ok := anthResp["usage"]
	if !ok {
		return map[string]interface{}{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		}
	}
	usage, ok := usageRaw.(map[string]interface{})
	if !ok {
		return map[string]interface{}{
			"prompt_tokens":     0,
			"completion_tokens": 0,
			"total_tokens":      0,
		}
	}

	inputTokens := toFloat64(usage["input_tokens"])
	outputTokens := toFloat64(usage["output_tokens"])
	return map[string]interface{}{
		"input_tokens":     inputTokens,
		"output_tokens":    outputTokens,
		"prompt_tokens":     inputTokens,
		"completion_tokens": outputTokens,
		"total_tokens":      inputTokens + outputTokens,
	}
}

// toFloat64 safely converts interface{} to float64
func toFloat64(v interface{}) float64 {
	if v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

// extractAnthropicContent extracts text content from an Anthropic response
func extractAnthropicContent(anthResp map[string]interface{}) string {
	content, ok := anthResp["content"].([]interface{})
	if !ok {
		return ""
	}
	var texts []string
	for _, c := range content {
		if block, ok := c.(map[string]interface{}); ok {
			if block["type"] == "text" {
				if t, ok := block["text"].(string); ok {
					texts = append(texts, t)
				}
			}
		}
	}
	return strings.Join(texts, "")
}

// parseTokenUsage extracts token usage from a response body
func ParseTokenUsage(body []byte, format string) *model.TokenUsage {
	usage := &model.TokenUsage{}

	if format == "openai" {
		var resp struct {
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
				TotalTokens      int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &resp); err == nil && resp.Usage != nil {
			usage.PromptTokens = resp.Usage.PromptTokens
			usage.CompletionTokens = resp.Usage.CompletionTokens
			usage.TotalTokens = resp.Usage.TotalTokens
		}
	} else if format == "anthropic" {
		var resp struct {
			Usage *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &resp); err == nil && resp.Usage != nil {
			usage.PromptTokens = resp.Usage.InputTokens
			usage.CompletionTokens = resp.Usage.OutputTokens
			usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens
		}
	}

	return usage
}
