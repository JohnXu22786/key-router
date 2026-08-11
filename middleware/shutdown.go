package middleware

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

// shutdownState is a process-wide switch: once BeginShutdown runs, every new
// request (except the health check) is refused with 503 so in-flight SSE
// streams can finish in the background before the process exits.
var shutdownState = struct {
	sync.Mutex
	stopping bool
}{}

// BeginShutdown marks the server as stopping: new requests are refused.
func BeginShutdown() {
	shutdownState.Lock()
	shutdownState.stopping = true
	shutdownState.Unlock()
}

// IsStopping reports whether a shutdown has begun.
func IsStopping() bool {
	shutdownState.Lock()
	defer shutdownState.Unlock()
	return shutdownState.stopping
}

// ShutdownMiddleware rejects new requests once shutdown has begun. The
// health endpoint stays up so the updater/probes can still see the process
// during the drain; everything else gets 503 "shutting down".
func ShutdownMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !IsStopping() {
			c.Next()
			return
		}
		if c.Request.URL.Path == "/api/health" || c.Request.URL.Path == "/health" {
			c.Next() // keep liveness visible while draining
			return
		}
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
			"error": "server is shutting down",
		})
	}
}
