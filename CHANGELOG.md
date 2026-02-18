# Changelog

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
