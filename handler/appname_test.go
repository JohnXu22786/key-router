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
		body []byte
		want string
	}{
		{"X-OpenRouter-Title wins", hdr("X-OpenRouter-Title", "Continue", "X-Title", "Other", "HTTP-Referer", "https://continue.dev"), nil, "Continue"},
		{"X-Title fallback", hdr("X-Title", "Chatbox", "HTTP-Referer", "https://chatbox.app"), nil, "Chatbox"},
		{"x-app generic plus claude-cli UA", hdr("x-app", "cli", "User-Agent", "claude-cli/2.1.96 (external, cli)"), nil, "Claude Code"},
		{"x-app named", hdr("x-app", "my-app"), nil, "my-app"},
		{"Cursor X-Cursor-Mode", hdr("X-Cursor-Mode", "agent", "User-Agent", "axios/1.7"), nil, "Cursor"},
		{"OpenCode UA", hdr("User-Agent", "opencode/1.14.28 ai-sdk/provider-utils/X runtime/bun/Y"), nil, "OpenCode"},
		{"LobeChat UA", hdr("User-Agent", "lobe-chat/1.0"), nil, "LobeChat"},
		{"Codex client_metadata body", hdr(), []byte(`{"model":"gpt-5","client_metadata":{"app_name":"codex"}}`), "codex"},
		{"Referer hostname", hdr("HTTP-Referer", "https://github.com/continuedev/continue"), nil, "github.com"},
		{"User-Agent product", hdr("User-Agent", "curl/8.7.1"), nil, "curl"},
		{"User-Agent OpenAI SDK", hdr("User-Agent", "OpenAI/Python 1.3.5"), nil, "OpenAI"},
		{"Browser UA strips chrome", hdr("User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/139.0.0.0 Safari/537.36"), nil, "Chrome"},
		{"nothing -> empty (Unknown)", hdr(), nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractAppName(c.h, c.body)
			if got != c.want {
				t.Errorf("extractAppName = %q, want %q", got, c.want)
			}
		})
	}
}
