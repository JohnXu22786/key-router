package middleware

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strings"

	"local-router/db"
	"local-router/model"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware validates the local authentication token.
// Only protects forwarding paths (/v1/*) when a token is configured.
// Management API (/api/*) and UI always pass through regardless.
// The expected token is read from the DB on every request so that changes
// made via PUT /api/settings take effect immediately, without restart.
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Management API and UI always pass through (case-insensitive paths)
		p := c.Request.URL.Path
		if isUIRequest(c.Request) || strings.HasPrefix(strings.ToLower(p), "/api/") {
			c.Next()
			return
		}

		// No token configured → allow everything. A DB read error must FAIL
		// CLOSED (a transient SQLite hiccup must not silently disable auth
		// on the forwarding API while a token is configured).
		expectedToken, err := db.GetSettingChecked(model.SettingAuthToken)
		if err != nil {
			log.Printf("[auth] failed to read auth token setting: %v", err)
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "auth backend unavailable"})
			return
		}
		if expectedToken == "" {
			c.Next()
			return
		}

		// Only /v1/* (forwarding) paths require auth when token is set
		if !isForwardingPath(c.Request.URL.Path) {
			c.Next()
			return
		}

		// Try Authorization header (OpenAI style) and x-api-key (Anthropic
		// style) independently: a client may send a stale Bearer plus a fresh
		// x-api-key, and a valid credential in EITHER header should pass.
		// RFC 7235 auth schemes are case-insensitive; custom tokens may be
		// sent raw without a scheme.
		auth := c.GetHeader("Authorization")
		token := ""

		if len(auth) >= 7 && strings.EqualFold(auth[:7], "Bearer ") {
			token = strings.TrimPrefix(auth[7:], " ")
		} else if auth != "" {
			// raw token (with or without an sk- prefix)
			token = auth
		}

		xapi := c.GetHeader("x-api-key")

		matches := func(candidate string) bool {
			if candidate == "" {
				return false
			}
			return subtle.ConstantTimeCompare([]byte(candidate), []byte(expectedToken)) == 1
		}

		if matches(token) || matches(xapi) {
			c.Next()
			return
		}

		log.Printf("[auth] 401: path=%s method=%s", c.Request.URL.Path, c.Request.Method)
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
// (case-insensitive, consistent with the /api/ bypass and the router's 404)
func isForwardingPath(path string) bool {
	return strings.HasPrefix(strings.ToLower(path), "/v1/")
}
