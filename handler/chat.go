package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"key-router/billing"
	"key-router/db"
	"key-router/format"
	"key-router/health"
	"key-router/model"
	"key-router/relay"
	"key-router/selector"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ChatHandler handles OpenAI-compatible chat completion requests
type ChatHandler struct {
	Engine *selector.Engine
}

// NewChatHandler creates a new handler
func NewChatHandler(engine *selector.Engine) *ChatHandler {
	return &ChatHandler{Engine: engine}
}

// HandleChatCompletion handles POST /v1/chat/completions
func (h *ChatHandler) HandleChatCompletion(c *gin.Context) {
	h.handleRelay(c, "openai")
}

// HandleResponses handles POST /v1/responses (OpenAI Responses API)
func (h *ChatHandler) HandleResponses(c *gin.Context) {
	h.handleRelay(c, "responses")
}

// HandleMessages handles POST /v1/messages (Anthropic format)
func (h *ChatHandler) HandleMessages(c *gin.Context) {
	h.handleRelay(c, "anthropic")
}

// HandleModels handles GET /v1/models
func (h *ChatHandler) HandleModels(c *gin.Context) {
	var groups []model.ModelGroup
	if err := db.GetDB().Where("enabled = ?", true).Find(&groups).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// The response carries per-model metadata (context_length /
	// max_output_tokens) so model-discovery tools (opencode's
	// models-discovery plugin, and the upcoming official auto-discovery)
	// can populate accurate limits without a hand-written models map.
	data := make([]gin.H, 0, len(groups))
	for _, g := range groups {
		entry := gin.H{
			"id":       g.GroupID,
			"object":   "model",
			"created":  0,
			"owned_by": "key-router",
		}
		if g.Name != "" {
			entry["name"] = g.Name
		}
		if g.ContextLength > 0 {
			entry["context_length"] = g.ContextLength
			entry["max_context_length"] = g.ContextLength
		}
		if g.MaxOutputTokens > 0 {
			entry["max_output_tokens"] = g.MaxOutputTokens
		}
		data = append(data, entry)
	}

	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   data,
	})
}

// HandleEmbeddings handles POST /v1/embeddings
func (h *ChatHandler) HandleEmbeddings(c *gin.Context) {
	h.handleRelay(c, "openai")
}

// maxRequestBody caps buffered request/response bodies (LLM payloads can be
// large, but this prevents unbounded RAM use from malformed inputs)
const maxRequestBody = 256 << 20 // 256 MiB

// handleRelay is the core relay handler with a bounded retry loop
func (h *ChatHandler) handleRelay(c *gin.Context, inputFormat string) {
	// Read request body (bounded)
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxRequestBody+1))
	if err != nil {
		writeRelayError(c, inputFormat, http.StatusBadRequest, "body_read_failed", "invalid_request_error",
			"failed to read request body")
		return
	}
	if len(body) > maxRequestBody {
		writeRelayError(c, inputFormat, http.StatusRequestEntityTooLarge, "body_too_large", "invalid_request_error",
			"request body too large")
		return
	}

	// Extract model from body
	var reqMeta struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &reqMeta); err != nil || reqMeta.Model == "" {
		writeRelayError(c, inputFormat, http.StatusBadRequest, "invalid_request", "invalid_request_error",
			"invalid request body or missing model")
		return
	}

	// Get retry times from settings (per-group retry_times overrides when set)
	retryStr := db.GetSetting(model.SettingRetryTimes)
	maxRetries, err := strconv.Atoi(retryStr)
	if err != nil {
		maxRetries = 3 // default when unset/invalid
	}
	var group model.ModelGroup
	groupErr := db.GetDB().Where("group_id = ?", reqMeta.Model).First(&group).Error
	if groupErr != nil && !errors.Is(groupErr, gorm.ErrRecordNotFound) {
		log.Printf("[relay] group lookup error for %s: %v", reqMeta.Model, groupErr)
	}
	if groupErr == nil && group.RetryTimes > 0 {
		maxRetries = group.RetryTimes
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxRetries > 20 {
		maxRetries = 20 // guard against unbounded retry storms from API-set values
	}

	// Unknown model: return 404 (OpenAI convention) instead of a misleading
	// 429 key-exhausted that clients would retry forever. A genuine DB
	// failure during the lookup is a 500, not a 404.
	if groupErr != nil {
		if errors.Is(groupErr, gorm.ErrRecordNotFound) {
			log.Printf("[relay] model group not found: %s", reqMeta.Model)
			writeRelayError(c, inputFormat, http.StatusNotFound, "model_not_found", "invalid_request_error",
				fmt.Sprintf("model %q does not exist", reqMeta.Model))
		} else {
			log.Printf("[relay] group lookup error for %s: %v", reqMeta.Model, groupErr)
			writeRelayError(c, inputFormat, http.StatusInternalServerError, "group_lookup_failed", "server_error",
				"failed to resolve model group")
		}
		return
	}

	// Group exists but has no enabled routes configured — a permanent
	// misconfiguration, not a transient exhaustion.
	if len(h.Engine.GetRoutes(reqMeta.Model)) == 0 {
		log.Printf("[relay] no routes configured for model %s", reqMeta.Model)
		writeRelayError(c, inputFormat, http.StatusNotFound, "no_routes_configured", "invalid_request_error",
			fmt.Sprintf("no routes configured for model %q", reqMeta.Model))
		return
	}

	// Bounded retry loop. Routes that prove unable to serve this request type
	// (ErrUnsupportedRoute) are excluded and re-selected WITHOUT consuming an
	// attempt, so selection converges onto a serving route even with
	// retry_times=0. Attempts are consumed by actual upstream failures
	// (network errors, 429/401/403/5xx).
	excludedRoutes := make(map[int64]bool)
	lastUpstreamStatus := 0
	for attempt := 0; attempt <= maxRetries; {
		// Select a route and key across ALL priority tiers
		route, key, err := h.Engine.RetryLoop(reqMeta.Model, excludedRoutes)
		if err != nil {
			allRoutes := h.Engine.GetRoutes(reqMeta.Model)
			if len(excludedRoutes) > 0 && len(excludedRoutes) == len(allRoutes) {
				// Every route of the group was unsupported for this request
				// type (e.g. embeddings with only anthropic providers)
				log.Printf("[relay] no route can serve model %s: %v", reqMeta.Model, err)
				writeRelayError(c, inputFormat, http.StatusBadRequest, "unsupported_route", "unsupported_route",
					"no route can serve this request (e.g. embeddings with only anthropic providers)")
				return
			}
			log.Printf("[relay] no available route for model %s (attempt %d/%d): %v",
				reqMeta.Model, attempt+1, maxRetries+1, err)
			writeRelayError(c, inputFormat, http.StatusTooManyRequests, "key_exhausted", "rate_limit_error",
				"All available keys have been exhausted. Please try again later.")
			return
		}

		// Resolve target model name
		targetModel := reqMeta.Model
		if route.Route.TargetModel != "" {
			targetModel = route.Route.TargetModel
		}

		// Build request metadata with forwarded headers
		// Route-level extra params (model groups have none).
		meta := &model.RequestMetadata{
			Format:      inputFormat,
			Model:       reqMeta.Model,
			Stream:      reqMeta.Stream,
			RequestPath: c.Request.URL.Path,
			RequestBody: body,
			Headers:     c.Request.Header.Clone(),
			TargetModel: targetModel,
			ExtraParams: route.Route.ExtraParams,
			Ctx:         c.Request.Context(),
		}

		// Forward request to upstream
		rr, err := relay.ForwardRequest(meta, key, route.Provider)
		if err != nil {
			// Unsupported routes are per-route: another route in the group
			// may still serve the request (e.g. embeddings via an openai
			// provider). Exclude and re-select without burning an attempt.
			if errors.Is(err, relay.ErrUnsupportedRoute) {
				excludedRoutes[route.Route.ID] = true
				log.Printf("[relay] unsupported route %d for key %d: %v", route.Route.ID, key.ID, err)
				continue
			}
			log.Printf("[relay] forward error for key %d: %v", key.ID, err)
			// Client disconnected (context canceled): abort WITHOUT cooling
			// the key — cooling every key of the group on one disconnect
			// would take the whole group out of rotation for 30s.
			if c.Request.Context().Err() != nil {
				return
			}
			// Cool the key down so the retry loop fails over to sibling keys
			// instead of re-selecting the same dead route every attempt.
			// A network error is transient: mark once, fail over, never
			// disable on it.
			h.Engine.RecordResult(key.ID, false, model.ReasonNetworkError, 30*time.Second)
			attempt++
			continue
		}
		resp := rr.Resp

		// Drain a bounded amount of retry-eligible bodies before closing so
		// SMALL error bodies still let the connection be reused; larger ones
		// (verbose gateway pages) close the connection — a fresh dial is
		// acceptable for those.
		drainClose := func(resp *http.Response) {
			io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
		}

		// Handle retry-eligible HTTP responses (close resp before retrying).
		// Track the last attempt's status so the terminal error can preserve
		// a rate-limit signal.
		lastUpstreamStatus = resp.StatusCode
		if resp.StatusCode == http.StatusTooManyRequests {
			// 429 is ambiguous: OpenAI returns 429 + error.code
			// "insufficient_quota" / "billing_hard_limit_reached" when the
			// account has no balance left, and a plain 429 (no such code) for
			// genuine rate limiting. Quota exhaustion DISABLES the key after
			// 2 consecutive identical observations (it would never recover
			// on its own); a real rate limit only cools it. Read a bounded
			// chunk of the body to classify.
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
			if isQuotaExhaustedError(errBody) {
				h.Engine.RecordResult(key.ID, false, model.ReasonInsufficientQuota, 30*time.Second)
				log.Printf("[relay] key %d quota exhausted (429 + quota error, attempt %d/%d)", key.ID, attempt+1, maxRetries+1)
				attempt++
				continue
			}

			retryAfter := 60 * time.Second
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if sec, err := strconv.Atoi(ra); err == nil {
					// Clamp to [1s, 1h]: a 0/negative Retry-After would
					// immediately re-admit the same hot key and defeat
					// failover; an absurd value would bench the key forever.
					if sec < 1 {
						sec = 1
					}
					if sec > 3600 {
						sec = 3600
					}
					retryAfter = time.Duration(sec) * time.Second
				}
			}
			// Plain 429: the key is hot, not broken — mark once with the
			// upstream's own cooldown and fail over. The UI shows "HTTP 429".
			h.Engine.RecordResult(key.ID, false, model.HTTPStatusReason(resp.StatusCode), retryAfter)
			log.Printf("[relay] key %d rate limited, cooling %v (attempt %d/%d)", key.ID, retryAfter, attempt+1, maxRetries+1)
			attempt++
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized {
			// A 401 usually means the key is invalid, but many gateways also
			// answer 401 for an unknown / not-entitled MODEL. The health
			// probe classifies such bodies as alive (the key authenticated;
			// only the probe's model choice was wrong) — the relay must use
			// the SAME classification, or a model-problem 401 disables the
			// key here while the next probe pass recovers it (the disable →
			// active → disable flap). Model/access problems cool the key
			// down like the 403 path; genuine key-invalidity 401s disable
			// after 2 consecutive identical observations.
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
			if health.ModelProblemInBody(errBody) {
				h.Engine.RecordResult(key.ID, false, model.HTTPStatusReason(resp.StatusCode), 30*time.Second)
				log.Printf("[relay] key %d unauthorized for the requested model (401, cooling 30s, attempt %d/%d)", key.ID, attempt+1, maxRetries+1)
				attempt++
				continue
			}
			h.Engine.RecordResult(key.ID, false, model.ReasonAuthFailed, 30*time.Second)
			log.Printf("[relay] key %d auth failed (401, cooling 30s, attempt %d/%d)", key.ID, attempt+1, maxRetries+1)
			attempt++
			continue
		}
		if resp.StatusCode == http.StatusForbidden {
			// 403 is often model/endpoint access (not key invalidity) on
			// OpenAI-compatible gateways — cool down temporarily instead of
			// permanently disabling (which would brick valid keys). The UI
			// shows "HTTP 403".
			h.Engine.RecordResult(key.ID, false, model.HTTPStatusReason(resp.StatusCode), 30*time.Second)
			log.Printf("[relay] key %d forbidden (403, cooling 30s, attempt %d/%d)", key.ID, attempt+1, maxRetries+1)
			drainClose(resp)
			attempt++
			continue
		}
		if resp.StatusCode == http.StatusPaymentRequired {
			// 402 insufficient_quota: the key has no balance left — after 2
			// consecutive identical observations it is taken out of rotation
			// (a successful probe can later recover it) and the retry loop
			// fails over to a sibling key.
			h.Engine.RecordResult(key.ID, false, model.ReasonInsufficientQuota, 30*time.Second)
			log.Printf("[relay] key %d quota exhausted (402, attempt %d/%d)", key.ID, attempt+1, maxRetries+1)
			drainClose(resp)
			attempt++
			continue
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout ||
			resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusTooEarly {
			log.Printf("[relay] upstream %d for key %d (attempt %d/%d)", resp.StatusCode, key.ID, attempt+1, maxRetries+1)
			// Upstream failures are transient: mark the key once (the UI
			// shows the bare status, e.g. "HTTP 500") and fail over to the
			// next key this attempt.
			h.Engine.RecordResult(key.ID, false, model.HTTPStatusReason(resp.StatusCode), 30*time.Second)
			drainClose(resp)
			attempt++
			continue
		}

		// Non-2xx streaming responses (4xx, and 3xx which must not be
		// mis-streamed as SSE): official SDKs parse non-2xx bodies as plain
		// JSON, so relay a JSON error envelope in the client's format.
		// 3xx gets a 502 (a redirect the client can't follow without leaking
		// the upstream URL), 4xx gets the upstream status.
		if reqMeta.Stream && resp.StatusCode >= 300 {
			// The upstream ANSWERED (client/model error, not a key problem —
			// the same classification the health probe applies to 4xx/3xx):
			// record a success observation so the key can build its recovery
			// streak even when every request errors client-side.
			h.Engine.RecordResult(key.ID, true, "", 0)
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxRequestBody+1))
			resp.Body.Close()
			if len(errBody) > maxRequestBody {
				errBody = errBody[:maxRequestBody]
			}
			status := resp.StatusCode
			code := "upstream_error"
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				status = http.StatusBadGateway
				code = "upstream_redirect"
			}
			if format.NeedConvert(inputFormat, rr.UpstreamFormat) {
				msg := extractUpstreamError(errBody, resp.StatusCode)
				if status == http.StatusBadGateway {
					msg = "upstream returned a redirect"
				}
				log.Printf("[relay] upstream %d for key %d: %s (attempt %d/%d)", resp.StatusCode, key.ID, msg, attempt+1, maxRetries+1)
				writeRelayError(c, inputFormat, status, code, "upstream_error", msg)
				return
			}
			// Same format: de-frame SSE-framed error bodies so the SDK can
			// parse the JSON (Azure-style gateways), then pass through.
			trimmed := strings.TrimSpace(string(errBody))
			if strings.HasPrefix(trimmed, "data:") {
				firstFrame := trimmed
				if idx := strings.Index(firstFrame, "\n\n"); idx >= 0 {
					firstFrame = firstFrame[:idx]
				} else if idx := strings.Index(firstFrame, "\r\n\r\n"); idx >= 0 {
					firstFrame = firstFrame[:idx]
				}
				if deframed := strings.TrimSpace(strings.TrimPrefix(firstFrame, "data:")); deframed != "" {
					errBody = []byte(deframed)
				}
			}
			c.Writer.Header().Set("Content-Type", "application/json")
			c.Status(status)
			c.Writer.Write(errBody)
			return
		}

		// Set response headers before writing body. Location is never
		// forwarded (an SDK following a 3xx redirect would bypass the
		// gateway's rate limits/retry/auth/billing and leak the upstream
		// URL), and security-sensitive end-to-end headers are dropped (a
		// hostile upstream must not set cookies/CSP on the local UI origin).
		for k, v := range resp.Header {
			if k != "Content-Length" && k != "Content-Encoding" && k != "Transfer-Encoding" && k != "Connection" && k != "Location" &&
				k != "Set-Cookie" && k != "Content-Security-Policy" && k != "Access-Control-Allow-Origin" {
				for _, hv := range v {
					c.Writer.Header().Add(k, hv)
				}
			}
		}

		if reqMeta.Stream {
			// The output is always SSE-framed from here on; force the
			// content type (upstream headers were copied above and may say
			// application/json for upstreams that ignored stream:true)
			c.Writer.Header().Set("Content-Type", "text/event-stream")

			// Commit status before streaming starts
			c.Status(resp.StatusCode)

			// Stream response and capture token usage from stream end events.
			// The upstream format comes from the relay (a /v1/responses
			// request may have been fallback-routed to chat completions).
			usage, streamErr := relay.StreamResponse(c.Writer, resp, inputFormat, rr.UpstreamFormat, targetModel)
			resp.Body.Close()

			if streamErr != nil {
				log.Printf("[relay] streaming error: %v", streamErr)
				// The error frame was already written by StreamResponse
				// before any termination frame (SDKs stop at [DONE]/
				// message_stop and would swallow a late error).
			}

			// Record consumption only for successful 2xx streams (3xx is
			// written through but not billed). A stream that died mid-response
			// (read/conversion failure) must not inflate costs or burn
			// rate-limit quotas — the client received an error, not work.
			if streamErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				consumption, err := billing.RecordConsumption(key.ID, reqMeta.Model, targetModel, extractAppName(c.Request.Header, meta.RequestBody), usage, route.Route)
				if err != nil {
					log.Printf("[relay] failed to record consumption for key %d: %v", key.ID, err)
				}
				costMicro := int64(consumption.CostUSD * 1e6)
				if usage.TotalTokens > 0 {
					h.Engine.RecordSuccess(key.ID, usage.TotalTokens, costMicro)
				} else {
					h.Engine.WindowManager.IncrementAllWithCost(key.ID, 0, costMicro)
				}
				// Every successful request is one ordered observation toward
				// the key's recovery streak (2 consecutive successes return
				// a cooled/disabled key to active).
				h.Engine.RecordResult(key.ID, true, "", 0)
				// Lifetime budget: accumulate spend; if the key's total
				// budget is exhausted, take it out of rotation permanently.
				if costMicro > 0 {
					h.applySpendLimit(key.ID, costMicro)
				}
			}
		} else {
			// Read response body BEFORE committing the status code so that a
			// read failure can still produce a proper 502 instead of being
			// silently ignored after the upstream status was already sent.
			responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRequestBody+1))
			resp.Body.Close()
			if readErr != nil {
				log.Printf("[relay] failed to read upstream response: %v", readErr)
				writeRelayError(c, inputFormat, http.StatusBadGateway, "upstream_read_failed", "upstream_error",
					"failed to read upstream response")
				return
			}
			if len(responseBody) > maxRequestBody {
				log.Printf("[relay] upstream response too large for key %d (%d bytes)", key.ID, len(responseBody))
				writeRelayError(c, inputFormat, http.StatusBadGateway, "upstream_response_too_large", "upstream_error",
					"upstream response too large")
				return
			}

			// Error responses in a cross-format route get a format-aware
			// envelope (like the streaming path) so SDK error parsers work.
			// NOTE: this runs BEFORE c.Status() so the envelope's own status
			// and Content-Type can be written.
			if resp.StatusCode >= 400 && format.NeedConvert(inputFormat, rr.UpstreamFormat) {
				// The upstream answered (4xx = client/model problem, not a
				// key problem — the probe classifies 4xx as alive): record
				// the success observation like the streaming path.
				h.Engine.RecordResult(key.ID, true, "", 0)
				msg := extractUpstreamError(responseBody, resp.StatusCode)
				writeRelayError(c, inputFormat, resp.StatusCode, "upstream_error", "upstream_error", msg)
				return
			}

			// A 2xx body that is actually an error object (some gateways do
			// this for context-length/model errors) must be surfaced as an
			// error, not a successful completion.
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				var errCheck struct {
					Error json.RawMessage `json:"error"`
				}
				if json.Unmarshal(responseBody, &errCheck) == nil && relay.IsErrorPayload(errCheck.Error) {
					msg := extractUpstreamError(responseBody, resp.StatusCode)
					log.Printf("[relay] upstream 200-with-error for key %d: %s", key.ID, msg)
					// The key itself worked (it answered 200) — the error is
					// request/model-scoped, so it still counts as a success
					// observation for the key's streak.
					h.Engine.RecordResult(key.ID, true, "", 0)
					writeRelayError(c, inputFormat, http.StatusBadGateway, "upstream_error", "upstream_error", msg)
					return
				}
			}

			// Convert format if needed. Only successful 2xx bodies are
			// converted: converting an error/redirect envelope into the
			// client's format would produce a fake "successful" empty
			// completion and lose the real error message. The dispatch is on
			// the format the upstream actually spoke.
			bodyToWrite := responseBody
			if resp.StatusCode >= 200 && resp.StatusCode < 300 && format.NeedConvert(inputFormat, rr.UpstreamFormat) {
				var convErr error
				switch {
				case inputFormat == "responses" && rr.UpstreamFormat == "anthropic":
					bodyToWrite, convErr = relay.AnthropicResponseToResponses(responseBody, targetModel)
				case inputFormat == "responses" && rr.UpstreamFormat == "openai":
					bodyToWrite, convErr = relay.ChatCompletionResponseToResponses(responseBody, targetModel)
				case inputFormat == "openai":
					bodyToWrite, convErr = relay.ConvertAnthropicResponseToOpenAI(responseBody, targetModel)
				default:
					bodyToWrite, convErr = relay.ConvertOpenAIResponseToAnthropic(responseBody)
				}
				if convErr != nil {
					// A malformed 2xx body in the wrong format must not be
					// handed to the client as a successful response. The
					// upstream still ANSWERED 2xx — the key worked — so it
					// counts as a success observation (like the 200-with-
					// error path).
					log.Printf("[relay] format conversion error: %v", convErr)
					h.Engine.RecordResult(key.ID, true, "", 0)
					writeRelayError(c, inputFormat, http.StatusBadGateway, "upstream_response_invalid", "upstream_error",
						"upstream returned an invalid response")
					return
				}
			}

			// Only record consumption/rate-limit usage for successful 2xx
			// responses: 3xx/4xx/5xx represent work the upstream never
			// performed and must not burn the key's request budgets.
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				usage := relay.ParseTokenUsage(responseBody, rr.UpstreamFormat)
				consumption, err := billing.RecordConsumption(key.ID, reqMeta.Model, targetModel, extractAppName(c.Request.Header, meta.RequestBody), usage, route.Route)
				if err != nil {
					log.Printf("[relay] failed to record consumption for key %d: %v", key.ID, err)
				}
				costMicro := int64(consumption.CostUSD * 1e6)
				h.Engine.RecordSuccess(key.ID, usage.TotalTokens, costMicro)
				// Every successful request is one ordered observation toward
				// the key's recovery streak (2 consecutive successes return
				// a cooled/disabled key to active).
				h.Engine.RecordResult(key.ID, true, "", 0)
				if costMicro > 0 {
					h.applySpendLimit(key.ID, costMicro)
				}
			}

			// 3xx without Location is useless to the client (and a redirect
			// would bypass the gateway) — surface it as an upstream error.
			// The upstream still answered, so it counts as a success
			// observation for the key.
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				log.Printf("[relay] upstream redirect %d for key %d", resp.StatusCode, key.ID)
				h.Engine.RecordResult(key.ID, true, "", 0)
				writeRelayError(c, inputFormat, http.StatusBadGateway, "upstream_redirect", "upstream_error",
					"upstream returned a redirect")
				return
			}

			// Remaining statuses here are non-retryable same-format 4xx
			// (400/404/422/...): pass the upstream error through to the
			// client. The key answered — success observation (the probe
			// classifies 4xx as alive for the same reason).
			if resp.StatusCode >= 400 {
				h.Engine.RecordResult(key.ID, true, "", 0)
			}

			// Commit status, then write the (possibly converted) body
			c.Status(resp.StatusCode)
			c.Writer.Write(bodyToWrite)
		}
		return // success
	}

	// All attempts exhausted (every upstream returned 429/401/403/402/5xx or
	// a network error). No response was written yet — emit a proper error
	// instead of letting gin send an empty 200. Preserve the LAST attempt's
	// upstream status so client retry/key-rotation logic gets the real
	// signal instead of a generic 502.
	switch lastUpstreamStatus {
	case http.StatusTooManyRequests:
		log.Printf("[relay] all %d attempts rate-limited for model %s", maxRetries+1, reqMeta.Model)
		writeRelayError(c, inputFormat, http.StatusTooManyRequests, "key_exhausted", "rate_limit_error",
			"All available keys have been exhausted. Please try again later.")
		return
	case http.StatusUnauthorized:
		log.Printf("[relay] all %d attempts unauthorized for model %s", maxRetries+1, reqMeta.Model)
		writeRelayError(c, inputFormat, http.StatusUnauthorized, "upstream_key_invalid", "upstream_error",
			"All upstream keys were rejected as invalid")
		return
	case http.StatusForbidden:
		log.Printf("[relay] all %d attempts forbidden for model %s", maxRetries+1, reqMeta.Model)
		writeRelayError(c, inputFormat, http.StatusForbidden, "upstream_forbidden", "upstream_error",
			"All upstream keys were forbidden")
		return
	case http.StatusPaymentRequired:
		log.Printf("[relay] all %d attempts quota-exhausted for model %s", maxRetries+1, reqMeta.Model)
		writeRelayError(c, inputFormat, http.StatusPaymentRequired, "upstream_quota_exhausted", "upstream_error",
			"All upstream keys have exhausted their quota")
		return
	}
	log.Printf("[relay] all %d attempts failed for model %s", maxRetries+1, reqMeta.Model)
	writeRelayError(c, inputFormat, http.StatusBadGateway, "all_attempts_failed", "upstream_error",
		"all upstream attempts failed")
}

// writeRelayError writes an error response in the client's format:
// OpenAI clients expect {"error":{...}}, Anthropic clients expect
// {"type":"error","error":{...}}. Content-Type is forced to JSON because
// upstream headers may already carry a different one.
func writeRelayError(c *gin.Context, inputFormat string, status int, code, errType, message string) {
	c.Writer.Header().Set("Content-Type", "application/json")
	if inputFormat == "anthropic" {
		c.JSON(status, gin.H{
			"type": "error",
			"error": gin.H{
				"type":    errType,
				"message": message,
				"code":    code,
			},
		})
		return
	}
	c.JSON(status, gin.H{
		"error": gin.H{
			"message": message,
			"type":    errType,
			"code":    code,
		},
	})
}

// applySpendLimit accumulates the key's lifetime spend (micro-USD) after a
// successful relayed request. If the key has a total spend budget and it is
// now exhausted, the key is disabled permanently with reason
// "spend_limit_exhausted" until an admin resets it. The accumulation is
// atomic (DB-side increment) so concurrent requests can't lose spend.
func (h *ChatHandler) applySpendLimit(keyID, costMicro int64) {
	// Atomically add and read back the new total.
	var key model.Key
	if err := db.GetDB().Model(&model.Key{}).
		Where("id = ?", keyID).
		UpdateColumn("total_spent", gorm.Expr("total_spent + ?", costMicro)).Error; err != nil {
		log.Printf("[relay] failed to accumulate spend for key %d: %v", keyID, err)
		return
	}
	if err := db.GetDB().First(&key, keyID).Error; err != nil {
		log.Printf("[relay] failed to reload key %d spend: %v", keyID, err)
		return
	}
	if key.TotalSpendLimit <= 0 || key.TotalSpent < key.TotalSpendLimit {
		return
	}
	log.Printf("[relay] key %d spent total %d (limit %d) — disabling (spend budget exhausted)",
		keyID, key.TotalSpent, key.TotalSpendLimit)
	h.Engine.MarkKeyDisabled(keyID, model.KeyDisabledReasonSpendLimit)
}

// extractAppName derives the client app name from the request headers and
// caps it at the AppName column width (varchar(255), 255 characters) on a
// UTF-8 rune boundary, so a hostile over-long header cannot fail or truncate
// the consumption insert.
func extractAppName(h http.Header, body []byte) string {
	return truncateAppName(extractAppNameUnchecked(h, body))
}

// extractAppNameUnchecked derives the client app name from the request
// headers. The User-Agent is NOT a reliable app identifier — SDKs and proxies
// overwrite it (axios, node-fetch, python-httpx, curl), so it is only used
// for the few clients whose UA is their documented identity. Detection
// follows each provider's actual attribution conventions, in trust order:
//
//  1. X-OpenRouter-Title / X-Title — OpenRouter attribution display name
//     (X-Title is its backwards-compatible alias). Highest trust.
//  2. x-app                        — Anthropic-ecosystem app id; the generic
//     value "cli" is resolved to Claude Code by the signals in 3.
//  3. Claude Code                  — X-Claude-Code-Session-Id (v2.1.86+),
//     anthropic-beta: claude-code-*, or x-app: cli with a claude-cli UA.
//  4. Provider-specific headers    — X-Cursor-Mode (Cursor), originator
//     (Codex CLI family), X-OpenWebUI-* (Open WebUI).
//  5. HTTP-Referer hostname        — OpenRouter's primary attribution
//     identifier (the app's URL). Localhost referers are ignored.
//  6. Known client User-Agent tokens — claude-cli, opencode, GeminiCLI,
//     CherryStudio (Electron UA token), lobe-chat, lobehub, chatbox,
//     continue, cline, cursor, codex, aider, open-webui.
//  7. Request body client_metadata.app_name — older Codex releases.
//  8. Browser User-Agent (Mozilla/...) — Chrome / Edge / Firefox / Safari.
//  9. ""                           — shown as "Unknown" in the Activity page.
func extractAppNameUnchecked(h http.Header, body []byte) string {
	// 1. OpenRouter attribution display name (highest trust).
	for _, key := range []string{"X-OpenRouter-Title", "X-Title"} {
		if t := strings.TrimSpace(h.Get(key)); t != "" {
			return t
		}
	}

	// 2. Anthropic ecosystem: x-app carries the app id ("cli" for Claude
	//    Code, resolved below once the other Claude Code signals are known).
	if t := strings.TrimSpace(h.Get("x-app")); t != "" && !strings.EqualFold(t, "cli") {
		return t
	}

	// 3. Claude Code: session header (v2.1.86+), anthropic-beta marker, or
	//    the generic x-app: cli paired with the claude-cli UA.
	if h.Get("X-Claude-Code-Session-Id") != "" ||
		strings.Contains(strings.ToLower(h.Get("anthropic-beta")), "claude-code") ||
		(strings.EqualFold(strings.TrimSpace(h.Get("x-app")), "cli") && strings.HasPrefix(h.Get("User-Agent"), "claude-cli/")) {
		return "Claude Code"
	}

	// 4. Provider-specific identifying headers.
	if h.Get("X-Cursor-Mode") != "" {
		return "Cursor"
	}
	if v := h.Get("originator"); v != "" {
		// Codex CLI family (openai/codex default_client.rs): the originator
		// header is the real end-client identity, e.g. codex_cli_rs,
		// codex-tui, codex_vscode, codex_atlas, codex_chatgpt_desktop.
		switch strings.ToLower(v) {
		case "codex-tui":
			return "Codex TUI"
		case "codex_vscode":
			return "Codex (VS Code)"
		case "codex_atlas":
			return "Atlas"
		case "codex_chatgpt_desktop":
			return "ChatGPT"
		default:
			return "Codex"
		}
	}
	for _, key := range []string{
		"X-OpenWebUI-User-Name", "X-OpenWebUI-User-Id", "X-OpenWebUI-User-Email",
		"X-OpenWebUI-User-Role", "X-OpenWebUI-Chat-Id",
	} {
		if h.Get(key) != "" {
			return "Open WebUI"
		}
	}

	// 5. HTTP-Referer hostname (OpenRouter attribution URL = app identity).
	//    Clients send either "HTTP-Referer" (OpenRouter's documented name) or
	//    the RFC-standard "Referer"; Go canonicalizes the wire name into the
	//    Header map, so both keys must be probed.
	ref := strings.TrimSpace(h.Get("HTTP-Referer"))
	if ref == "" {
		ref = strings.TrimSpace(h.Get("Referer"))
	}
	if ref != "" {
		lref := strings.ToLower(ref)
		if strings.HasPrefix(lref, "https://") {
			ref = ref[len("https://"):]
		} else if strings.HasPrefix(lref, "http://") {
			ref = ref[len("http://"):]
		}
		if strings.HasPrefix(strings.ToLower(ref), "www.") {
			ref = ref[len("www."):]
		}
		if i := strings.IndexAny(ref, "/"); i > 0 {
			ref = ref[:i]
		}
		if i := strings.IndexByte(ref, '?'); i >= 0 {
			ref = ref[:i]
		}
		if !isLocalHostname(ref) {
			return ref
		}
	}

	// 6. Known client User-Agent tokens. Only apps whose UA IS their
	//    documented identity are matched here; SDK/proxy UAs (axios,
	//    node-fetch, python-httpx, curl, OpenAI/Python, ...) never are.
	if ua := h.Get("User-Agent"); ua != "" {
		lua := strings.ToLower(ua)
		known := []struct{ token, name string }{
			{"claude-cli", "Claude Code"},
			{"opencode", "OpenCode"},
			{"geminicli", "Gemini CLI"},
			{"cherrystudio", "Cherry Studio"},
			{"lobe-chat", "LobeChat"},
			{"lobehub", "LobeChat"},
			{"chatbox", "Chatbox"},
			{"continue", "Continue"},
			{"cline", "Cline"},
			{"cursor", "Cursor"},
			{"codex", "Codex"},
			{"aider", "Aider"},
			{"open-webui", "Open WebUI"},
		}
		for _, k := range known {
			if strings.Contains(lua, k.token) {
				return k.name
			}
		}
	}

	// 7. Codex sends app identity in the request body's client_metadata
	//    (client_metadata.app_name). Only parse when the body looks like a
	//    chat request — don't choke on arbitrary bodies.
	if len(body) > 0 {
		var meta struct {
			ClientMetadata map[string]interface{} `json:"client_metadata"`
		}
		if json.Unmarshal(body, &meta) == nil && meta.ClientMetadata != nil {
			if app, ok := meta.ClientMetadata["app_name"].(string); ok && strings.TrimSpace(app) != "" {
				return strings.TrimSpace(app)
			}
		}
	}

	// 8. Browser User-Agents. The rendering-engine tokens differ between
	//    Chrome/Edge/Firefox/Safari; Edg must be checked before Chrome
	//    because Edge's UA contains both. Electron apps that carry no known
	//    app token (VS Code, Slack, Discord, ...) are not "Chrome" — they
	//    stay unknown unless identified by an earlier signal.
	if ua := strings.ToLower(h.Get("User-Agent")); strings.HasPrefix(ua, "mozilla/") {
		switch {
		case strings.Contains(ua, "electron/"):
			return ""
		case strings.Contains(ua, "edg/"):
			return "Edge"
		case strings.Contains(ua, "firefox/"):
			return "Firefox"
		case strings.Contains(ua, "chrome/"):
			return "Chrome"
		case strings.Contains(ua, "safari/"):
			return "Safari"
		}
	}
	return ""
}

// isLocalHostname reports whether the host is the local machine — such
// referers are meaningless for app attribution (OpenRouter requires
// X-OpenRouter-Title to accompany localhost URLs).
func isLocalHostname(host string) bool {
	if host == "" {
		return true
	}
	lower := strings.ToLower(host)
	// Strip a trailing port; IPv6 literals keep their colons and brackets.
	if i := strings.LastIndexByte(lower, ':'); i > 0 && !strings.Contains(lower[:i], ":") {
		lower = lower[:i]
	} else if strings.HasPrefix(lower, "[") {
		if i := strings.IndexByte(lower, ']'); i >= 0 {
			lower = lower[:i+1]
		}
	}
	// "localhost" itself, its FQDN forms, and the reserved subdomains
	// .localhost (RFC 6761) and .localhost.localdomain (RFC 6762, the
	// macOS/BSD hostname). Suffix matching avoids swallowing real domains
	// like localhost.com.
	if lower == "localhost" || lower == "localhost." ||
		strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".localhost.") ||
		lower == "localhost.localdomain" || lower == "localhost.localdomain." ||
		strings.HasSuffix(lower, ".localhost.localdomain") || strings.HasSuffix(lower, ".localhost.localdomain.") {
		return true
	}
	// 0.0.0.0 is the any-address (not loopback per net.IP.IsLoopback), and
	// the loopback ranges 127.0.0.0/8 and ::1 are handled by ParseIP below.
	if lower == "0.0.0.0" {
		return true
	}
	ip := net.ParseIP(strings.Trim(lower, "[]"))
	return ip != nil && ip.IsLoopback()
}

// truncateAppName caps the detected app name at the AppName column width
// (varchar(255) — 255 characters, not bytes) on a UTF-8 rune boundary.
func truncateAppName(s string) string {
	const max = 255
	if len(s) <= max {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// extractUpstreamError pulls a readable error message from an upstream error
// body (OpenAI-style {"error":{...}} or Anthropic-style {"type":"error","error":{...}}),
// falling back to the raw (truncated) body.
func extractUpstreamError(body []byte, statusCode int) string {
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && len(payload.Error) > 0 {
		var inner struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(payload.Error, &inner) == nil && inner.Message != "" {
			return inner.Message
		}
	}
	msg := strings.TrimSpace(string(body))
	if len(msg) > 200 {
		// Truncate at a UTF-8 rune boundary to avoid emitting invalid UTF-8
		truncated := msg[:200]
		for len(truncated) > 0 && !utf8.RuneStart(truncated[len(truncated)-1]) {
			truncated = truncated[:len(truncated)-1]
		}
		msg = truncated + "..."
	}
	if msg == "" {
		msg = http.StatusText(statusCode)
	}
	return msg
}

// isQuotaExhaustedError reports whether an upstream error body means the
// account/key has no balance left (as opposed to a transient rate limit).
// OpenAI-compatible APIs return 429 with error.code "insufficient_quota" or
// "billing_hard_limit_reached"; Anthropic uses 402 Payment Required (handled
// separately by status code) and some gateways return the code as a string
// or in "error.type". "quota_exceeded" is deliberately NOT matched: gateways
// use it for request/model rate-limit throttles as well as billing
// exhaustion, and the cost of a wrong disable (a healthy key taken out of
// rotation) outweighs the cost of a wrong cool-down — so it cools the key
// down instead of disabling it.
func isQuotaExhaustedError(body []byte) bool {
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Error) == 0 {
		return false
	}
	var inner struct {
		Code string `json:"code"`
		Type string `json:"type"`
	}
	if json.Unmarshal(payload.Error, &inner) != nil {
		return false
	}
	switch inner.Code {
	case "insufficient_quota", "billing_hard_limit_reached", "billing_not_active", "card_declined":
		return true
	}
	switch inner.Type {
	case "insufficient_quota", "billing_error", "billing_not_active":
		return true
	}
	return false
}
