package format

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Client-facing OpenAI Responses API (POST /v1/responses) support.
//
// The Responses API is OpenAI's newer-generation protocol. KeyRouter accepts
// it as a third client format and converts to the upstream protocol when
// needed:
//   - OpenAI-format provider that implements /v1/responses: passed through
//     natively.
//   - OpenAI-format gateway without /v1/responses (404/501): automatically
//     converted to chat completions (ResponsesRequestToChatCompletion and
//     the relay's response-side converter).
//   - Anthropic provider: converted to the Messages API
//     (ResponsesRequestToAnthropic); the response is converted back.
//
// The Responses API request shape (model, input, instructions,
// max_output_tokens, stream, tools, tool_choice, ...) and the Anthropic
// Messages shape (model, messages, system, max_tokens, ...) are close enough
// that most conversions are direct field mappings.

// ResponsesRequestToChatCompletion converts an OpenAI Responses API request
// body into a chat completions request body. Responses-only fields with no
// chat-completions equivalent (store, previous_response_id, truncation,
// reasoning, include, service_tier, ...) are dropped.
func ResponsesRequestToChatCompletion(body []byte, modelOverride string) ([]byte, error) {
	var rReq map[string]interface{}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&rReq); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after JSON body")
	}

	out := make(map[string]interface{})
	if modelOverride != "" {
		out["model"] = modelOverride
	} else if m, ok := rReq["model"]; ok {
		out["model"] = m
	}

	var messages []interface{}

	// instructions → leading system message; system/developer input items
	// are hoisted up next to it (chat completions only accepts system
	// content at the start, and several gateways reject the "developer"
	// role outright — the Responses API allows both anywhere in input).
	var systemParts []string
	if inst, ok := safeString(rReq, "instructions"); ok && strings.TrimSpace(inst) != "" {
		systemParts = append(systemParts, inst)
	}
	if len(systemParts) > 0 {
		messages = append(messages, map[string]interface{}{
			"role":    "system",
			"content": strings.Join(systemParts, "\n\n"),
		})
	}

	// input: a plain string, or a list of message / function_call_output items
	switch in := rReq["input"].(type) {
	case string:
		if in != "" {
			messages = append(messages, map[string]interface{}{
				"role":    "user",
				"content": in,
			})
		}
	case []interface{}:
		for _, item := range in {
			messages = append(messages, responsesInputItemToChat(item, &systemParts)...)
		}
	}
	// system/developer items may appear anywhere in the input — hoist them
	// to the leading system message (chat only accepts leading system)
	if len(systemParts) > 0 {
		if len(messages) > 0 && messages[0].(map[string]interface{})["role"] == "system" {
			// instructions already produced the leading system message
			messages[0].(map[string]interface{})["content"] = strings.Join(systemParts, "\n\n")
		} else {
			messages = append([]interface{}{map[string]interface{}{
				"role":    "system",
				"content": strings.Join(systemParts, "\n\n"),
			}}, messages...)
		}
	}
	if len(messages) > 0 {
		out["messages"] = messages
	} else {
		// chat completions REQUIRES a messages array; an instructions-less
		// empty input (or none at all) gets a minimal user turn
		out["messages"] = []interface{}{map[string]interface{}{
			"role":    "user",
			"content": "",
		}}
	}

	// max_output_tokens → max_tokens (universally accepted by OpenAI-
	// compatible gateways; max_completion_tokens is rejected by several)
	if v, ok := rReq["max_output_tokens"]; ok {
		out["max_tokens"] = v
	}

	// Fields both protocols share
	for _, f := range []string{"stream", "temperature", "top_p", "metadata", "user", "seed", "parallel_tool_calls"} {
		if v, ok := rReq[f]; ok {
			out[f] = v
		}
	}

	// tools and tool_choice have identical schemas in both protocols
	if v, ok := rReq["tools"]; ok {
		out["tools"] = v
	}
	if v, ok := rReq["tool_choice"]; ok {
		out["tool_choice"] = v
	}

	// text.format → response_format
	if v, ok := rReq["text"]; ok {
		if rf, ok := responsesTextFormatToResponseFormat(v); ok {
			out["response_format"] = rf
		}
	}

	// Ask for streamed usage (like the Anthropic→OpenAI conversion does) so
	// converted streams can report token usage to billing and to the
	// client's response.completed event. Strict gateways that reject the
	// parameter get a 400 → the relay retries once without it.
	if b, ok := rReq["stream"].(bool); ok && b {
		out["stream_options"] = map[string]interface{}{"include_usage": true}
	}

	return json.Marshal(out)
}

// responsesInputItemToChat converts one Responses API input item into the
// chat messages it corresponds to (function_call_output items become tool
// messages; a message item stays a single message). system/developer items
// are hoisted into the leading system message via systemParts (chat only
// accepts leading system content).
func responsesInputItemToChat(item interface{}, systemParts *[]string) []interface{} {
	m, ok := safeMap(item)
	if !ok {
		return nil
	}
	switch safeStringOrDefault(m, "type", "") {
	case "message":
		role := safeStringOrDefault(m, "role", "user")
		switch role {
		case "system", "developer":
			if content := responsesItemText(m); content != "" {
				*systemParts = append(*systemParts, content)
			}
			return nil
		}
		msg := map[string]interface{}{"role": role}
		var toolCalls []interface{}
		var textParts []interface{}

		switch content := m["content"].(type) {
		case string:
			msg["content"] = content
		case []interface{}:
			for _, part := range content {
				p, ok := safeMap(part)
				if !ok {
					continue
				}
				switch safeStringOrDefault(p, "type", "") {
				case "input_text", "output_text":
					textParts = append(textParts, map[string]interface{}{
						"type": "text",
						"text": p["text"],
					})
				case "input_image":
					textParts = append(textParts, map[string]interface{}{
						"type": "image_url",
						"image_url": map[string]interface{}{
							"url": responsesImageURL(p),
						},
					})
				case "function_call":
					// assistant tool-call part → chat tool_calls. The id MUST
					// be the CALL id — the client echoes it back in
					// function_call_output.call_id, which becomes the chat
					// tool message's tool_call_id.
					args, _ := p["arguments"].(string)
					toolCalls = append(toolCalls, map[string]interface{}{
						"id":   firstNonEmpty(p["call_id"], p["id"]),
						"type": "function",
						"function": map[string]interface{}{
							"name":      p["name"],
							"arguments": args,
						},
					})
				}
			}
			if len(textParts) > 0 {
				msg["content"] = textParts
			} else {
				msg["content"] = ""
			}
		}
		if len(toolCalls) > 0 {
			msg["tool_calls"] = toolCalls
		}
		return []interface{}{msg}
	case "function_call_output":
		return []interface{}{map[string]interface{}{
			"role":         "tool",
			"tool_call_id": m["call_id"],
			"content":      m["output"],
		}}
	}
	return nil
}

// responsesTextFormatToResponseFormat maps the Responses API's text.format
// object onto a chat completions response_format ("" → not representable).
func responsesTextFormatToResponseFormat(v interface{}) (interface{}, bool) {
	text, ok := safeMap(v)
	if !ok {
		return nil, false
	}
	f, ok := safeMap(text["format"])
	if !ok {
		return nil, false
	}
	switch safeStringOrDefault(f, "type", "") {
	case "json_schema":
		schema := map[string]interface{}{"type": "json_schema"}
		js := map[string]interface{}{}
		if name, ok := safeString(f, "name"); ok {
			js["name"] = name
		}
		if s, ok := f["schema"]; ok {
			js["schema"] = s
		}
		if strict, ok := f["strict"]; ok {
			js["strict"] = strict
		}
		schema["json_schema"] = js
		return schema, true
	case "json_object":
		return map[string]interface{}{"type": "json_object"}, true
	}
	return nil, false
}

// responsesImageURL extracts the image URL string from an input_image part
// (the field is a plain string, or {"url": ...}).
func responsesImageURL(p map[string]interface{}) string {
	if s, ok := safeString(p, "image_url"); ok {
		return s
	}
	if o, ok := safeMap(p["image_url"]); ok {
		if s, ok := safeString(o, "url"); ok {
			return s
		}
	}
	return ""
}

// ResponsesRequestToAnthropic converts an OpenAI Responses API request body
// into an Anthropic Messages request body. Anthropic has no equivalents for
// responses-only fields (store, previous_response_id, truncation, reasoning,
// text.format, parallel_tool_calls, ...) — they are dropped.
func ResponsesRequestToAnthropic(body []byte, modelOverride string) ([]byte, error) {
	var rReq map[string]interface{}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if err := dec.Decode(&rReq); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("trailing data after JSON body")
	}

	out := make(map[string]interface{})
	if modelOverride != "" {
		out["model"] = modelOverride
	} else if m, ok := rReq["model"]; ok {
		out["model"] = m
	}

	// Anthropic REQUIRES max_tokens; the Responses API calls the same
	// concept max_output_tokens and clients often omit it entirely.
	if v, ok := rReq["max_output_tokens"]; ok {
		out["max_tokens"] = v
	} else {
		out["max_tokens"] = 4096
	}

	for _, f := range []string{"stream", "temperature", "top_p", "metadata"} {
		if v, ok := rReq[f]; ok {
			out[f] = v
		}
	}
	if v, ok := rReq["tool_choice"]; ok {
		out["tool_choice"] = openAIToolChoiceToAnthropic(v)
	}
	if tools, ok := safeArr(rReq, "tools"); ok {
		out["tools"] = convertOpenAITools(tools)
	}

	// instructions + system/developer messages → the system prompt
	var systemParts []string
	if inst, ok := safeString(rReq, "instructions"); ok && strings.TrimSpace(inst) != "" {
		systemParts = append(systemParts, inst)
	}

	var anthMessages []interface{}
	switch in := rReq["input"].(type) {
	case string:
		if in != "" {
			anthMessages = append(anthMessages, map[string]interface{}{
				"role": "user",
				"content": []interface{}{map[string]interface{}{
					"type": "text",
					"text": in,
				}},
			})
		}
	case []interface{}:
		for _, item := range in {
			m, ok := safeMap(item)
			if !ok {
				continue
			}
			switch safeStringOrDefault(m, "type", "") {
			case "message":
				switch safeStringOrDefault(m, "role", "user") {
				case "system", "developer":
					if content := responsesItemText(m); content != "" {
						systemParts = append(systemParts, content)
					}
				case "user":
					anthMessages = append(anthMessages, map[string]interface{}{
						"role":    "user",
						"content": responsesUserContent(m["content"]),
					})
				case "assistant":
					anthMessages = append(anthMessages, map[string]interface{}{
						"role":    "assistant",
						"content": responsesAssistantContent(m["content"]),
					})
				}
			case "function_call_output":
				// tool output → user message with a tool_result part
				anthMessages = append(anthMessages, map[string]interface{}{
					"role": "user",
					"content": []interface{}{map[string]interface{}{
						"type":        "tool_result",
						"tool_use_id": m["call_id"],
						"content":     m["output"],
					}},
				})
			}
		}
	}

	// Anthropic requires alternating roles; merge consecutive same-role
	// messages (e.g. back-to-back function_call_output items).
	var merged []interface{}
	for _, msg := range anthMessages {
		if m, ok := safeMap(msg); ok {
			appendMerged(&merged, m)
		}
	}

	// Anthropic also requires the conversation to END with a user message.
	// A Responses-API continuation that ends on an assistant turn (pending
	// tool calls, or a resumed conversation) needs a trailing empty user
	// message or the upstream rejects the request.
	if len(merged) > 0 {
		if last, ok := merged[len(merged)-1].(map[string]interface{}); ok &&
			safeStringOrDefault(last, "role", "") == "assistant" {
			merged = append(merged, map[string]interface{}{
				"role": "user",
				"content": []interface{}{map[string]interface{}{
					"type": "text",
					"text": "",
				}},
			})
		}
	} else {
		// Anthropic REQUIRES a messages array; an instructions-only (or
		// empty input) request gets a minimal user turn.
		merged = append(merged, map[string]interface{}{
			"role": "user",
			"content": []interface{}{map[string]interface{}{
				"type": "text",
				"text": "",
			}},
		})
	}

	if len(systemParts) > 0 {
		out["system"] = strings.Join(systemParts, "\n\n")
	}
	if len(merged) > 0 {
		out["messages"] = merged
	}

	return json.Marshal(out)
}

// responsesItemText returns the plain text of a message item (string content
// or concatenated text parts) — used for system/developer items.
func responsesItemText(m map[string]interface{}) string {
	switch c := m["content"].(type) {
	case string:
		return c
	case []interface{}:
		var texts []string
		for _, part := range c {
			if p, ok := safeMap(part); ok {
				if t, ok := safeString(p, "text"); ok {
					texts = append(texts, t)
				}
			}
		}
		return strings.Join(texts, "")
	}
	return ""
}

// responsesUserContent converts a user message item's content (string or
// input_text/input_image parts) into Anthropic content parts. Empty content
// yields an empty text block — Anthropic rejects null content, not empty.
func responsesUserContent(content interface{}) []interface{} {
	var parts []interface{}
	switch c := content.(type) {
	case string:
		return []interface{}{map[string]interface{}{"type": "text", "text": c}}
	case []interface{}:
		for _, part := range c {
			p, ok := safeMap(part)
			if !ok {
				continue
			}
			switch safeStringOrDefault(p, "type", "") {
			case "input_text", "output_text":
				parts = append(parts, map[string]interface{}{"type": "text", "text": p["text"]})
			case "input_image":
				parts = append(parts, responsesImageToAnthropic(p))
			}
		}
	}
	if len(parts) == 0 {
		return []interface{}{map[string]interface{}{"type": "text", "text": ""}}
	}
	return parts
}

// responsesImageToAnthropic converts an input_image part into an Anthropic
// image block (data URIs → base64 source, http(s) URLs → url source).
func responsesImageToAnthropic(p map[string]interface{}) interface{} {
	url := responsesImageURL(p)
	if strings.HasPrefix(url, "data:") {
		parts := strings.SplitN(url, ",", 2)
		if len(parts) == 2 {
			mediaType := strings.TrimPrefix(parts[0], "data:")
			mediaType = strings.Split(mediaType, ";")[0]
			if mediaType == "" {
				mediaType = "image/png"
			}
			return map[string]interface{}{
				"type": "image",
				"source": map[string]interface{}{
					"type":       "base64",
					"media_type": mediaType,
					"data":       parts[1],
				},
			}
		}
	}
	return map[string]interface{}{
		"type": "image",
		"source": map[string]interface{}{
			"type": "url",
			"url":  url,
		},
	}
}

// responsesAssistantContent converts an assistant message item's content
// (output_text parts and function_call parts) into Anthropic content blocks
// (text and tool_use). The tool_use id is the function_call's CALL id — that
// is what function_call_output items reference when the client reports tool
// results.
func responsesAssistantContent(content interface{}) []interface{} {
	var parts []interface{}
	switch c := content.(type) {
	case string:
		return []interface{}{map[string]interface{}{"type": "text", "text": c}}
	case []interface{}:
		for _, part := range c {
			p, ok := safeMap(part)
			if !ok {
				continue
			}
			switch safeStringOrDefault(p, "type", "") {
			case "output_text", "input_text":
				parts = append(parts, map[string]interface{}{"type": "text", "text": p["text"]})
			case "function_call":
				// arguments is a JSON string; Anthropic input must be an object
				parts = append(parts, map[string]interface{}{
					"type":  "tool_use",
					"id":    firstNonEmpty(p["call_id"], p["id"]),
					"name":  p["name"],
					"input": parseArgumentsObject(p["arguments"]),
				})
			}
		}
	}
	if len(parts) == 0 {
		return []interface{}{map[string]interface{}{"type": "text", "text": ""}}
	}
	return parts
}

// firstNonEmpty returns the first non-empty string value.
func firstNonEmpty(vals ...interface{}) interface{} {
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
		if v != nil {
			return v
		}
	}
	return nil
}

// jsonString marshals v into a JSON string (or "{}" on failure).
func jsonString(v interface{}) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ResponsesStreamConverter converts an upstream SSE stream (OpenAI chat
// completions chunks or Anthropic Messages events) into Responses API
// streaming events (response.created, response.output_text.delta,
// response.function_call_arguments.delta, response.completed, ...). It is
// stateful: deltas are accumulated so the "done" events and the final
// response.completed object carry complete text/arguments.
type ResponsesStreamConverter struct {
	upstream  string // "openai" (chat chunks) or "anthropic" (Messages events)
	modelName string
	respID    string
	createdAt int64

	started  bool
	closed   bool // output items closed; stream waits for the usage chunk
	finished bool // response.completed emitted
	errored  bool

	items       []*responsesItem
	msgItem     *responsesItem // the (single) message output item
	msgBlockIdx int            // anthropic content block index of the text block
	reasonItem  *responsesItem // chat mode: the single reasoning item
	// chat mode: tool_call index → function_call item
	chatTools map[int]*responsesItem
	// anthropic mode: content block index → function_call/reasoning item
	blockTools map[int]*responsesItem

	// usage (Responses semantics: input_tokens INCLUDES cached tokens)
	inputTokens  int64
	outputTokens int64
	cachedTokens int64
	hasUsage     bool

	// stopReason captured from the upstream finish chunk / message_delta
	stopReason string
}

type responsesItem struct {
	typ    string // "message" | "function_call" | "reasoning"
	id     string
	outIdx int
	added  bool // output_item.added emitted
	done   bool

	// message item
	partOpened bool
	partIdx    int
	parts      []map[string]interface{} // closed output_text parts
	curText    string

	// function_call item
	callID string
	name   string
	args   string

	// reasoning item
	summary string
}

// NewResponsesStreamConverter creates a fresh converter for the given
// upstream protocol ("openai" = chat completions chunks, "anthropic" =
// Messages events).
func NewResponsesStreamConverter(upstream string) *ResponsesStreamConverter {
	return &ResponsesStreamConverter{
		upstream:   upstream,
		respID:     fmt.Sprintf("resp_%d", time.Now().UnixNano()),
		createdAt:  time.Now().Unix(),
		chatTools:  make(map[int]*responsesItem),
		blockTools: make(map[int]*responsesItem),
	}
}

// SetModel sets the model name stamped on the synthesized response objects.
func (c *ResponsesStreamConverter) SetModel(name string) { c.modelName = name }

// Finished reports whether response.completed has been emitted.
func (c *ResponsesStreamConverter) Finished() bool { return c.finished }

// Errored reports whether a mid-stream upstream error was surfaced.
func (c *ResponsesStreamConverter) Errored() bool { return c.errored }

// SetUsage supplies token usage parsed from a full (non-SSE) upstream body,
// so the synthesized response.completed carries real numbers.
func (c *ResponsesStreamConverter) SetUsage(promptTokens, completionTokens, cachedTokens int64) {
	c.inputTokens = promptTokens
	c.outputTokens = completionTokens
	c.cachedTokens = cachedTokens
	c.hasUsage = true
}

// begin emits the leading lifecycle events (once): response.created and
// response.in_progress.
func (c *ResponsesStreamConverter) begin() [][]byte {
	if c.started {
		return nil
	}
	c.started = true
	created, _ := json.Marshal(map[string]interface{}{
		"type":     "response.created",
		"response": c.responseObject("in_progress", nil, nil, nil),
	})
	inProgress, _ := json.Marshal(map[string]interface{}{
		"type":     "response.in_progress",
		"response": c.responseObject("in_progress", nil, nil, nil),
	})
	return [][]byte{created, inProgress}
}

// responseObject builds the response object carried by lifecycle events.
// usage nil → reported as null (matching the real API: only
// response.completed carries usage).
func (c *ResponsesStreamConverter) responseObject(status string, incomplete interface{}, output []interface{}, usage map[string]interface{}) map[string]interface{} {
	if output == nil {
		output = []interface{}{}
	}
	return map[string]interface{}{
		"id":                   c.respID,
		"object":               "response",
		"created_at":           c.createdAt,
		"status":               status,
		"model":                c.modelName,
		"output":               output,
		"error":                nil,
		"incomplete_details":   incomplete,
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
		"usage":                usage,
		"user":                 nil,
	}
}

// Convert converts a single upstream frame (chat chunk or Anthropic event)
// into 0+ Responses API events.
func (c *ResponsesStreamConverter) Convert(data []byte) ([][]byte, error) {
	leading := c.begin()
	if c.finished {
		return leading, ErrSkipChunk
	}
	var ev map[string]interface{}
	if err := json.Unmarshal(data, &ev); err != nil {
		return leading, err
	}
	if c.upstream == "anthropic" {
		// A full message object (the upstream ignored stream:true) is
		// converted by synthesizing the equivalent stream events.
		if safeStringOrDefault(ev, "type", "") == "message" {
			events, err := c.anthropicFullMessage(ev)
			return append(leading, events...), err
		}
		events, err := c.anthropicEvent(ev)
		return append(leading, events...), err
	}
	events, err := c.chatChunk(ev)
	return append(leading, events...), err
}

// chatChunk converts one OpenAI chat.completion.chunk into Responses events.
func (c *ResponsesStreamConverter) chatChunk(ev map[string]interface{}) ([][]byte, error) {
	// Mid-stream error frame ({"error": ...}) must surface as an error event,
	// never as a clean response.completed. The Responses API's streaming
	// error event carries code/message at the TOP level (not nested).
	if errField, has := ev["error"]; has {
		c.errored = true
		c.finished = true
		msg := "upstream stream error"
		if m, ok := safeMap(errField); ok {
			msg = safeStringOrDefault(m, "message", msg)
		}
		errEv, _ := json.Marshal(map[string]interface{}{
			"type":    "error",
			"code":    "stream_error",
			"message": msg,
			"param":   nil,
		})
		return [][]byte{errEv}, nil
	}

	var events [][]byte

	// Usage may arrive on the finish chunk or on a separate usage-only chunk
	// AFTER it (include_usage), but a non-conforming gateway may send it
	// earlier too — capture it on any frame (a pre-finish frame just records
	// the numbers; completed is only emitted once the items are closed).
	if u, ok := safeMap(ev["usage"]); ok {
		c.captureChatUsage(u)
		if c.closed {
			return append(events, c.completedEvents()...), nil
		}
	}
	// Nothing valid may follow the finish chunk (before its usage chunk).
	if c.closed {
		return events, ErrSkipChunk
	}

	choices, ok := safeArr(ev, "choices")
	if !ok || len(choices) == 0 {
		return events, ErrSkipChunk
	}
	choice, ok := safeMap(choices[0])
	if !ok {
		return events, ErrSkipChunk
	}

	if delta, ok := safeMap(choice["delta"]); ok {
		if rc, ok := safeString(delta, "reasoning_content"); ok && rc != "" {
			events = append(events, c.reasoningDelta(rc)...)
		}
		if content, ok := safeString(delta, "content"); ok && content != "" {
			events = append(events, c.textDelta(content)...)
		}
		if tcs, ok := safeArr(delta, "tool_calls"); ok {
			for i, tc := range tcs {
				events = append(events, c.chatToolDelta(i, tc)...)
			}
		}
	}

	if fr, ok := safeString(choice, "finish_reason"); ok && fr != "" {
		c.stopReason = fr
		events = append(events, c.closeAll()...)
		c.closed = true
		if c.hasUsage {
			events = append(events, c.completedEvents()...)
		}
	}

	if len(events) == 0 {
		return events, ErrSkipChunk
	}
	return events, nil
}

// anthropicEvent converts one Anthropic Messages stream event into Responses
// events.
func (c *ResponsesStreamConverter) anthropicEvent(ev map[string]interface{}) ([][]byte, error) {
	switch safeStringOrDefault(ev, "type", "") {
	case "message_start":
		if msg, ok := safeMap(ev["message"]); ok {
			if u, ok := safeMap(msg["usage"]); ok {
				c.captureAnthUsage(u)
			}
		}
		return nil, ErrSkipChunk

	case "content_block_start":
		cb, ok := safeMap(ev["content_block"])
		if !ok {
			return nil, ErrSkipChunk
		}
		blockIdx := jsonIndex(ev["index"])
		switch safeStringOrDefault(cb, "type", "") {
		case "text":
			if c.msgItem == nil {
				c.msgItem = c.newItem("message")
			}
			if !c.msgItem.partOpened {
				c.msgItem.partOpened = true
				c.msgBlockIdx = blockIdx
				return append(c.outputItemAdded(c.msgItem), c.contentPartAdded(c.msgItem)...), nil
			}
		case "tool_use":
			it := c.newItem("function_call")
			if id, ok := safeString(cb, "id"); ok && id != "" {
				it.id = id
				it.callID = id
			}
			it.name = safeStringOrDefault(cb, "name", "")
			c.blockTools[blockIdx] = it
			return c.outputItemAdded(it), nil
		case "thinking":
			it := c.newItem("reasoning")
			c.blockTools[blockIdx] = it
			return c.outputItemAdded(it), nil
		}
		return nil, ErrSkipChunk

	case "content_block_delta":
		delta, ok := safeMap(ev["delta"])
		if !ok {
			return nil, ErrSkipChunk
		}
		blockIdx := jsonIndex(ev["index"])
		switch safeStringOrDefault(delta, "type", "") {
		case "text_delta":
			text, ok := safeString(delta, "text")
			if !ok || text == "" {
				return nil, ErrSkipChunk
			}
			// The part may open without its content_block_start (missed
			// frame) — record which block owns it so the matching stop can
			// close it.
			c.msgBlockIdx = blockIdx
			return c.textDelta(text), nil
		case "input_json_delta":
			it := c.blockTools[blockIdx]
			if it == nil {
				return nil, ErrSkipChunk
			}
			partial, _ := delta["partial_json"].(string)
			if partial == "" {
				return nil, ErrSkipChunk
			}
			it.args += partial
			ev2, _ := json.Marshal(map[string]interface{}{
				"type":         "response.function_call_arguments.delta",
				"item_id":      it.id,
				"output_index": it.outIdx,
				"delta":        partial,
			})
			return [][]byte{ev2}, nil
		case "thinking_delta":
			it := c.blockTools[blockIdx]
			if it == nil {
				return nil, ErrSkipChunk
			}
			text, _ := delta["thinking"].(string)
			if text == "" {
				return nil, ErrSkipChunk
			}
			it.summary += text
			return c.reasoningDeltaFor(it, text), nil
		}
		return nil, ErrSkipChunk

	case "content_block_stop":
		// The text block closes the message item's CURRENT part only — the
		// item itself stays open for further parts (multiple text blocks)
		// and is closed at message_stop. Check the text block's index so an
		// out-of-order stop can't close the wrong item.
		if c.msgItem != nil && !c.msgItem.done && c.msgItem.partOpened &&
			jsonIndex(ev["index"]) == c.msgBlockIdx {
			return c.closeMessagePart(), nil
		}
		if it := c.blockTools[jsonIndex(ev["index"])]; it != nil {
			return c.closeToolItem(it), nil
		}
		return nil, ErrSkipChunk

	case "message_delta":
		if delta, ok := safeMap(ev["delta"]); ok {
			if sr, ok := safeString(delta, "stop_reason"); ok && sr != "" {
				c.stopReason = sr
			}
		}
		if u, ok := safeMap(ev["usage"]); ok {
			if ot, ok := u["output_tokens"].(float64); ok {
				c.outputTokens = int64(ot)
				c.hasUsage = true
			}
		}
		return nil, ErrSkipChunk

	case "message_stop":
		// Close the message item (open part + output_item.done) and any
		// tool/reasoning blocks that never got their stop (non-conforming
		// upstream), in output order, then complete.
		var events [][]byte
		for _, it := range c.items {
			if it.done {
				continue
			}
			if it == c.msgItem {
				events = append(events, c.closeMessageItem()...)
			} else {
				events = append(events, c.closeToolItem(it)...)
			}
		}
		return append(events, c.completedEvents()...), nil

	case "error":
		c.errored = true
		c.finished = true
		errBlock, _ := safeMap(ev["error"])
		msg := safeStringOrDefault(errBlock, "message", "upstream stream error")
		errEv, _ := json.Marshal(map[string]interface{}{
			"type":    "error",
			"code":    "stream_error",
			"message": msg,
			"param":   nil,
		})
		return [][]byte{errEv}, nil

	case "ping":
		return nil, ErrSkipChunk
	}
	return nil, ErrSkipChunk
}

// anthropicFullMessage converts a complete Anthropic message object (a
// non-SSE upstream that ignored stream:true) by synthesizing the stream event
// sequence (message_start → blocks → message_delta → message_stop) and
// running it through the regular event conversion.
func (c *ResponsesStreamConverter) anthropicFullMessage(ev map[string]interface{}) ([][]byte, error) {
	synthetic := []map[string]interface{}{}
	start := map[string]interface{}{"type": "message_start"}
	if u, ok := safeMap(ev["usage"]); ok {
		start["message"] = map[string]interface{}{"usage": u}
	}
	synthetic = append(synthetic, start)

	blocks, _ := safeArr(ev, "content")
	for i, b := range blocks {
		block, ok := safeMap(b)
		if !ok {
			continue
		}
		idx := i
		synthetic = append(synthetic, map[string]interface{}{
			"type":          "content_block_start",
			"index":         idx,
			"content_block": block,
		})
		switch safeStringOrDefault(block, "type", "") {
		case "text":
			if t, ok := safeString(block, "text"); ok && t != "" {
				synthetic = append(synthetic, map[string]interface{}{
					"type":  "content_block_delta",
					"index": idx,
					"delta": map[string]interface{}{"type": "text_delta", "text": t},
				})
			}
		case "tool_use":
			synthetic = append(synthetic, map[string]interface{}{
				"type":  "content_block_delta",
				"index": idx,
				"delta": map[string]interface{}{"type": "input_json_delta", "partial_json": jsonString(block["input"])},
			})
		case "thinking":
			if t, ok := safeString(block, "thinking"); ok && t != "" {
				synthetic = append(synthetic, map[string]interface{}{
					"type":  "content_block_delta",
					"index": idx,
					"delta": map[string]interface{}{"type": "thinking_delta", "thinking": t},
				})
			}
		}
		synthetic = append(synthetic, map[string]interface{}{"type": "content_block_stop", "index": idx})
	}

	delta := map[string]interface{}{
		"type":  "message_delta",
		"delta": map[string]interface{}{"stop_reason": safeStringOrDefault(ev, "stop_reason", "end_turn")},
	}
	if u, ok := safeMap(ev["usage"]); ok {
		delta["usage"] = map[string]interface{}{"output_tokens": u["output_tokens"]}
	}
	synthetic = append(synthetic, delta, map[string]interface{}{"type": "message_stop"})

	var out [][]byte
	for _, e := range synthetic {
		converted, err := c.anthropicEvent(e)
		if err != nil && err != ErrSkipChunk {
			return out, err
		}
		out = append(out, converted...)
	}
	return out, nil
}

// CloseStream emits the events that terminate a stream which ended without a
// natural finish (EOF before the usage chunk or before message_stop): opens
// the lifecycle events if nothing was ever converted, closes any open output
// items and always emits response.completed.
func (c *ResponsesStreamConverter) CloseStream() [][]byte {
	if c.finished {
		return nil
	}
	var events [][]byte
	events = append(events, c.begin()...)
	if !c.closed {
		events = append(events, c.closeAll()...)
		c.closed = true
	}
	events = append(events, c.completedEvents()...)
	return events
}

// completedEvents emits response.completed with the final output items and
// usage (Responses semantics: input_tokens includes cached tokens).
func (c *ResponsesStreamConverter) completedEvents() [][]byte {
	c.finished = true
	output := make([]interface{}, 0, len(c.items))
	for _, it := range c.items {
		output = append(output, it.finalItem())
	}
	status := "completed"
	var incomplete interface{}
	var failedError interface{}
	switch c.stopReason {
	case "max_tokens", "model_context_window_exceeded", "length":
		status = "incomplete"
		incomplete = map[string]interface{}{"reason": "max_output_tokens"}
	case "content_filter":
		// chat completions content_filter → refusal-style failure
		status = "failed"
		failedError = map[string]interface{}{
			"code":    "content_filter",
			"message": "The model produced content that was filtered",
			"type":    "server_error",
		}
	case "refusal":
		status = "failed"
		failedError = map[string]interface{}{
			"code":    "refusal",
			"message": "The model refused to respond",
			"type":    "server_error",
		}
	}
	var usage map[string]interface{}
	if c.hasUsage {
		usage = map[string]interface{}{
			"input_tokens": c.inputTokens,
			"input_tokens_details": map[string]interface{}{
				"cached_tokens": c.cachedTokens,
			},
			"output_tokens": c.outputTokens,
			"output_tokens_details": map[string]interface{}{
				"reasoning_tokens": 0,
			},
			"total_tokens": c.inputTokens + c.outputTokens,
		}
	}
	resp := c.responseObject(status, incomplete, output, usage)
	resp["error"] = failedError
	ev, _ := json.Marshal(map[string]interface{}{
		"type":     "response.completed",
		"response": resp,
	})
	return [][]byte{ev}
}

// captureChatUsage extracts usage from a chat chunk (OpenAI convention:
// prompt_tokens includes cached tokens).
func (c *ResponsesStreamConverter) captureChatUsage(u map[string]interface{}) {
	if pt, ok := u["prompt_tokens"].(float64); ok {
		c.inputTokens = int64(pt)
	}
	if ct, ok := u["completion_tokens"].(float64); ok {
		c.outputTokens = int64(ct)
	}
	if d, ok := safeMap(u["prompt_tokens_details"]); ok {
		if ct, ok := d["cached_tokens"].(float64); ok {
			c.cachedTokens = int64(ct)
		}
	}
	c.hasUsage = true
}

// captureAnthUsage extracts usage from an Anthropic message_start/message
// object. Anthropic's input_tokens EXCLUDE cache tokens; the Responses API's
// input_tokens include them (with cached broken out in details) — match the
// OpenAI convention like the rest of the relay does.
func (c *ResponsesStreamConverter) captureAnthUsage(u map[string]interface{}) {
	var in, cacheRead, cacheWrite, out int64
	if v, ok := u["input_tokens"].(float64); ok {
		in = int64(v)
	}
	if v, ok := u["cache_read_input_tokens"].(float64); ok {
		cacheRead = int64(v)
	}
	if v, ok := u["cache_creation_input_tokens"].(float64); ok {
		cacheWrite = int64(v)
	}
	if v, ok := u["output_tokens"].(float64); ok {
		out = int64(v)
	}
	c.inputTokens = in + cacheRead + cacheWrite
	c.cachedTokens = cacheRead + cacheWrite
	c.outputTokens = out
	c.hasUsage = true
}

// textDelta opens (and adds) the message item + its first content part on the
// first call, then emits response.output_text.delta.
func (c *ResponsesStreamConverter) textDelta(content string) [][]byte {
	if c.msgItem == nil {
		c.msgItem = c.newItem("message")
	}
	var events [][]byte
	if !c.msgItem.partOpened {
		c.msgItem.partOpened = true
		events = append(events, c.outputItemAdded(c.msgItem)...)
		events = append(events, c.contentPartAdded(c.msgItem)...)
	}
	c.msgItem.curText += content
	ev, _ := json.Marshal(map[string]interface{}{
		"type":          "response.output_text.delta",
		"item_id":       c.msgItem.id,
		"output_index":  c.msgItem.outIdx,
		"content_index": c.msgItem.partIdx,
		"delta":         content,
	})
	return append(events, ev)
}

// chatToolDelta handles one chat tool_calls fragment: the first fragment for
// a tool emits output_item.added (function_call), later fragments emit
// response.function_call_arguments.delta.
func (c *ResponsesStreamConverter) chatToolDelta(arrIdx int, tc interface{}) [][]byte {
	m, ok := safeMap(tc)
	if !ok {
		return nil
	}
	// Some gateways omit "index"; fall back to the array position
	idx := arrIdx
	if f, has := m["index"].(float64); has {
		idx = int(f)
	}
	it := c.chatTools[idx]
	if it == nil {
		it = c.newItem("function_call")
		if callID, ok := safeString(m, "id"); ok && callID != "" {
			it.id = callID
			it.callID = callID
		}
		if fn, ok := safeMap(m["function"]); ok {
			it.name = safeStringOrDefault(fn, "name", "")
		}
		c.chatTools[idx] = it
		events := c.outputItemAdded(it)
		// Some gateways send the whole tool call in ONE fragment (id + name
		// + arguments) — emit the arguments delta from the first fragment.
		if fn, ok := safeMap(m["function"]); ok {
			if args, ok := safeString(fn, "arguments"); ok && args != "" {
				events = append(events, c.toolArgsDelta(it, args)...)
			}
		}
		return events
	}
	// Some gateways delay the tool id/name to a later fragment — pick up
	// the CALL id and name (the announced item id stays stable: clients
	// correlate events by item_id).
	var events [][]byte
	if callID, ok := safeString(m, "id"); ok && callID != "" && it.callID == "" {
		it.callID = callID
	}
	if fn, ok := safeMap(m["function"]); ok {
		if it.name == "" {
			it.name = safeStringOrDefault(fn, "name", "")
		}
		if args, ok := safeString(fn, "arguments"); ok && args != "" {
			events = append(events, c.toolArgsDelta(it, args)...)
		}
	}
	return events
}

// toolArgsDelta accumulates a tool-call arguments fragment and emits
// response.function_call_arguments.delta.
func (c *ResponsesStreamConverter) toolArgsDelta(it *responsesItem, args string) [][]byte {
	it.args += args
	ev, _ := json.Marshal(map[string]interface{}{
		"type":         "response.function_call_arguments.delta",
		"item_id":      it.id,
		"output_index": it.outIdx,
		"delta":        args,
	})
	return [][]byte{ev}
}

// reasoningDelta accumulates chat-mode reasoning deltas (single reasoning
// item) into response.reasoning_summary_text.delta events.
func (c *ResponsesStreamConverter) reasoningDelta(delta string) [][]byte {
	if c.reasonItem == nil {
		c.reasonItem = c.newItem("reasoning")
	}
	c.reasonItem.summary += delta
	return c.reasoningDeltaFor(c.reasonItem, delta)
}

// reasoningDeltaFor emits response.reasoning_summary_text.delta for the
// given reasoning item (opening it with output_item.added on first use).
func (c *ResponsesStreamConverter) reasoningDeltaFor(it *responsesItem, delta string) [][]byte {
	var events [][]byte
	if it != nil && !it.added {
		events = append(events, c.outputItemAdded(it)...)
	}
	ev, _ := json.Marshal(map[string]interface{}{
		"type":          "response.reasoning_summary_text.delta",
		"item_id":       it.id,
		"output_index":  it.outIdx,
		"summary_index": 0,
		"delta":         delta,
	})
	return append(events, ev)
}

// closeAll closes every open output item IN OUTPUT ORDER (a tool/reasoning
// item created before the message item must close first — clients expect
// done events in output_index order). The message item's open part is
// closed together with the item.
func (c *ResponsesStreamConverter) closeAll() [][]byte {
	var events [][]byte
	for _, it := range c.items {
		if it.done {
			continue
		}
		if it == c.msgItem {
			events = append(events, c.closeMessageItem()...)
		} else {
			events = append(events, c.closeToolItem(it)...)
		}
	}
	return events
}

// closeMessagePart closes the message item's open content part only
// (output_text.done, content_part.done). The item stays open — in Anthropic
// mode a message may carry several text blocks, and the item is closed at
// message_stop.
func (c *ResponsesStreamConverter) closeMessagePart() [][]byte {
	it := c.msgItem
	if it == nil || !it.partOpened {
		return nil
	}
	it.partOpened = false
	text := it.curText
	it.curText = ""
	it.parts = append(it.parts, outputTextPart(text))
	idx := it.partIdx
	it.partIdx++

	done, _ := json.Marshal(map[string]interface{}{
		"type":          "response.output_text.done",
		"item_id":       it.id,
		"output_index":  it.outIdx,
		"content_index": idx,
		"text":          text,
	})
	partDone, _ := json.Marshal(map[string]interface{}{
		"type":          "response.content_part.done",
		"item_id":       it.id,
		"output_index":  it.outIdx,
		"content_index": idx,
		"part":          outputTextPart(text),
	})
	return [][]byte{done, partDone}
}

// closeMessageItem closes the message item's open part (if any) and then
// the item itself (output_item.done).
func (c *ResponsesStreamConverter) closeMessageItem() [][]byte {
	if c.msgItem == nil || c.msgItem.done {
		return nil
	}
	events := c.closeMessagePart()
	return append(events, c.closeItem(c.msgItem)...)
}

// closeToolItem closes a function_call item (function_call_arguments.done +
// output_item.done) or a reasoning item (reasoning_summary_text.done +
// output_item.done).
func (c *ResponsesStreamConverter) closeToolItem(it *responsesItem) [][]byte {
	if it == nil || it.done {
		return nil
	}
	var events [][]byte
	if it.typ == "function_call" {
		done, _ := json.Marshal(map[string]interface{}{
			"type":         "response.function_call_arguments.done",
			"item_id":      it.id,
			"output_index": it.outIdx,
			"arguments":    it.args,
		})
		events = append(events, done)
	} else if it.typ == "reasoning" {
		done, _ := json.Marshal(map[string]interface{}{
			"type":          "response.reasoning_summary_text.done",
			"item_id":       it.id,
			"output_index":  it.outIdx,
			"summary_index": 0,
			"text":          it.summary,
		})
		events = append(events, done)
	}
	return append(events, c.closeItem(it)...)
}

// closeItem emits output_item.done with the item's final shape.
func (c *ResponsesStreamConverter) closeItem(it *responsesItem) [][]byte {
	if it.done {
		return nil
	}
	it.done = true
	ev, _ := json.Marshal(map[string]interface{}{
		"type":         "response.output_item.done",
		"output_index": it.outIdx,
		"item":         it.finalItem(),
	})
	return [][]byte{ev}
}

// outputItemAdded emits response.output_item.added (once per item).
func (c *ResponsesStreamConverter) outputItemAdded(it *responsesItem) [][]byte {
	if it.added {
		return nil
	}
	it.added = true
	var item map[string]interface{}
	switch it.typ {
	case "message":
		item = map[string]interface{}{
			"id": it.id, "type": "message", "status": "in_progress",
			"role": "assistant", "content": []interface{}{},
		}
	case "function_call":
		item = map[string]interface{}{
			"id": it.id, "type": "function_call", "status": "in_progress",
			"call_id": it.callID, "name": it.name, "arguments": "",
		}
	case "reasoning":
		item = map[string]interface{}{
			"id": it.id, "type": "reasoning", "status": "in_progress",
			"summary": []interface{}{},
		}
	}
	ev, _ := json.Marshal(map[string]interface{}{
		"type":         "response.output_item.added",
		"output_index": it.outIdx,
		"item":         item,
	})
	return [][]byte{ev}
}

// contentPartAdded emits response.content_part.added for the item's current
// content index.
func (c *ResponsesStreamConverter) contentPartAdded(it *responsesItem) [][]byte {
	ev, _ := json.Marshal(map[string]interface{}{
		"type":          "response.content_part.added",
		"item_id":       it.id,
		"output_index":  it.outIdx,
		"content_index": it.partIdx,
		"part":          outputTextPart(""),
	})
	return [][]byte{ev}
}

// finalItem returns the item's completed shape (used by output_item.done and
// the response.completed output array).
func (it *responsesItem) finalItem() map[string]interface{} {
	switch it.typ {
	case "message":
		return map[string]interface{}{
			"id": it.id, "type": "message", "status": "completed",
			"role": "assistant", "content": it.finalParts(),
		}
	case "function_call":
		return map[string]interface{}{
			"id": it.id, "type": "function_call", "status": "completed",
			"call_id": it.callID, "name": it.name, "arguments": it.args,
		}
	case "reasoning":
		return map[string]interface{}{
			"id": it.id, "type": "reasoning", "status": "completed",
			"summary": []interface{}{map[string]interface{}{
				"type": "summary_text", "text": it.summary,
			}},
		}
	}
	return map[string]interface{}{}
}

// finalParts returns the message item's closed parts plus any still-open
// part's text.
func (it *responsesItem) finalParts() []interface{} {
	parts := make([]interface{}, 0, len(it.parts)+1)
	for _, p := range it.parts {
		parts = append(parts, p)
	}
	if it.partOpened && it.curText != "" {
		parts = append(parts, outputTextPart(it.curText))
	}
	return parts
}

// newItem appends a new output item, assigning its output index and id.
func (c *ResponsesStreamConverter) newItem(typ string) *responsesItem {
	it := &responsesItem{typ: typ, outIdx: len(c.items)}
	switch typ {
	case "message":
		it.id = fmt.Sprintf("msg_%d", len(c.items)+1)
	case "function_call":
		it.id = fmt.Sprintf("fc_%d", len(c.items)+1)
	case "reasoning":
		it.id = fmt.Sprintf("rs_%d", len(c.items)+1)
	}
	c.items = append(c.items, it)
	return it
}

// outputTextPart builds a message content part in the Responses shape.
func outputTextPart(text string) map[string]interface{} {
	return map[string]interface{}{
		"type":        "output_text",
		"text":        text,
		"annotations": []interface{}{},
	}
}

// jsonIndex converts a JSON-decoded array/block index to an int.
func jsonIndex(v interface{}) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}
