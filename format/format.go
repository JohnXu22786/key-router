package format

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

var (
	ErrSkipChunk = errors.New("skip chunk")
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
	// UseNumber: large integers (e.g. seed) must not be corrupted by float64
	var oaiReq map[string]interface{}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&oaiReq); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after JSON body")
	}

	anthReq := make(map[string]interface{})

	if modelOverride != "" {
		anthReq["model"] = modelOverride
	} else if m, ok := oaiReq["model"]; ok {
		anthReq["model"] = m
	}

	// Copy known fields (map OpenAI "stop" to Anthropic "stop_sequences")
	copyFields := []string{"max_tokens", "stream", "temperature", "top_p", "metadata"}
	for _, f := range copyFields {
		if v, ok := oaiReq[f]; ok {
			anthReq[f] = v
		}
	}
	// Anthropic requires max_tokens; OpenAI clients commonly omit it or use
	// max_completion_tokens (o-series SDKs) which maps to the same field.
	if _, ok := anthReq["max_tokens"]; !ok {
		if v, ok := oaiReq["max_completion_tokens"]; ok {
			anthReq["max_tokens"] = v
		} else {
			anthReq["max_tokens"] = 4096
		}
	}
	if v, ok := oaiReq["stop"]; ok {
		anthReq["stop_sequences"] = normalizeStopSequences(v)
	} else if v, ok := oaiReq["stop_sequences"]; ok {
		anthReq["stop_sequences"] = v
	}

	// Map tool_choice: OpenAI {"type":"function","function":{"name":X}} →
	// Anthropic {"type":"tool","name":X}
	if v, ok := oaiReq["tool_choice"]; ok {
		anthReq["tool_choice"] = openAIToolChoiceToAnthropic(v)
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
			case "system", "developer":
				// System content may be a string or an array of text parts
				// (current OpenAI APIs); "developer" is mapped to system
				// since Anthropic has no developer role. Multiple
				// system/developer messages are joined (not overwritten).
				content, ok := safeString(m, "content")
				if !ok {
					if parts, ok := safeArr(m, "content"); ok {
						var texts []string
						for _, part := range parts {
							if p, ok := safeMap(part); ok && safeStringOrDefault(p, "type", "") == "text" {
								if t, ok := safeString(p, "text"); ok {
									texts = append(texts, t)
								}
							}
						}
						content = strings.Join(texts, "")
					}
				}
				if content != "" {
					if systemContent != "" {
						systemContent += "\n\n" + content
					} else {
						systemContent = content
					}
				}
			case "user":
				appendMerged(&anthMessages, convertOpenAIUserMessage(m))
			case "assistant":
				appendMerged(&anthMessages, convertOpenAIAssistantMessage(m))
			case "tool":
				// Anthropic requires alternating roles; consecutive tool
				// results become consecutive user messages and must merge
				appendMerged(&anthMessages, convertOpenAIToolMessage(m))
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

// appendMerged appends a converted message to the list, merging it into the
// previous message when they share a role (Anthropic rejects non-alternating
// roles, while OpenAI permits consecutive same-role messages — e.g. multiple
// tool results or back-to-back assistant messages with tool_calls).
func appendMerged(list *[]interface{}, msg map[string]interface{}) {
	role := safeStringOrDefault(msg, "role", "")
	if len(*list) > 0 {
		if prev, ok := (*list)[len(*list)-1].(map[string]interface{}); ok &&
			safeStringOrDefault(prev, "role", "") == role {
			// Merge content: arrays concatenate, strings join, and mixed
			// forms normalize to arrays so nothing is dropped.
			prevContent, prevHas := prev["content"]
			msgContent, msgHas := msg["content"]
			if prevHas || msgHas {
				prev["content"] = mergeContentParts(prevContent, prevHas, msgContent, msgHas)
			}
			// Merge tool_calls from both sides (assistant messages with tool
			// calls must not lose them when merged)
			prevTC, prevTCHas := safeArr(prev, "tool_calls")
			msgTC, msgTCHas := safeArr(msg, "tool_calls")
			if prevTCHas && msgTCHas {
				prev["tool_calls"] = append(prevTC, msgTC...)
			} else if msgTCHas {
				prev["tool_calls"] = msgTC
			}
			return
		}
	}
	*list = append(*list, msg)
}

// mergeContentParts combines two message contents (strings and/or arrays)
// into one content value, normalizing mixed forms to an array.
func mergeContentParts(a interface{}, aHas bool, b interface{}, bHas bool) interface{} {
	aArr, aIsArr := a.([]interface{})
	bArr, bIsArr := b.([]interface{})
	if aIsArr && bIsArr {
		return append(aArr, bArr...)
	}
	if aIsArr {
		if s, ok := b.(string); ok && bHas {
			return append(aArr, map[string]interface{}{"type": "text", "text": s})
		}
		return aArr
	}
	if bIsArr {
		if s, ok := a.(string); ok && aHas {
			return append([]interface{}{map[string]interface{}{"type": "text", "text": s}}, bArr...)
		}
		return bArr
	}
	aStr, aIsStr := a.(string)
	bStr, bIsStr := b.(string)
	if aIsStr && bIsStr {
		return aStr + bStr
	}
	if bHas {
		return b
	}
	return a
}

// AnthropicRequestToOpenAI converts Anthropic format to OpenAI format
func AnthropicRequestToOpenAI(body []byte, modelOverride string) ([]byte, error) {
	// UseNumber: large integers must not be corrupted by float64
	var anthReq map[string]interface{}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&anthReq); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after JSON body")
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
		// Ask the OpenAI upstream to include usage in streamed responses so
		// converted streams can report token usage to clients (and billing).
		// Some strict OpenAI-compatible endpoints reject unknown params; the
		// relay retries once without it on a 400.
		if b, ok := v.(bool); ok && b {
			oaiReq["stream_options"] = map[string]interface{}{"include_usage": true}
		}
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

	// Map tool_choice: Anthropic {"type":"tool","name":X} →
	// OpenAI {"type":"function","function":{"name":X}}
	if v, ok := anthReq["tool_choice"]; ok {
		oaiReq["tool_choice"] = anthropicToolChoiceToOpenAI(v)
	}

	var oaiMessages []interface{}

	// System prompt becomes first message. Anthropic accepts system as a
	// string OR an array of text blocks — join both forms.
	systemContent, _ := safeString(anthReq, "system")
	if systemContent == "" {
		if parts, ok := safeArr(anthReq, "system"); ok {
			var texts []string
			for _, part := range parts {
				if p, ok := safeMap(part); ok && safeStringOrDefault(p, "type", "") == "text" {
					if t, ok := safeString(p, "text"); ok {
						texts = append(texts, t)
					}
				}
			}
			systemContent = strings.Join(texts, "")
		}
	}
	if systemContent != "" {
		oaiMessages = append(oaiMessages, map[string]interface{}{
			"role":    "system",
			"content": systemContent,
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
				// Anthropic interleaves tool results inside user messages as
				// content parts; OpenAI requires them as separate {"role":"tool"}
				// messages.
				toolParts, otherParts := splitToolResults(m)
				for _, tp := range toolParts {
					oaiMessages = append(oaiMessages, convertAnthropicToolResult(tp))
				}
				if len(otherParts) > 0 || (len(otherParts) == 0 && len(toolParts) == 0) {
					userMsg := map[string]interface{}{"role": "user"}
					if content, ok := m["content"]; ok {
						if arr, isArr := content.([]interface{}); isArr {
							userMsg["content"] = convertAnthropicContentArray(arr, true)
						} else {
							userMsg["content"] = content
						}
					}
					oaiMessages = append(oaiMessages, userMsg)
				}
			case "assistant":
				oaiMessages = append(oaiMessages, convertAnthropicAssistantMessage(m))
			}
		}
	}

	if len(oaiMessages) > 0 {
		oaiReq["messages"] = oaiMessages
	}

	// OpenAI does not accept a conversation ending in a tool message (the
	// Anthropic agent-loop shape: user message containing only tool_result
	// parts converts to trailing tool messages) — append an empty assistant
	// message so the request is valid.
	if len(oaiMessages) > 0 {
		if last, ok := oaiMessages[len(oaiMessages)-1].(map[string]interface{}); ok {
			if safeStringOrDefault(last, "role", "") == "tool" {
				oaiMessages = append(oaiMessages, map[string]interface{}{
					"role":    "assistant",
					"content": "",
				})
				oaiReq["messages"] = oaiMessages
			}
		}
	}

	if tools, ok := safeArr(anthReq, "tools"); ok {
		oaiReq["tools"] = convertAnthropicTools(tools)
	}

	return json.Marshal(oaiReq)
}

// normalizeStopSequences converts OpenAI's stop value (string or array of
// strings) to Anthropic's stop_sequences, which requires an array of strings.
func normalizeStopSequences(v interface{}) interface{} {
	switch s := v.(type) {
	case string:
		return []interface{}{s}
	case []interface{}:
		return s
	case []string:
		arr := make([]interface{}, len(s))
		for i, x := range s {
			arr[i] = x
		}
		return arr
	default:
		return v
	}
}

// openAIToolChoiceToAnthropic maps OpenAI tool_choice to Anthropic tool_choice
func openAIToolChoiceToAnthropic(v interface{}) interface{} {
	if s, ok := v.(string); ok {
		// "auto" | "none" | "required" → anthropic equivalents
		switch s {
		case "none":
			return map[string]interface{}{"type": "none"}
		case "required":
			return map[string]interface{}{"type": "any"}
		default:
			return map[string]interface{}{"type": "auto"}
		}
	}
	m, ok := safeMap(v)
	if !ok {
		return v
	}
	if t := safeStringOrDefault(m, "type", ""); t == "function" {
		fn, ok := safeMap(m["function"])
		if ok {
			return map[string]interface{}{
				"type": "tool",
				"name": fn["name"],
			}
		}
	}
	return v
}

// anthropicToolChoiceToOpenAI maps Anthropic tool_choice to OpenAI tool_choice
func anthropicToolChoiceToOpenAI(v interface{}) interface{} {
	if s, ok := v.(string); ok {
		switch s {
		case "none":
			return "none"
		case "any":
			return "required"
		default:
			return "auto"
		}
	}
	m, ok := safeMap(v)
	if !ok {
		return v
	}
	// Object forms: {"type":"auto"}, {"type":"any"}, {"type":"none"},
	// {"type":"tool","name":X} — OpenAI accepts only "auto"/"none"/"required"
	// strings or {"type":"function",...}
	switch safeStringOrDefault(m, "type", "") {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		return map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name": m["name"],
			},
		}
	}
	return v
}

// AnthropicStreamConverter converts OpenAI stream chunks into Anthropic SSE
// events with the event ordering Anthropic clients require:
// message_start → content_block_start → content_block_delta → ... →
// message_delta → message_stop. It is stateful: OpenAI tool_calls arrive
// as indexed fragments across multiple chunks and must be re-joined to
// Anthropic content block indexes.
type AnthropicStreamConverter struct {
	started      bool
	finished     bool
	textOpened   bool
	textBlockIdx int // assigned when the text block first opens
	nextBlockIdx int // next free content block index
	toolBlocks   map[int]int // openai tool_call index -> anthropic content block index
	// OpenAI sends usage in a chunk AFTER the finish chunk (with
	// include_usage). message_delta/message_stop are therefore deferred until
	// that chunk arrives (or the stream ends) so the synthesized
	// message_delta can carry real output_tokens instead of 0.
	finishPending   bool
	finishStopReason string
	pendingTokens    int64
}

// NewAnthropicStreamConverter creates a fresh converter
func NewAnthropicStreamConverter() *AnthropicStreamConverter {
	return &AnthropicStreamConverter{toolBlocks: make(map[int]int)}
}

// Begin returns the synthetic message_start event (once) and any other
// leading events required before content deltas.
func (c *AnthropicStreamConverter) Begin(modelName string) [][]byte {
	if c.started {
		return nil
	}
	c.started = true
	start := map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            "msg_local_1",
			"type":          "message",
			"role":          "assistant",
			"model":         modelName,
			"content":       []interface{}{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]interface{}{
				"input_tokens":  0,
				"output_tokens": 0,
			},
		},
	}
	b, _ := json.Marshal(start)
	return [][]byte{b}
}

// Convert converts a single OpenAI chunk into 0+ Anthropic events.
// The chunk's JSON object is returned unmarshalled so callers can also
// extract usage before conversion.
func (c *AnthropicStreamConverter) Convert(chunk []byte, modelName string) ([][]byte, error) {
	leading := c.Begin(modelName)

	if c.finished {
		return leading, ErrSkipChunk
	}

	var oai map[string]interface{}
	if err := json.Unmarshal(chunk, &oai); err != nil {
		return leading, err
	}

	// Mid-stream OpenAI error frame ({"error":{...}} or {"error":"msg"}):
	// surface it to the Anthropic client instead of silently ending with a
	// clean message_stop.
	if _, hasErr := oai["error"]; hasErr {
		errMsg := "upstream stream error"
		if errBlock, ok := safeMap(oai["error"]); ok {
			errMsg = safeStringOrDefault(errBlock, "message", errMsg)
		} else if s, ok := oai["error"].(string); ok && s != "" {
			errMsg = s
		}
		ev, _ := json.Marshal(map[string]interface{}{
			"type": "error",
			"error": map[string]interface{}{
				"type":    "stream_error",
				"message": errMsg,
			},
		})
		c.finished = true
		return append(leading, ev), nil
	}

	choices, ok := safeArr(oai, "choices")
	if !ok || len(choices) == 0 {
		// Usage-only chunk (choices:[] with include_usage) — OpenAI sends it
		// AFTER the finish chunk. Buffer the tokens; if the finish already
		// happened, emit the deferred message_delta + message_stop now.
		if u, ok := safeMap(oai["usage"]); ok {
			if ct, ok := u["completion_tokens"].(float64); ok {
				c.pendingTokens = int64(ct)
			}
		}
		if c.finishPending {
			events := c.terminate()
			return append(leading, events...), nil
		}
		return leading, ErrSkipChunk
	}

	// After the finish chunk, only the usage-only chunk may follow; any
	// other content is ignored (non-conforming upstream).
	if c.finishPending {
		return leading, ErrSkipChunk
	}
	choice, ok := safeMap(choices[0])
	if !ok {
		return leading, ErrSkipChunk
	}

	finishReason := safeStringOrDefault(choice, "finish_reason", "")
	var events [][]byte

	// Process delta (content/tool fragments) FIRST: some OpenAI-compatible
	// upstreams (vLLM, llama.cpp, Ollama) send the last fragment together
	// with finish_reason — dropping it would truncate the final token.
	if delta, ok := safeMap(choice["delta"]); ok {
		// Content delta (text). Emit content_block_start before the first
		// text delta so strict clients have an open block. The text block
		// takes the next free index (0 unless a tool block opened first).
		if content, ok := safeString(delta, "content"); ok && content != "" {
			if !c.textOpened {
				c.textBlockIdx = c.nextBlockIdx
				c.nextBlockIdx++
				c.textOpened = true
				startEv, _ := json.Marshal(map[string]interface{}{
					"type":  "content_block_start",
					"index": c.textBlockIdx,
					"content_block": map[string]interface{}{
						"type": "text",
						"text": "",
					},
				})
				events = append(events, startEv)
			}
			ev, _ := json.Marshal(map[string]interface{}{
				"type":  "content_block_delta",
				"index": c.textBlockIdx,
				"delta": map[string]interface{}{
					"type": "text_delta",
					"text": content,
				},
			})
			events = append(events, ev)
		}

		// Tool call fragments (delta.tool_calls[i].function.arguments)
		if toolCalls, ok := safeArr(delta, "tool_calls"); ok {
			for j, tc := range toolCalls {
				tcMap, ok := safeMap(tc)
				if !ok {
					continue
				}
				// Some gateways omit "index"; fall back to the array position
				// so multiple tools in one chunk stay distinct
				toolIdx, hasIdx := tcMap["index"].(float64)
				ti := j
				if hasIdx {
					ti = int(toolIdx)
				}
				blockIdx, seen := c.toolBlocks[ti]
				if !seen {
					// First fragment for this tool: emit content_block_start
					blockIdx = c.nextBlockIdx
					c.nextBlockIdx++
					c.toolBlocks[ti] = blockIdx

					fn, _ := safeMap(tcMap["function"])
					startEv, _ := json.Marshal(map[string]interface{}{
						"type":  "content_block_start",
						"index": blockIdx,
						"content_block": map[string]interface{}{
							"type":  "tool_use",
							"id":    tcMap["id"],
							"name":  fn["name"],
							"input": map[string]interface{}{},
						},
					})
					events = append(events, startEv)
				}
				if fn, ok := safeMap(tcMap["function"]); ok {
					if args, ok := safeString(fn, "arguments"); ok && args != "" {
						deltaEv, _ := json.Marshal(map[string]interface{}{
							"type":  "content_block_delta",
							"index": blockIdx,
							"delta": map[string]interface{}{
								"type":         "input_json_delta",
								"partial_json": args,
							},
						})
						events = append(events, deltaEv)
					}
				}
			}
		}
	}

	// Finish: close open blocks and defer message_delta/message_stop until the
	// usage-only chunk arrives (OpenAI sends it right after finish when
	// include_usage is set) so the message_delta can carry real tokens.
	// OpenAI finishes with "stop", "length", "tool_calls", "function_call"
	// (legacy) or "content_filter" — all must terminate the Anthropic stream.
	if finishReason == "stop" || finishReason == "length" || finishReason == "tool_calls" || finishReason == "function_call" || finishReason == "content_filter" {
		stopReason := "end_turn"
		switch finishReason {
		case "length":
			stopReason = "max_tokens"
		case "tool_calls", "function_call":
			stopReason = "tool_use"
		case "content_filter":
			stopReason = "refusal"
		}

		// If the finish chunk itself carries usage (non-standard providers),
		// terminate immediately with it; otherwise defer.
		outputTokens := c.pendingTokens
		if u, ok := safeMap(oai["usage"]); ok {
			if ct, ok := u["completion_tokens"].(float64); ok {
				outputTokens = int64(ct)
			}
		}

		// Close all open blocks in ascending index order (text block and any
		// tool blocks)
		closeIdx := make([]int, 0, len(c.toolBlocks)+1)
		for _, blockIdx := range c.toolBlocks {
			closeIdx = append(closeIdx, blockIdx)
		}
		if c.textOpened {
			closeIdx = append(closeIdx, c.textBlockIdx)
		}
		sort.Ints(closeIdx)
		for _, blockIdx := range closeIdx {
			stopEv, _ := json.Marshal(map[string]interface{}{
				"type":  "content_block_stop",
				"index": blockIdx,
			})
			events = append(events, stopEv)
		}

		c.finishPending = true
		c.finishStopReason = stopReason
		if _, hasUsage := oai["usage"]; hasUsage {
			c.pendingTokens = outputTokens
			events = append(events, c.terminate()...)
		}
	}

	if len(events) == 0 {
		return leading, ErrSkipChunk
	}
	return append(leading, events...), nil
}

// terminate emits the deferred message_delta + message_stop. Call once the
// finish chunk's blocks are closed and usage is known (or the stream ends).
func (c *AnthropicStreamConverter) terminate() [][]byte {
	if c.finished {
		return nil
	}
	c.finished = true
	stopReason := c.finishStopReason
	if stopReason == "" {
		stopReason = "end_turn" // abnormal stream end without a finish chunk
	}
	deltaEv, _ := json.Marshal(map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": map[string]interface{}{
			"output_tokens": c.pendingTokens,
		},
	})
	stopEv, _ := json.Marshal(map[string]interface{}{
		"type": "message_stop",
	})
	return [][]byte{deltaEv, stopEv}
}

// Finished reports whether the stream has ended (message_stop emitted)
func (c *AnthropicStreamConverter) Finished() bool {
	return c.finished
}

// CloseStream emits termination events for a stream that ended without a
// finish chunk (EOF) or whose deferred message_delta never fired (no usage
// chunk arrived): content_block_stop for any open blocks, a message_delta
// (with the deferred stop_reason, or end_turn) and a message_stop.
func (c *AnthropicStreamConverter) CloseStream() [][]byte {
	if c.finished {
		return nil
	}

	// Finish was already seen: blocks are closed and only the deferred
	// message_delta/message_stop remain.
	if c.finishPending {
		return c.terminate()
	}

	closeIdx := make([]int, 0, len(c.toolBlocks)+1)
	for _, blockIdx := range c.toolBlocks {
		closeIdx = append(closeIdx, blockIdx)
	}
	if c.textOpened {
		closeIdx = append(closeIdx, c.textBlockIdx)
	}
	sort.Ints(closeIdx)

	var events [][]byte
	for _, blockIdx := range closeIdx {
		stopEv, _ := json.Marshal(map[string]interface{}{
			"type":  "content_block_stop",
			"index": blockIdx,
		})
		events = append(events, stopEv)
	}
	events = append(events, c.terminate()...)
	return events
}

// OpenAIStreamChunkToAnthropic converts a single OpenAI stream chunk to
// Anthropic format. Retained for stateless single-chunk conversions
// (e.g. unit tests); for full streams use AnthropicStreamConverter.
func OpenAIStreamChunkToAnthropic(chunk []byte) ([]byte, error) {
	conv := NewAnthropicStreamConverter()
	events, err := conv.Convert(chunk, "")
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, ErrSkipChunk
	}
	return events[len(events)-1], nil
}

// OpenAIStreamConverter converts Anthropic SSE events into OpenAI stream
// chunks. Stateful: Anthropic tool_use blocks (content_block_start) map to
// OpenAI tool_call indices, and input_json_delta fragments are re-emitted as
// tool_calls arguments fragments.
type OpenAIStreamConverter struct {
	toolIndexes map[int]int // anthropic content block index -> openai tool index
	nextToolIdx int
	errored     bool // a mid-stream error event was surfaced
	// input tokens and cache figures arrive in message_start (nested under
	// message.usage); message_delta only carries output_tokens, so the final
	// usage chunk merges all of them.
	inputTokens  int64
	cacheHit     int64
	cacheWrite   int64
	modelName    string
}

// NewOpenAIStreamConverter creates a fresh converter
func NewOpenAIStreamConverter() *OpenAIStreamConverter {
	return &OpenAIStreamConverter{toolIndexes: make(map[int]int)}
}

// SetModel sets the model name stamped on every synthesized chunk
func (c *OpenAIStreamConverter) SetModel(name string) {
	c.modelName = name
}

// Errored reports whether a mid-stream error event was converted
func (c *OpenAIStreamConverter) Errored() bool {
	return c.errored
}

// Convert converts a single Anthropic event into 0+ OpenAI chunks
func (c *OpenAIStreamConverter) Convert(event []byte) ([][]byte, error) {
	var anth map[string]interface{}
	if err := json.Unmarshal(event, &anth); err != nil {
		return nil, err
	}

	eventType := safeStringOrDefault(anth, "type", "")
	var chunks [][]byte

	switch eventType {
	case "content_block_start":
		// Only tool_use blocks matter; text blocks are skipped
		cb, ok := safeMap(anth["content_block"])
		if !ok || safeStringOrDefault(cb, "type", "") != "tool_use" {
			return nil, ErrSkipChunk
		}
		blockIdx, _ := anth["index"].(float64)
		toolIdx := c.nextToolIdx
		c.nextToolIdx++
		c.toolIndexes[int(blockIdx)] = toolIdx

		tc := map[string]interface{}{
			"index": toolIdx,
			"id":    cb["id"],
			"type":  "function",
			"function": map[string]interface{}{
				"name":      cb["name"],
				"arguments": "",
			},
		}
		b, _ := json.Marshal(c.openAIToolStreamChunk([]interface{}{tc}, ""))
		chunks = append(chunks, b)

	case "content_block_delta":
		delta, ok := safeMap(anth["delta"])
		if !ok {
			return nil, ErrSkipChunk
		}
		blockIdx, _ := anth["index"].(float64)
		switch safeStringOrDefault(delta, "type", "") {
		case "text_delta":
			if text, ok := safeString(delta, "text"); ok {
				b, _ := json.Marshal(c.openAIStreamChunk(text, ""))
				chunks = append(chunks, b)
			}
		case "input_json_delta":
			toolIdx, seen := c.toolIndexes[int(blockIdx)]
			if !seen {
				return nil, ErrSkipChunk
			}
			partial, _ := delta["partial_json"].(string)
			tc := map[string]interface{}{
				"index": toolIdx,
				"function": map[string]interface{}{
					"arguments": partial,
				},
			}
			b, _ := json.Marshal(c.openAIToolStreamChunk([]interface{}{tc}, ""))
			chunks = append(chunks, b)
		}

	case "message_start":
		// input tokens (and cache figures) live in message.usage
		if msg, ok := safeMap(anth["message"]); ok {
			if u, ok := safeMap(msg["usage"]); ok {
				if in, ok := u["input_tokens"].(float64); ok {
					c.inputTokens = int64(in)
				}
				if ch, ok := u["cache_read_input_tokens"].(float64); ok {
					c.cacheHit = int64(ch)
				}
				if cw, ok := u["cache_creation_input_tokens"].(float64); ok {
					c.cacheWrite = int64(cw)
				}
			}
		}
		return nil, ErrSkipChunk

	case "message_delta":
		delta, ok := safeMap(anth["delta"])
		if !ok {
			return nil, ErrSkipChunk
		}
		stopReason := safeStringOrDefault(delta, "stop_reason", "")
		switch stopReason {
		case "end_turn", "stop_sequence", "pause_turn":
			b, _ := json.Marshal(c.openAIStreamChunkWithUsage("", "stop", anth))
			chunks = append(chunks, b)
		case "max_tokens", "model_context_window_exceeded":
			b, _ := json.Marshal(c.openAIStreamChunkWithUsage("", "length", anth))
			chunks = append(chunks, b)
		case "refusal":
			b, _ := json.Marshal(c.openAIStreamChunkWithUsage("", "content_filter", anth))
			chunks = append(chunks, b)
		case "tool_use":
			b, _ := json.Marshal(c.openAIStreamChunkWithUsage("", "tool_calls", anth))
			chunks = append(chunks, b)
		default:
			return nil, ErrSkipChunk
		}

	case "message_stop", "content_block_stop", "ping":
		// finish_reason was already emitted by message_delta; nothing to do
		return nil, ErrSkipChunk

	case "error":
		// Mid-stream upstream error: surface it to the OpenAI client instead
		// of silently ending the stream with [DONE].
		c.errored = true
		errBlock, _ := safeMap(anth["error"])
		errMsg := safeStringOrDefault(errBlock, "message", "upstream stream error")
		ev, _ := json.Marshal(map[string]interface{}{
			"error": map[string]interface{}{
				"message": errMsg,
				"type":    "stream_error",
			},
		})
		return [][]byte{ev}, nil
	}

	if len(chunks) == 0 {
		return nil, ErrSkipChunk
	}
	return chunks, nil
}

// AnthropicStreamEventToOpenAI converts an Anthropic stream event to OpenAI format.
// Retained for stateless single-event conversions (e.g. unit tests); for full
// streams use OpenAIStreamConverter.
func AnthropicStreamEventToOpenAI(event []byte) ([]byte, error) {
	conv := NewOpenAIStreamConverter()
	chunks, err := conv.Convert(event)
	if err != nil {
		return nil, err
	}
	return chunks[len(chunks)-1], nil
}

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

// openAIStreamChunk is the converter variant that stamps the model name
func (c *OpenAIStreamConverter) openAIStreamChunk(content, finishReason string) map[string]interface{} {
	chunk := openAIStreamChunk(content, finishReason)
	chunk["model"] = c.modelName
	return chunk
}

// openAIStreamChunkWithUsage builds an OpenAI stream chunk (with the
// converter's model name), optionally attaching usage merged from the
// message_start input/cache tokens and the message_delta output tokens.
// Follows the OpenAI convention: prompt_tokens INCLUDES cached tokens and
// total_tokens == prompt_tokens + completion_tokens.
func (c *OpenAIStreamConverter) openAIStreamChunkWithUsage(content, finishReason string, anthEvent map[string]interface{}) map[string]interface{} {
	chunk := openAIStreamChunk(content, finishReason)
	chunk["model"] = c.modelName
	if anthEvent != nil {
		if usage, ok := safeMap(anthEvent["usage"]); ok {
			out, _ := usage["output_tokens"].(float64)
			prompt := c.inputTokens + c.cacheHit + c.cacheWrite
			chunk["usage"] = map[string]interface{}{
				"prompt_tokens":     prompt,
				"completion_tokens": int64(out),
				"total_tokens":      prompt + int64(out),
				"prompt_tokens_details": map[string]interface{}{
					"cached_tokens": c.cacheHit + c.cacheWrite,
				},
			}
		}
	}
	return chunk
}

// openAIToolStreamChunk builds a tool_calls chunk (without model stamping;
// the converter method below adds it)
func openAIToolStreamChunk(toolCalls []interface{}, finishReason string) map[string]interface{} {
	delta := map[string]interface{}{
		"role":       "assistant",
		"tool_calls": toolCalls,
	}
	return map[string]interface{}{
		"id":      "chatcmpl-local",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   "",
		"choices": []interface{}{
			map[string]interface{}{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			},
		},
	}
}

// openAIToolStreamChunk is the converter variant that stamps the model name
func (c *OpenAIStreamConverter) openAIToolStreamChunk(toolCalls []interface{}, finishReason string) map[string]interface{} {
	chunk := openAIToolStreamChunk(toolCalls, finishReason)
	chunk["model"] = c.modelName
	return chunk
}

// Internal helpers

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
			if strings.HasPrefix(url, "data:") {
				// data:image/png;base64,iVBOR... → split into media_type + raw data
				parts := strings.SplitN(url, ",", 2)
				if len(parts) == 2 {
					meta := strings.TrimPrefix(parts[0], "data:")
					mediaType := strings.Split(meta, ";")[0]
					if mediaType == "" {
						mediaType = "image/png"
					}
					anthContent = append(anthContent, map[string]interface{}{
						"type": "image",
						"source": map[string]interface{}{
							"type":       "base64",
							"media_type": mediaType,
							"data":       parts[1],
						},
					})
				}
			} else {
				// HTTP URL
				anthContent = append(anthContent, map[string]interface{}{
					"type": "image",
					"source": map[string]interface{}{
						"type": "url",
						"url":  url,
					},
				})
			}
		}
	}
	return anthContent
}

func convertOpenAIAssistantMessage(m map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{
		"role": "assistant",
	}

	// Content may be a string OR an array of parts (audio/video modalities,
	// refusal parts, some OpenAI-compatible gateways) — extract the text.
	var content string
	if s, ok := safeString(m, "content"); ok {
		content = s
	} else if parts, ok := safeArr(m, "content"); ok {
		var texts []string
		for _, part := range parts {
			if p, ok := safeMap(part); ok && safeStringOrDefault(p, "type", "") == "text" {
				if t, ok := safeString(p, "text"); ok {
					texts = append(texts, t)
				}
			}
		}
		content = strings.Join(texts, "")
	}

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
			// OpenAI arguments is a JSON-encoded string; Anthropic input must
			// be a JSON object.
			input := parseArgumentsObject(fn["arguments"])
			anthContent = append(anthContent, map[string]interface{}{
				"type":  "tool_use",
				"id":    tcMap["id"],
				"name":  fn["name"],
				"input": input,
			})
		}
	}

	result["content"] = anthContent
	return result
}

// parseArgumentsObject converts an OpenAI tool_call arguments value (a JSON
// string) into a JSON object for Anthropic's tool_use.input. Falls back to {}.
func parseArgumentsObject(arguments interface{}) map[string]interface{} {
	s, ok := arguments.(string)
	if !ok || s == "" {
		return map[string]interface{}{}
	}
	var obj map[string]interface{}
	if json.Unmarshal([]byte(s), &obj) != nil || obj == nil {
		return map[string]interface{}{}
	}
	return obj
}

func convertOpenAIToolMessage(m map[string]interface{}) map[string]interface{} {
	// Anthropic tool_result.content must be a string or array — OpenAI may
	// legally send null content (a tool that returned nothing)
	content := m["content"]
	if content == nil {
		content = ""
	}
	return map[string]interface{}{
		"role": "user",
		"content": []interface{}{
			map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": m["tool_call_id"],
				"content":     content,
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

// splitToolResults splits a user message's content parts into tool_result
// parts and everything else.
func splitToolResults(m map[string]interface{}) (toolParts, otherParts []interface{}) {
	arr, ok := safeArr(m, "content")
	if !ok {
		return nil, []interface{}{}
	}
	for _, part := range arr {
		p, ok := safeMap(part)
		if !ok {
			otherParts = append(otherParts, part)
			continue
		}
		if safeStringOrDefault(p, "type", "") == "tool_result" {
			toolParts = append(toolParts, part)
		} else {
			otherParts = append(otherParts, part)
		}
	}
	return toolParts, otherParts
}

// convertAnthropicToolResult converts an Anthropic tool_result content part
// into an OpenAI {"role":"tool"} message.
func convertAnthropicToolResult(part interface{}) map[string]interface{} {
	p, _ := safeMap(part)
	return map[string]interface{}{
		"role":         "tool",
		"tool_call_id": p["tool_use_id"],
		"content":      p["content"],
	}
}

func convertAnthropicContentArray(content []interface{}, skipToolResults bool) []interface{} {
	var oaiContent []interface{}
	for _, part := range content {
		p, ok := safeMap(part)
		if !ok {
			continue
		}
		pType := safeStringOrDefault(p, "type", "")

		// tool_result is handled at the message level (as {"role":"tool"}),
		// never as a content part
		if skipToolResults && pType == "tool_result" {
			continue
		}

		switch pType {
		case "text":
			oaiContent = append(oaiContent, map[string]interface{}{
				"type": "text",
				"text": p["text"],
			})
		case "image":
			source, ok := safeMap(p["source"])
			if !ok {
				continue
			}
			srcType := safeStringOrDefault(source, "type", "")
			if srcType == "base64" {
				mediaType := safeStringOrDefault(source, "media_type", "image/png")
				data, _ := source["data"].(string)
				oaiContent = append(oaiContent, map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]interface{}{
						"url": "data:" + mediaType + ";base64," + data,
					},
				})
			} else if srcType == "url" {
				url, _ := source["url"].(string)
				oaiContent = append(oaiContent, map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]interface{}{
						"url": url,
					},
				})
			}
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
			// Anthropic input is a JSON object; OpenAI requires a
			// JSON-encoded STRING for function.arguments.
			args := []byte("{}")
			if p["input"] != nil {
				if b, err := json.Marshal(p["input"]); err == nil {
					args = b
				}
			}
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   p["id"],
				"type": "function",
				"function": map[string]interface{}{
					"name":      p["name"],
					"arguments": string(args),
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
