package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"local-router/billing"
	"local-router/db"
	"local-router/format"
	"local-router/model"
	"local-router/relay"
	"local-router/selector"

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

	data := make([]gin.H, 0, len(groups))
	for _, g := range groups {
		data = append(data, gin.H{
			"id":      g.GroupID,
			"object":  "model",
			"created": 0,
			"owned_by": "local-router",
		})
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
		meta := &model.RequestMetadata{
			Format:      inputFormat,
			Model:       reqMeta.Model,
			Stream:      reqMeta.Stream,
			RequestPath: c.Request.URL.Path,
			RequestBody: body,
			Headers:     c.Request.Header.Clone(),
			TargetModel: targetModel,
			Ctx:         c.Request.Context(),
		}

		// Forward request to upstream
		resp, err := relay.ForwardRequest(meta, key, route.Provider)
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
			h.Engine.MarkKeyRateLimited(key.ID, 30*time.Second)
			attempt++
			continue
		}

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
			h.Engine.MarkKeyRateLimited(key.ID, retryAfter)
			log.Printf("[relay] key %d rate limited, cooling %v (attempt %d/%d)", key.ID, retryAfter, attempt+1, maxRetries+1)
			drainClose(resp)
			attempt++
			continue
		}
		if resp.StatusCode == http.StatusUnauthorized {
			h.Engine.MarkKeyDisabled(key.ID, "auth_failed")
			log.Printf("[relay] key %d disabled (auth failed, attempt %d/%d)", key.ID, attempt+1, maxRetries+1)
			drainClose(resp)
			attempt++
			continue
		}
		if resp.StatusCode == http.StatusForbidden {
			// 403 is often model/endpoint access (not key invalidity) on
			// OpenAI-compatible gateways — cool down temporarily instead of
			// permanently disabling (which would brick valid keys).
			h.Engine.MarkKeyRateLimited(key.ID, 30*time.Second)
			log.Printf("[relay] key %d forbidden (403, cooling 30s, attempt %d/%d)", key.ID, attempt+1, maxRetries+1)
			drainClose(resp)
			attempt++
			continue
		}
		if resp.StatusCode == http.StatusPaymentRequired {
			// 402 insufficient_quota: the key has no balance left — take it
			// out of rotation (no auto-recovery; needs an admin top-up) and
			// fail over to a sibling key.
			h.Engine.MarkKeyDisabled(key.ID, "insufficient_quota")
			log.Printf("[relay] key %d quota exhausted (402, attempt %d/%d)", key.ID, attempt+1, maxRetries+1)
			drainClose(resp)
			attempt++
			continue
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout ||
			resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusTooEarly {
			log.Printf("[relay] upstream %d for key %d (attempt %d/%d)", resp.StatusCode, key.ID, attempt+1, maxRetries+1)
			// Mark key as temporarily unavailable so another key is picked next attempt
			h.Engine.MarkKeyRateLimited(key.ID, 30*time.Second)
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
			if format.NeedConvert(inputFormat, route.Provider.Type) {
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

			// Stream response and capture token usage from stream end events
			usage, streamErr := relay.StreamResponse(c.Writer, resp, inputFormat, route.Provider.Type, targetModel)
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
				if _, err := billing.RecordConsumption(key.ID, targetModel, usage); err != nil {
					log.Printf("[relay] failed to record consumption for key %d: %v", key.ID, err)
				}
				if usage.TotalTokens > 0 {
					h.Engine.RecordSuccess(key.ID, usage.TotalTokens)
				} else {
					h.Engine.WindowManager.IncrementAll(key.ID, 0)
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
			if resp.StatusCode >= 400 && format.NeedConvert(inputFormat, route.Provider.Type) {
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
					writeRelayError(c, inputFormat, http.StatusBadGateway, "upstream_error", "upstream_error", msg)
					return
				}
			}

			// Convert format if needed. Only successful 2xx bodies are
			// converted: converting an error/redirect envelope into the
			// client's format would produce a fake "successful" empty
			// completion and lose the real error message.
			bodyToWrite := responseBody
			if resp.StatusCode >= 200 && resp.StatusCode < 300 && format.NeedConvert(inputFormat, route.Provider.Type) {
				var convErr error
				if inputFormat == "openai" {
					bodyToWrite, convErr = relay.ConvertAnthropicResponseToOpenAI(responseBody, targetModel)
				} else {
					bodyToWrite, convErr = relay.ConvertOpenAIResponseToAnthropic(responseBody)
				}
				if convErr != nil {
					// A malformed 2xx body in the wrong format must not be
					// handed to the client as a successful response
					log.Printf("[relay] format conversion error: %v", convErr)
					writeRelayError(c, inputFormat, http.StatusBadGateway, "upstream_response_invalid", "upstream_error",
						"upstream returned an invalid response")
					return
				}
			}

			// Only record consumption/rate-limit usage for successful 2xx
			// responses: 3xx/4xx/5xx represent work the upstream never
			// performed and must not burn the key's request budgets.
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				usage := relay.ParseTokenUsage(responseBody, route.Provider.Type)
				if _, err := billing.RecordConsumption(key.ID, targetModel, usage); err != nil {
					log.Printf("[relay] failed to record consumption for key %d: %v", key.ID, err)
				}
				h.Engine.RecordSuccess(key.ID, usage.TotalTokens)
			}

			// 3xx without Location is useless to the client (and a redirect
			// would bypass the gateway) — surface it as an upstream error.
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				log.Printf("[relay] upstream redirect %d for key %d", resp.StatusCode, key.ID)
				writeRelayError(c, inputFormat, http.StatusBadGateway, "upstream_redirect", "upstream_error",
					"upstream returned a redirect")
				return
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
