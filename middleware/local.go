package middleware

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// LocalOnlyMiddleware rejects requests that come from a non-local Origin or
// are addressed to a non-local Host. The management API carries no auth, so
// this defends against CSRF (malicious websites POSTing to localhost) and
// DNS rebinding (attacker domain resolving to 127.0.0.1).
func LocalOnlyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Host check: reject requests addressed to non-local hostnames
		host := c.Request.Host
		if h, _, err := splitHostPort(host); err == nil {
			host = h
		}
		if !isLocalHost(host) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden: non-local host"})
			return
		}

		// Origin check: browser requests must come from THIS server's origin.
		// The hostname must be local AND the effective origin port must match
		// the request's port — a portless Origin means the page came from the
		// default port (80/443), which never equals the management port, so
		// it is rejected (a dev server or local process must not CSRF the
		// unauthenticated management API).
		origin := c.GetHeader("Origin")
		if origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !isLocalHost(u.Hostname()) {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden: cross-origin request"})
				return
			}
			originPort := u.Port()
			if originPort == "" {
				if u.Scheme == "https" {
					originPort = "443"
				} else {
					originPort = "80"
				}
			}
			if originPort != hostPort(c.Request.Host) {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden: cross-origin request"})
				return
			}
		}

		c.Next()
	}
}

// hostPort extracts the port from a Host header ("" when absent)
func hostPort(host string) string {
	_, port, err := splitHostPort(host)
	if err != nil {
		return ""
	}
	return port
}

func splitHostPort(hostport string) (string, string, error) {
	// Bracketed IPv6: [::1]:9998 or [::1]
	if strings.HasPrefix(hostport, "[") {
		if end := strings.IndexByte(hostport, ']'); end >= 0 {
			host := hostport[1:end]
			port := strings.TrimPrefix(hostport[end+1:], ":")
			return host, port, nil
		}
		return hostport, "", nil
	}
	// Bare IPv6 without port: "::1"
	if strings.Count(hostport, ":") > 1 {
		return hostport, "", nil
	}
	// host:port
	if idx := strings.LastIndexByte(hostport, ':'); idx >= 0 {
		return hostport[:idx], hostport[idx+1:], nil
	}
	return hostport, "", nil
}

func isLocalHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}
