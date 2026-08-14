<div align="center">

# 🔀 KeyRouter

**The local-first LLM API gateway: pool all your OpenAI, Anthropic, and compatible-gateway API keys behind one OpenAI-compatible endpoint — with weighted load balancing, automatic failover, cross-protocol format conversion, rate limiting, spend budgets, and billing. Runs as a desktop app on Windows, macOS, and Linux, with a built-in web dashboard.**

[![CI](https://github.com/JohnXu22786/key-router/actions/workflows/ci.yml/badge.svg)](https://github.com/JohnXu22786/key-router/actions/workflows/ci.yml)
[![Release](https://github.com/JohnXu22786/key-router/actions/workflows/release.yml/badge.svg)](https://github.com/JohnXu22786/key-router/actions/workflows/release.yml)
[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)](LICENSE)

**Go · Gin · SQLite · React · Ant Design** — [Releases](https://github.com/JohnXu22786/key-router/releases) · [Issues](https://github.com/JohnXu22786/key-router/issues)

</div>

---

## What is KeyRouter?

KeyRouter runs on your machine and exposes **one local OpenAI-compatible endpoint** (`http://localhost:9998/v1`) that fronts **many upstream providers and API keys**. Point any OpenAI/Anthropic SDK or AI agent at it, and KeyRouter handles the messy parts of working with multiple LLM providers:

- **Multi-key pooling & load balancing** — spread traffic across keys and providers with priority tiers, weights, and a drag-to-reorder call order, with weighted-random selection within each tier.
- **Automatic failover** — when a key is rate-limited, banned, out of quota, or broken, the next key/route/tier is tried automatically, with per-error retry semantics (default 3 retries, configurable up to 20).
- **Cross-protocol conversion** — accept **OpenAI chat completions**, the **OpenAI Responses API** (`/v1/responses`), and **Anthropic Messages** (`/v1/messages`); convert requests, responses, SSE streams, tools, images, files, and reasoning content between OpenAI and Anthropic transparently. Gateways without `/v1/responses` get an automatic chat-completions fallback.
- **Rate limiting & budgets** — per-key RPM / TPM / 5-hour / daily / weekly / monthly sliding windows (the long windows each measure **requests, tokens, or cost**, the short ones count requests and tokens), plus an optional lifetime spend cap. All budgets survive restarts.
- **Health checking** — disabled or cooled-down keys are probed with real billable requests and automatically brought back when they recover; repeated failures back off to avoid surprise provider charges.
- **Billing & analytics** — per-model token pricing (cache pricing, `*` wildcard rules, per-route overrides), per-key cost tracking, and an OpenRouter-style Activity dashboard (Overview / Trends / Explore).
- **A real desktop app** — native window with a built-in dashboard, auto-update (never silent), and graceful restart; on Windows it also adds a system tray (close-to-tray, single-click restore) and launch-at-login — not another self-hosted server to babysit.

> **Short version:** one install, one endpoint, all your keys — failover, quotas, and bills handled for you.

## Why KeyRouter?

| | Raw API keys | **KeyRouter** | LiteLLM / one-api / new-api |
|---|---|---|---|
| Setup | none | install + add keys in UI | deploy & maintain a server |
| Failover | manual | automatic, per-key health probing | proxy-level, varies |
| Protocol conversion | — | OpenAI ⇄ Anthropic ⇄ Responses | partial / provider-specific |
| Rate limits | provider-side only | per-key, 6 windows (metric selectable) | per-key, varies |
| Cost tracking | — | built-in pricing + analytics | varies |
| Runs where | in your app | your machine (`127.0.0.1`) | server / container |

KeyRouter is for **individuals and small teams** who want a zero-ops, local-first gateway with a GUI — the LLM-client equivalent of a good router for your home network. If you need a multi-user shared service, a server deployment (e.g. LiteLLM or one-api) is the better fit.

## Quick Start

### Windows

Download the **installer** `KeyRouter-x.y.z-windows-amd64-setup.exe` from the [Releases page](https://github.com/JohnXu22786/key-router/releases) and run it (installs to Program Files, adds Start-menu/desktop shortcuts and an uninstaller). A portable `KeyRouter-x.y.z-windows-amd64.exe` is also published — run it anywhere; it uses the same data location as the installed version.

### macOS

Download the DMG for your architecture — `KeyRouter-x.y.z-darwin-arm64.dmg` (Apple Silicon) or `KeyRouter-x.y.z-darwin-amd64.dmg` (Intel) — open it and drag `KeyRouter.app` into Applications. The app is ad-hoc signed and **not notarized**: if Gatekeeper refuses to open it, right-click the app → Open, or remove the quarantine attribute (`xattr -dr com.apple.quarantine /Applications/KeyRouter.app`). Plain executables for both architectures are also published.

### Linux

Download the Debian/Ubuntu package and install it (built for **Ubuntu 22.04 / Debian 12**; needs WebKitGTK 4.0 / GTK 3):

```bash
sudo apt install ./KeyRouter-x.y.z-linux-amd64.deb
```

Alternatively, use `KeyRouter-x.y.z-linux-amd64.tar.gz` or the raw binary directly.

### Data location

All user data — the SQLite database, rate-limit windows, and logs — is stored in the **system application-data directory**, never next to the executable, so it survives updates and behaves identically for every build type and platform:

| Platform | Location |
|---|---|
| Windows | `%LOCALAPPDATA%\KeyRouter` |
| macOS | `~/Library/Application Support/KeyRouter` |
| Linux | `$XDG_DATA_HOME/keyrouter` (default: `~/.local/share/keyrouter`) |

Set the `KEYROUTER_DATA` environment variable to override (e.g. for tests or portable isolated instances).

## Use it

Point any OpenAI/Anthropic SDK at the gateway — the `model` field selects a **model group** you configured:

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:9998/v1", api_key="anything")
resp = client.chat.completions.create(
    model="gpt-4o",          # any model group you configured
    messages=[{"role": "user", "content": "Hello!"}],
)
```

The **Responses API** works the same way — `client.responses.create(...)` against the same base URL — as does the Anthropic SDK against `/v1/messages`, and `curl` against `/v1/embeddings` and `/v1/models`:

```bash
curl http://localhost:9998/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model": "gpt-4o", "messages": [{"role": "user", "content": "Hi"}]}'
```

> The gateway listens on `127.0.0.1` only — it is **not** exposed to your LAN. API keys are stored locally in SQLite.

## How it works

```
  Your app / agent — any OpenAI or Anthropic SDK
                      │  http://localhost:9998/v1
                      ▼
  ┌──────────────────────────────────────────────────────────┐
  │                      KeyRouter (local)                    │
  │                                                           │
  │  /v1/* ──► selector.Engine: model group ─► routes        │
  │          (priority tier + weight) ─► keys (status,       │
  │          rate windows, sort order)                       │
  │                     │                                    │
  │                     ▼                                    │
  │          relay: format conversion (OpenAI ⇄ Anthropic    │
  │          ⇄ Responses), SSE streaming, retry, failover,   │
  │          consumption + cost recording                    │
  │                                                           │
  │  health checker · sliding-window rate limiter (persisted)│
  └────────────────────────┬─────────────────────────────────┘
                           ▼
  OpenAI · Anthropic · any OpenAI-compatible gateway (your keys)
```

A request flows through five steps:

1. **Resolve** — the `model` field maps to a model group; routes are ordered by priority tier, then tried in weighted-random order within a tier (default weight 10). A route can rewrite the upstream model (`TargetModel`) and inject extra params.
2. **Select** — within a route, keys are tried in the order you dragged them (immediate-recovery keys always before lazy ones). Keys that are disabled, in cooldown, or over any rate-limit window are skipped; `rate_limited` keys whose cooldown has expired are eligible again.
3. **Convert & send** — the request is translated to the upstream's protocol (if needed) and sent, with streaming when requested. Responses and SSE streams are converted back to the client's format — tool calls, images, files, and reasoning content included.
4. **Failover** — on failure the reason is classified: 429 with a quota error code → key disabled (`insufficient_quota`); plain 429 → cooldown honoring `Retry-After`; 401 → disabled; 403 → 30s cooldown (often model access, not a bad key); 5xx/408/409/425 → 30s cooldown. The next key, route, or tier is tried, up to `retry_times` (default 3, max 20). Unsupported route shapes (e.g. embeddings on an Anthropic-only route) are excluded without burning a retry.
5. **Record** — on success, usage and cost are recorded (cache-aware: OpenAI's cached tokens are billed at the cache-read rate) and rate-limit windows tick. Every key status change — failover, health recovery — pushes an SSE event that makes the UI refresh instantly.

## Configure

1. Open the web UI (`http://localhost:9998` — it opens automatically in the desktop window).
2. **Providers** — add OpenAI, Anthropic, or any compatible gateway (base URL without `/v1`; extra headers supported).
3. **Keys** — add API keys, set rate-limit budgets, a recovery strategy (immediate / lazy), and an optional lifetime spend cap.
4. **Model Groups** — create a group named after the model your clients will send (e.g. `gpt-4o`); it appears in `/v1/models` with its context length and max output tokens.
5. **Routes** — map a model group to a provider (priority tier + weight, drag to reorder). Rewrite the target model or override pricing per route if you like.
6. **Pricing** — set per-model token rates (optional; enables cost tracking). Cache read/write rates and a `*` wildcard rule are supported.

### Rate limit windows

| Window | Buckets | Applies to |
|---|---|---|
| RPM | 60 × 1s | requests/minute |
| TPM | 60 × 1s | tokens/minute |
| 5-hour | 60 × 5m | requests or tokens or cost |
| Daily | 24 × 1h | requests or tokens or cost |
| Weekly | 7 × 24h | requests or tokens or cost |
| Monthly | 30 × 24h | requests or tokens or cost |

The 5-hour, daily, weekly, and monthly windows each measure **one** of requests / tokens / cost (choose per key); RPM always counts requests and TPM always counts tokens. Windows are persisted to `windows.json` every 15 seconds and on shutdown, so budgets survive restarts — even crashes.

## Security & privacy

- **Local-only by design**: the server binds to `127.0.0.1` and rejects non-local `Host` headers (DNS-rebinding defense) and cross-origin requests (CSRF defense). Your keys never leave your machine except to the upstream providers you configured.
- **Optional auth token** protects the `/v1/*` forwarding API when configured (the management UI stays local-only); token checks use constant-time comparison and read the current setting per request.
- **Health probes are real, tiny, billable requests** (`max_tokens: 1`) — never `GET /v1/models`, which most gateways answer 200 for even with an invalid key. After repeated failures, probing backs off entirely.
- **Safe updates & shutdown**: updates are size-checked against release metadata, install-aware (installer vs portable), and never apply silently; shutdown lets in-flight streams finish while new connections fail fast (the one failure mode every client auto-retries).

## Build from source

Requires Go 1.26.5+ and Node 20.19+ (for the web UI build).

```bash
# Build the web UI first: web/dist is generated (not committed) and is
# embedded into the binary via //go:embed web/dist/*.
cd web && npm install && npm run build && cd ..

# Backend + embedded UI
go build -o keyrouter .
```

The UI is a React + Ant Design SPA served by the Go binary (no external server needed).

## Project layout

```
main.go                  # entry point (native window + server bootstrap)
handler/                 # HTTP handlers: /v1/* relay, /api/* management
relay/                   # upstream relay, streaming SSE, format conversion
selector/                # routing engine: key selection, retry, failover
window/                  # sliding-window rate limiters (+ persistence)
health/                  # key health checker with probe backoff
billing/                 # token pricing and consumption records
format/                  # OpenAI ↔ Anthropic ↔ Responses conversion
events/                  # SSE push hub (instant UI hot reload)
middleware/              # local-only, auth, graceful-shutdown
router/                  # HTTP routing + SPA static serving
server/                  # listener (127.0.0.1, streaming-safe timeouts)
update/                  # auto-update (size-checked, install-aware)
web/                     # React + Ant Design management UI
db/                      # SQLite (WAL, single-writer, migrations)
```

## FAQ

**Which clients can use it?** Anything that can set a base URL: OpenAI SDKs, the Anthropic SDK, Claude Code (`ANTHROPIC_BASE_URL`), Cursor, Continue, Cline, opencode, LobeChat, Cherry Studio, Chatbox, and more. The API also honors the OpenRouter `X-OpenRouter-Title` convention for app attribution in analytics.

**What happens when a key runs out of quota?** A 402 (or a 429 with a quota error code in the body) disables the key (`insufficient_quota`) — it will not auto-recover, but can be re-enabled after topping up. Plain rate limits cool the key down (honoring `Retry-After`), and the health checker probes it back into service automatically once it recovers.

**Can I expose the gateway to my LAN or the internet?** Not without modifying the code. The management API is intentionally unauthenticated (it returns your keys in plaintext), so the app binds to `127.0.0.1` only.

**Is it free?** Yes — KeyRouter is open source under AGPL-3.0. No accounts, no telemetry, no cloud.

## License

**KeyRouter is licensed under the [GNU Affero General Public License v3.0 (AGPL-3.0)](LICENSE).**

If you use a modified version to provide a service over a network, you must make your modified source code available to users of that service under the same license.

---

<div align="center"><sub>Built with Go, Gin, GORM, SQLite, React, and Ant Design.</sub></div>
