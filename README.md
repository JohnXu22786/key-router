<div align="center">

# 🔀 KeyRouter

**A local OpenAI/Anthropic API gateway with multi-key management, automatic failover, format conversion, rate limiting, and billing — all in one desktop app.**

[![CI](https://github.com/JohnXu22786/key-router/actions/workflows/ci.yml/badge.svg)](https://github.com/JohnXu22786/key-router/actions/workflows/ci.yml)
[![Release](https://github.com/JohnXu22786/key-router/actions/workflows/release.yml/badge.svg)](https://github.com/JohnXu22786/key-router/actions/workflows/release.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/License-AGPL--3.0-blue.svg)](LICENSE)

*Windows · macOS · Linux — desktop app with an embedded web UI*

</div>

---

## What is KeyRouter?

KeyRouter runs on your machine and exposes **one OpenAI-compatible endpoint** that fronts **many upstream providers and API keys**. It handles the messy parts of working with multiple LLM providers:

- **Multi-key pooling** — spread traffic across many API keys from many providers, with weighted routing and automatic failover when a key is rate-limited, banned, or out of quota.
- **Format conversion** — send **OpenAI-format** requests (`/v1/chat/completions`, **and the newer Responses API** `/v1/responses`) to Anthropic providers (and vice versa). Requests, responses, streaming SSE, tools, and images are converted transparently. `/v1/responses` also works against OpenAI-compatible gateways that don't implement it — those get an automatic chat-completions fallback.
- **Rate limiting** — per-key RPM / TPM / 5-hour / daily / weekly / monthly budgets with sliding windows that survive restarts.
- **Health checking** — disabled or cooled-down keys are probed automatically and brought back when they recover.
- **Billing & stats** — per-model token pricing (including cache pricing and a `*` wildcard rule), per-key cost tracking, and usage charts.
- **A clean management UI** — manage providers, keys, model groups, routes, pricing, and settings from a built-in web dashboard.

## Quick Start

### Windows

Download the latest **installer** `KeyRouter-x.y.z-windows-amd64-setup.exe` from the [Releases page](https://github.com/JohnXu22786/key-router/releases) and run it (installs to Program Files, adds Start-menu/desktop shortcuts and an uninstaller). A portable `KeyRouter-x.y.z-windows-amd64.exe` is also published — run it anywhere; it uses the same data location as the installed version.

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

### Use it

Point any OpenAI/Anthropic SDK at the gateway:

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:9998/v1", api_key="anything")
resp = client.chat.completions.create(
    model="gpt-4o",          # any model group you configured
    messages=[{"role": "user", "content": "Hello!"}],
)
```

The **Responses API** works the same way — `client.responses.create(...)` against the same base URL. Requests are routed by the `model` field like any other request; OpenAI/Anthropic providers and non-Responses OpenAI-compatible gateways are all supported (gateways without `/v1/responses` are converted to chat completions automatically).

> The gateway listens on `127.0.0.1` only — it is **not** exposed to your LAN. API keys are stored locally in SQLite.

## Configure

1. Open the web UI (`http://localhost:9998` — it opens automatically in the desktop window).
2. **Providers** — add OpenAI, Anthropic, or any compatible gateway (base URL without `/v1`; extra headers supported).
3. **Keys** — add API keys, set rate-limit budgets and a recovery strategy.
4. **Model Groups** — create a group named after the model your clients will send (e.g. `gpt-4o`).
5. **Routes** — map a model group to a provider (priority tier + weight, drag to reorder).
6. **Pricing** — set per-model token rates (optional; enables cost tracking).

### Rate limit windows

| Window | Buckets | Applies to |
|---|---|---|
| RPM | 60 × 1s | requests/minute |
| TPM | 60 × 1s | tokens/minute |
| 5-hour | 60 × 5m | requests or tokens |
| Daily | 24 × 1h | requests or tokens |
| Weekly | 7 × 24h | requests or tokens |
| Monthly | 30 × 24h | requests or tokens |

Windows are persisted to `windows.json` every 60s and on shutdown, so budgets survive restarts.

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
main.go                  # entry point (WebView2 window + server bootstrap)
handler/                 # HTTP handlers: /v1/* relay, /api/* management
relay/                   # upstream relay, streaming SSE, format conversion
selector/                # routing engine: key selection, retry, failover
window/                  # sliding-window rate limiters (+ persistence)
health/                  # key health checker with probe backoff
billing/                 # token pricing and consumption records
format/                  # OpenAI ↔ Anthropic request/response/stream conversion
web/                     # React + Ant Design management UI
```

## Security

- The server binds to `127.0.0.1` and rejects cross-origin / non-local requests.
- An optional auth token protects the `/v1/*` forwarding API (management UI stays local-only).
- Health-check probes back off after repeated failures (no surprise provider charges).

## License

**KeyRouter is licensed under the [GNU Affero General Public License v3.0 (AGPL-3.0)](LICENSE).**

If you use a modified version to provide a service over a network, you must make your modified source code available to users of that service under the same license.

---

<div align="center"><sub>Built with Go, Gin, GORM, React, and Ant Design.</sub></div>
