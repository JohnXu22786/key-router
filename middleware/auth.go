package middleware

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware validates the local authentication token.
// Only protects forwarding paths (/v1/*) when a token is configured.
// Management API (/api/*) and UI always pass through regardless.
func AuthMiddleware(expectedToken string) gin.HandlerFunc {
	log.Printf("[auth] middleware initialized, token=%q (empty=disabled)", expectedToken)
	return func(c *gin.Context) {
		// Management API and UI always pass through
		if isUIRequest(c.Request) || strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Next()
			return
		}

		// No token configured → allow everything
		if expectedToken == "" {
			c.Next()
			return
		}

		// Only /v1/* (forwarding) paths require auth when token is set
		if !isForwardingPath(c.Request.URL.Path) {
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

		log.Printf("[auth] 401: path=%s method=%s expected=%q got=%q", c.Request.URL.Path, c.Request.Method, expectedToken, token)
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid auth token"})
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
