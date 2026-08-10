# Contributing to KeyRouter

Thanks for your interest in contributing! KeyRouter is a small, focused project — here's how to help.

## Development setup

```bash
# Clone and build
git clone https://github.com/JohnXu22786/key-router.git
cd key-router
go build ./...          # requires web/dist (committed) — builds out of the box

# Web UI development
cd web
npm install
npm run dev             # vite dev server with HMR
npm run build           # production build → web/dist (commit the result)
```

Run the app: `go run .` (opens the desktop window; `KEYROUTER_DATA=/tmp/lr go run .` for an isolated data dir).

## Before you open a PR

1. **Build & vet & test**
   ```bash
   go build ./... && go vet ./... && go test ./...
   ```
2. **Format** — `gofmt -l .` must be empty. Frontend: `cd web && npm run build` must pass.
3. **Keep the embedded UI in sync** — if you changed `web/src`, run `npm run build` and commit `web/dist` (the CI enforces this).
4. **Write tests** for new behavior. Prefer table tests; the existing `format` and `window` packages show the house style.

## Pull request process

- CI runs on every PR (Go build/vet/test/format + web build) — a green CI is required.
- Keep PRs small and focused. One logical change per PR.
- Update the README if user-facing behavior changed.
- The maintainer will review and merge. For significant changes, expect a
  design discussion first — open an issue to propose the change before writing code.

## Project conventions

- Go 1.26+, standard library first, Gin + GORM.
- Package layout: `handler/` (HTTP), `relay/` (upstream + streaming), `selector/`
  (routing/retry), `window/` (rate limiters), `billing/`, `format/` (conversion), `health/`.
- Error paths must fail closed; the gateway deals with money and API keys.

## License

By contributing, you agree that your contributions are licensed under the
[AGPL-3.0](LICENSE) license of this project.
