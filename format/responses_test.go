package format

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---- Request conversions ----

func TestResponsesRequestToChatCompletion(t *testing.T) {
	body := `{
		"model": "gpt-5",
		"instructions": "You are helpful.",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "message", "role": "assistant", "content": [
				{"type": "output_text", "text": "Hello!"},
				{"type": "function_call", "id": "fc_1", "call_id": "call_9", "name": "get_weather", "arguments": "{\"city\":\"SF\"}"}
			]},
			{"type": "function_call_output", "call_id": "call_9", "output": "72F"}
		],
		"max_output_tokens": 512,
		"temperature": 0.5,
		"stream": true,
		"text": {"format": {"type": "json_schema", "name": "weather", "schema": {"type": "object"}, "strict": true}},
		"store": false,
		"previous_response_id": "resp_123"
	}`

	out, err := ResponsesRequestToChatCompletion([]byte(body), "gpt-5-upstream")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}

	if req["model"] != "gpt-5-upstream" {
		t.Errorf("model = %v, want gpt-5-upstream", req["model"])
	}
	if req["max_tokens"] != float64(512) {
		t.Errorf("max_tokens = %v, want 512", req["max_tokens"])
	}
	if req["temperature"] != 0.5 {
		t.Errorf("temperature = %v, want 0.5", req["temperature"])
	}
	if req["stream"] != true {
		t.Errorf("stream = %v, want true", req["stream"])
	}
	// stream:true â†’ include_usage so converted streams report tokens
	so, ok := req["stream_options"].(map[string]interface{})
	if !ok || so["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage", req["stream_options"])
	}

	// responses-only fields must be dropped
	for _, k := range []string{"store", "previous_response_id", "input", "instructions", "max_output_tokens", "text"} {
		if _, has := req[k]; has {
			t.Errorf("responses-only field %q leaked into chat request", k)
		}
	}

	// messages: system (instructions) + user + assistant-with-tool-calls +
	// tool + padded trailing assistant (chat completions rejects a
	// conversation ending in a tool message)
	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 5 {
		t.Fatalf("messages = %d, want 5: %v", len(msgs), msgs)
	}
	sys := msgs[0].(map[string]interface{})
	if sys["role"] != "system" || sys["content"] != "You are helpful." {
		t.Errorf("system message = %v", sys)
	}
	usr := msgs[1].(map[string]interface{})
	content := usr["content"].([]interface{})
	if usr["role"] != "user" || content[0].(map[string]interface{})["text"] != "hi" {
		t.Errorf("user message = %v", usr)
	}
	asst := msgs[2].(map[string]interface{})
	if asst["role"] != "assistant" {
		t.Errorf("assistant role = %v", asst["role"])
	}
	tcs := asst["tool_calls"].([]interface{})
	tc := tcs[0].(map[string]interface{})
	if tc["id"] != "call_9" {
		t.Errorf("tool_call id = %v, want call_9 (call_id wins over part id)", tc["id"])
	}
	fn := tc["function"].(map[string]interface{})
	if fn["name"] != "get_weather" || fn["arguments"] != `{"city":"SF"}` {
		t.Errorf("tool_call function = %v", fn)
	}
	tool := msgs[3].(map[string]interface{})
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_9" || tool["content"] != "72F" {
		t.Errorf("tool message = %v", tool)
	}
	pad := msgs[4].(map[string]interface{})
	if pad["role"] != "assistant" || pad["content"] != "" {
		t.Errorf("trailing pad message = %v, want assistant with empty content", pad)
	}

	// text.format json_schema â†’ response_format
	rf, _ := req["response_format"].(map[string]interface{})
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format = %v, want json_schema", rf)
	}
	js, _ := rf["json_schema"].(map[string]interface{})
	if js["name"] != "weather" || js["strict"] != true {
		t.Errorf("response_format json_schema = %v", js)
	}
}

func TestResponsesRequestToChatCompletionStringInput(t *testing.T) {
	out, err := ResponsesRequestToChatCompletion([]byte(`{"model":"m","input":"just a string"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	json.Unmarshal(out, &req)
	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	m := msgs[0].(map[string]interface{})
	if m["role"] != "user" || m["content"] != "just a string" {
		t.Errorf("string input message = %v", m)
	}
}

func TestResponsesRequestToChatCompletionImageAndChoices(t *testing.T) {
	// input_image â†’ image_url part; tool_choice "required" + tools passthrough
	body := `{
		"model": "m",
		"input": [{"type": "message", "role": "user", "content": [
			{"type": "input_image", "image_url": "data:image/png;base64,AAAA"}
		]}],
		"tool_choice": "required",
		"tools": [{"type": "function", "function": {"name": "f", "description": "d", "parameters": {"type": "object"}}}]
	}`
	out, err := ResponsesRequestToChatCompletion([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	json.Unmarshal(out, &req)
	if req["tool_choice"] != "required" {
		t.Errorf("tool_choice = %v, want required", req["tool_choice"])
	}
	if tools, ok := req["tools"].([]interface{}); !ok || len(tools) != 1 {
		t.Errorf("tools = %v", req["tools"])
	}
	msgs, _ := req["messages"].([]interface{})
	content := msgs[0].(map[string]interface{})["content"].([]interface{})
	part := content[0].(map[string]interface{})
	if part["type"] != "image_url" {
		t.Fatalf("image part = %v", part)
	}
	iu := part["image_url"].(map[string]interface{})
	if iu["url"] != "data:image/png;base64,AAAA" {
		t.Errorf("image_url = %v", iu["url"])
	}
}

func TestResponsesRequestToAnthropic(t *testing.T) {
	body := `{
		"model": "claude-sonnet",
		"instructions": "Be concise.",
		"input": [
			{"type": "message", "role": "system", "content": "Follow the rules."},
			{"type": "message", "role": "user", "content": "Question?"},
			{"type": "message", "role": "assistant", "content": [
				{"type": "output_text", "text": "Answer:"},
				{"type": "function_call", "id": "fc_1", "call_id": "call_7", "name": "search", "arguments": "{\"q\":\"x\"}"}
			]},
			{"type": "function_call_output", "call_id": "call_7", "output": "result"},
			{"type": "function_call_output", "call_id": "call_7", "output": "more"}
		],
		"max_output_tokens": 100,
		"tool_choice": {"type": "function", "function": {"name": "search"}},
		"tools": [{"type": "function", "function": {"name": "search", "description": "d", "parameters": {"type": "object"}}}],
		"store": true,
		"stream": false
	}`

	out, err := ResponsesRequestToAnthropic([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}

	if req["model"] != "claude-sonnet" {
		t.Errorf("model = %v", req["model"])
	}
	if req["max_tokens"] != float64(100) {
		t.Errorf("max_tokens = %v, want 100", req["max_tokens"])
	}
	// instructions + system message join the system prompt
	if req["system"] != "Be concise.\n\nFollow the rules." {
		t.Errorf("system = %q", req["system"])
	}
	// responses-only fields dropped
	if _, has := req["store"]; has {
		t.Errorf("store leaked into anthropic request")
	}

	// tool_choice mapped: function â†’ tool
	tc, _ := req["tool_choice"].(map[string]interface{})
	if tc["type"] != "tool" || tc["name"] != "search" {
		t.Errorf("tool_choice = %v", req["tool_choice"])
	}
	// tools converted to anthropic schema
	tools, _ := req["tools"].([]interface{})
	tt := tools[0].(map[string]interface{})
	if tt["type"] != "custom" || tt["name"] != "search" || tt["input_schema"] == nil {
		t.Errorf("tool = %v", tt)
	}

	msgs, _ := req["messages"].([]interface{})
	// user, assistant (text + tool_use), then the two tool outputs MERGED
	// into a single user message (anthropic requires alternating roles)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3: %v", len(msgs), msgs)
	}
	if msgs[0].(map[string]interface{})["role"] != "user" {
		t.Errorf("msg[0] = %v", msgs[0])
	}
	asst := msgs[1].(map[string]interface{})
	if asst["role"] != "assistant" {
		t.Errorf("msg[1] = %v", msgs[1])
	}
	asstContent := asst["content"].([]interface{})
	if len(asstContent) != 2 {
		t.Fatalf("assistant content = %v", asstContent)
	}
	tu := asstContent[1].(map[string]interface{})
	if tu["type"] != "tool_use" || tu["id"] != "call_7" || tu["name"] != "search" {
		t.Errorf("tool_use block = %v", tu)
	}
	// arguments JSON string â†’ object
	input, _ := tu["input"].(map[string]interface{})
	if input["q"] != "x" {
		t.Errorf("tool_use input = %v, want parsed object", tu["input"])
	}
	// merged tool results
	last := msgs[2].(map[string]interface{})
	if last["role"] != "user" {
		t.Errorf("msg[2] = %v", msgs[2])
	}
	lastContent := last["content"].([]interface{})
	if len(lastContent) != 2 {
		t.Errorf("merged tool results = %d, want 2: %v", len(lastContent), lastContent)
	}
	tr := lastContent[0].(map[string]interface{})
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "call_7" {
		t.Errorf("tool_result = %v", tr)
	}
}

func TestResponsesRequestToAnthropicDefaultsAndTrailingAssistant(t *testing.T) {
	// no max_output_tokens â†’ required max_tokens defaults to 4096
	out, err := ResponsesRequestToAnthropic([]byte(`{"model":"m","input":"hello"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	json.Unmarshal(out, &req)
	if req["max_tokens"] != float64(4096) {
		t.Errorf("default max_tokens = %v, want 4096", req["max_tokens"])
	}

	// a conversation ending on an assistant turn needs a trailing user message
	out, err = ResponsesRequestToAnthropic([]byte(`{
		"model": "m",
		"input": [
			{"type": "message", "role": "user", "content": "hi"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "ok"}]}
		]
	}`), "")
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(out, &req)
	msgs, _ := req["messages"].([]interface{})
	last := msgs[len(msgs)-1].(map[string]interface{})
	if last["role"] != "user" {
		t.Errorf("last message = %v, want trailing user (anthropic requires user-ending)", last)
	}
}

func TestResponsesRequestToChatCompletionSystemItems(t *testing.T) {
	// system/developer input items (allowed anywhere in Responses input)
	// must be hoisted into the leading system message — chat completions
	// rejects mid-conversation system content and the developer role.
	body := `{
		"model": "m",
		"input": [
			{"type": "message", "role": "user", "content": "hi"},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "ok"}]},
			{"type": "message", "role": "system", "content": "remember the rules"},
			{"type": "message", "role": "developer", "content": "be terse"}
		]
	}`
	out, err := ResponsesRequestToChatCompletion([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	json.Unmarshal(out, &req)
	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3: %v", len(msgs), msgs)
	}
	first := msgs[0].(map[string]interface{})
	if first["role"] != "system" || first["content"] != "remember the rules\n\nbe terse" {
		t.Errorf("leading system = %v", first)
	}
	// no system/developer message may remain mid-conversation
	for i, m := range msgs {
		role := m.(map[string]interface{})["role"]
		if i > 0 && (role == "system" || role == "developer") {
			t.Errorf("system/developer item not hoisted at %d: %v", i, msgs)
		}
	}
}

func TestResponsesRequestToChatCompletionInstructionsOnly(t *testing.T) {
	out, err := ResponsesRequestToChatCompletion([]byte(`{"model":"m","instructions":"rules"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	json.Unmarshal(out, &req)
	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 1 || msgs[0].(map[string]interface{})["role"] != "system" {
		t.Errorf("messages = %v, want [system]", msgs)
	}
}

func TestResponsesRequestToAnthropicEmptyInput(t *testing.T) {
	// Anthropic REQUIRES a messages array — an instructions-only request
	// must still produce one
	out, err := ResponsesRequestToAnthropic([]byte(`{"model":"m","instructions":"rules"}`), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	json.Unmarshal(out, &req)
	if req["system"] != "rules" {
		t.Errorf("system = %v", req["system"])
	}
	msgs, ok := req["messages"].([]interface{})
	if !ok || len(msgs) != 1 {
		t.Fatalf("messages = %v, want a minimal user turn", req["messages"])
	}
	if msgs[0].(map[string]interface{})["role"] != "user" {
		t.Errorf("msg[0] = %v", msgs[0])
	}
}

// ---- Streaming converter: chat completions upstream ----

func decodeEvents(t *testing.T, raw [][]byte) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, b := range raw {
		var m map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("bad event json %q: %v", b, err)
		}
		out = append(out, m)
	}
	return out
}

func eventTypes(t *testing.T, raw [][]byte) []string {
	t.Helper()
	var types []string
	for _, m := range decodeEvents(t, raw) {
		types = append(types, m["type"].(string))
	}
	return types
}

// TestResponsesStreamConverterFromChat drives a full chat-completions stream
// (content, tool calls, finish, usage) and asserts the Responses event
// sequence a codex/chatgpt-style client would consume.
func TestResponsesStreamConverterFromChat(t *testing.T) {
	conv := NewResponsesStreamConverter("openai")
	conv.SetModel("gpt-5")

	chunk := func(s string) []byte { return []byte(s) }

	// 1. text delta, closes over created + in_progress + message item
	evs, err := conv.Convert(chunk(`{"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(t, evs)
	if strings.Join(types, ",") != "response.created,response.in_progress,response.output_item.added,response.content_part.added,response.output_text.delta" {
		t.Fatalf("events after first chunk = %v", types)
	}

	// 2. more text
	evs, err = conv.Convert(chunk(`{"id":"c2","choices":[{"index":0,"delta":{"content":" world"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if types = eventTypes(t, evs); len(types) != 1 || types[0] != "response.output_text.delta" {
		t.Fatalf("events after second chunk = %v", types)
	}

	// 3. first tool_call fragment, id/name present
	evs, err = conv.Convert(chunk(`{"id":"c3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"f","arguments":""}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if types = eventTypes(t, evs); len(types) != 1 || types[0] != "response.output_item.added" {
		t.Fatalf("events after tool fragment = %v", types)
	}
	item := decodeEvents(t, evs)[0]["item"].(map[string]interface{})
	if item["type"] != "function_call" || item["call_id"] != "call_1" || item["name"] != "f" {
		t.Errorf("function_call item = %v", item)
	}

	// 4. arguments fragment
	evs, err = conv.Convert(chunk(`{"id":"c4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":"}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if types = eventTypes(t, evs); len(types) != 1 || types[0] != "response.function_call_arguments.delta" {
		t.Fatalf("events after args fragment = %v", types)
	}

	// 5. finish chunk with reasoning delta: reasoning item opens, then
	// close text/tool items
	evs, err = conv.Convert(chunk(`{"id":"c5","choices":[{"index":0,"delta":{"reasoning_content":"think step 1"},"finish_reason":"tool_calls"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"response.output_item.added", "response.reasoning_summary_text.delta",
		"response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.function_call_arguments.done", "response.output_item.done",
		"response.reasoning_summary_text.done", "response.output_item.done",
	}
	if types = eventTypes(t, evs); strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("events after finish chunk = %v\nwant %v", types, want)
	}
	if conv.Finished() {
		t.Fatal("converter finished before the usage chunk")
	}

	// 6. usage-only chunk → response.completed with output + usage
	evs, err = conv.Convert(chunk(`{"id":"c6","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":9,"total_tokens":16}}`))
	if err != nil {
		t.Fatal(err)
	}
	if types = eventTypes(t, evs); len(types) != 1 || types[0] != "response.completed" {
		t.Fatalf("events after usage chunk = %v", types)
	}
	respObj := decodeEvents(t, evs)[0]["response"].(map[string]interface{})
	if respObj["status"] != "completed" {
		t.Errorf("completed status = %v", respObj["status"])
	}
	u := respObj["usage"].(map[string]interface{})
	if u["input_tokens"] != float64(7) || u["output_tokens"] != float64(9) || u["total_tokens"] != float64(16) {
		t.Errorf("completed usage = %v", u)
	}
	output := respObj["output"].([]interface{})
	if len(output) != 3 {
		t.Fatalf("completed output = %d, want 3: %v", len(output), output)
	}
	parts := output[0].(map[string]interface{})["content"].([]interface{})
	if parts[0].(map[string]interface{})["text"] != "Hello world" {
		t.Errorf("message text = %v", parts)
	}
	fc := output[1].(map[string]interface{})
	if fc["arguments"] != `{"a":` {
		t.Errorf("function_call arguments = %v", fc["arguments"])
	}
	summary := output[2].(map[string]interface{})["summary"].([]interface{})[0].(map[string]interface{})
	if summary["text"] != "think step 1" {
		t.Errorf("reasoning summary = %v", summary)
	}

	// 7. after completion everything is skipped
	if _, err := conv.Convert(chunk(`{"id":"c7","choices":[{"index":0,"delta":{"content":"late"}}]}`)); err != ErrSkipChunk {
		t.Fatalf("post-completion chunk err = %v, want ErrSkipChunk", err)
	}
	if out := conv.CloseStream(); out != nil {
		t.Errorf("CloseStream after completion = %v, want nil", out)
	}
}

func TestResponsesStreamConverterFromChatNoUsage(t *testing.T) {
	conv := NewResponsesStreamConverter("openai")
	conv.SetModel("m")
	if _, err := conv.Convert([]byte(`{"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`)); err != nil {
		t.Fatal(err)
	}
	// EOF without a usage chunk → CloseStream emits the completion
	evs := conv.CloseStream()
	types := eventTypes(t, evs)
	if len(types) != 1 || types[0] != "response.completed" {
		t.Fatalf("CloseStream events = %v, want [response.completed]", types)
	}
	if !conv.Finished() {
		t.Error("converter not finished after CloseStream")
	}
}

func TestResponsesStreamConverterFromChatError(t *testing.T) {
	conv := NewResponsesStreamConverter("openai")
	evs, err := conv.Convert([]byte(`{"error":{"message":"boom","type":"server_error"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !conv.Errored() {
		t.Error("converter should be errored")
	}
	types := eventTypes(t, evs)
	if len(types) != 3 || types[2] != "error" {
		t.Fatalf("error events = %v", types)
	}
	// The Responses API's streaming error event carries code/message at the
	// TOP level, not nested under "error"
	errEv := decodeEvents(t, evs)[2]
	if errEv["message"] != "boom" || errEv["code"] != "stream_error" {
		t.Errorf("error event = %v, want top-level message/code", errEv)
	}
	if _, has := errEv["error"]; has {
		t.Errorf("error event must not nest under \"error\": %v", errEv)
	}
}

func TestResponsesStreamConverterContentFilterFails(t *testing.T) {
	// chat finish_reason "content_filter" must produce a FAILED response,
	// not a successful completed one
	conv := NewResponsesStreamConverter("openai")
	conv.SetModel("m")
	if _, err := conv.Convert([]byte(`{"choices":[{"delta":{"content":"bad"},"finish_reason":"content_filter"}]}`)); err != nil {
		t.Fatal(err)
	}
	evs := conv.CloseStream()
	completed := decodeEvents(t, evs)[len(evs)-1]
	respObj := completed["response"].(map[string]interface{})
	if respObj["status"] != "failed" {
		t.Errorf("status = %v, want failed", respObj["status"])
	}
	errBlock, ok := respObj["error"].(map[string]interface{})
	if !ok || errBlock["code"] != "content_filter" {
		t.Errorf("error = %v, want content_filter", respObj["error"])
	}
}

func TestResponsesStreamConverterAnthropicRefusalFails(t *testing.T) {
	conv := NewResponsesStreamConverter("anthropic")
	conv.SetModel("m")
	if _, err := conv.Convert([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":1}}}`)); err != ErrSkipChunk {
		t.Fatal(err)
	}
	if _, err := conv.Convert([]byte(`{"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":1}}`)); err != ErrSkipChunk {
		t.Fatal(err)
	}
	evs, err := conv.Convert([]byte(`{"type":"message_stop"}`))
	if err != nil {
		t.Fatal(err)
	}
	completed := decodeEvents(t, evs)[0]
	respObj := completed["response"].(map[string]interface{})
	if respObj["status"] != "failed" {
		t.Errorf("status = %v, want failed", respObj["status"])
	}
}

func TestResponsesStreamConverterSingleFragmentToolCall(t *testing.T) {
	// A gateway sending the ENTIRE tool call in one fragment (id + name +
	// arguments) must not lose the arguments
	conv := NewResponsesStreamConverter("openai")
	evs, err := conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_5","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(t, evs)
	if strings.Join(types, ",") != "response.created,response.in_progress,response.output_item.added,response.function_call_arguments.delta" {
		t.Fatalf("single-fragment events = %v", types)
	}
	evs, err = conv.Convert([]byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	evs = conv.CloseStream()
	completed := decodeEvents(t, evs)[len(evs)-1]
	output := completed["response"].(map[string]interface{})["output"].([]interface{})
	fc := output[0].(map[string]interface{})
	if fc["arguments"] != `{"a":1}` || fc["call_id"] != "call_5" {
		t.Errorf("function_call item = %v, want args preserved", fc)
	}
}

func TestResponsesStreamConverterReasoningBeforeContent(t *testing.T) {
	// DeepSeek-style order: reasoning_content deltas arrive BEFORE any
	// content — done events must still come out in output_index order
	// (reasoning item first)
	conv := NewResponsesStreamConverter("openai")
	conv.SetModel("deepseek")
	if _, err := conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`)); err != nil {
		t.Fatal(err)
	}
	evs, err := conv.Convert([]byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(t, evs)
	want := []string{
		"response.reasoning_summary_text.done", "response.output_item.done",
		"response.output_text.done", "response.content_part.done", "response.output_item.done",
	}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("done events = %v\nwant %v (output order)", types, want)
	}
}

func TestResponsesStreamConverterAnthropicMultipleTextBlocks(t *testing.T) {
	conv := NewResponsesStreamConverter("anthropic")
	if _, err := conv.Convert([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":1}}}`)); err != ErrSkipChunk {
		t.Fatal(err)
	}
	// block 0: text "one"
	if _, err := conv.Convert([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Convert([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"one"}}`)); err != nil {
		t.Fatal(err)
	}
	evs, err := conv.Convert([]byte(`{"type":"content_block_stop","index":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.output_text.done,response.content_part.done" {
		t.Fatalf("block 0 stop = %v", types)
	}
	// block 1: text "two" — part index must increment and stay closable
	if _, err := conv.Convert([]byte(`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`)); err != nil {
		t.Fatal(err)
	}
	evs, err = conv.Convert([]byte(`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"two"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); len(types) != 1 || types[0] != "response.output_text.delta" {
		t.Fatalf("block 1 delta = %v", types)
	}
	evs, err = conv.Convert([]byte(`{"type":"content_block_stop","index":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.output_text.done,response.content_part.done" {
		t.Fatalf("block 1 stop = %v", types)
	}
	// message_stop closes the item ONCE
	evs, err = conv.Convert([]byte(`{"type":"message_stop"}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.output_item.done,response.completed" {
		t.Fatalf("message_stop = %v", types)
	}
	// both parts present in the final item
	completed := decodeEvents(t, evs)[1]
	output := completed["response"].(map[string]interface{})["output"].([]interface{})
	parts := output[0].(map[string]interface{})["content"].([]interface{})
	if len(parts) != 2 {
		t.Fatalf("content parts = %v, want 2", parts)
	}
	if parts[0].(map[string]interface{})["text"] != "one" || parts[1].(map[string]interface{})["text"] != "two" {
		t.Errorf("parts = %v", parts)
	}
}

func TestResponsesStreamConverterAnthropicMissedStart(t *testing.T) {
	// A text_delta without its content_block_start at a NON-ZERO index must
	// still be closed by its content_block_stop
	conv := NewResponsesStreamConverter("anthropic")
	if _, err := conv.Convert([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":1}}}`)); err != ErrSkipChunk {
		t.Fatal(err)
	}
	if _, err := conv.Convert([]byte(`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"orphan"}}`)); err != nil {
		t.Fatal(err)
	}
	evs, err := conv.Convert([]byte(`{"type":"content_block_stop","index":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.output_text.done,response.content_part.done" {
		t.Fatalf("missed-start stop = %v", types)
	}
}

func TestResponsesStreamConverterAnthropicEOFMidTool(t *testing.T) {
	// EOF in the middle of a tool block: CloseStream must close the tool
	// item with its accumulated arguments and complete
	conv := NewResponsesStreamConverter("anthropic")
	if _, err := conv.Convert([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":1}}}`)); err != ErrSkipChunk {
		t.Fatal(err)
	}
	if _, err := conv.Convert([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"f","input":{}}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Convert([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`)); err != nil {
		t.Fatal(err)
	}
	evs := conv.CloseStream()
	types := eventTypes(t, evs)
	want := []string{
		"response.function_call_arguments.done", "response.output_item.done",
		"response.completed",
	}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("CloseStream events = %v\nwant %v", types, want)
	}
	completed := decodeEvents(t, evs)[len(evs)-1]
	fc := completed["response"].(map[string]interface{})["output"].([]interface{})[0].(map[string]interface{})
	if fc["arguments"] != `{"a":` {
		t.Errorf("arguments = %v", fc["arguments"])
	}
	// CloseStream is idempotent after completion
	if out := conv.CloseStream(); out != nil {
		t.Errorf("second CloseStream = %v, want nil", out)
	}
}

func TestResponsesStreamConverterUsageBeforeFinish(t *testing.T) {
	// A non-conforming gateway sending usage BEFORE the finish chunk: the
	// numbers must still land in response.completed
	conv := NewResponsesStreamConverter("openai")
	if _, err := conv.Convert([]byte(`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)); err != ErrSkipChunk {
		t.Fatalf("pre-finish usage chunk err = %v, want ErrSkipChunk", err)
	}
	// the finish chunk carries the captured usage straight into completed
	evs, err := conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	completed := decodeEvents(t, evs)[len(evs)-1]
	if completed["type"] != "response.completed" {
		t.Fatalf("last event = %v, want response.completed", completed["type"])
	}
	u := completed["response"].(map[string]interface{})["usage"].(map[string]interface{})
	if u["input_tokens"] != float64(3) || u["output_tokens"] != float64(4) {
		t.Errorf("usage = %v, want input 3 output 4", u)
	}
}

func TestResponsesStreamConverterDelayedToolID(t *testing.T) {
	// A gateway delaying the tool id to a LATER fragment: the announced item
	// id must stay stable (clients correlate events by item_id), while the
	// final call_id is picked up from the late fragment.
	conv := NewResponsesStreamConverter("openai")
	evs, err := conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":"}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	added := decodeEvents(t, evs)[len(evs)-2] // output_item.added precedes the args delta
	item := added["item"].(map[string]interface{})
	announcedID := item["id"].(string)
	if item["call_id"] != "" {
		t.Fatalf("first fragment carried a call_id it shouldn't: %v", item)
	}
	// late fragment with the id
	evs, err = conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"arguments":"1}"}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	deltaEv := decodeEvents(t, evs)[0]
	if deltaEv["item_id"] != announcedID {
		t.Errorf("delta item_id = %v, want announced id %v", deltaEv["item_id"], announcedID)
	}
	evs, err = conv.Convert([]byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	evs = conv.CloseStream()
	completed := decodeEvents(t, evs)[len(evs)-1]
	fc := completed["response"].(map[string]interface{})["output"].([]interface{})[0].(map[string]interface{})
	if fc["id"] != announcedID {
		t.Errorf("final item id = %v, want stable announced id %v", fc["id"], announcedID)
	}
	if fc["call_id"] != "call_9" {
		t.Errorf("final call_id = %v, want call_9", fc["call_id"])
	}
	if fc["arguments"] != `{"a":1}` {
		t.Errorf("arguments = %v, want accumulated", fc["arguments"])
	}
}

func TestResponsesStreamConverterIndexlessToolCallsAcrossChunks(t *testing.T) {
	// A gateway omitting the tool_call "index" streams one fragment per chunk
	// (every one at array position 0). Two different tools in consecutive
	// chunks must NOT merge into one function_call item — each is its own
	// output item with its own call_id/name/arguments.
	conv := NewResponsesStreamConverter("openai")

	// chunk 1: call_A, function f, arguments {"a": — the first Convert brings
	// the created/in_progress lifecycle events, then output_item.added
	evs, err := conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_A","type":"function","function":{"name":"f","arguments":"{\"a\":"}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.created,response.in_progress,response.output_item.added,response.function_call_arguments.delta" {
		t.Fatalf("chunk 1 events = %v", types)
	}
	addedA := decodeEvents(t, evs)[2]["item"].(map[string]interface{})
	if addedA["call_id"] != "call_A" || addedA["name"] != "f" {
		t.Errorf("call_A item = %v", addedA)
	}
	itemIDA := addedA["id"].(string)

	// chunk 2: call_B, function g, arguments {b:1} — must key to a NEW item
	evs, err = conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_B","type":"function","function":{"name":"g","arguments":"{b:1}"}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.output_item.added,response.function_call_arguments.delta" {
		t.Fatalf("chunk 2 events = %v", types)
	}
	addedB := decodeEvents(t, evs)[0]["item"].(map[string]interface{})
	if addedB["call_id"] != "call_B" || addedB["name"] != "g" {
		t.Errorf("call_B item = %v, want distinct call_B/g (bug: merged onto call_A)", addedB)
	}
	itemIDB := addedB["id"].(string)
	if itemIDB == itemIDA {
		t.Fatalf("call_B reused call_A's item id %v — the two tools merged", itemIDB)
	}
	// call_B's arguments delta must target call_B's own item
	if deltaEv := decodeEvents(t, evs)[1]; deltaEv["item_id"] != itemIDB {
		t.Errorf("call_B arguments delta item_id = %v, want %v", deltaEv["item_id"], itemIDB)
	}

	// finish
	if _, err := conv.Convert([]byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)); err != nil {
		t.Fatal(err)
	}
	evs = conv.CloseStream()
	completed := decodeEvents(t, evs)[len(evs)-1]
	output := completed["response"].(map[string]interface{})["output"].([]interface{})
	if len(output) != 2 {
		t.Fatalf("completed output has %d items, want 2: %v", len(output), output)
	}
	fcA := output[0].(map[string]interface{})
	if fcA["call_id"] != "call_A" || fcA["name"] != "f" || fcA["arguments"] != `{"a":` {
		t.Errorf("call_A output = %v", fcA)
	}
	fcB := output[1].(map[string]interface{})
	if fcB["call_id"] != "call_B" || fcB["name"] != "g" || fcB["arguments"] != "{b:1}" {
		t.Errorf("call_B output = %v (bug: args concatenated onto call_A)", fcB)
	}
}

func TestResponsesStreamConverterIndexlessToolCallContinueByID(t *testing.T) {
	// Index-less fragments of the SAME tool (id present on each) across
	// chunks must rejoin the same function_call item, accumulating arguments.
	conv := NewResponsesStreamConverter("openai")

	evs, err := conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_X","type":"function","function":{"name":"f"}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.created,response.in_progress,response.output_item.added" {
		t.Fatalf("first fragment events = %v", types)
	}
	added := decodeEvents(t, evs)[2]["item"].(map[string]interface{})
	if added["call_id"] != "call_X" || added["name"] != "f" {
		t.Fatalf("first fragment item = %v", added)
	}
	itemID := added["id"].(string)

	// continuation 1
	evs, err = conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_X","function":{"arguments":"{\"a\":"}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); len(types) != 1 || types[0] != "response.function_call_arguments.delta" {
		t.Fatalf("continuation fragment events = %v", types)
	}
	if deltaEv := decodeEvents(t, evs)[0]; deltaEv["item_id"] != itemID {
		t.Errorf("delta item_id = %v, want rejoined item %v", deltaEv["item_id"], itemID)
	}
	// continuation 2
	evs, err = conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_X","function":{"arguments":"1}"}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); len(types) != 1 || types[0] != "response.function_call_arguments.delta" {
		t.Fatalf("second continuation events = %v", types)
	}

	if _, err := conv.Convert([]byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)); err != nil {
		t.Fatal(err)
	}
	evs = conv.CloseStream()
	completed := decodeEvents(t, evs)[len(evs)-1]
	output := completed["response"].(map[string]interface{})["output"].([]interface{})
	if len(output) != 1 {
		t.Fatalf("completed output has %d items, want 1: %v", len(output), output)
	}
	fc := output[0].(map[string]interface{})
	if fc["call_id"] != "call_X" || fc["name"] != "f" || fc["arguments"] != `{"a":1}` {
		t.Errorf("function_call output = %v, want accumulated args", fc)
	}
}

func TestResponsesStreamConverterIndexlessAnonymousToolCallsDoNotMerge(t *testing.T) {
	// Index-less fragments WITHOUT an id in consecutive chunks must not merge
	// purely on their (identical) array position — each gets a fresh anonymous
	// identity, so they become distinct output items.
	conv := NewResponsesStreamConverter("openai")

	// chunk 1: index-less, no id (first Convert brings the lifecycle events)
	evs, err := conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"function":{"name":"f","arguments":"{\"a\":"}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.created,response.in_progress,response.output_item.added,response.function_call_arguments.delta" {
		t.Fatalf("chunk 1 events = %v", types)
	}
	itemIDA := decodeEvents(t, evs)[2]["item"].(map[string]interface{})["id"].(string)

	evs, err = conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"function":{"name":"g","arguments":"{b:1}"}}]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.output_item.added,response.function_call_arguments.delta" {
		t.Fatalf("chunk 2 events = %v", types)
	}
	itemIDB := decodeEvents(t, evs)[0]["item"].(map[string]interface{})["id"].(string)
	if itemIDB == itemIDA {
		t.Fatalf("anonymous tools merged into item %v", itemIDB)
	}

	if _, err := conv.Convert([]byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)); err != nil {
		t.Fatal(err)
	}
	evs = conv.CloseStream()
	completed := decodeEvents(t, evs)[len(evs)-1]
	output := completed["response"].(map[string]interface{})["output"].([]interface{})
	if len(output) != 2 {
		t.Fatalf("completed output has %d items, want 2: %v", len(output), output)
	}
	if n := output[0].(map[string]interface{})["name"]; n != "f" {
		t.Errorf("output[0] name = %v, want f", n)
	}
	if n := output[1].(map[string]interface{})["name"]; n != "g" {
		t.Errorf("output[1] name = %v, want g", n)
	}
}

func TestResponsesStreamConverterIndexlessSameChunkStaysDistinct(t *testing.T) {
	// Two tools as separate fragments in ONE chunk (positions 0 and 1, no
	// index) must still become two distinct function_call items — index-less
	// fragments keep serving the simultaneous multi-tool case within a chunk.
	conv := NewResponsesStreamConverter("openai")
	evs, err := conv.Convert([]byte(`{"choices":[{"index":0,"delta":{"tool_calls":[
		{"id":"call_0","type":"function","function":{"name":"f0","arguments":"{\"a\":"}},
		{"id":"call_1","type":"function","function":{"name":"f1","arguments":"{\"b\":"}}
	]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(t, evs)
	want := "response.created,response.in_progress,response.output_item.added,response.function_call_arguments.delta,response.output_item.added,response.function_call_arguments.delta"
	if strings.Join(types, ",") != want {
		t.Fatalf("same-chunk events = %v\nwant %v", types, want)
	}
	dec := decodeEvents(t, evs)
	itemIDA := dec[2]["item"].(map[string]interface{})["id"].(string)
	itemIDB := dec[4]["item"].(map[string]interface{})["id"].(string)
	if itemIDA == itemIDB {
		t.Fatalf("same-chunk tools merged into item %v", itemIDA)
	}
	// each tool's arguments delta targets its own item
	if dec[3]["item_id"] != itemIDA {
		t.Errorf("f0 delta item_id = %v, want %v", dec[3]["item_id"], itemIDA)
	}
	if dec[5]["item_id"] != itemIDB {
		t.Errorf("f1 delta item_id = %v, want %v", dec[5]["item_id"], itemIDB)
	}
}

func TestResponsesStreamConverterAnthropicEOFMidText(t *testing.T) {
	// EOF in the middle of a text block: CloseStream closes the open part
	// (part events first) then the item, then completes
	conv := NewResponsesStreamConverter("anthropic")
	if _, err := conv.Convert([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":1}}}`)); err != ErrSkipChunk {
		t.Fatal(err)
	}
	if _, err := conv.Convert([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Convert([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cut"}}`)); err != nil {
		t.Fatal(err)
	}
	evs := conv.CloseStream()
	types := eventTypes(t, evs)
	want := []string{
		"response.output_text.done", "response.content_part.done", "response.output_item.done",
		"response.completed",
	}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("CloseStream events = %v\nwant %v", types, want)
	}
	msg := decodeEvents(t, evs)[3]["response"].(map[string]interface{})["output"].([]interface{})[0].(map[string]interface{})
	parts := msg["content"].([]interface{})
	if parts[0].(map[string]interface{})["text"] != "cut" {
		t.Errorf("text = %v", parts)
	}
}

// ---- Streaming converter: Anthropic upstream ----

func TestResponsesStreamConverterFromAnthropic(t *testing.T) {
	conv := NewResponsesStreamConverter("anthropic")
	conv.SetModel("claude-sonnet")

	// message_start: begin() emits the lifecycle events on the first Convert
	evs, err := conv.Convert([]byte(`{"type":"message_start","message":{"usage":{"input_tokens":5,"cache_read_input_tokens":2}}}`))
	if err != ErrSkipChunk {
		t.Fatalf("message_start err = %v, want ErrSkipChunk", err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.created,response.in_progress" {
		t.Fatalf("message_start events = %v", types)
	}

	// text block opens
	evs, err = conv.Convert([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.output_item.added,response.content_part.added" {
		t.Fatalf("text block start events = %v", types)
	}

	// text delta
	evs, err = conv.Convert([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi "}}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); len(types) != 1 || types[0] != "response.output_text.delta" {
		t.Fatalf("text delta events = %v", types)
	}

	// tool block opens + args delta
	evs, err = conv.Convert([]byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"f","input":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); len(types) != 1 || types[0] != "response.output_item.added" {
		t.Fatalf("tool block start events = %v", types)
	}
	evs, err = conv.Convert([]byte(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":1}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); len(types) != 1 || types[0] != "response.function_call_arguments.delta" {
		t.Fatalf("args delta events = %v", types)
	}

	// text block stops → text part done (the message ITEM stays open — more
	// text blocks may follow — and is closed at message_stop); tool block
	// stops → tool item done
	evs, err = conv.Convert([]byte(`{"type":"content_block_stop","index":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.output_text.done,response.content_part.done" {
		t.Fatalf("text stop events = %v", types)
	}
	evs, err = conv.Convert([]byte(`{"type":"content_block_stop","index":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.function_call_arguments.done,response.output_item.done" {
		t.Fatalf("tool stop events = %v", types)
	}

	// message_delta (usage + stop_reason) → no events
	evs, err = conv.Convert([]byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":4}}`))
	if err != ErrSkipChunk {
		t.Fatalf("message_delta err = %v, want ErrSkipChunk", err)
	}

	// message_stop → the message item closes, then response.completed with
	// usage (input 5+2 cached, output 4)
	evs, err = conv.Convert([]byte(`{"type":"message_stop"}`))
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(t, evs)
	if strings.Join(types, ",") != "response.output_item.done,response.completed" {
		t.Fatalf("message_stop events = %v", types)
	}
	respObj := decodeEvents(t, evs)[1]["response"].(map[string]interface{})
	u := respObj["usage"].(map[string]interface{})
	if u["input_tokens"] != float64(7) || u["output_tokens"] != float64(4) {
		t.Errorf("usage = %v, want input 7 (5+2 cached), output 4", u)
	}
	details := u["input_tokens_details"].(map[string]interface{})
	if details["cached_tokens"] != float64(2) {
		t.Errorf("cached_tokens = %v", details["cached_tokens"])
	}
	if output := respObj["output"].([]interface{}); len(output) != 2 {
		t.Errorf("output = %v, want message + function_call", output)
	}
	if !conv.Finished() {
		t.Error("converter not finished after message_stop")
	}
}

func TestResponsesStreamConverterFromAnthropicFullMessage(t *testing.T) {
	// A non-SSE upstream returning a full message object
	conv := NewResponsesStreamConverter("anthropic")
	conv.SetModel("claude-sonnet")
	evs, err := conv.Convert([]byte(`{
		"id": "msg_1", "type": "message", "role": "assistant",
		"model": "claude-sonnet", "stop_reason": "end_turn",
		"content": [
			{"type": "text", "text": "full answer"},
			{"type": "tool_use", "id": "toolu_1", "name": "f", "input": {"a": 1}}
		],
		"usage": {"input_tokens": 3, "output_tokens": 2}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if !conv.Finished() {
		t.Error("converter should finish on a full message")
	}
	want := []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added", "response.output_text.delta",
		"response.output_text.done", "response.content_part.done",
		"response.output_item.added", "response.function_call_arguments.delta",
		"response.function_call_arguments.done", "response.output_item.done",
		"response.output_item.done",
		"response.completed",
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("full message events = %v\nwant %v", types, want)
	}
}

func TestResponsesStreamConverterFromAnthropicError(t *testing.T) {
	conv := NewResponsesStreamConverter("anthropic")
	evs, err := conv.Convert([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"overloaded"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !conv.Errored() {
		t.Error("converter should be errored")
	}
	types := eventTypes(t, evs)
	if types[len(types)-1] != "error" {
		t.Fatalf("error events = %v", types)
	}
}

func TestResponsesStreamConverterThinkingDelta(t *testing.T) {
	// Anthropic thinking blocks map to reasoning summary events
	conv := NewResponsesStreamConverter("anthropic")
	evs, err := conv.Convert([]byte(`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	// begin() lifecycle events + the reasoning item open
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.created,response.in_progress,response.output_item.added" {
		t.Fatalf("thinking block start events = %v", types)
	}
	evs, err = conv.Convert([]byte(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"step 1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); len(types) != 1 || types[0] != "response.reasoning_summary_text.delta" {
		t.Fatalf("thinking events = %v", types)
	}
	evs, err = conv.Convert([]byte(`{"type":"content_block_stop","index":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if types := eventTypes(t, evs); strings.Join(types, ",") != "response.reasoning_summary_text.done,response.output_item.done" {
		t.Fatalf("thinking stop events = %v", types)
	}
}

func TestResponsesRequestToChatCompletionFileAttachment(t *testing.T) {
	// An input_file part must survive into the chat file content part
	// (regression: it used to be silently dropped).
	body := `{"model":"m","input":[
		{"type":"message","role":"user","content":[
			{"type":"input_text","text":"read this"},
			{"type":"input_file","file_data":"data:application/pdf;base64,JVBER","filename":"report.pdf"}
		]}
	]}`
	out, err := ResponsesRequestToChatCompletion([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", msgs)
	}
	content := msgs[0].(map[string]interface{})["content"].([]interface{})
	if len(content) != 2 {
		t.Fatalf("content parts = %v", content)
	}
	file := content[1].(map[string]interface{})
	if file["type"] != "file" {
		t.Fatalf("part = %v, want file", file)
	}
	f, _ := file["file"].(map[string]interface{})
	if f["file_data"] != "data:application/pdf;base64,JVBER" || f["filename"] != "report.pdf" {
		t.Errorf("file part = %v", f)
	}
}

func TestResponsesRequestToChatCompletionToolOutputParts(t *testing.T) {
	// function_call_output with a parts array → chat tool content parts
	// (regression: Responses-shaped parts used to pass through unconverted).
	body := `{"model":"m","input":[
		{"type":"function_call_output","call_id":"call_9","output":[
			{"type":"output_text","text":"72F"},
			{"type":"input_image","image_url":"data:image/png;base64,iVBOR"}
		]}
	]}`
	out, err := ResponsesRequestToChatCompletion([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 2 {
		t.Fatalf("messages = %v (tool + padded assistant)", msgs)
	}
	tool := msgs[0].(map[string]interface{})
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_9" {
		t.Fatalf("tool message = %v", tool)
	}
	parts := tool["content"].([]interface{})
	if len(parts) != 2 {
		t.Fatalf("tool content parts = %v", parts)
	}
	txt := parts[0].(map[string]interface{})
	if txt["type"] != "text" || txt["text"] != "72F" {
		t.Errorf("text part = %v", txt)
	}
	img := parts[1].(map[string]interface{})
	iu, _ := img["image_url"].(map[string]interface{})
	if img["type"] != "image_url" || iu["url"] != "data:image/png;base64,iVBOR" {
		t.Errorf("image part = %v", img)
	}
}

func TestResponsesRequestToChatCompletionToolOutputNull(t *testing.T) {
	// A tool that returned nothing → "" content, never null.
	body := `{"model":"m","input":[{"type":"function_call_output","call_id":"call_9"}]}`
	out, err := ResponsesRequestToChatCompletion([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	tool := msgs[0].(map[string]interface{})
	if tool["content"] != "" {
		t.Errorf("tool content = %v, want empty string", tool["content"])
	}
	if tool["tool_call_id"] != "call_9" {
		t.Errorf("tool_call_id = %v", tool["tool_call_id"])
	}
}

func TestResponsesRequestToAnthropicFileAndImage(t *testing.T) {
	// input_file → Anthropic document block; uppercase DATA: + MIME-wrapped
	// base64 image → base64 source with stripped newlines.
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	wrapped := strings.ReplaceAll(png, "AAAA", "AAA\nAAA")
	// the payload must be JSON-escaped inside the request body
	wrappedJSON := strings.ReplaceAll(wrapped, "\n", "\\n")
	body := `{"model":"m","input":[{"type":"message","role":"user","content":[
		{"type":"input_file","file_data":"data:application/pdf;base64,JVBER","filename":"a.pdf"},
		{"type":"input_image","image_url":"DATA:image/png;base64,` + wrappedJSON + `"}
	]}]}`
	out, err := ResponsesRequestToAnthropic([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	content := msgs[0].(map[string]interface{})["content"].([]interface{})
	if len(content) != 2 {
		t.Fatalf("content = %v, want 2 blocks (document + image)", content)
	}
	doc := content[0].(map[string]interface{})
	if doc["type"] != "document" {
		t.Fatalf("block = %v, want document", doc)
	}
	if doc["title"] != "a.pdf" {
		t.Errorf("document title = %v", doc["title"])
	}
	img := content[1].(map[string]interface{})
	src, _ := img["source"].(map[string]interface{})
	if img["type"] != "image" || src["type"] != "base64" || src["media_type"] != "image/png" {
		t.Fatalf("image block = %v", img)
	}
	if src["data"] != strings.ReplaceAll(png, "AAAA", "AAAAAA") {
		t.Errorf("image data = %q, want newline-stripped base64", src["data"])
	}
}

func TestResponsesRequestToAnthropicToolOutputParts(t *testing.T) {
	// function_call_output parts array → Anthropic tool_result blocks;
	// missing output → "" (never null).
	body := `{"model":"m","input":[
		{"type":"function_call_output","call_id":"call_9","output":[
			{"type":"output_text","text":"72F"},
			{"type":"input_file","file_data":"data:application/pdf;base64,JVBER","filename":"r.pdf"}
		]},
		{"type":"function_call_output","call_id":"call_10"}
	]}`
	out, err := ResponsesRequestToAnthropic([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	// consecutive function_call_output items merge into one user message
	// with two tool_result blocks
	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", msgs)
	}
	content := msgs[0].(map[string]interface{})["content"].([]interface{})
	if len(content) != 2 {
		t.Fatalf("content = %v, want 2 tool_result blocks", content)
	}
	tr := content[0].(map[string]interface{})
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "call_9" {
		t.Fatalf("tool_result = %v", tr)
	}
	blocks := tr["content"].([]interface{})
	if len(blocks) != 2 {
		t.Fatalf("tool_result content = %v", blocks)
	}
	if blocks[0].(map[string]interface{})["type"] != "text" {
		t.Errorf("first block = %v, want text", blocks[0])
	}
	if blocks[1].(map[string]interface{})["type"] != "document" {
		t.Errorf("second block = %v, want document", blocks[1])
	}
	tr2 := content[1].(map[string]interface{})
	if tr2["content"] != "" || tr2["tool_use_id"] != "call_10" {
		t.Errorf("empty tool_result = %v", tr2)
	}
}

func TestResponsesRequestToChatCompletionToolOutputUnexpectedShape(t *testing.T) {
	// A non-conformant client that reports a number/object as tool output
	// must not have it shipped upstream as chat tool content (400s) — it
	// becomes "" instead.
	body := `{"model":"m","input":[{"type":"function_call_output","call_id":"c1","output":42}]}`
	out, err := ResponsesRequestToChatCompletion([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	tool := msgs[0].(map[string]interface{})
	if tool["content"] != "" {
		t.Errorf("tool content = %v, want empty string", tool["content"])
	}
}

func TestResponsesRequestToAnthropicToolOutputUnexpectedShape(t *testing.T) {
	// Non-conformant tool output (number/object/bool) must not reach
	// Anthropic tool_result.content — it only accepts a string or blocks.
	body := `{"model":"m","input":[{"type":"function_call_output","call_id":"c1","output":42}]}`
	out, err := ResponsesRequestToAnthropic([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	tr := msgs[0].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})
	if tr["content"] != "" {
		t.Errorf("tool_result content = %v, want empty string", tr["content"])
	}
}

func TestResponsesRequestToChatCompletionEndsWithAssistantToolCalls(t *testing.T) {
	// A conversation ending in an assistant message with pending
	// function_call parts must be padded with tool messages answering each
	// call (OpenAI rejects the bare tool_calls ending).
	body := `{"model":"m","input":[{"type":"message","role":"assistant","content":[
		{"type":"function_call","id":"fc_1","call_id":"call_7","name":"f","arguments":"{}"}
	]}]}`
	out, err := ResponsesRequestToChatCompletion([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 2 {
		t.Fatalf("messages = %v, want assistant + tool pad", msgs)
	}
	tool := msgs[1].(map[string]interface{})
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_7" || tool["content"] != "" {
		t.Errorf("pad tool message = %v", tool)
	}
}

func TestResponsesRequestToChatCompletionInputAudio(t *testing.T) {
	// An input_audio part on a user message must become a chat completions
	// input_audio content part (regression: it used to be silently dropped,
	// so the audio never reached the model).
	body := `{"model":"m","input":[
		{"type":"message","role":"user","content":[
			{"type":"input_text","text":"what tone is this"},
			{"type":"input_audio","format":"wav","data":"UklGRg=="}
		]}
	]}`
	out, err := ResponsesRequestToChatCompletion([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", msgs)
	}
	content := msgs[0].(map[string]interface{})["content"].([]interface{})
	if len(content) != 2 {
		t.Fatalf("content parts = %v, want text + input_audio", content)
	}
	// the audio-less neighbor keeps its exact chat shape
	txt := content[0].(map[string]interface{})
	if txt["type"] != "text" || txt["text"] != "what tone is this" {
		t.Errorf("text part = %v", txt)
	}
	audio := content[1].(map[string]interface{})
	if audio["type"] != "input_audio" {
		t.Fatalf("part = %v, want input_audio", audio)
	}
	ia, _ := audio["input_audio"].(map[string]interface{})
	if ia["format"] != "wav" || ia["data"] != "UklGRg==" {
		t.Errorf("input_audio = %v, want format wav + data", ia)
	}
}

func TestResponsesRequestToAnthropicInputAudio(t *testing.T) {
	// An input_audio part on a user message must become an Anthropic audio
	// block (regression: it used to be silently dropped).
	body := `{"model":"m","input":[
		{"type":"message","role":"user","content":[
			{"type":"input_text","text":"what tone is this"},
			{"type":"input_audio","format":"mp3","data":"SUQzBQ=="}
		]}
	]}`
	out, err := ResponsesRequestToAnthropic([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", msgs)
	}
	content := msgs[0].(map[string]interface{})["content"].([]interface{})
	if len(content) != 2 {
		t.Fatalf("content = %v, want 2 blocks (text + audio)", content)
	}
	// the audio-less neighbor keeps its exact anthropic shape
	if content[0].(map[string]interface{})["type"] != "text" ||
		content[0].(map[string]interface{})["text"] != "what tone is this" {
		t.Errorf("first block = %v, want text", content[0])
	}
	audio := content[1].(map[string]interface{})
	if audio["type"] != "audio" {
		t.Fatalf("block = %v, want audio", audio)
	}
	src, _ := audio["source"].(map[string]interface{})
	if src["type"] != "base64" || src["media_type"] != "audio/mpeg" || src["data"] != "SUQzBQ==" {
		t.Errorf("audio source = %v, want base64 audio/mpeg", src)
	}
}

func TestResponsesRequestToChatCompletionToolOutputAudio(t *testing.T) {
	// An input_audio part inside a function_call_output output array must
	// become a chat tool content part (regression: it used to be silently
	// dropped).
	body := `{"model":"m","input":[
		{"type":"function_call_output","call_id":"call_9","output":[
			{"type":"output_text","text":"72F"},
			{"type":"input_audio","format":"wav","data":"UklGRg=="}
		]}
	]}`
	out, err := ResponsesRequestToChatCompletion([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 2 {
		t.Fatalf("messages = %v (tool + padded assistant)", msgs)
	}
	tool := msgs[0].(map[string]interface{})
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_9" {
		t.Fatalf("tool message = %v", tool)
	}
	parts := tool["content"].([]interface{})
	if len(parts) != 2 {
		t.Fatalf("tool content parts = %v, want text + input_audio", parts)
	}
	if parts[0].(map[string]interface{})["type"] != "text" ||
		parts[0].(map[string]interface{})["text"] != "72F" {
		t.Errorf("first part = %v, want unchanged text", parts[0])
	}
	audio := parts[1].(map[string]interface{})
	if audio["type"] != "input_audio" {
		t.Fatalf("part = %v, want input_audio", audio)
	}
	ia, _ := audio["input_audio"].(map[string]interface{})
	if ia["format"] != "wav" || ia["data"] != "UklGRg==" {
		t.Errorf("input_audio = %v, want format wav + data", ia)
	}
}

func TestResponsesRequestToAnthropicToolOutputAudio(t *testing.T) {
	// An input_audio part inside a function_call_output output array must
	// become an Anthropic tool_result audio block (regression: it used to
	// be silently dropped).
	body := `{"model":"m","input":[
		{"type":"function_call_output","call_id":"call_9","output":[
			{"type":"output_text","text":"72F"},
			{"type":"input_audio","format":"wav","data":"UklGRg=="}
		]}
	]}`
	out, err := ResponsesRequestToAnthropic([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", msgs)
	}
	tr := msgs[0].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})
	if tr["type"] != "tool_result" || tr["tool_use_id"] != "call_9" {
		t.Fatalf("tool_result = %v", tr)
	}
	blocks := tr["content"].([]interface{})
	if len(blocks) != 2 {
		t.Fatalf("tool_result content = %v, want text + audio", blocks)
	}
	if blocks[0].(map[string]interface{})["type"] != "text" ||
		blocks[0].(map[string]interface{})["text"] != "72F" {
		t.Errorf("first block = %v, want unchanged text", blocks[0])
	}
	audio := blocks[1].(map[string]interface{})
	if audio["type"] != "audio" {
		t.Fatalf("block = %v, want audio", audio)
	}
	src, _ := audio["source"].(map[string]interface{})
	if src["type"] != "base64" || src["media_type"] != "audio/wav" || src["data"] != "UklGRg==" {
		t.Errorf("audio source = %v, want base64 audio/wav", src)
	}
}

func TestResponsesRequestToChatCompletionAudioUnsupportedFormat(t *testing.T) {
	// Audio in a codec chat completions cannot represent must be dropped,
	// never shipped upstream (strict gateways 400 on unknown formats) —
	// the audio-less parts stay untouched.
	body := `{"model":"m","input":[
		{"type":"message","role":"user","content":[
			{"type":"input_text","text":"hi"},
			{"type":"input_audio","format":"flac","data":"ZmxhYw=="},
			{"type":"input_audio","format":"wav","data":""}
		]}
	]}`
	out, err := ResponsesRequestToChatCompletion([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	content := msgs[0].(map[string]interface{})["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("content parts = %v, want only the text part", content)
	}
	if content[0].(map[string]interface{})["type"] != "text" {
		t.Errorf("part = %v, want text", content[0])
	}
}

func TestResponsesRequestToAnthropicAudioFormatPassthrough(t *testing.T) {
	// Unknown codecs pass through toward Anthropic as audio/<codec>
	// (mirroring openAIAudioPartToAnthropic), while empty data drops —
	// leaving the empty-text fallback when nothing is representable.
	body := `{"model":"m","input":[
		{"type":"message","role":"user","content":[
			{"type":"input_audio","format":"flac","data":"ZmxhYw=="},
			{"type":"input_audio","format":"wav","data":""}
		]}
	]}`
	out, err := ResponsesRequestToAnthropic([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	content := msgs[0].(map[string]interface{})["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("content = %v, want only the flac audio block (empty data dropped)", content)
	}
	audio := content[0].(map[string]interface{})
	src, _ := audio["source"].(map[string]interface{})
	if audio["type"] != "audio" || src["type"] != "base64" || src["media_type"] != "audio/flac" {
		t.Errorf("audio block = %v, want base64 audio/flac passthrough", audio)
	}

	// an only-empty-data message has nothing left → empty-text fallback
	body = `{"model":"m","input":[{"type":"message","role":"user","content":[
		{"type":"input_audio","format":"wav","data":""}
	]}]}`
	if out, err = ResponsesRequestToAnthropic([]byte(body), ""); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ = req["messages"].([]interface{})
	content = msgs[0].(map[string]interface{})["content"].([]interface{})
	if len(content) != 1 || content[0].(map[string]interface{})["type"] != "text" ||
		content[0].(map[string]interface{})["text"] != "" {
		t.Errorf("content = %v, want empty-text fallback", content)
	}
}

func TestResponsesRequestNumericCallIDCoerced(t *testing.T) {
	// A numeric call_id (non-conformant client) must not leak into
	// tool_call_id/tool_use_id as a JSON number — upstreams require strings.
	body := `{"model":"m","input":[{"type":"function_call_output","call_id":123,"output":"x"}]}`
	out, err := ResponsesRequestToChatCompletion([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	var req map[string]interface{}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ := req["messages"].([]interface{})
	tool := msgs[0].(map[string]interface{})
	if tool["tool_call_id"] != "" {
		t.Errorf("chat tool_call_id = %v, want empty string (numeric coerced)", tool["tool_call_id"])
	}
	out, err = ResponsesRequestToAnthropic([]byte(body), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	msgs, _ = req["messages"].([]interface{})
	tr := msgs[0].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})
	if tr["tool_use_id"] != "" {
		t.Errorf("anthropic tool_use_id = %v, want empty string (numeric coerced)", tr["tool_use_id"])
	}
}
