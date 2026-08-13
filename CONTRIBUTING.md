# Contributing to KeyRouter

Thanks for your interest in contributing! KeyRouter is a small, focused project — here's how to help.

## Development setup

```bash
# Clone and build
git clone https://github.com/JohnXu22786/key-router.git
cd key-router
cd web && npm install && npm run build && cd ..   # generates web/dist (not committed)
go build ./...          # embeds web/dist via //go:embed — build the web first

# Web UI development
cd web
npm install
npm run dev             # vite dev server with HMR
npm run build           # production build → web/dist (gitignored, regenerated on demand)
```

Run the app: `go run .` (opens the desktop window; `KEYROUTER_DATA=/tmp/lr go run .` for an isolated data dir).

## Before you open a PR

1. **Build & vet & test** (build the web UI first — `go build` fails without it)
   ```bash
   cd web && npm install && npm run build && cd ..
   go build ./... && go vet ./... && go test ./...
   ```
2. **Format** — `gofmt -l .` must be empty. Frontend: `cd web && npm run build` must pass.
3. **Build the web UI before compiling Go** — `web/dist` is gitignored build output (not committed, to avoid merge conflicts from content-hashed filenames); `//go:embed web/dist/*` fails to compile without it. CI builds the web UI itself.
4. **Write tests** for new behavior. Prefer table tests; the existing `format` and `window` packages show the house style.

## Pull request process

- CI runs on every PR (Go build/vet/test/format + web build) — a green CI is required.
- Keep PRs small and focused. One logical change per PR.
- Update the README if user-facing behavior changed.
- In-flight branches that modified `web/dist` (from before it was gitignored):
  on rebase, resolve the modify/delete conflict with `git rm -r web/dist`,
  then run `npm run build`.
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
