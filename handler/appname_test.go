package handler

import (
	"net/http"
	"strings"
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
		// Explicit attribution headers (OpenRouter convention) win over everything.
		{"X-OpenRouter-Title wins", hdr("X-OpenRouter-Title", "Continue", "X-Title", "Other", "HTTP-Referer", "https://www.continue.dev"), nil, "Continue"},
		{"X-Title fallback", hdr("X-Title", "Chatbox", "HTTP-Referer", "https://chatboxai.app"), nil, "Chatbox"},
		{"x-app named", hdr("x-app", "my-app"), nil, "my-app"},

		// Claude Code identification signals.
		{"Claude Code x-app cli + UA", hdr("x-app", "cli", "User-Agent", "claude-cli/2.1.96 (external, cli)"), nil, "Claude Code"},
		{"Claude Code session header", hdr("X-Claude-Code-Session-Id", "8f4a-2b1c-d3e5-6a7b"), nil, "Claude Code"},
		{"Claude Code anthropic-beta", hdr("anthropic-beta", "claude-code-20250219,oauth-2025-04-20"), nil, "Claude Code"},
		{"x-app cli alone is not Claude Code", hdr("x-app", "cli"), nil, ""},
		{"x-app CLI uppercase is generic too", hdr("x-app", "CLI"), nil, ""},
		{"Claude Code x-app cli-bg + session", hdr("x-app", "cli-bg", "X-Claude-Code-Session-Id", "8f4a-2b1c-d3e5-6a7b"), nil, "Claude Code"},
		{"Claude Code x-app cli-bg + UA", hdr("x-app", "cli-bg", "User-Agent", "claude-cli/2.1.96 (external, cli)"), nil, "Claude Code"},
		{"x-app cli-bg alone is Claude Code", hdr("x-app", "cli-bg"), nil, "Claude Code"},
		{"x-app CLI-BG uppercase is Claude Code", hdr("x-app", "CLI-BG"), nil, "Claude Code"},
		{"x-app cli-bg-pro passthrough intact", hdr("x-app", "cli-bg-pro"), nil, "cli-bg-pro"},

		// Provider-specific identifying headers.
		{"Cursor X-Cursor-Mode", hdr("X-Cursor-Mode", "agent", "User-Agent", "axios/1.7"), nil, "Cursor"},
		{"Codex originator cli", hdr("originator", "codex_cli_rs"), nil, "Codex"},
		{"Codex originator vscode", hdr("originator", "codex_vscode"), nil, "Codex (VS Code)"},
		{"Codex originator tui", hdr("originator", "codex-tui"), nil, "Codex TUI"},
		{"Codex originator atlas", hdr("originator", "codex_atlas"), nil, "Atlas"},
		{"Codex originator chatgpt", hdr("originator", "codex_chatgpt_desktop"), nil, "ChatGPT"},
		{"Codex originator unknown prefixed", hdr("originator", "Codex exec"), nil, "Codex"},
		{"Open WebUI user header", hdr("X-OpenWebUI-User-Name", "alice"), nil, "Open WebUI"},

		// HTTP-Referer hostname (OpenRouter attribution URL).
		{"Referer hostname", hdr("HTTP-Referer", "https://github.com/continuedev/continue"), nil, "github.com"},
		{"Standard Referer header works too", hdr("Referer", "https://example.com/x"), nil, "example.com"},
		{"Referer wins over UA token", hdr("HTTP-Referer", "https://cline.bot", "User-Agent", "Cline/3.4.0 (VSCode)"), nil, "cline.bot"},
		{"Referer uppercase scheme", hdr("HTTP-Referer", "HTTPS://example.com/x"), nil, "example.com"},
		{"Referer uppercase www", hdr("HTTP-Referer", "https://WWW.example.com/x"), nil, "example.com"},
		{"Referer query stripped", hdr("HTTP-Referer", "https://example.com?from=app"), nil, "example.com"},
		{"localhost referer ignored", hdr("HTTP-Referer", "http://127.0.0.1:8787/dashboard"), nil, ""},
		{"localhost.localdomain referer ignored", hdr("HTTP-Referer", "http://localhost.localdomain/x"), nil, ""},
		{"ipv6 loopback referer ignored", hdr("HTTP-Referer", "http://[::1]:8787/x"), nil, ""},
		{"localhost.com is a real domain", hdr("HTTP-Referer", "https://localhost.com/x"), nil, "localhost.com"},
		{"Referer port stripped", hdr("HTTP-Referer", "https://myapp.example.com:8443/chat"), nil, "myapp.example.com"},
		{"Referer www and port stripped", hdr("HTTP-Referer", "https://www.myapp.example.com:8443/chat"), nil, "myapp.example.com"},
		{"IPv6 referer port stripped, literal kept", hdr("HTTP-Referer", "http://[2001:db8::1]:3000/chat"), nil, "[2001:db8::1]"},
		{"unbracketed IPv6 referer kept intact", hdr("HTTP-Referer", "http://2001:db8::1/chat"), nil, "2001:db8::1"},
		{"LAN IP referer port stripped", hdr("HTTP-Referer", "http://192.168.1.5:3000"), nil, "192.168.1.5"},

		// Explicit attribution display name is capped at the DB column width.
		{"over-long title truncated", hdr("X-OpenRouter-Title", strings.Repeat("a", 300)), nil, strings.Repeat("a", 255)},
		{"over-long title rune-safe", hdr("X-OpenRouter-Title", strings.Repeat("中", 300)), nil, strings.Repeat("中", 255)},

		// Known client User-Agent tokens (the only apps that identify via UA).
		{"OpenCode UA", hdr("User-Agent", "opencode/1.14.28 ai-sdk/provider-utils/X runtime/bun/Y"), nil, "OpenCode"},
		{"LobeChat UA", hdr("User-Agent", "lobe-chat/1.0"), nil, "LobeChat"},
		{"LobeHub UA", hdr("User-Agent", "lobehub/2.0"), nil, "LobeChat"},
		{"Continue UA", hdr("User-Agent", "Continue/0.9.42"), nil, "Continue"},
		{"Cline UA", hdr("User-Agent", "Cline/3.4.0 (VSCode)"), nil, "Cline"},
		{"Gemini CLI UA", hdr("User-Agent", "GeminiCLI/0.35.3/gemini-3-pro-preview (linux; x64; GitHub) google-api-nodejs-client/9.15.1"), nil, "Gemini CLI"},
		{"Cherry Studio Electron UA", hdr("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) CherryStudio/1.5.11 Chrome/138.0.7204.243 Electron/37.4.0 Safari/537.36"), nil, "Cherry Studio"},

		// SDK / tool User-Agents are NOT app identity.
		{"SDK UA is not an app", hdr("User-Agent", "OpenAI/Python 1.3.5"), nil, ""},
		{"curl UA is not an app", hdr("User-Agent", "curl/8.7.1"), nil, ""},
		{"axios UA is not an app", hdr("User-Agent", "axios/1.7.4"), nil, ""},

		// Request body client_metadata (older Codex).
		{"Codex client_metadata body", hdr(), []byte(`{"model":"gpt-5","client_metadata":{"app_name":"codex"}}`), "codex"},
		{"garbage body ignored", hdr(), []byte("not json at all"), ""},

		// Browser User-Agents.
		{"Browser UA chrome", hdr("User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/139.0.0.0 Safari/537.36"), nil, "Chrome"},
		{"Browser UA edge", hdr("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/139.0.0.0 Safari/537.36 Edg/139.0.0.0"), nil, "Edge"},
		{"Browser UA firefox", hdr("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:139.0) Gecko/20100101 Firefox/139.0"), nil, "Firefox"},
		{"Browser UA safari", hdr("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15"), nil, "Safari"},
		{"Electron UA without app token", hdr("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.7204.243 Electron/37.4.0 Safari/537.36"), nil, ""},

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
