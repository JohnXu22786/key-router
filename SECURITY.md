# Security Policy

## Reporting a Vulnerability

This project stores API keys for LLM providers (OpenAI, Anthropic, etc.) locally in SQLite. A vulnerability in LocalRouter could expose those keys, so please treat security issues seriously.

**Do not open a public issue for a security vulnerability.** Instead, report it privately by emailing the maintainers (see the repository's About / profile for contact details), or open a [private security advisory](https://github.com/JohnXu22786/key-router/security/advisories/new) if available.

Please include:

- A description of the vulnerability and its impact
- Steps to reproduce (or a minimal proof of concept)
- Any suggested fix, if you have one

We aim to acknowledge reports within 5 business days and to ship a fix as soon as possible.

## Security Model

- The HTTP server binds to `127.0.0.1` only; it is not exposed to the LAN.
- Cross-origin requests and non-local Host/Origin headers are rejected.
- The management API (`/api/*`) is intentionally unauthenticated because it
  only listens on localhost — keep it that way (do not add a reverse proxy
  that exposes it).
- The optional auth token protects only the `/v1/*` forwarding API.

## Supported Versions

| Version | Supported |
|---------|-----------|
| latest release | ✅ |
| older releases | ❌ |

## Reporting

**Contact:** file a private security advisory via the GitHub UI.
