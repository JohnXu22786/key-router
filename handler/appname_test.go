package handler

import (
	"net/http"
	"testing"
)

func TestExtractAppName(t *testing.T) {
	hdr := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i+1 < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}

	cases := []struct {
		name string
		h    http.Header
		want string
	}{
		{"X-OpenRouter-Title wins", hdr("X-OpenRouter-Title", "Continue", "X-Title", "Other", "HTTP-Referer", "https://continue.dev"), "Continue"},
		{"X-Title fallback", hdr("X-Title", "Chatbox", "HTTP-Referer", "https://chatbox.app"), "Chatbox"},
		{"Referer hostname", hdr("HTTP-Referer", "https://github.com/continuedev/continue"), "github.com"},
		{"Referer with www stripped", hdr("HTTP-Referer", "http://www.myapp.com/"), "myapp.com"},
		{"User-Agent product", hdr("User-Agent", "claude-code/2.1.89 (cli)"), "claude-code"},
		{"User-Agent curl", hdr("User-Agent", "curl/8.7.1"), "curl"},
		{"User-Agent OpenAI SDK", hdr("User-Agent", "OpenAI/Python 1.3.5"), "OpenAI"},
		{"Browser UA strips chrome", hdr("User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/139.0.0.0 Safari/537.36"), "Chrome"},
		{"nothing -> empty (Unknown)", hdr(), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractAppName(c.h)
			if got != c.want {
				t.Errorf("extractAppName = %q, want %q", got, c.want)
			}
		})
	}
}
