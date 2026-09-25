package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestLocalOnlyMiddlewareOriginMustMatchRequestHost(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		requestHost string
		origin      string
		wantStatus  int
	}{
		{
			name:        "matching IPv4 host",
			requestHost: "127.0.0.1:9998",
			origin:      "http://127.0.0.1:9998",
			wantStatus:  http.StatusNoContent,
		},
		{
			name:        "IPv6 loopback origin targeting IPv4 host",
			requestHost: "127.0.0.1:9998",
			origin:      "http://[::1]:9998",
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "localhost origin targeting IPv4 host",
			requestHost: "127.0.0.1:9998",
			origin:      "http://localhost:9998",
			wantStatus:  http.StatusForbidden,
		},
		{
			name:        "matching IPv6 host",
			requestHost: "[::1]:9998",
			origin:      "http://[::1]:9998",
			wantStatus:  http.StatusNoContent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.Use(LocalOnlyMiddleware())
			r.POST("/api/settings", func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})

			req := httptest.NewRequest(http.MethodPost, "/api/settings", nil)
			req.Host = tt.requestHost
			req.Header.Set("Origin", tt.origin)
			resp := httptest.NewRecorder()
			r.ServeHTTP(resp, req)

			if resp.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.Code, tt.wantStatus)
			}
		})
	}
}
