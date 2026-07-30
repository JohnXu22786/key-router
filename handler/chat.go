package handler

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"local-router/billing"
	"local-router/db"
	"local-router/format"
	"local-router/model"
	"local-router/relay"
	"local-router/selector"

	"github.com/gin-gonic/gin"
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

// handleRelay is the core relay handler with a bounded retry loop
func (h *ChatHandler) handleRelay(c *gin.Context, inputFormat string) {
	// Read request body
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
		return
	}

	// Extract model from body
	var reqMeta struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(body, &reqMeta); err != nil || reqMeta.Model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body or missing model"})
		return
	}

	// Get retry times from settings
	retryStr := db.GetSetting(model.SettingRetryTimes)
	maxRetries, _ := strconv.Atoi(retryStr)
	if maxRetries <= 0 {
		maxRetries = 3
	}

	// Bounded retry loop (replaces recursion)
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Select a route and key
		route, key, err := h.Engine.RetryLoop(reqMeta.Model, maxRetries-attempt)
		if err != nil {
			log.Printf("[relay] no available route for model %s (attempt %d/%d): %v",
				reqMeta.Model, attempt+1, maxRetries+1, err)
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error": gin.H{
					"message":  "All available keys have been exhausted. Please try again later.",
					"type":     "rate_limit_error",
					"code":     "key_exhausted",
				},
			})
			return
		}

		// Resolve target model name
		targetModel := reqMeta.Model
		if route.Route.TargetModel != "" {
			targetModel = route.Route.TargetModel
		}

		// Build request metadata
		meta := &model.RequestMetadata{
			Format:      inputFormat,
			Model:       reqMeta.Model,
			Stream:      reqMeta.Stream,
			RequestPath: c.Request.URL.Path,
			RequestBody: body,
			Headers:     make(map[string]string),
			TargetModel: targetModel,
		}

		// Forward request to upstream
		resp, err := relay.ForwardRequest(meta, key, route.Provider)
		if err != nil {
			log.Printf("[relay] forward error for key %d: %v", key.ID, err)
			continue
		}

		// Handle retry-eligible HTTP responses
		shouldRetry := false

		if resp.StatusCode == http.StatusTooManyRequests { // 429
			retryAfter := 60 * time.Second
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if sec, err := strconv.Atoi(ra); err == nil {
					retryAfter = time.Duration(sec) * time.Second
				}
			}
			h.Engine.MarkKeyRateLimited(key.ID, retryAfter)
			log.Printf("[relay] key %d rate limited, cooling %v (attempt %d/%d)",
				key.ID, retryAfter, attempt+1, maxRetries+1)
			resp.Body.Close()
			shouldRetry = true
		} else if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			h.Engine.MarkKeyDisabled(key.ID, "auth_failed")
			log.Printf("[relay] key %d disabled (auth failed, attempt %d/%d)",
				key.ID, attempt+1, maxRetries+1)
			resp.Body.Close()
			shouldRetry = true
		} else if resp.StatusCode >= 500 {
			log.Printf("[relay] upstream 5xx for key %d: %d (attempt %d/%d)",
				key.ID, resp.StatusCode, attempt+1, maxRetries+1)
			resp.Body.Close()
			shouldRetry = true
		}

		if shouldRetry {
			continue
		}
		defer resp.Body.Close()

		// Read response body for non-streaming
		var responseBody []byte
		if !reqMeta.Stream {
			responseBody, err = io.ReadAll(resp.Body)
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read upstream response"})
				return
			}

			// Parse token usage
			usage := relay.ParseTokenUsage(responseBody, route.Provider.Type)

			// Record consumption
			billing.RecordConsumption(key.ID, targetModel, usage)

			// Update window counters
			h.Engine.RecordSuccess(key.ID, usage.TotalTokens)
		}

		// Set response headers
		for k, v := range resp.Header {
			if k != "Content-Length" && k != "Content-Encoding" {
				for _, hv := range v {
					c.Writer.Header().Add(k, hv)
				}
			}
		}
		c.Status(resp.StatusCode)

		if reqMeta.Stream {
			// Stream the response (no retry on streaming failure)
			err = relay.StreamResponse(c.Writer, resp, inputFormat, route.Provider.Type)
			if err != nil {
				log.Printf("[relay] streaming error: %v", err)
			}
		} else {
			// Convert format if needed
			if format.NeedConvert(inputFormat, route.Provider.Type) {
				var converted []byte
				if inputFormat == "openai" {
					converted, _ = relay.ConvertAnthropicResponseToOpenAI(responseBody, targetModel)
				} else {
					converted, _ = relay.ConvertOpenAIResponseToAnthropic(responseBody)
				}
				if converted != nil {
					c.Writer.Write(converted)
					return
				}
			}
			c.Writer.Write(responseBody)
		}
		return // success
	}
}
