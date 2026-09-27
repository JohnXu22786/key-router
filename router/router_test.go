package router

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"key-router/db"
	"key-router/events"
)

type subErrorFS struct{}

func (subErrorFS) Open(string) (fs.File, error) {
	return nil, fs.ErrNotExist
}

func (subErrorFS) Sub(string) (fs.FS, error) {
	return nil, fs.ErrNotExist
}

func TestMissingUIFallbackReturnsJSONNotFoundForAPIPaths(t *testing.T) {
	r := newMissingUIRouter(t)

	for _, path := range []string{"/api/missing", "/v1/missing"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Host = "localhost"
			response := httptest.NewRecorder()

			r.ServeHTTP(response, request)

			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d; body = %q", response.Code, http.StatusNotFound, response.Body.String())
			}
			if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
				t.Fatalf("Content-Type = %q, want application/json", contentType)
			}

			var body map[string]string
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatalf("decode JSON response: %v", err)
			}
			if body["error"] != "not found" {
				t.Fatalf("error = %q, want %q", body["error"], "not found")
			}
		})
	}
}

func TestMissingUIFallbackServesHTMLForNonAPIPaths(t *testing.T) {
	r := newMissingUIRouter(t)
	request := httptest.NewRequest(http.MethodGet, "/some/page", nil)
	request.Host = "localhost"
	response := httptest.NewRecorder()

	r.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), "Web UI not built") {
		t.Fatalf("body = %q, want missing-UI HTML", response.Body.String())
	}
	assertManagementUIFrameProtection(t, response)
}

func TestEmbeddedUIFallbackServesHTMLWithFrameProtection(t *testing.T) {
	staticFS := fstest.MapFS{
		"web/dist/index.html": &fstest.MapFile{Data: []byte("<!doctype html><html><body>Management UI</body></html>")},
	}
	r := newRouter(t, staticFS)
	request := httptest.NewRequest(http.MethodGet, "/some/page", nil)
	request.Host = "localhost"
	response := httptest.NewRecorder()

	r.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), "Management UI") {
		t.Fatalf("body = %q, want embedded SPA HTML", response.Body.String())
	}
	assertManagementUIFrameProtection(t, response)
}

func TestAPIFallbackDoesNotReceiveManagementUIFrameHeaders(t *testing.T) {
	r := newMissingUIRouter(t)
	request := httptest.NewRequest(http.MethodGet, "/api/missing", nil)
	request.Host = "localhost"
	response := httptest.NewRecorder()

	r.ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	if got := response.Header().Get("X-Frame-Options"); got != "" {
		t.Fatalf("X-Frame-Options = %q, want no management UI header", got)
	}
	if got := response.Header().Get("Content-Security-Policy"); got != "" {
		t.Fatalf("Content-Security-Policy = %q, want no management UI header", got)
	}
}

func newMissingUIRouter(t *testing.T) http.Handler {
	return newRouter(t, subErrorFS{})
}

func newRouter(t *testing.T, staticFS fs.FS) http.Handler {
	t.Helper()

	previousDB := db.GetDB()
	if err := db.Init(t.TempDir()); err != nil {
		t.Fatalf("initialize temporary database: %v", err)
	}
	currentDB := db.GetDB()
	t.Cleanup(func() {
		if currentDB != nil && currentDB != previousDB {
			if sqlDB, err := currentDB.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}
		db.DB = previousDB
	})

	return Setup(staticFS, nil, nil, events.NewHub())
}

func assertManagementUIFrameProtection(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if got := response.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := response.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
		t.Errorf("Content-Security-Policy = %q, want frame-ancestors 'none'", got)
	}
}
