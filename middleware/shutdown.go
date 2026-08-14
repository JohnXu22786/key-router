package middleware

import (
	"bufio"
	"net"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

// shutdownState is a process-wide switch: once BeginShutdown runs, new
// requests get their connection closed without a response, so clients see a
// connection failure and auto-retry.
var shutdownState = struct {
	sync.Mutex
	stopping bool
}{}

// shutdownSignal is closed (once) by BeginShutdown. Long-lived handlers
// (the /api/events SSE stream) select on it so the connection terminates
// promptly on shutdown — otherwise the drain would block for a stream that
// would otherwise live forever.
var (
	shutdownOnce   sync.Once
	shutdownSignal = make(chan struct{})
)

// BeginShutdown marks the server as stopping: new requests get their
// connections closed unanswered, and long-lived connections are told to
// finish.
func BeginShutdown() {
	shutdownState.Lock()
	shutdownState.stopping = true
	shutdownState.Unlock()
	shutdownOnce.Do(func() { close(shutdownSignal) })
}

// ShutdownSignal returns a channel that is closed when BeginShutdown runs.
func ShutdownSignal() <-chan struct{} {
	return shutdownSignal
}

// IsStopping reports whether a shutdown has begun.
func IsStopping() bool {
	shutdownState.Lock()
	defer shutdownState.Unlock()
	return shutdownState.stopping
}

// ShutdownMiddleware guards requests during shutdown: once shutdown has
// begun, new requests' connections are closed WITHOUT writing a response:
// the client sees a connection failure (reset/EOF) and auto-retries — the
// one failure mode that every agent, SDK, and harness retries by default. A
// 503 would be retried by some clients but treated as fatal by others (e.g.
// DeepEval, Continue, promptfoo defaults, Cline/Claude Code reports), and a
// silent hang stalls clients until their own timeout without any retry. The
// health endpoint stays up so probes can still see the process until the
// listener closes.
func ShutdownMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !IsStopping() {
			c.Next()
			return
		}
		if c.Request.URL.Path == "/api/health" || c.Request.URL.Path == "/health" {
			c.Next() // keep liveness answerable until the listener closes
			return
		}
		// Abort the request and close its connection without a response, so
		// the client sees a connection failure and auto-retries. A 503 would
		// be retried by some clients but treated as fatal by others (DeepEval,
		// Continue, promptfoo defaults, Cline/Claude Code reports), and a
		// silent hang stalls clients until their own timeout without any
		// retry. Panicking with http.ErrAbortHandler would be the idiomatic
		// way, but gin v1.12's Recovery classifies it as a broken pipe and
		// aborts WITHOUT re-panicking — the engine then writes an empty 200
		// to the live connection, which an agent would take for a success.
		// Abort() is essential: without it the chain would keep running and
		// the relay would make a full upstream call for a dead client.
		c.Abort()
		if !hijackAndClose(c.Writer) {
			// The connection cannot be hijacked — hang until the client
			// gives up or the process exits (their transport still reports
			// the failure). Cannot occur with this server (plain HTTP/1.1,
			// no TLS), kept as defense in depth.
			<-c.Request.Context().Done()
		}
	}
}

// hijackAndClose hijacks the connection and closes it without writing a
// response, so the client sees a connection failure. Returns false when
// hijacking is unsupported. gin's ResponseWriter always claims to implement
// http.Hijacker, but its implementation panics when the underlying
// connection does not support it — the recover turns that into a plain
// "unsupported" result.
func hijackAndClose(w http.ResponseWriter) bool {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return false
	}
	conn, _, err := func() (conn net.Conn, _ *bufio.ReadWriter, err error) {
		defer func() {
			if recover() != nil {
				err = http.ErrNotSupported
			}
		}()
		return hj.Hijack()
	}()
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
