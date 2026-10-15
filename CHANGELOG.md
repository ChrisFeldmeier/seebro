# Changelog

## seebro-speed (unreleased)

### Second engine: obscura
- `BRIDGE_ENGINE=chrome|obscura|auto`; obscura runs as a managed sidecar (`obscura serve`), no Chrome needed
- Hybrid mode: Chrome primary, `{"engine":"obscura"}` on `/tab` / `/navigate` / MCP `tab_new` opens tabs in obscura (started on first use)
- `/health` and `/tabs` report engines; isolated contexts are refused on obscura with a clear error
- obscura-specific paths: single-round-trip main-world snapshot in lite mode, structural text walker, no frame/box-model calls, in-page download of `<a download>` links
- `scripts/install-obscura.sh` (release download or build from source)

### Screen recording
- `POST /screencast/start` / `/screencast/stop` and MCP `screencast_start` / `screencast_stop`: frames from `Page.startScreencast` with arrival times, encoded to a real-time MP4 with ffmpeg (libx264, mpeg4 fallback)
- Clicks and typing are outlined and labelled in the page while a tab records
- `scripts/demo-wikipedia.py`: automated Wikipedia tour that produces a video

### Goal navigation (Jev)
- `POST /goal` and MCP `navigate_goal`: a whole multi-step navigation in one call. Each step: Seebro snapshot → action space (`click_`, `search_`, `type_`/`submit_`, `select_`, scroll/back/done) → one decider call (action, goal reached, stuck) → stop gates before acting → execute, settle, record the outcome
- Deciders: Jev via TypeSafe (`TYPESAFE_API_KEY`), OpenRouter (`OPENROUTER_API_KEY`) or the Vercel AI Gateway (`AI_GATEWAY_API_KEY`, evaluation-model API; the gateway's error message is passed through) with retry/backoff and answer validation; `local` offline keyword matcher
- `decider: "auto"` (default): Jev when a key is configured, the offline matcher otherwise; a failed Jev call (network, rate limit, credits, bad answer) falls back to the matcher for that step; each step reports `decided_by` and `fallback_reason`
- Relevance-aware element cap (task matches survive the 240-option limit), next-best recovery for repeated no-op actions, dropdown second-stage question, typing only from `query`/`inputs`
- Search boxes: real key events, falling back to form submission for fields a collapsed header hides
- Cloudflare challenge/block detection stops the run with evidence and guidance
- Optional recording of the run (`record: true`)
- Ported from jev-browser (MIT, © Joey Kudish); benchmark: `/goal` 1.1–1.3× faster than jev-browser with identical decisions

### Fixes found on the way
- `wait_for` with only a selector always timed out (empty text list encoded as `null`)
- Snapshot engine and text extraction no longer rely on iterable DOM collections or `selectedOptions`; a single-select without explicit selection reports its first option
- HTML5 drag works without a native `DataTransfer`/`DragEvent`
- `scripts/loadtest.py`: load test (small, large, click-through shop flow, mixed, real sites) with a generated local test site

## Unreleased

### Performance
- In-page snapshot engine (isolated world) for interactive snapshots: Wikipedia 1,248 ms → 77 ms, GitHub 251 ms → 18 ms; `viewport=true` cuts output to what is on screen (83 KB → 2.6 KB)
- Stable refs: `e12` keeps pointing at the same element across snapshots; refs from a previous page fail as "stale" instead of hitting another element
- Typing uses `Input.insertText` (300 chars: 495 ms → 7 ms); navigation no longer waits 2 s for a missing `<title>`; `waitUntil: load|networkidle`
- `wait_for` reacts instantly via MutationObserver
- Text and snapshot read URL/title in the same round trip

### New
- iframes, including cross-origin ones, in snapshots, text and actions (refs `f1e3`)
- `/text` modes `markdown` (headings, lists, links, tables) and paging (`maxChars`, `offset`, `nextOffset`); text is read from the live DOM (line breaks preserved)
- WebSocket/SSE capture (`/network/websocket`), HAR export (`/network/har`), performance metrics and Web Vitals (`/performance`)
- Isolated browser contexts (`"context": "name"` on new tabs, `/contexts`, `/context`)
- Tab health: hung/crashed tabs are detected (504 with instructions), `POST /tab {"action":"reset"}`; idle tabs close after 30 min headless (`BRIDGE_TAB_IDLE`)
- Tab locks are enforced (423) by owner (`X-Seebro-Owner`, sent per session by `seebro mcp`)
- Screenshots: `maxDim` scaling (MCP default 1568 px), full page / tall elements without `captureBeyondViewport`
- MCP: `tab_reset`, `contexts`, `websocket_messages`, `export_har`, `performance_metrics` (43 tools)

### Security
- Listens on 127.0.0.1 by default (`BRIDGE_BIND`); a token is generated and required when binding elsewhere
- Requests from web pages (Origin / Sec-Fetch-Site) and foreign Host headers are rejected (CSRF, DNS rebinding); CORS only for `BRIDGE_ALLOWED_ORIGINS`, never `*`
- Output paths may not write into hidden files/folders outside the state dir

### Stealth
- `navigator.webdriver` is `false` via `--disable-blink-features=AutomationControlled`
- User agent and client hints without "HeadlessChrome" (headless)
- Removed detectable overrides (fake plugins, platform, webdriver=undefined); injected script has no globals and native-looking `toString()`
- `/stealth/status` runs real checks instead of reporting fixed values; bot.sannysoft.com passes completely

### Fixes
- Full-page screenshots of long pages wedged the tab forever (Chrome 155)
- `/text` glued lines together (text of a detached DOM clone)
- Isolated worlds are bound to the document; context ids repeat after cross-site navigation
- Restored tabs ran without the stealth script; restore is now off in headless mode (`BRIDGE_RESTORE`)

### Changed
- Accessibility-tree refs (`filter=all`) are now `a0, a1 …`; `e…` refs come from the snapshot engine
- Chrome starts without site isolation (`BRIDGE_SITE_ISOLATION=true` to keep it) and with background services disabled (`BRIDGE_LEAN=false` to keep them)
- Successful requests are logged at debug level (`BRIDGE_LOG_LEVEL`); `seebro mcp` rotates its log at 10 MB
- Calls without `tabId` use the last tab used instead of the first one
- CI on Node 24 actions; goreleaser publishes to ChrisFeldmeier/seebro

### MCP server (first part)
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
