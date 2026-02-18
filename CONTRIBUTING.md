# Contributing to Seebro

## Setup

```bash
git clone https://github.com/ChrisFeldmeier/seebro.git
cd seebro

# Build
go build -o seebro .

# Run (headed — Chrome window opens)
./seebro

# Run headless
BRIDGE_HEADLESS=true ./seebro

# Enable pre-commit hook
git config core.hooksPath .githooks
```

Requires **Go 1.25+** and **Google Chrome**.

## Development Workflow

1. Make your changes
2. Format: `gofmt -w .`
3. Test: `go test ./... -count=1`
4. Lint: `golangci-lint run ./...` (v2.9.0+)
5. Commit — pre-commit hook runs checks automatically
6. Push: `git pull --rebase && git push`

## Running Tests

```bash
# All tests
go test ./... -count=1 -v

# With coverage
go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out
```

Tests don't require a running Chrome instance.

## Project Layout

Single Go package, all files in root:

- `main.go` — entry point, Chrome launch, routes
- `handlers.go` — HTTP handlers (navigate, screenshot, tabs, lock)
- `handler_snapshot.go` — snapshot handler (a11y tree, format, file output)
- `handler_actions.go` — action/actions handlers
- `handler_cookies.go` — cookie get/set
- `handler_stealth.go` — stealth status, fingerprint rotation
- `snapshot.go` — a11y tree parsing
- `cdp.go` — Chrome DevTools Protocol helpers
- `bridge.go` — tab management, Chrome lifecycle
- `config.go` — environment config, embedded assets
- `lock.go` — tab locking for multi-agent coordination
- `animations.go` — CSS animation disabling
- `human.go` — human-like interaction (bezier mouse, typing)
- `state.go` — session persistence
- `middleware.go` — auth, CORS, logging
- `stealth.js` — stealth script (light/full modes)
- `welcome.html` — headed mode welcome page

## Style

- `gofmt` enforced (CI + pre-commit)
- Handle all error returns
- Lowercase error strings, wrap with `%w`
- Tests next to source (`foo.go` → `foo_test.go`)
- No new dependencies without good reason

## For AI Agents

See [AGENTS.md](AGENTS.md) for detailed conventions and patterns.
