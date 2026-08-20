package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Response-object converters for the client-facing OpenAI Responses API
// (POST /v1/responses). They turn a full upstream response body into a
// Responses API response object when a /v1/responses request was routed to
// an upstream that doesn't speak it natively (chat completions fallback, or
// an Anthropic provider).

// ChatCompletionResponseToResponses converts an OpenAI chat completions
// response body into an OpenAI Responses API response object.
func ChatCompletionResponseToResponses(body []byte, modelName string) ([]byte, error) {
	var chat struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Created int64  `json:"created"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      *struct {
				Content          interface{}   `json:"content"`
				ToolCalls        []interface{} `json:"tool_calls"`
				ReasoningContent interface{}   `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
			PromptDetails    *struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &chat); err != nil {
		return nil, fmt.Errorf("invalid chat completions response body: %w", err)
	}

	resp := responsesBaseObject(responsesID("resp", chat.ID), time.Now().Unix(), chat.Model, modelName)
	resp["status"] = "completed"
	resp["error"] = nil

	var output []interface{}
	if len(chat.Choices) > 0 {
		ch := chat.Choices[0]
		// finish_reason â†’ response status: "length" means the model hit the
		// max-output-token cap (incomplete), content_filter is a refusal.
		switch ch.FinishReason {
		case "length":
			resp["status"] = "incomplete"
			resp["incomplete_details"] = map[string]interface{}{"reason": "max_output_tokens"}
		case "content_filter":
			resp["status"] = "failed"
			resp["error"] = map[string]interface{}{
				"code":    "content_filter",
				"message": "The model produced content that was filtered",
				"type":    "server_error",
			}
		}

		if msg := ch.Message; msg != nil {
			var textParts []interface{}
			// text content: string, or an array of text parts (some
			// OpenAI-compatible gateways)
			if s, ok := msg.Content.(string); ok && s != "" {
				textParts = append(textParts, responsesTextPart(s))
			} else if arr, ok := msg.Content.([]interface{}); ok {
				for _, part := range arr {
					if p, ok := part.(map[string]interface{}); ok && p["type"] == "text" {
						if t, ok := p["text"].(string); ok && t != "" {
							textParts = append(textParts, responsesTextPart(t))
						}
					}
				}
			}
			if len(textParts) > 0 {
				output = append(output, map[string]interface{}{
					"id":      responsesID("msg", chat.ID),
					"type":    "message",
					"status":  "completed",
					"role":    "assistant",
					"content": textParts,
				})
			}

			// tool_calls â†’ function_call output items (id doubles as the
			// call_id clients must echo back in function_call_output)
			for _, tc := range msg.ToolCalls {
				tcMap, ok := tc.(map[string]interface{})
				if !ok {
					continue
				}
				fn, _ := tcMap["function"].(map[string]interface{})
				callID, _ := tcMap["id"].(string)
				if callID == "" {
					callID = fmt.Sprintf("call_%d", len(output)+1)
				}
				args, _ := fn["arguments"].(string)
				name, _ := fn["name"].(string)
				output = append(output, map[string]interface{}{
					"id":        callID,
					"type":      "function_call",
					"status":    "completed",
					"call_id":   callID,
					"name":      name,
					"arguments": args,
				})
			}

			// reasoning_content â†’ reasoning output item (DeepSeek-style
			// thinking survives the conversion)
			if rc, ok := msg.ReasoningContent.(string); ok && rc != "" {
				output = append(output, map[string]interface{}{
					"id":     responsesID("rs", chat.ID),
					"type":   "reasoning",
					"status": "completed",
					"summary": []interface{}{map[string]interface{}{
						"type": "summary_text",
						"text": rc,
					}},
				})
			}
		}
	}
	resp["output"] = output

	// usage: prompt_tokens â†’ input_tokens with cached tokens broken out,
	// like the Responses API reports it
	if chat.Usage != nil {
		u := chat.Usage
		cached := int64(0)
		if u.PromptDetails != nil {
			cached = u.PromptDetails.CachedTokens
		}
		// Some gateways omit total_tokens — derive it from the parts that
		// are present so the Responses object never reports a bogus 0.
		total := u.TotalTokens
		if total == 0 {
			total = u.PromptTokens + u.CompletionTokens
		}
		resp["usage"] = map[string]interface{}{
			"input_tokens": u.PromptTokens,
			"input_tokens_details": map[string]interface{}{
				"cached_tokens": cached,
			},
			"output_tokens": u.CompletionTokens,
			"output_tokens_details": map[string]interface{}{
				"reasoning_tokens": 0,
			},
			"total_tokens": total,
		}
	}

	return json.Marshal(resp)
}

// AnthropicResponseToResponses converts an Anthropic Messages response body
// into an OpenAI Responses API response object.
func AnthropicResponseToResponses(body []byte, modelName string) ([]byte, error) {
	var anth struct {
		ID         string        `json:"id"`
		Type       string        `json:"type"`
		Model      string        `json:"model"`
		StopReason string        `json:"stop_reason"`
		Content    []interface{} `json:"content"`
		Usage      *struct {
			InputTokens         int64 `json:"input_tokens"`
			OutputTokens        int64 `json:"output_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &anth); err != nil {
		return nil, fmt.Errorf("invalid anthropic response body: %w", err)
	}

	resp := responsesBaseObject(responsesID("resp", anth.ID), time.Now().Unix(), anth.Model, modelName)
	resp["status"] = "completed"
	resp["error"] = nil

	// stop_reason â†’ response status (max_tokens hit â†’ incomplete like the
	// Responses API does; a refusal â†’ failed)
	switch anth.StopReason {
	case "max_tokens", "model_context_window_exceeded":
		resp["status"] = "incomplete"
		resp["incomplete_details"] = map[string]interface{}{"reason": "max_output_tokens"}
	case "refusal":
		resp["status"] = "failed"
		resp["error"] = map[string]interface{}{
			"code":    "refusal",
			"message": "The model refused to respond",
			"type":    "server_error",
		}
	}

	// Per-item-type sequence counters: one response can hold several message
	// items (text blocks split by tool_use/thinking) and several reasoning
	// items (multiple thinking blocks). The Responses API requires unique item
	// ids — clients key output items by id and correlate response.completed
	// output with output_item events — so each item of a type stamps the
	// type's running sequence on the response seed (msg_<seed>_1, msg_<seed>_2,
	// rs_<seed>_n). Tool ids stay stable: they come from the upstream block id.
	var msgSeq, rsSeq int

	var output []interface{}
	var textParts []interface{}
	for _, c := range anth.Content {
		block, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			if t, ok := block["text"].(string); ok && t != "" {
				textParts = append(textParts, responsesTextPart(t))
			}
		case "tool_use", "thinking":
			// Flush pending text first so output items keep block order
			// (a thinking block precedes the text in Anthropic responses)
			if len(textParts) > 0 {
				msgSeq++
				output = append(output, anthMessageItem(anth.ID, msgSeq, textParts))
				textParts = nil
			}
			if block["type"] == "tool_use" {
				id, _ := block["id"].(string)
				if id == "" {
					id = fmt.Sprintf("call_%d", len(output)+1)
				}
				name, _ := block["name"].(string)
				output = append(output, map[string]interface{}{
					"id":        id,
					"type":      "function_call",
					"status":    "completed",
					"call_id":   id,
					"name":      name,
					"arguments": toJSONString(block["input"]),
				})
			} else {
				// thinking block â†’ reasoning item (summary text)
				rsSeq++
				text, _ := block["thinking"].(string)
				output = append(output, map[string]interface{}{
					"id":     responsesID("rs", fmt.Sprintf("%s_%d", anth.ID, rsSeq)),
					"type":   "reasoning",
					"status": "completed",
					"summary": []interface{}{map[string]interface{}{
						"type": "summary_text",
						"text": text,
					}},
				})
			}
		}
	}
	if len(textParts) > 0 {
		msgSeq++
		output = append(output, anthMessageItem(anth.ID, msgSeq, textParts))
	}
	resp["output"] = output

	// usage: Anthropic input_tokens EXCLUDE cache tokens; the Responses API
	// input_tokens include them (cached broken out in details) â€” match the
	// OpenAI convention like the rest of the relay does.
	if anth.Usage != nil {
		u := anth.Usage
		input := u.InputTokens + u.CacheCreationTokens + u.CacheReadTokens
		cached := u.CacheCreationTokens + u.CacheReadTokens
		resp["usage"] = map[string]interface{}{
			"input_tokens": input,
			"input_tokens_details": map[string]interface{}{
				"cached_tokens": cached,
			},
			"output_tokens": u.OutputTokens,
			"output_tokens_details": map[string]interface{}{
				"reasoning_tokens": 0,
			},
			"total_tokens": input + u.OutputTokens,
		}
	}

	return json.Marshal(resp)
}

// anthMessageItem builds a message output item from accumulated text parts;
// seq disambiguates multiple message items within one response.
func anthMessageItem(seed string, seq int, parts []interface{}) map[string]interface{} {
	return map[string]interface{}{
		"id":      responsesID("msg", fmt.Sprintf("%s_%d", seed, seq)),
		"type":    "message",
		"status":  "completed",
		"role":    "assistant",
		"content": parts,
	}
}

// responsesBaseObject builds the shared response-object skeleton; model falls
// back to modelName when the upstream omitted it.
func responsesBaseObject(id string, createdAt int64, model, modelName string) map[string]interface{} {
	if model == "" {
		model = modelName
	}
	return map[string]interface{}{
		"id":                   id,
		"object":               "response",
		"created_at":           createdAt,
		"model":                model,
		"output":               []interface{}{},
		"error":                nil,
		"incomplete_details":   nil,
		"instructions":         nil,
		"metadata":             map[string]interface{}{},
		"parallel_tool_calls":  true,
		"previous_response_id": nil,
		"store":                false,
		"temperature":          1.0,
		"text":                 map[string]interface{}{"format": map[string]interface{}{"type": "text"}},
		"tool_choice":          "auto",
		"tools":                []interface{}{},
		"top_p":                1.0,
		"truncation":           "disabled",
		"usage":                nil,
		"user":                 nil,
	}
}

// responsesID builds a Responses-API-style id from a seed string (stable per
// upstream response) or the current time.
func responsesID(prefix, seed string) string {
	if seed != "" {
		return fmt.Sprintf("%s_%s", prefix, seed)
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

// responsesBodyToEvents wraps a full Responses API response object (an
// upstream that ignored stream:true) into lifecycle events, so stream
// clients still get a consumable sequence (created → in_progress →
// completed). The same completed object is stamped into all three events
// (the created/in_progress status fields are informational; SDKs converge
// on the last event). An empty body becomes an empty completion.
func responsesBodyToEvents(body []byte) ([][]byte, error) {
	var obj map[string]interface{}
	if len(bytes.TrimSpace(body)) == 0 {
		obj = map[string]interface{}{
			"id":         "resp_local",
			"object":     "response",
			"created_at": time.Now().Unix(),
			"status":     "completed",
			"output":     []interface{}{},
			"usage":      nil,
		}
	} else if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("invalid responses body: %w", err)
	} else if obj["object"] != "response" {
		return nil, fmt.Errorf("not a responses object")
	}
	created, _ := json.Marshal(map[string]interface{}{"type": "response.created", "response": obj})
	inProgress, _ := json.Marshal(map[string]interface{}{"type": "response.in_progress", "response": obj})
	completed, _ := json.Marshal(map[string]interface{}{"type": "response.completed", "response": obj})
	return [][]byte{created, inProgress, completed}, nil
}

// responsesTextPart builds a message content part in the Responses shape.
func responsesTextPart(text string) map[string]interface{} {
	return map[string]interface{}{
		"type":        "output_text",
		"text":        text,
		"annotations": []interface{}{},
	}
}
