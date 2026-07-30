package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware validates the local authentication token
// It's a simple protection against unauthorized access on LAN
func AuthMiddleware(expectedToken string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip auth for the web UI
		if isUIRequest(c.Request) {
			c.Next()
			return
		}

		// Skip auth for health check
		if c.Request.URL.Path == "/api/health" {
			c.Next()
			return
		}

		if expectedToken == "" {
			c.Next()
			return
		}

		// Try Authorization header (OpenAI style)
		auth := c.GetHeader("Authorization")
		token := ""

		if strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		} else if strings.HasPrefix(auth, "sk-") {
			token = auth
		}

		// Try x-api-key header (Anthropic style)
		if token == "" {
			token = c.GetHeader("x-api-key")
		}

		if token == expectedToken {
			c.Next()
			return
		}

		// For API routes, check token
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid auth token"})
			return
		}

		// For forwarding paths, reject
		if isForwardingPath(c.Request.URL.Path) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid auth token"})
			return
		}

		c.Next()
	}
}

// isUIRequest checks if the request is for the web UI
func isUIRequest(r *http.Request) bool {
	path := r.URL.Path
	return path == "/" || path == "" ||
		strings.HasPrefix(path, "/assets/") ||
		strings.HasPrefix(path, "/index.html")
}

// isForwardingPath checks if the request is for an API forwarding endpoint
func isForwardingPath(path string) bool {
	return strings.HasPrefix(path, "/v1/")
}
