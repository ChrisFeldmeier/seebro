# Changelog

## Unreleased

### MCP server
- `seebro mcp` — stdio MCP server for Claude Code / Claude Desktop; proxies to the running server and starts it headless if needed
- `POST /mcp` — Streamable HTTP MCP endpoint on the normal port (same auth)
- 38 tools covering the whole HTTP API (shutdown excluded); screenshots are returned as images, open JS dialogs are reported on every call
- Claude skill in `skill/seebro` (workflows for reading, forms, logins and data extraction from web apps) plus HTTP reference

### DevTools features
- Network log per tab: `/network`, `/network/request` (headers, POST data, response body on demand), `/network/clear`
- Console log per tab (console API, uncaught exceptions, browser log): `/console`, `/console/clear`
- JS dialogs: `/dialog` (inspect, accept, dismiss, prompt text); actions and evaluate return immediately when a dialog blocks the page; `BRIDGE_DIALOG` auto policy
- `/wait` for text, selector (or absence), URL, network idle
- `/emulate`: viewport/device, user agent, color scheme, reduced motion, geolocation, network and CPU throttling, extra headers, timezone, locale
- `/element`, `/pdf`, `/downloads`; `/navigate` back/forward/reload; `/tab` focus
- Screenshots: PNG/WebP, full page, single element
- `/evaluate` awaits promises and can run a function on a ref/selector
- New actions: `dblclick`, `clickAt`, `drag` (HTML5 + mouse), `upload`

### Fixes
- `fill` works with refs and fires input/change events (React/Vue forms no longer submit empty values)
- `press` understands key names and combos (`Enter`, `ArrowDown`, `Control+A`) instead of typing them as text
- Selector-based actions no longer hang on pages restored from the back/forward cache
- `humanClick` / `humanType` with refs used the wrong node ID type
- Startup health check sends the auth token

## v1.0.0

### Core Features
- **HTTP API** — 18 REST endpoints for browser automation
- **Accessibility tree snapshots** — stable refs (`e0`, `e1`...) for reliable element interaction
- **Chrome DevTools Protocol bridge** — single 12MB Go binary controls Chrome
- **Tab management** — create, close, list, lock tabs for multi-agent coordination
- **Session persistence** — cookies, auth, tabs survive restarts
- **Stealth mode** — bypasses bot detection (light/full modes)

### API Endpoints
- `/health`, `/tabs`, `/snapshot`, `/screenshot`, `/text`, `/cookies`, `/stealth/status`
- `/navigate`, `/action`, `/actions`, `/evaluate`, `/tab`, `/tab/lock`, `/tab/unlock`
- `/cookies` (POST), `/fingerprint/rotate`, `/shutdown`, `/welcome`

### Snapshot Features
- `?filter=interactive` — only buttons, links, inputs (~75% fewer tokens)
- `?format=compact` — minimal one-line-per-node output
- `?format=text` — indented tree format
- `?diff=true` — only changed elements since last call
- `?depth=N` — limit tree depth
- `?selector=CSS` — scope to element subtree
- `?maxTokens=N` — truncate to token budget
- `?noAnimations=true` — disable CSS animations before capture
- `?output=file` — save to disk with optional custom path

### Actions
- click, type, fill, press, focus, hover, select, scroll
- humanClick, humanType — bezier mouse movement, natural typing with typos

### Stealth Suite
- `navigator.webdriver` hidden
- Realistic plugins, languages, platform
- WebGL vendor spoofing
- Canvas fingerprint noise
- Font metric fuzzing
- WebRTC IP leak prevention
- Timezone spoofing
- CDP-level User-Agent override

### Configuration
- `BRIDGE_PORT`, `BRIDGE_TOKEN`, `BRIDGE_HEADLESS`
- `BRIDGE_PROFILE`, `BRIDGE_STATE_DIR`, `BRIDGE_STEALTH`
- `BRIDGE_BLOCK_IMAGES`, `BRIDGE_BLOCK_MEDIA`, `BRIDGE_NO_ANIMATIONS`
- `CHROME_BINARY`, `CHROME_FLAGS`, `CDP_URL`
- JSON config file support (`~/.seebro/config.json`)

### Infrastructure
- Docker support with multi-stage build
- systemd/launchd auto-start scripts
- GitHub Actions CI/CD (build, test, lint, release)
- GoReleaser for cross-platform binaries
- 100+ tests with 36%+ coverage
