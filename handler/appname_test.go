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
		{"ipv6 any-address referer ignored", hdr("HTTP-Referer", "http://[::]:3000/dashboard"), nil, ""},
		{"localhost.com is a real domain", hdr("HTTP-Referer", "https://localhost.com/x"), nil, "localhost.com"},
		{"Referer port stripped", hdr("HTTP-Referer", "https://myapp.example.com:8443/chat"), nil, "myapp.example.com"},
		{"Referer www and port stripped", hdr("HTTP-Referer", "https://www.myapp.example.com:8443/chat"), nil, "myapp.example.com"},
		{"IPv6 referer port stripped, literal kept", hdr("HTTP-Referer", "http://[2001:db8::1]:3000/chat"), nil, "[2001:db8::1]"},
		{"unbracketed IPv6 referer kept intact", hdr("HTTP-Referer", "http://2001:db8::1/chat"), nil, "2001:db8::1"},
		// Regression (#130): a single-colon compressed IPv6 literal
		// ("2001:db8", "a:b") must survive the port strip and become the
		// app name itself, not the truncated fragment ("2001", "a").
		{"single-colon IPv6 referer kept", hdr("HTTP-Referer", "https://2001:db8/chat"), nil, "2001:db8"},
		{"single-colon IPv6 referer kept a:b", hdr("HTTP-Referer", "https://a:b/chat"), nil, "a:b"},
		// The bracketed form is what browsers actually emit for IPv6 URLs;
		// it must survive the referer path end to end, port included.
		{"bracketed single-colon IPv6 referer kept with port", hdr("HTTP-Referer", "https://[a:b]:3000/chat"), nil, "[a:b]"},
		{"LAN IP referer port stripped", hdr("HTTP-Referer", "http://192.168.1.5:3000"), nil, "192.168.1.5"},
		{"Referer userinfo kept out of host", hdr("HTTP-Referer", "https://user@example.com/x"), nil, "example.com"},
		{"Referer userinfo with password kept out of host", hdr("HTTP-Referer", "https://user:pass@example.com/x"), nil, "example.com"},
		{"Referer userinfo and port stripped", hdr("HTTP-Referer", "https://user:pass@example.com:8443/chat"), nil, "example.com"},
		{"IPv6 referer with userinfo and port stripped", hdr("HTTP-Referer", "http://user:pass@[2001:db8::1]:3000/chat"), nil, "[2001:db8::1]"},
		{"Referer userinfo does not hide www", hdr("HTTP-Referer", "https://user:pass@www.example.com/x"), nil, "example.com"},
		// Userinfo is dropped only after the path/query/fragment cuts; an
		// '@' in those parts must not be mistaken for the userinfo
		// separator (regression guards for the cut ordering).
		{"Referer query @ not mistaken for userinfo", hdr("HTTP-Referer", "https://user@example.com?x=a@b"), nil, "example.com"},
		{"Referer path @ not mistaken for userinfo", hdr("HTTP-Referer", "https://user@example.com/a@b/x"), nil, "example.com"},
		{"Referer fragment @ not mistaken for userinfo", hdr("HTTP-Referer", "https://example.com#a@b"), nil, "example.com"},

		// Explicit attribution display name is capped at the DB column width.
		{"over-long title truncated", hdr("X-OpenRouter-Title", strings.Repeat("a", 300)), nil, strings.Repeat("a", 255)},
		{"over-long title rune-safe", hdr("X-OpenRouter-Title", strings.Repeat("中", 300)), nil, strings.Repeat("中", 255)},

		// Known client User-Agent tokens (the only apps that identify via UA).
		{"OpenCode UA", hdr("User-Agent", "opencode/1.14.28 ai-sdk/provider-utils/X runtime/bun/Y"), nil, "OpenCode"},
		{"LobeChat UA", hdr("User-Agent", "lobe-chat/1.0"), nil, "LobeChat"},
		{"LobeHub UA", hdr("User-Agent", "lobehub/2.0"), nil, "LobeChat"},
		{"Continue UA", hdr("User-Agent", "Continue/0.9.42"), nil, "Continue"},
		{"Cline UA", hdr("User-Agent", "Cline/3.4.0 (VSCode)"), nil, "Cline"},
		{"Kilo Code UA", hdr("User-Agent", "KiloCode/2.35.0 (VSCode)"), nil, "Kilo Code"},
		{"Kilo Code hyphenated UA", hdr("User-Agent", "Kilo-Code/5.3.0"), nil, "Kilo Code"},
		{"Roo Code UA", hdr("User-Agent", "RooCode/3.5.5"), nil, "Roo Code"},
		{"bare kilo substring no false match", hdr("User-Agent", "KiloMeter/1.0"), nil, ""},
		{"bare roo substring no false match", hdr("User-Agent", "Kangaroo/2.0"), nil, ""},
		{"bare Roo token no false match", hdr("User-Agent", "Roo/1.0"), nil, ""},
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
		// Edge's mobile builds brand their UA with the EdgA (Android) and
		// EdgiOS (iOS) tokens instead of the desktop "Edg/". Neither
		// contains "edg/", so Chrome/Safari must not win on the engine
		// tokens those UAs still carry — regression for the mislabel that
		// showed Edge on Android as "Chrome" and Edge on iOS as "Safari".
		{"Browser UA edge android EdgA", hdr("User-Agent", "Mozilla/5.0 (Linux; Android 13) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/118.0.0.0 Mobile Safari/537.36 EdgA/118.0.2218.37"), nil, "Edge"},
		{"Browser UA edge iOS EdgiOS", hdr("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1 EdgiOS/118.0.2218.36"), nil, "Edge"},
		{"Browser UA edge desktop regression", hdr("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 Edg/140.0.0.0"), nil, "Edge"},
		// Ordering guard: the chrome/ token appears before EdgA/ in this
		// UA; only the branch order (Edg family checked before Chrome)
		// keeps it labelled Edge.
		{"Browser UA edge EdgA after chrome token", hdr("User-Agent", "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/119.0.0.0 Mobile Safari/537.36 EdgA/119.0.2151.58"), nil, "Edge"},
		{"Browser UA chrome regression", hdr("User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/140.0.0.0 Safari/537.36"), nil, "Chrome"},
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

func TestStripHostPort(t *testing.T) {
	cases := []struct {
		name     string
		in, want string
	}{
		{"plain host", "example.com", "example.com"},
		// Plain host:port regression — the port is not part of the host identity.
		{"host with port", "example.com:8080", "example.com"},
		{"IPv6 literal with port", "[2001:db8::1]:3000", "[2001:db8::1]"},
		{"IPv6 loopback with port", "[::1]:8787", "[::1]"},
		// Bracketed single-colon literals (the form browsers emit for IPv6):
		// the bracket branch must be evaluated before the colon strip, or
		// the strip branch sees one colon with none before it and truncates
		// ("[2001:db8]" -> "[2001").
		{"bracketed single-colon IPv6 literal", "[2001:db8]", "[2001:db8]"},
		{"bracketed single-colon IPv6 literal a:b", "[a:b]", "[a:b]"},
		{"bracketed single-colon IPv6 literal with port", "[a:b]:3000", "[a:b]"},
		// Unbracketed IPv6 is left untouched: its colons are indistinguishable
		// from a port separator.
		{"unbracketed IPv6", "2001:db8::1", "2001:db8::1"},
		// Single-colon compressed forms are valid unbracketed IPv6 literals
		// too (RFC 4291 zero-group omission: "2001:db8" is 2001:db8::, and
		// "a:b" is a:b::). Their one colon must not be read as a port
		// separator — regression: they used to be truncated to "2001" / "a".
		{"single-colon IPv6 literal", "2001:db8", "2001:db8"},
		{"single-colon IPv6 literal a:b", "a:b", "a:b"},
		// The deliberately documented flip side: a hex-word hostname with an
		// all-hex-digit port ("cafe" and "8080" are both hex) is structurally
		// identical to a compressed literal, and the parse favors the literal
		// — kept whole. Pinned so the trade-off cannot drift silently.
		{"hex-word host with hex port kept as literal", "cafe:8080", "cafe:8080"},
		// net.ParseIP is case-insensitive in hex groups; the recognition must
		// survive a future reimplementation.
		{"uppercase single-colon IPv6 literal", "Ab:Cd", "Ab:Cd"},
		// RFC 3986 userinfo precedes the host; credentials must never become
		// the host identity.
		{"userinfo", "user@example.com", "example.com"},
		{"userinfo with password", "user:pass@example.com", "example.com"},
		{"userinfo with password and port", "user:pass@example.com:8443", "example.com"},
		{"malformed userinfo with inner @", "user@name@example.com", "example.com"},
		{"userinfo with IPv6 literal and port", "user@[2001:db8::1]:3000", "[2001:db8::1]"},
		{"userinfo with password and IPv6 literal", "user:pass@[2001:db8::1]", "[2001:db8::1]"},
		// localhost semantics unchanged: userinfo is dropped, loopback still
		// detected through the brackets.
		{"userinfo with localhost and port", "user:pass@localhost:3000", "localhost"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripHostPort(c.in); got != c.want {
				t.Errorf("stripHostPort(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestIsLocalHostname(t *testing.T) {
	cases := []struct {
		name string
		host string
		want bool
	}{
		{"empty", "", true},
		{"localhost", "localhost", true},
		{"ipv4 loopback", "127.0.0.1", true},
		{"ipv4 any-address", "0.0.0.0", true},
		{"ipv6 loopback", "::1", true},
		{"ipv6 loopback bracketed with port", "[::1]:8787", true},
		// :: is the IPv6 any-address: like 0.0.0.0, it binds every
		// interface and must be treated as local even though
		// net.IP.IsLoopback() excludes it.
		{"ipv6 any-address", "::", true},
		{"ipv6 any-address bracketed", "[::]", true},
		{"ipv6 any-address bracketed with port", "[::]:3000", true},
		// Regression: other IPv6 literals are NOT local.
		{"ipv6 literal still remote", "2001:db8::1", false},
		{"ipv6 literal bracketed still remote", "[2001:db8::1]", false},
		{"ipv6 literal with port still remote", "[2001:db8::1]:3000", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isLocalHostname(c.host); got != c.want {
				t.Errorf("isLocalHostname(%q) = %v, want %v", c.host, got, c.want)
			}
		})
	}
}
