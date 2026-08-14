package relay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"key-router/format"
	"key-router/model"
)

// ErrUnsupportedRoute is returned when a request cannot be routed to the
// selected provider regardless of retries (e.g. embeddings → anthropic).
var ErrUnsupportedRoute = errors.New("unsupported route")

// RelayResponse is the result of ForwardRequest: the upstream response plus
// the protocol the upstream actually spoke. For a /v1/responses request the
// two can differ — an OpenAI-format gateway without Responses API support
// gets an automatic chat-completions fallback.
type RelayResponse struct {
	Resp *http.Response
	// UpstreamFormat is "openai", "anthropic" or "responses" — the format
	// of Resp's body. The response side converts back to the client's
	// format based on this, never on the provider config alone.
	UpstreamFormat string
}

// streamTransport bounds time-to-first-byte for streaming requests while
// reusing connections across requests (per-request Transports would leak
// idle conns and pay a fresh TLS handshake per stream).
var streamTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	ResponseHeaderTimeout: 10 * time.Minute,
}

// ForwardRequest forwards an API request to the upstream provider
// Returns the response with proper streaming support
func ForwardRequest(meta *model.RequestMetadata, key *model.Key, provider *model.Provider) (*RelayResponse, error) {
	// Determine target format
	targetFormat := provider.Type

	// Embeddings have no Anthropic equivalent — reject early with a clear
	// error instead of converting the body into an invalid chat request and
	// relaying a confusing upstream 400.
	if strings.HasSuffix(meta.RequestPath, "/embeddings") && targetFormat == "anthropic" {
		return nil, fmt.Errorf("%w: embeddings cannot be routed to an anthropic provider", ErrUnsupportedRoute)
	}

	// Map the request path for cross-format requests: the client sends
	// /v1/chat/completions (OpenAI), /v1/messages (Anthropic) or
	// /v1/responses (OpenAI Responses API), but the upstream speaks the
	// other endpoint.
	//
	// upstreamFormat tracks the protocol the upstream actually receives (and
	// will answer in). For /v1/responses requests it can differ from
	// targetFormat: an OpenAI-format gateway gets /v1/responses when it
	// supports it natively, and /v1/chat/completions otherwise (fallback
	// below).
	upstreamPath := meta.RequestPath
	upstreamFormat := targetFormat
	var bodyToSend []byte
	var err error
	switch {
	case meta.Format == "responses" && targetFormat == "anthropic":
		// The Responses API is OpenAI's equivalent of the Messages API:
		// convert the request and let the response side convert back.
		upstreamPath = "/v1/messages"
		bodyToSend, err = format.ResponsesRequestToAnthropic(meta.RequestBody, meta.TargetModel)
		if err != nil {
			return nil, fmt.Errorf("format conversion error: %w", err)
		}
	case meta.Format == "responses":
		// Native /v1/responses passthrough (model name may be rewritten).
		upstreamPath = "/v1/responses"
		upstreamFormat = "responses"
		bodyToSend, err = replaceModelName(meta.RequestBody, meta.TargetModel)
		if err != nil {
			return nil, fmt.Errorf("model replacement error: %w", err)
		}
	case format.NeedConvert(meta.Format, targetFormat):
		if targetFormat == "anthropic" {
			upstreamPath = "/v1/messages"
		} else {
			upstreamPath = "/v1/chat/completions"
		}
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
	default:
		// Same format, just possibly replace model name
		bodyToSend, err = replaceModelName(meta.RequestBody, meta.TargetModel)
		if err != nil {
			return nil, fmt.Errorf("model replacement error: %w", err)
		}
	}
	upstreamURL := strings.TrimRight(provider.BaseURL, "/") + upstreamPath

	// Merge the model group's extra params (client keys are OVERWRITTEN by
	// the group config — e.g. {"temperature": 0.2} pins sampling).
	if meta.ExtraParams != "" {
		bodyToSend, err = mergeExtraParams(bodyToSend, meta.ExtraParams)
		if err != nil {
			return nil, fmt.Errorf("extra params merge error: %w", err)
		}
	}

	// Create upstream request. Derive from the client's context so a
	// disconnected downstream client cancels the upstream fetch instead of
	// stalling the goroutine for the full client timeout.
	req, err := http.NewRequestWithContext(meta.Ctx, "POST", upstreamURL, bytes.NewReader(bodyToSend))
	if err != nil {
		return nil, err
	}

	// Forward incoming headers, replacing auth and skipping local-only and
	// hop-by-hop headers (forwarding Connection: close would disable
	// connection reuse for that client's requests)
	for k, vals := range meta.Headers {
		// Skip local-only headers that shouldn't leak upstream
		if k == "Authorization" || k == "X-Api-Key" || k == "Cookie" || k == "Origin" || k == "Referer" {
			continue
		}
		// Skip hop-by-hop headers (Connection, Keep-Alive, TE, Trailer,
		// Upgrade, Proxy-Connection, Proxy-Authorization) — they describe
		// THIS hop's transport or credentials
		if strings.EqualFold(k, "Connection") || strings.EqualFold(k, "Keep-Alive") ||
			strings.EqualFold(k, "Proxy-Connection") || strings.EqualFold(k, "TE") ||
			strings.EqualFold(k, "Trailer") || strings.EqualFold(k, "Upgrade") ||
			strings.EqualFold(k, "Proxy-Authorization") {
			continue
		}
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}

	// Ensure content-type is set
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	// Set auth based on provider type
	if targetFormat == "anthropic" {
		req.Header.Set("x-api-key", key.KeyValue)
		// Honor a client-sent anthropic-version (newer API semantics);
		// default to the base version when absent.
		if req.Header.Get("anthropic-version") == "" {
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+key.KeyValue)
	}

	// Apply extra headers from provider config (never overwrite the key's
	// auth header — a misconfigured extra_headers must not break every key)
	if provider.ExtraHeaders != "" {
		var extraHeaders map[string]string
		if err := json.Unmarshal([]byte(provider.ExtraHeaders), &extraHeaders); err == nil {
			for k, v := range extraHeaders {
				// Header names are case-insensitive
				if strings.EqualFold(k, "Authorization") || strings.EqualFold(k, "X-Api-Key") {
					continue
				}
				req.Header.Set(k, v)
			}
		}
	}

	// Don't forward the client's Accept-Encoding upstream: if the caller sets it,
	// Go's http.Transport does NOT transparently decompress, so gzip bodies would
	// be relayed as compressed bytes while we strip Content-Encoding below.
	// Letting Go handle compression keeps relayed bodies plain.
	req.Header.Del("Accept-Encoding")

	// Use a timeout client
	client := &http.Client{
		Timeout: 300 * time.Second,
	}

	// For streaming, don't apply the total timeout — but keep a large bound
	// so dial and time-to-first-byte can't hang forever (the server side has
	// no write timeout either). The client's context (round-trip wiring)
	// cancels the fetch when the downstream client disconnects.
	if meta.Stream {
		client.Timeout = 24 * time.Hour
		client.Transport = streamTransport // bounds TTFB, shares connections
	}

	// doOnce performs the upstream request, retrying once without the
	// stream_options WE injected for converted streams (strict OpenAI-
	// compatible endpoints reject unknown parameters with a 400). Only
	// applies to converted requests — client-sent stream_options are the
	// client's feature and are never stripped. (Set to true for the fallback
	// chat body below, which carries our injected stream_options.)
	streamOptionsInjected := meta.Format == "anthropic" && targetFormat == "openai"
	// Pre-declared so the closure can retry itself on the stream_options 400.
	var doOnce func(body []byte) (*http.Response, error)
	doOnce = func(body []byte) (*http.Response, error) {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("upstream request failed: %w", err)
		}
		if resp.StatusCode == http.StatusBadRequest && streamOptionsInjected && bodyHasStreamOptions(body) {
			cleanBody := stripStreamOptions(body)
			if cleanBody != nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				resp2, retryErr := doOnce(cleanBody)
				if retryErr != nil {
					return nil, retryErr
				}
				return resp2, nil
			}
		}
		return resp, nil
	}

	resp, err := doOnce(bodyToSend)
	if err != nil {
		return nil, err
	}

	// OpenAI-format gateways that don't implement the Responses API return
	// 404/405/501 for /v1/responses. Convert the request to chat completions
	// and retry once; the response side converts back so the client still
	// gets Responses-API output. (400 is deliberately NOT a fallback signal:
	// genuine request errors must not be masked by a conversion retry.)
	if meta.Format == "responses" && targetFormat == "openai" &&
		(resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed ||
			resp.StatusCode == http.StatusNotImplemented) {
		// Convert from the UNMERGED body (like the Anthropic path does) so
		// route extra_params survive the conversion, then re-apply the
		// group's overrides on the converted chat body.
		convBody, convErr := format.ResponsesRequestToChatCompletion(meta.RequestBody, meta.TargetModel)
		if convErr == nil {
			if meta.ExtraParams != "" {
				convBody, convErr = mergeExtraParams(convBody, meta.ExtraParams)
			}
		}
		if convErr == nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			// Re-point the same request (headers, auth, context intact) at
			// the chat completions endpoint (recompute the URL so a base
			// path in provider.BaseURL survives).
			if u, perr := url.Parse(strings.TrimRight(provider.BaseURL, "/") + "/v1/chat/completions"); perr == nil {
				req.URL.Path = u.Path
				req.URL.RawPath = u.RawPath
			}
			streamOptionsInjected = true // the converted chat body carries our stream_options
			retryResp, retryErr := doOnce(convBody)
			if retryErr != nil {
				return nil, retryErr
			}
			return &RelayResponse{Resp: retryResp, UpstreamFormat: "openai"}, nil
		}
	}

	return &RelayResponse{Resp: resp, UpstreamFormat: upstreamFormat}, nil
}

// bodyHasStreamOptions reports whether the body carries stream_options
func bodyHasStreamOptions(body []byte) bool {
	var req map[string]interface{}
	if json.Unmarshal(body, &req) != nil {
		return false
	}
	_, ok := req["stream_options"]
	return ok
}

// stripStreamOptions returns the body without stream_options (nil on failure)
func stripStreamOptions(body []byte) []byte {
	var req map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		return nil
	}
	delete(req, "stream_options")
	clean, err := json.Marshal(req)
	if err != nil {
		return nil
	}
	return clean
}

// StreamResponse streams an SSE response from upstream to the client response writer.
// upstreamFormat is the format the upstream actually spoke ("openai",
// "anthropic" or "responses") — for /v1/responses requests it can differ
// from the provider type (chat-completions fallback).
// Returns captured token usage if available from the stream end events.
func StreamResponse(w http.ResponseWriter, resp *http.Response, inputFormat, upstreamFormat, modelName string) (*model.TokenUsage, error) {
	usage := &model.TokenUsage{}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}

	// Some upstreams ignore stream:true and return a plain JSON 200 body.
	// Sniff: explicit SSE content type, or a non-JSON first byte, means a
	// real SSE stream; anything else (json CT, empty CT + '{'/'[' body) is
	// relayed as a single data frame so the client doesn't get a silent
	// empty stream (and usage is still parsed). Leading whitespace/BOM is
	// skipped before the first-byte check.
	br := bufio.NewReader(resp.Body)
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	isSSE := strings.Contains(ct, "text/event-stream")
	if !isSSE {
		// Peek up to 64 bytes, skipping whitespace and the full UTF-8 BOM
		// (EF BB BF — skipping only 0xEF would leave 0xBB as the first byte)
		peeked, _ := br.Peek(64)
		first := byte(0)
		for _, b := range peeked {
			if b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == 0xEF || b == 0xBB || b == 0xBF {
				continue
			}
			first = b
			break
		}
		if first == '{' || first == '[' {
			isSSE = false // JSON body
		} else {
			isSSE = true // unknown → treat as SSE
		}
	}

	if !isSSE {
		body, err := io.ReadAll(io.LimitReader(br, 256<<20+1))
		resp.Body.Close()
		if err != nil {
			WriteStreamError(w, inputFormat, "failed to read upstream response")
			return usage, fmt.Errorf("failed to read upstream response: %w", err)
		}
		if len(body) > 256<<20 {
			WriteStreamError(w, inputFormat, "upstream response too large")
			return usage, fmt.Errorf("upstream response too large")
		}
		// Strip a UTF-8 BOM and leading/trailing whitespace the sniff skipped
		// over — json parsers reject the BOM. Whitespace may precede the BOM,
		// so trim, strip, trim again.
		body = bytes.TrimSpace(body)
		body = bytes.TrimPrefix(body, []byte{0xEF, 0xBB, 0xBF})
		body = bytes.TrimSpace(body)
		usage = ParseTokenUsage(body, upstreamFormat)

		// A 200 with a JSON error body (some gateways do this for
		// context-length/model errors) must be surfaced as an error, not
		// billed as a successful completion.
		var errCheck struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal(body, &errCheck) == nil && isErrorPayload(errCheck.Error) {
			msg := "upstream stream error"
			var inner struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(errCheck.Error, &inner) == nil && inner.Message != "" {
				msg = inner.Message
			}
			WriteStreamError(w, inputFormat, msg)
			return usage, fmt.Errorf("upstream returned an error: %s", msg)
		}

		// A Responses-format client gets its full event sequence synthesized
		// from the JSON body (created → ... → completed), regardless of which
		// protocol the upstream actually spoke.
		if inputFormat == "responses" {
			var events [][]byte
			if upstreamFormat == "responses" {
				// Native: the body already IS a response object — wrap it in
				// lifecycle events so stream clients get a consumable
				// sequence (created → completed).
				events, err = responsesBodyToEvents(body)
				if err != nil {
					WriteStreamError(w, inputFormat, "failed to convert upstream response")
					return usage, fmt.Errorf("failed to convert upstream response: %w", err)
				}
			} else {
				conv := format.NewResponsesStreamConverter(upstreamFormat)
				conv.SetModel(modelName)
				// An empty body (stream:true returned nothing) must still
				// yield a clean empty completion — skip conversion so
				// CloseStream synthesizes created → completed.
				if len(bytes.TrimSpace(body)) > 0 {
					if upstreamFormat == "openai" {
						// Normalize the full completion into a chunk the
						// chat-mode converter understands, then attach the
						// usage parsed from the full body.
						pseudo := completionToStreamChunk(body, modelName)
						converted, convErr := conv.Convert(pseudo)
						if convErr != nil && convErr != format.ErrSkipChunk {
							WriteStreamError(w, inputFormat, "failed to convert upstream response")
							return usage, fmt.Errorf("failed to convert upstream response: %w", convErr)
						}
						if u := ParseTokenUsage(body, upstreamFormat); u.TotalTokens > 0 {
							conv.SetUsage(u.PromptTokens, u.CompletionTokens, u.CacheHitTokens)
						}
						events = append(events, converted...)
					} else {
						// Anthropic mode: the converter accepts a full
						// message object.
						converted, convErr := conv.Convert(body)
						if convErr != nil && convErr != format.ErrSkipChunk {
							WriteStreamError(w, inputFormat, "failed to convert upstream response")
							return usage, fmt.Errorf("failed to convert upstream response: %w", convErr)
						}
						events = append(events, converted...)
					}
				}
				events = append(events, conv.CloseStream()...)
			}
			for _, ev := range events {
				out := append([]byte("data: "), ev...)
				out = append(out, '\n', '\n')
				if _, err := w.Write(out); err != nil {
					return usage, err
				}
			}
			flusher.Flush()
			return usage, nil
		}

		// Convert the body for cross-format routes so the client gets its
		// own format even when the upstream ignored stream:true.
		frame := body
		if format.NeedConvert(upstreamFormat, inputFormat) {
			var convErr error
			if upstreamFormat == "anthropic" {
				frame, convErr = ConvertAnthropicResponseToOpenAI(body, modelName)
			} else {
				frame, convErr = ConvertOpenAIResponseToAnthropic(body)
			}
			if convErr != nil {
				WriteStreamError(w, inputFormat, "failed to convert upstream response")
				return usage, fmt.Errorf("failed to convert upstream response: %w", convErr)
			}
		}
		// Deliver the completion as consumable STREAM events in the client's
		// format (a bare message object is not a valid stream event).
		if inputFormat == "openai" {
			chunk := completionToStreamChunk(frame, modelName)
			out := append([]byte("data: "), chunk...)
			out = append(out, '\n', '\n')
			if _, err := w.Write(out); err != nil {
				return usage, err
			}
			if _, err := fmt.Fprintf(w, "data: [DONE]\n\n"); err != nil {
				return usage, err
			}
		} else {
			// Empty body (stream:true with nothing returned): emit a clean
			// termination instead of a malformed message_start frame.
			if len(bytes.TrimSpace(frame)) == 0 {
				stop := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":0}}` + "\n\n" +
					`data: {"type":"message_stop"}` + "\n\n")
				if _, err := w.Write(stop); err != nil {
					return usage, err
				}
				flusher.Flush()
				return usage, nil
			}
			// message_start with the message object, then delta + stop.
			// Preserve the frame's actual stop_reason and usage when present.
			stopReason := "end_turn"
			outputTokens := 0
			var msgObj map[string]interface{}
			if json.Unmarshal(frame, &msgObj) == nil {
				if sr, ok := msgObj["stop_reason"].(string); ok && sr != "" {
					stopReason = sr
				}
				if u, ok := msgObj["usage"].(map[string]interface{}); ok {
					if ot, ok := u["output_tokens"].(float64); ok {
						outputTokens = int(ot)
					}
				}
			}
			start := []byte("data: {\"type\":\"message_start\",\"message\":")
			start = append(start, frame...)
			start = append(start, []byte("}\n\n")...)
			if _, err := w.Write(start); err != nil {
				return usage, err
			}
			delta := fmt.Sprintf(`data: {"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":%d}}`+"\n\n", stopReason, outputTokens)
			stop := delta + `data: {"type":"message_stop"}` + "\n\n"
			if _, err := w.Write([]byte(stop)); err != nil {
				return usage, err
			}
		}
		flusher.Flush()
		return usage, nil
	}

	usage = &model.TokenUsage{}
	// IMPORTANT: scan br, not resp.Body — br may hold peeked bytes
	scanner := bufio.NewScanner(br)
	scanner.Buffer(make([]byte, 1024*64), 10*1024*1024) // 64KB initial, 10MB max line

	// Stateful converter for streams where the CLIENT speaks Anthropic and
	// the upstream is OpenAI (synthesizes message_start / message_delta /
	// message_stop and re-joins tool_calls)
	var anthConv *format.AnthropicStreamConverter
	// Stateful converter for streams where the CLIENT speaks OpenAI and the
	// upstream is Anthropic (maps tool_use blocks to tool_calls deltas)
	var oaiConv *format.OpenAIStreamConverter
	// Stateful converter for streams where the CLIENT speaks the Responses
	// API and the upstream speaks chat completions or Anthropic Messages
	// (synthesizes response.created / output deltas / response.completed)
	var rsc *format.ResponsesStreamConverter
	// Whether the upstream emitted the client-format terminator itself
	sawDone := false
	sawStop := false
	sawDelta := false
	sawCompleted := false
	// A same-format stream may end with an error frame and no terminator —
	// don't append [DONE] after it (SDKs treat [DONE] as success)
	sawErrorFrame := false

	for scanner.Scan() {
		line := scanner.Text()

		// Forward SSE non-data lines (e.g. Anthropic "event: message_start").
		// They must be dropped whenever the stream is being converted: the
		// event names leak the upstream's framing and strict SSE parsers
		// break on them.
		if !strings.HasPrefix(line, "data:") {
			if format.NeedConvert(upstreamFormat, inputFormat) {
				continue
			}
			_, err := fmt.Fprintf(w, "%s\n", line)
			if err != nil {
				resp.Body.Close()
				return usage, err
			}
			flusher.Flush()
			continue
		}

		// Handle "[DONE]" message (tolerate "data:[DONE]" without space)
		if strings.TrimSpace(strings.TrimPrefix(line, "data:")) == "[DONE]" {
			// Anthropic-format and Responses-format clients don't speak
			// "[DONE]"; drop it when converting. The loop appends a [DONE]
			// for OpenAI-format clients when converting from anthropic.
			if inputFormat == "anthropic" || inputFormat == "responses" {
				continue
			}
			sawDone = true
			_, err := fmt.Fprintf(w, "%s\n", line)
			if err != nil {
				resp.Body.Close()
				return usage, err
			}
			flusher.Flush()
			continue
		}

		// Extract JSON data (tolerate "data:{...}" without a space — some
		// OpenAI-compatible gateways omit it)
		jsonStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

		// Empty data frames are valid SSE keepalives — skip them, don't feed
		// them to the converters (which would error and kill the stream)
		if jsonStr == "" {
			continue
		}

		// Track whether the upstream emitted its own message_stop/message_delta
		// (relevant for synthesizing termination on clean EOF) or a real error
		// frame (don't append [DONE] after it)
		var ev struct {
			Type  string          `json:"type"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(jsonStr), &ev) == nil {
			if inputFormat == "anthropic" {
				switch ev.Type {
				case "message_stop":
					sawStop = true
				case "message_delta":
					sawDelta = true
				}
			}
			// A native Responses stream ends with response.completed /
			// response.failed — nothing may be synthesized after them. The
			// streaming error event ({"type":"error",...}) is also terminal.
			if inputFormat == "responses" && ev.Type == "response.completed" {
				sawCompleted = true
			}
			if isErrorPayload(ev.Error) || (inputFormat == "responses" &&
				(ev.Type == "response.failed" || ev.Type == "error")) {
				sawErrorFrame = true
			}
		}

		// Try to extract token usage from stream events
		extractStreamUsage([]byte(jsonStr), inputFormat, upstreamFormat, usage)

		// Convert format if needed; each event becomes its own data frame
		var converted [][]byte
		var err error

		if format.NeedConvert(upstreamFormat, inputFormat) {
			switch {
			case inputFormat == "responses" && upstreamFormat == "openai":
				// Client speaks the Responses API, upstream is chat
				// completions: convert each chunk into Responses events.
				if rsc == nil {
					rsc = format.NewResponsesStreamConverter("openai")
					rsc.SetModel(modelName)
				}
				converted, err = rsc.Convert([]byte(jsonStr))
			case inputFormat == "responses" && upstreamFormat == "anthropic":
				// Client speaks the Responses API, upstream is Anthropic:
				// convert each event into Responses events.
				if rsc == nil {
					rsc = format.NewResponsesStreamConverter("anthropic")
					rsc.SetModel(modelName)
				}
				converted, err = rsc.Convert([]byte(jsonStr))
			case inputFormat == "anthropic" && upstreamFormat == "openai":
				// Client speaks Anthropic, upstream is OpenAI: convert each
				// OpenAI chunk into properly-ordered Anthropic events.
				if anthConv == nil {
					anthConv = format.NewAnthropicStreamConverter()
				}
				converted, err = anthConv.Convert([]byte(jsonStr), modelName)
			case inputFormat == "openai" && upstreamFormat == "anthropic":
				// Client speaks OpenAI, upstream is Anthropic: convert each
				// Anthropic event into an OpenAI chunk (incl. tool_calls).
				if oaiConv == nil {
					oaiConv = format.NewOpenAIStreamConverter()
					oaiConv.SetModel(modelName)
				}
				converted, err = oaiConv.Convert([]byte(jsonStr))
			default:
				converted = [][]byte{[]byte(jsonStr)}
			}
		} else {
			converted = [][]byte{[]byte(jsonStr)}
		}

		// Write converted chunk(s) to client FIRST: even on ErrSkipChunk the
		// converter may have produced leading events (e.g. the synthesized
		// message_start that accompanies a role-only first chunk).
		for _, chunk := range converted {
			out := append([]byte("data: "), chunk...)
			out = append(out, '\n', '\n')
			_, writeErr := w.Write(out)
			if writeErr != nil {
				resp.Body.Close()
				return usage, writeErr
			}
		}

		if err != nil {
			if err == format.ErrSkipChunk {
				flusher.Flush()
				continue
			}
			// Genuine conversion failure (e.g. malformed upstream chunk) —
			// surface it instead of silently dropping the chunk
			log.Printf("[relay] stream chunk conversion error: %v", err)
			WriteStreamError(w, inputFormat, "stream conversion error")
			flusher.Flush()
			return usage, err
		}
		flusher.Flush()
	}

	// Hard failure (connection drop, oversized SSE line): surface it to the
	// client BEFORE any termination frame — SDKs stop at [DONE]/message_stop
	// and would otherwise swallow the error and treat a truncated stream as
	// a successful completion.
	if scanErr := scanner.Err(); scanErr != nil {
		resp.Body.Close()
		WriteStreamError(w, inputFormat, "upstream connection lost")
		return usage, scanErr
	}

	// If the upstream ended without a finish chunk (EOF), close the converted
	// Anthropic stream (open content blocks + message_stop) so clients don't
	// wait for a message_stop that never comes.
	if anthConv != nil && !anthConv.Finished() {
		for _, ev := range anthConv.CloseStream() {
			out := append([]byte("data: "), ev...)
			out = append(out, '\n', '\n')
			if _, err := w.Write(out); err != nil {
				return usage, err
			}
		}
		flusher.Flush()
	}

	// Same for converted Responses streams: a clean EOF before the usage
	// chunk or before message_stop still needs its response.completed.
	if rsc != nil && !rsc.Finished() {
		for _, ev := range rsc.CloseStream() {
			out := append([]byte("data: "), ev...)
			out = append(out, '\n', '\n')
			if _, err := w.Write(out); err != nil {
				return usage, err
			}
		}
		flusher.Flush()
	}

	// Synthesize termination for ANY clean-EOF stream that lacks it, in the
	// client's format: same-format streams (most common case) have no
	// converter to do this, so an upstream that drops the connection after
	// content would otherwise leave the client hanging.
	if inputFormat == "openai" && !sawDone && !sawErrorFrame {
		// A converted stream may have ended with an error frame — don't add
		// [DONE] after it (SDKs treat [DONE] as success)
		if !(oaiConv != nil && oaiConv.Errored()) {
			if _, err := fmt.Fprintf(w, "data: [DONE]\n\n"); err != nil {
				return usage, err
			}
			flusher.Flush()
		}
	} else if inputFormat == "anthropic" && !sawStop && !sawErrorFrame && !(anthConv != nil && anthConv.Finished()) {
		if sawDelta {
			// A real message_delta was already forwarded — emit only the
			// missing message_stop (a second delta would zero client usage)
			if _, err := fmt.Fprintf(w, "data: {\"type\":\"message_stop\"}\n\n"); err != nil {
				return usage, err
			}
		} else {
			stop := []byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":0}}` + "\n\n" +
				`data: {"type":"message_stop"}` + "\n\n")
			if _, err := w.Write(stop); err != nil {
				return usage, err
			}
		}
		flusher.Flush()
	} else if inputFormat == "responses" && !sawCompleted && !sawErrorFrame && rsc == nil {
		// A native Responses stream ends with response.completed; an
		// upstream that drops the connection instead leaves the SDK waiting
		// forever. Synthesize a minimal completion so clients can finish.
		completed, _ := json.Marshal(map[string]interface{}{
			"type": "response.completed",
			"response": map[string]interface{}{
				"id":         "resp_local",
				"object":     "response",
				"created_at": time.Now().Unix(),
				"status":     "completed",
				"model":      modelName,
				"output":     []interface{}{},
				"usage":      nil,
			},
		})
		if _, err := fmt.Fprintf(w, "data: %s\n\n", completed); err != nil {
			return usage, err
		}
		flusher.Flush()
	}

	// A stream that ended with an upstream error frame must not be recorded
	// as a successful, billable completion — return an error so the handler
	// skips consumption/budget recording (the client already received the
	// error frame).
	if sawErrorFrame || (oaiConv != nil && oaiConv.Errored()) || (rsc != nil && rsc.Errored()) {
		return usage, errors.New("upstream stream error")
	}

	return usage, nil
}

// isErrorPayload reports whether a JSON "error" field value represents a real
// error (an object with fields or a non-empty string). Null, booleans,
// numbers and empty objects are not errors.
func isErrorPayload(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	s := string(raw)
	if s == "{}" || s == "[]" || s == "false" || s == "0" || s == `""` {
		return false
	}
	var v interface{}
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch t := v.(type) {
	case map[string]interface{}:
		return len(t) > 0
	case string:
		return t != ""
	default:
		return false
	}
}

// IsErrorPayload is the exported form of isErrorPayload (used by the handler
// package to detect 200-with-error bodies)
func IsErrorPayload(raw json.RawMessage) bool {
	return isErrorPayload(raw)
}

// completionToStreamChunk converts a full OpenAI completion object into a
// single valid chat.completion.chunk frame (content, tool_calls and
// finish_reason moved into the delta), for upstreams that ignored stream:true.
func completionToStreamChunk(body []byte, modelName string) []byte {
	var comp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      *struct {
				Content          interface{}   `json:"content"`
				Role             string        `json:"role"`
				ToolCalls        []interface{} `json:"tool_calls"`
				ReasoningContent interface{}   `json:"reasoning_content"`
			} `json:"message"`
			Delta *struct {
				Content          string        `json:"content"`
				Role             string        `json:"role"`
				ToolCalls        []interface{} `json:"tool_calls"`
				ReasoningContent interface{}   `json:"reasoning_content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &comp) != nil || len(comp.Choices) == 0 {
		return body // not a recognizable completion — pass through
	}

	content := ""
	role := "assistant"
	var toolCalls []interface{}
	var reasoningContent interface{}
	c := comp.Choices[0]
	if c.Message != nil {
		if s, ok := c.Message.Content.(string); ok {
			content = s
		} else if arr, ok := c.Message.Content.([]interface{}); ok {
			var texts []string
			for _, part := range arr {
				if p, ok := part.(map[string]interface{}); ok && p["type"] == "text" {
					if t, ok := p["text"].(string); ok {
						texts = append(texts, t)
					}
				}
			}
			content = strings.Join(texts, "")
		}
		if c.Message.Role != "" {
			role = c.Message.Role
		}
		toolCalls = c.Message.ToolCalls
		// DeepSeek-style reasoning: keep it so streaming clients still see
		// the model's thinking (e.g. opencode's reasoning_content interleave).
		reasoningContent = c.Message.ReasoningContent
	} else if c.Delta != nil {
		// Body was already a single chunk object (choices[0].delta)
		content = c.Delta.Content
		if c.Delta.Role != "" {
			role = c.Delta.Role
		}
		toolCalls = c.Delta.ToolCalls
		reasoningContent = c.Delta.ReasoningContent
	}

	delta := map[string]interface{}{
		"content": content,
		"role":    role,
	}
	if len(toolCalls) > 0 {
		delta["tool_calls"] = toolCalls
	}
	if reasoningContent != nil {
		delta["reasoning_content"] = reasoningContent
	}
	chunk := map[string]interface{}{
		"id":      "chatcmpl-local",
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   modelName,
		"choices": []interface{}{
			map[string]interface{}{
				"index":         0,
				"delta":         delta,
				"finish_reason": c.FinishReason,
			},
		},
	}
	out, err := json.Marshal(chunk)
	if err != nil {
		return body
	}
	return out
}

// extractStreamUsage tries to parse token usage from streaming events.
// Usage always arrives in the UPSTREAM's format, so the branch is chosen by
// upstreamFormat alone — this prevents e.g. an Anthropic message_delta event
// from being misparsed by the OpenAI branch (which would zero the prompt
// tokens captured at message_start).
func extractStreamUsage(data []byte, inputFormat, upstreamFormat string, usage *model.TokenUsage) {
	// Usage always arrives in the UPSTREAM's format, so the branch is chosen by
	// upstreamFormat alone — this prevents e.g. an Anthropic message_delta event
	// from being misparsed by the OpenAI branch (which would zero the prompt
	// tokens captured at message_start).
	//
	// usage.Format is only assigned where values are actually captured: a
	// non-conforming trailing frame after response.completed must not reset
	// billing semantics (responses → openai) to "responses".
	switch upstreamFormat {
	case "openai":
		// OpenAI final chunk may include usage
		var chunk struct {
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
				TotalTokens      int64 `json:"total_tokens"`
				PromptDetails    *struct {
					CachedTokens int64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(data, &chunk); err == nil && chunk.Usage != nil {
			usage.Format = "openai"
			usage.PromptTokens = chunk.Usage.PromptTokens
			usage.CompletionTokens = chunk.Usage.CompletionTokens
			usage.TotalTokens = chunk.Usage.TotalTokens
			// Same derivation as ParseTokenUsage: a gateway omitting
			// total_tokens must not zero out the usage record.
			if usage.TotalTokens == 0 {
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			}
			if chunk.Usage.PromptDetails != nil {
				usage.CacheHitTokens = chunk.Usage.PromptDetails.CachedTokens
			}
		}
	case "anthropic":
		// message_start carries input tokens and cache info (nested under
		// "message"); message_delta carries output tokens (top-level "usage")
		var event struct {
			Type  string `json:"type"`
			Usage *struct {
				InputTokens         int64 `json:"input_tokens"`
				OutputTokens        int64 `json:"output_tokens"`
				CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
				CacheReadTokens     int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
			Message *struct {
				Usage *struct {
					InputTokens         int64 `json:"input_tokens"`
					CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
					CacheReadTokens     int64 `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(data, &event); err == nil {
			switch event.Type {
			case "message_start":
				// input tokens and cache figures live inside message.usage
				if event.Message != nil && event.Message.Usage != nil {
					usage.Format = "anthropic"
					usage.PromptTokens = event.Message.Usage.InputTokens
					usage.CacheWriteTokens = event.Message.Usage.CacheCreationTokens
					usage.CacheHitTokens = event.Message.Usage.CacheReadTokens
				}
			case "message_delta":
				if event.Usage != nil {
					usage.CompletionTokens = event.Usage.OutputTokens
				}
			}
			// Anthropic counts cache tokens toward its own TPM limits, so
			// they must count toward our token budgets too (OpenAI's
			// total_tokens already includes them)
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens + usage.CacheHitTokens + usage.CacheWriteTokens
		}
	case "responses":
		// A native Responses stream carries usage only in response.completed
		// (nested under "response"). The Responses API's input_tokens INCLUDE
		// cached tokens — same semantics as OpenAI, so billing subtracts
		// cached tokens at the cache-read rate.
		var event struct {
			Type     string `json:"type"`
			Response *struct {
				Usage *struct {
					InputTokens  int64 `json:"input_tokens"`
					OutputTokens int64 `json:"output_tokens"`
					TotalTokens  int64 `json:"total_tokens"`
					InputDetails *struct {
						CachedTokens int64 `json:"cached_tokens"`
					} `json:"input_tokens_details"`
				} `json:"usage"`
			} `json:"response"`
		}
		if err := json.Unmarshal(data, &event); err == nil && event.Type == "response.completed" &&
			event.Response != nil && event.Response.Usage != nil {
			u := event.Response.Usage
			usage.PromptTokens = u.InputTokens
			usage.CompletionTokens = u.OutputTokens
			usage.TotalTokens = u.TotalTokens
			if usage.TotalTokens == 0 {
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			}
			if u.InputDetails != nil {
				usage.CacheHitTokens = u.InputDetails.CachedTokens
			}
			usage.Format = "openai" // responses semantics == OpenAI semantics
		}
	}
}

// WriteStreamError sends an error message to the downstream client in stream format (JSON-safe)
func WriteStreamError(w http.ResponseWriter, inputFormat string, errMsg string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	// SSE content type so SDKs that gate on it treat the body as a stream
	w.Header().Set("Content-Type", "text/event-stream")

	var errPayload []byte
	switch {
	case inputFormat == "openai":
		errPayload, _ = json.Marshal(map[string]interface{}{
			"error": map[string]string{
				"message": errMsg,
				"type":    "stream_error",
			},
		})
	case inputFormat == "responses":
		// The Responses API's streaming error event carries code/message at
		// the top level (not nested under "error")
		errPayload, _ = json.Marshal(map[string]interface{}{
			"type":    "error",
			"code":    "stream_error",
			"message": errMsg,
			"param":   nil,
		})
	default:
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

// mergeExtraParams merges a JSON object of extra params into a request body.
// The extra params win on key conflicts (deliberate override, e.g. pinning
// temperature). Both must be JSON objects; on any parse failure the original
// body is returned unchanged so a misconfigured group can't break every
// request it serves.
func mergeExtraParams(body []byte, extraParams string) ([]byte, error) {
	var req map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		return body, nil // not clean JSON — leave as-is
	}
	if dec.More() {
		return body, nil // trailing data
	}

	var extra map[string]interface{}
	if err := json.Unmarshal([]byte(extraParams), &extra); err != nil {
		return body, nil // invalid extra params — leave as-is
	}
	for k, v := range extra {
		req[k] = v
	}
	return json.Marshal(req)
}

// replaceModelName replaces the "model" field in a JSON request body if targetModel is set
func replaceModelName(body []byte, targetModel string) ([]byte, error) {
	if targetModel == "" {
		return body, nil
	}

	// Decode with UseNumber so large integers (e.g. big "seed" values) are
	// NOT coerced to float64 and corrupted on re-marshal. Trailing data
	// after the top-level value is rejected (like json.Unmarshal).
	var req map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		return body, nil // Return original on parse error
	}
	if dec.More() {
		return body, nil // trailing data → not clean JSON, return original
	}

	req["model"] = targetModel
	return json.Marshal(req)
}

// ConvertAnthropicResponseToOpenAI converts an Anthropic response to OpenAI format
func ConvertAnthropicResponseToOpenAI(body []byte, model string) ([]byte, error) {
	var anthResp map[string]interface{}
	if err := json.Unmarshal(body, &anthResp); err != nil {
		return nil, fmt.Errorf("invalid anthropic response body: %w", err)
	}

	// Map stop_reason → finish_reason (tool_use responses must not report "stop")
	finishReason := "stop"
	switch anthResp["stop_reason"] {
	case "max_tokens":
		finishReason = "length"
	case "tool_use":
		finishReason = "tool_calls"
	case "refusal":
		finishReason = "content_filter"
	}

	msg := map[string]interface{}{
		"role":    "assistant",
		"content": extractAnthropicContent(anthResp),
	}
	// tool_use content blocks → tool_calls
	if toolCalls := extractAnthropicToolCalls(anthResp); len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	// thinking blocks (extended thinking) → reasoning_content so OpenAI
	// clients (opencode, DeepSeek-style gateways) keep the reasoning chain
	if rc := extractAnthropicThinking(anthResp); rc != "" {
		msg["reasoning_content"] = rc
	}

	oaiResp := map[string]interface{}{
		"id":      anthResp["id"],
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []interface{}{
			map[string]interface{}{
				"index":         0,
				"finish_reason": finishReason,
				"message":       msg,
			},
		},
		"usage": extractAnthropicUsage(anthResp),
	}

	return json.Marshal(oaiResp)
}

// extractAnthropicToolCalls converts Anthropic tool_use content blocks to
// OpenAI tool_calls entries (arguments object → JSON string).
func extractAnthropicToolCalls(anthResp map[string]interface{}) []interface{} {
	content, ok := anthResp["content"].([]interface{})
	if !ok {
		return nil
	}
	var calls []interface{}
	for _, c := range content {
		block, ok := c.(map[string]interface{})
		if !ok || block["type"] != "tool_use" {
			continue
		}
		calls = append(calls, map[string]interface{}{
			"id":   block["id"],
			"type": "function",
			"function": map[string]interface{}{
				"name":      block["name"],
				"arguments": toJSONString(block["input"]),
			},
		})
	}
	return calls
}

// ConvertOpenAIResponseToAnthropic converts an OpenAI response to Anthropic format
func ConvertOpenAIResponseToAnthropic(body []byte) ([]byte, error) {
	var oaiResp map[string]interface{}
	if err := json.Unmarshal(body, &oaiResp); err != nil {
		return nil, fmt.Errorf("invalid openai response body: %w", err)
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

		// DeepSeek-style reasoning_content → thinking block (empty
		// signature: it is cryptographically bound to the original turn and
		// cannot be fabricated; compatible gateways accept it).
		var thinkingParts []string
		if rc, ok := msg["reasoning_content"].(string); ok && rc != "" {
			thinkingParts = append(thinkingParts, rc)
		}

		content := []interface{}{}
		switch c := msg["content"].(type) {
		case string:
			if c != "" {
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": c,
				})
			}
		case []interface{}:
			// Some OpenAI-compatible gateways return content as parts;
			// text parts become text blocks, reasoning parts (o-series
			// style) become a thinking block.
			var texts []string
			for _, part := range c {
				p, ok := part.(map[string]interface{})
				if !ok {
					continue
				}
				switch p["type"] {
				case "text":
					if t, ok := p["text"].(string); ok {
						texts = append(texts, t)
					}
				case "reasoning":
					if t := format.ReasoningSummaryText(p); t != "" {
						thinkingParts = append(thinkingParts, t)
					}
				}
			}
			if t := strings.Join(texts, ""); t != "" {
				content = append(content, map[string]interface{}{
					"type": "text",
					"text": t,
				})
			}
		}
		if len(thinkingParts) > 0 {
			// thinking precedes text in Anthropic responses
			content = append([]interface{}{map[string]interface{}{
				"type":      "thinking",
				"thinking":  strings.Join(thinkingParts, "\n"),
				"signature": "",
			}}, content...)
		}
		// tool_calls → tool_use content blocks (arguments string → object)
		if toolCalls, ok := msg["tool_calls"].([]interface{}); ok {
			for _, tc := range toolCalls {
				tcMap, ok := tc.(map[string]interface{})
				if !ok {
					continue
				}
				fn, ok := tcMap["function"].(map[string]interface{})
				if !ok {
					continue
				}
				content = append(content, map[string]interface{}{
					"type":  "tool_use",
					"id":    tcMap["id"],
					"name":  fn["name"],
					"input": parseJSONObject(fn["arguments"]),
				})
			}
		}
		if len(content) > 0 {
			anthResp["content"] = content
		}

		if reason, ok := choice["finish_reason"].(string); ok {
			switch reason {
			case "length":
				anthResp["stop_reason"] = "max_tokens"
			case "tool_calls", "function_call":
				anthResp["stop_reason"] = "tool_use"
			case "content_filter":
				anthResp["stop_reason"] = "refusal"
			default:
				anthResp["stop_reason"] = "end_turn"
			}
		}
	}

	return json.Marshal(anthResp)
}

// toJSONString marshals v into a JSON string (or "{}" on failure)
func toJSONString(v interface{}) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// parseJSONObject parses a JSON string into a map (or {} on failure)
func parseJSONObject(v interface{}) map[string]interface{} {
	s, ok := v.(string)
	if !ok || s == "" {
		return map[string]interface{}{}
	}
	var obj map[string]interface{}
	if json.Unmarshal([]byte(s), &obj) != nil || obj == nil {
		return map[string]interface{}{}
	}
	return obj
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
	cacheCreation := toFloat64(usage["cache_creation_input_tokens"])
	cacheRead := toFloat64(usage["cache_read_input_tokens"])
	// OpenAI convention: prompt_tokens INCLUDES cached tokens (the
	// streaming converter applies the same rule) so the client sees
	// identical usage whether or not stream:true was set.
	promptTokens := inputTokens + cacheCreation + cacheRead
	return map[string]interface{}{
		"input_tokens":      inputTokens,
		"output_tokens":     outputTokens,
		"prompt_tokens":     promptTokens,
		"completion_tokens": outputTokens,
		"total_tokens":      promptTokens + outputTokens,
		"prompt_tokens_details": map[string]interface{}{
			"cached_tokens": cacheCreation + cacheRead,
		},
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

// extractAnthropicThinking collects the text of an Anthropic response's
// thinking blocks (extended thinking) so it can be reported as OpenAI
// reasoning_content. redacted_thinking blocks have no text and are skipped.
// Multiple blocks are joined with "\n" (like the request-direction
// conversion) so block boundaries never merge words.
func extractAnthropicThinking(anthResp map[string]interface{}) string {
	content, ok := anthResp["content"].([]interface{})
	if !ok {
		return ""
	}
	var texts []string
	for _, c := range content {
		if block, ok := c.(map[string]interface{}); ok && block["type"] == "thinking" {
			if t, ok := block["thinking"].(string); ok && t != "" {
				texts = append(texts, t)
			}
		}
	}
	return strings.Join(texts, "\n")
}

// parseTokenUsage extracts token usage from a response body
func ParseTokenUsage(body []byte, format string) *model.TokenUsage {
	usage := &model.TokenUsage{Format: format}

	if format == "openai" {
		var resp struct {
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
				TotalTokens      int64 `json:"total_tokens"`
				PromptDetails    *struct {
					CachedTokens int64 `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &resp); err == nil && resp.Usage != nil {
			usage.PromptTokens = resp.Usage.PromptTokens
			usage.CompletionTokens = resp.Usage.CompletionTokens
			usage.TotalTokens = resp.Usage.TotalTokens
			// Some OpenAI-compatible gateways omit total_tokens — derive it
			// so billing (and the streaming SetUsage gate below) never
			// reports 0 for a request that consumed tokens.
			if usage.TotalTokens == 0 {
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			}
			if resp.Usage.PromptDetails != nil {
				usage.CacheHitTokens = resp.Usage.PromptDetails.CachedTokens
			}
		}
	} else if format == "anthropic" {
		var resp struct {
			Usage *struct {
				InputTokens         int64 `json:"input_tokens"`
				OutputTokens        int64 `json:"output_tokens"`
				CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
				CacheReadTokens     int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &resp); err == nil && resp.Usage != nil {
			usage.PromptTokens = resp.Usage.InputTokens
			usage.CompletionTokens = resp.Usage.OutputTokens
			usage.CacheWriteTokens = resp.Usage.CacheCreationTokens
			usage.CacheHitTokens = resp.Usage.CacheReadTokens
			// Anthropic counts cache tokens toward its own TPM limits, so
			// they must count toward our token budgets too
			usage.TotalTokens = resp.Usage.InputTokens + resp.Usage.OutputTokens +
				resp.Usage.CacheCreationTokens + resp.Usage.CacheReadTokens
		}
	} else if format == "responses" {
		// Native /v1/responses bodies carry usage with Responses semantics:
		// input_tokens INCLUDES cached tokens (like OpenAI's prompt_tokens),
		// so the usage record reports Format "openai" for billing.
		var resp struct {
			Usage *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
				TotalTokens  int64 `json:"total_tokens"`
				InputDetails *struct {
					CachedTokens int64 `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(body, &resp); err == nil && resp.Usage != nil {
			usage.PromptTokens = resp.Usage.InputTokens
			usage.CompletionTokens = resp.Usage.OutputTokens
			usage.TotalTokens = resp.Usage.TotalTokens
			// Same derivation as the openai branch: a gateway omitting
			// total_tokens must not zero out billing.
			if usage.TotalTokens == 0 {
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			}
			if resp.Usage.InputDetails != nil {
				usage.CacheHitTokens = resp.Usage.InputDetails.CachedTokens
			}
			usage.Format = "openai"
		}
	}

	return usage
}
