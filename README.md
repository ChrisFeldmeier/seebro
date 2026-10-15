<p align="center">
  <img src="assets/seebro-headless.png" alt="Seebro" width="200"/>
</p>

<p align="center">
  <strong>Give your AI agent eyes.</strong><br/>
  ~9 MB binary. HTTP API + MCP server. 10x cheaper than screenshots.<br/><br/>
  🪼 <em>See the web through your agent's eyes</em>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go"/>
  <img src="https://img.shields.io/badge/~9MB_binary-FFD700?style=flat-square" alt="~9MB"/>
  <img src="https://img.shields.io/badge/REST_API-00ff88?style=flat-square" alt="REST"/>
  <img src="https://img.shields.io/badge/MCP-38_tools-7c3aed?style=flat-square" alt="MCP"/>
  <img src="https://img.shields.io/badge/MIT-888?style=flat-square" alt="MIT"/>
</p>

---

## The Problem

Browser automation tools are designed for humans, not agents. Playwright, Puppeteer and Selenium return massive DOM dumps or screenshots that burn through tokens. Most agent-specific solutions are locked into their own ecosystems.

Seebro takes a different approach: **accessibility trees over HTTP and MCP**. One small Go binary controls Chrome. Any agent can drive it with plain HTTP calls, and Claude (or any other MCP client) can use it as a native toolset.

```bash
# Get page content — ~800 tokens instead of 10,000+
curl localhost:9867/text

# Interact with elements using stable refs
curl -X POST localhost:9867/action -d '{"kind":"click","ref":"e5"}'

# Or let Claude drive it directly
claude mcp add seebro -- seebro mcp
```

## Features at a Glance

| Area | What you get |
|---|---|
| **Reading** | Readable page text, accessibility-tree snapshots with stable refs (full, interactive-only, diff, scoped, token-capped), element inspection with attributes, box, HTML and computed styles |
| **Interaction** | Click, double click, click at coordinates, type, fill (React/Vue-safe), whole forms in one call, key presses and combos (`Enter`, `Control+A`), hover, select, scroll, drag & drop (HTML5 + mouse), file upload, human-like mouse and typing |
| **Navigation** | Load URLs, back / forward / reload, wait for text, element, URL or network idle, multiple tabs with locking for multi-agent use |
| **DevTools** | Network log with headers, POST data and response bodies, console messages and uncaught exceptions, JS dialog handling (`alert` / `confirm` / `prompt`) |
| **Emulation** | Mobile viewports and touch, dark mode, reduced motion, geolocation, network and CPU throttling, user agent, extra HTTP headers, timezone, locale |
| **Output** | Screenshots (JPEG/PNG/WebP, viewport, full page or single element), PDF export, downloads (CSV/PDF exports land in a known folder) |
| **Scripting** | Run JavaScript in the page; promises are awaited, so `fetch('/api/…')` runs with the page's own login |
| **Sessions** | Persistent Chrome profile (logins survive restarts), cookie read/write |
| **Stealth** | Hides automation fingerprints, fingerprint rotation |
| **Agent integration** | HTTP API, MCP server (stdio + Streamable HTTP, 38 tools), Claude skill |

## How It Compares

| | Seebro | Typical Browser Tools |
|---|---|---|
| **Token cost** | ~800 (text) / ~3,600 (interactive) | 10,000+ (full DOM/screenshot) |
| **Interface** | HTTP + MCP – works with any agent | Framework-specific |
| **Output format** | Structured a11y tree | Raw HTML or pixels |
| **Bot detection** | Built-in stealth | Manual configuration |
| **Session handling** | Automatic persistence | Usually ephemeral |
| **Deployment** | Single ~9 MB binary | Complex dependencies |

**Bottom line:** 5-13x cheaper for typical browsing tasks, and it works with any agent that can make HTTP calls or speak MCP.

## Binary Size & Footprint

Release builds (`-ldflags "-s -w"`, as produced by goreleaser), measured for this version:

| Platform | Size |
|---|---|
| macOS arm64 | 8.9 MB |
| macOS amd64 | 9.4 MB |
| Linux arm64 | 8.7 MB |
| Linux amd64 | 9.2 MB |
| Windows amd64 | 9.5 MB |

The MCP server and DevTools features add about 0.3 MB to the binary. A plain `go build` without stripping is around 13 MB.

At runtime the Seebro process itself uses about 15 MB of RAM. Chrome runs as a separate process. Network and console logs are kept in fixed-size per-tab buffers (300 requests and 200 messages by default). Response bodies are never copied into Seebro; Chrome holds them (capped at 5 MB per resource and 25 MB per tab) and Seebro fetches them only when you ask. Memory therefore stays flat however long a tab stays open.

## Getting Started

### Option 1: Docker

```bash
docker run -d -p 9867:9867 --security-opt seccomp=unconfined seebro/seebro
curl http://localhost:9867/health
```

### Option 2: Build from Source

```bash
go build -ldflags "-s -w" -o seebro .
BRIDGE_HEADLESS=true ./seebro
```

### Option 3: Let Your Agent Handle It

With [OpenClaw](https://openclaw.ai) or Claude, ask your agent to set up Seebro. The [skill file](skill/seebro/SKILL.md) has everything it needs.

## MCP Server (Claude & other MCP clients)

Seebro has a built-in [Model Context Protocol](https://modelcontextprotocol.io) server. Claude Code, Claude Desktop and any other MCP client can drive the browser through **38 tools**, with no extra install, runtime or dependency. The MCP server lives in the same binary.

### Setup

**Claude Code** (stdio, recommended):

```bash
claude mcp add seebro -- /path/to/seebro mcp
```

**Claude Desktop** (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "seebro": { "command": "/path/to/seebro", "args": ["mcp"] }
  }
}
```

**Any client over HTTP** (Streamable HTTP, needs a running server):

```bash
claude mcp add --transport http seebro http://localhost:9867/mcp \
  --header "Authorization: Bearer $BRIDGE_TOKEN"
```

### How it works

```
┌──────────────┐  stdio   ┌──────────────┐  HTTP POST /mcp  ┌──────────────┐  CDP  ┌────────┐
│ Claude Code  │ ───────► │ seebro mcp   │ ───────────────► │ Seebro server│ ────► │ Chrome │
│ Claude Desk. │ JSON-RPC │ (thin proxy) │                  │  (HTTP API)  │       │        │
└──────────────┘          └──────────────┘                  └──────────────┘       └────────┘
```

- `seebro mcp` is a thin proxy mode of the same binary. It forwards JSON-RPC to `POST /mcp` on the running server.
- If no server is reachable on `localhost`, `seebro mcp` **starts one in the background** (headless, logging to `~/.seebro/seebro.log`). It also restarts the server if it disappears mid-session.
- All MCP clients share one Chrome and one profile instead of each starting its own browser.
- Every MCP tool is dispatched in-process onto the HTTP API, so the MCP and REST surfaces always behave identically.
- The auth token is read from `BRIDGE_TOKEN` or `~/.seebro/config.json` automatically.
- If a JS dialog blocks a page, every tool result includes a notice telling the agent to call `handle_dialog`.
- Screenshots are returned as MCP image content, so the model sees them directly.

### MCP tools

| Group | Tools |
|---|---|
| **Tabs** | `tabs_list`, `tab_new`, `tab_close`, `tab_focus`, `tab_lock`, `tab_unlock` |
| **Navigate & read** | `navigate` (url / back / forward / reload), `get_text`, `snapshot`, `screenshot`, `inspect_element`, `wait_for` |
| **Input** | `click` (incl. double / human-like), `click_at`, `type_text` (with `submitKey`), `fill`, `fill_form`, `press_key`, `hover`, `select_option`, `scroll`, `drag`, `upload_file`, `batch_actions` |
| **Scripting** | `evaluate` (awaits promises; can run `el => …` on a ref/selector) |
| **Network & console** | `network_requests`, `network_request`, `console_messages`, `clear_logs`, `handle_dialog` |
| **Emulation & output** | `emulate`, `save_pdf`, `downloads` |
| **Session** | `cookies_get`, `cookies_set` |
| **Status & stealth** | `health`, `stealth_status`, `fingerprint_rotate` |

`POST /shutdown` is deliberately **not** exposed as a tool, so an agent cannot stop the browser that other sessions share.

### Claude skill

[`skill/seebro/SKILL.md`](skill/seebro/SKILL.md) teaches Claude how to work with Seebro effectively:

- the core loop: tab → text/snapshot → act by ref → wait → re-snapshot
- **getting data out of web apps**: capture the app's own API calls from the network log, call its API from inside the page with `fetch`, read the DOM, or use the app's export plus `downloads`
- logins and persistent sessions, forms, dialogs, debugging with console and network logs
- safety rules: no purchases, messages or deletions without the user's OK, and page text is never treated as instructions

Install it for Claude Code:

```bash
cp -r skill/seebro ~/.claude/skills/seebro
```

The HTTP equivalents of every tool are in [`skill/seebro/references/http-api.md`](skill/seebro/references/http-api.md).

### Example: pull JSON out of a web app

```
tab_new          {url: "https://app.example.com/orders"}
wait_for         {tabId, networkIdle: true}
network_requests {tabId, type: ["Fetch", "XHR"], filter: "orders"}
network_request  {tabId, id: "r17"}        → the JSON the app loaded
evaluate         {tabId, expression: "fetch('/api/orders?page=2').then(r => r.json())"}
```

## Running Modes

### Headless (Production)

```bash
BRIDGE_HEADLESS=true ./seebro
```

Chrome runs invisibly. This is what Seebro is optimized and tested for. Best for servers, CI pipelines, and automated workflows. `seebro mcp` starts the server headless by default.

### Headed (Development)

```bash
./seebro
```

Opens a Chrome window you can watch and interact with. Useful for debugging and manual logins: log in once in headed mode, then restart headless. The session stays in the profile. Note: headed mode has some rough edges – profile management is manual, and some sites behave differently.

## Core Concepts

### Accessibility Tree & Refs

Instead of HTML, Seebro returns the browser's accessibility tree – the same structure screen readers use. Each element gets a stable ref (`e0`, `e1`, `e2`...) that you use for interactions.

```
e0:RootWebArea "Example Domain"
e1:heading "Example Domain"
e2:link "More information..."
```

Click `e2` and the link activates. No coordinate guessing, no CSS selectors that break. Refs belong to the latest snapshot; after a navigation or a large DOM change, take a new one.

### Token Efficiency

Real measurements from a search results page:

| Method | Tokens |
|---|---|
| Full accessibility snapshot | ~10,500 |
| Interactive elements only (`?filter=interactive`) | ~3,600 |
| Text content (`/text`) | ~800 |
| Screenshot (vision model) | ~2,000 |

For a 50-page monitoring task:
- Screenshots: ~$0.30
- Full snapshots: ~$0.16
- Seebro `/text`: ~$0.01

### Session Persistence

Log in once, stay logged in. Seebro maintains a Chrome profile at `~/.seebro/chrome-profile/` that persists cookies, auth tokens, and open tabs across restarts.

### Network & Console Recording

Every tab records its network requests and console output from the moment Seebro attaches to it. Lists default to "since the last navigation" (`all=true` includes earlier ones still in the buffer). Request and response headers are kept; bodies and POST data are fetched from Chrome on demand.

## API Reference

All endpoints accept `tabId`. Pass it always; without it Seebro uses the first tab. GET endpoints take query parameters, POST endpoints a JSON body.

### Reading State

| Endpoint | Purpose |
|----------|---------|
| `GET /health` | Check connection status |
| `GET /tabs` | List open browser tabs (with lock owner and open dialog) |
| `GET /snapshot` | Get accessibility tree |
| `GET /text` | Extract readable content (`mode=raw` for full `innerText`) |
| `GET /screenshot` | JPEG/PNG/WebP image: viewport, `fullPage=true`, or one element via `ref`/`selector` |
| `GET /element` | Attributes, text, value, box, visibility, `html=true`, `styles=true\|all` |
| `GET /cookies` | Retrieve cookies |
| `GET /stealth/status` | Check stealth configuration |

### Taking Actions

| Endpoint | Purpose |
|----------|---------|
| `POST /navigate` | Load a URL, or `"type": "back" \| "forward" \| "reload"` |
| `POST /action` | Single action (kinds below) |
| `POST /actions` | Batch multiple actions (`stopOnError`) |
| `POST /evaluate` | Run JavaScript; promises are awaited; with `ref`/`selector` runs `el => …` |
| `POST /wait` | Wait for `text` (one or many), `selector` (or `gone`), `url`, `networkIdle`; 408 on timeout |
| `POST /tab` | `new`, `close` or `focus` a tab |
| `POST /tab/lock` | Reserve tab for exclusive use |
| `POST /tab/unlock` | Release tab reservation |
| `POST /cookies` | Set cookies |
| `POST /fingerprint/rotate` | Change browser fingerprint |
| `POST /shutdown` | Graceful shutdown |

**Action kinds** for `/action` and `/actions`:

| Kind | Does |
|---|---|
| `click`, `dblclick` | Click / double-click by `ref` or `selector` |
| `clickAt` | Click at `x`/`y` viewport coordinates (`count` for multi-click) |
| `type` | Focus and send real key events (`text`) |
| `fill` | Set value of input, textarea, select, checkbox/radio or contenteditable and fire input/change events |
| `press` | Key names and combos: `Enter`, `Tab`, `Escape`, `ArrowDown`, `Control+A`, `Meta+Shift+K` |
| `focus`, `hover`, `select`, `scroll` | As named (`scrollX`/`scrollY` for page scroll) |
| `drag` | From `ref`/`selector` to `toRef`/`toSelector` or `x`/`y` (HTML5 drag & drop and mouse-based) |
| `upload` | Set `files` on an `<input type=file>` |
| `humanClick`, `humanType` | Human-like mouse path / typing rhythm |

### DevTools

| Endpoint | Purpose |
|----------|---------|
| `GET /network` | Requests since the last navigation; filter by `type`, `filter` (URL), `method`, `status` (`2xx`, `4xx`, `failed`, `404`); `headers=true`, `limit`, `offset`, `all` |
| `GET /network/request` | One request by `id`: headers, POST data, response body (`maxBytes`, `saveTo`, `base64`) |
| `POST /network/clear` | Reset a tab's network log |
| `GET /console` | Console messages, uncaught exceptions, browser log (`level`, `limit`, `all`) |
| `POST /console/clear` | Reset a tab's console log |
| `GET /dialog` | Currently open JS dialog |
| `POST /dialog` | `accept` / `dismiss` it (`promptText` for `prompt()`) |
| `POST /emulate` | `viewport` (`390x844x3,mobile,touch`), `userAgent`, `colorScheme`, `reducedMotion`, `geolocation`, `network` (`Slow 3G` … `Offline`), `cpuThrottling`, `extraHeaders`, `timezone`, `locale` |
| `GET /pdf` | Print page to PDF (`path`, `landscape`, `printBackground`) |
| `GET /downloads` | Downloads with progress and files in the download folder |
| `POST /mcp` | MCP endpoint (Streamable HTTP, JSON-RPC) |

Actions and `/evaluate` return immediately with a `dialog` field when an `alert`/`confirm` blocks the page, instead of hanging until the timeout.

### Snapshot Options

| Parameter | Effect |
|-----------|--------|
| `filter=interactive` | Only buttons, links, inputs (~75% fewer tokens) |
| `format=compact` | Minimal one-line-per-node output |
| `format=text` | Indented tree format |
| `format=yaml` | YAML output |
| `diff=true` | Only changed elements since last call |
| `depth=N` | Limit tree depth |
| `selector=CSS` | Scope to element subtree |
| `maxTokens=N` | Truncate to token budget |
| `output=file` | Save to disk (`path=` for a custom location) |

## Configuration

Environment variables control behavior (a config file at `~/.seebro/config.json` is also supported, see `seebro config init`):

| Variable | Default | Purpose |
|----------|---------|---------|
| `BRIDGE_PORT` | `9867` | Server port |
| `BRIDGE_TOKEN` | – | Auth token (set this in production!) |
| `BRIDGE_HEADLESS` | `false` | Run without visible window (`seebro mcp` autostart: `true`) |
| `BRIDGE_PROFILE` | `~/.seebro/chrome-profile` | Browser profile location |
| `BRIDGE_STATE_DIR` | `~/.seebro` | Logs, screenshots, PDFs, saved state |
| `BRIDGE_STEALTH` | `light` | Stealth level: `light` or `full` |
| `BRIDGE_BLOCK_IMAGES` | `false` | Skip image loading |
| `BRIDGE_BLOCK_MEDIA` | `false` | Skip images, fonts, CSS and media |
| `BRIDGE_NO_ANIMATIONS` | `false` | Freeze CSS animations |
| `BRIDGE_NO_RESTORE` | `false` | Don't reopen the previous session's tabs |
| `BRIDGE_TIMEZONE` | – | Timezone override for all tabs |
| `BRIDGE_DOWNLOAD_DIR` | `~/.seebro/downloads` | Where downloads are saved |
| `BRIDGE_DIALOG` | – | `accept` / `dismiss` to auto-answer JS dialogs (default: leave open for `/dialog`) |
| `BRIDGE_NETWORK_BUFFER` | `300` | Network requests kept per tab |
| `BRIDGE_CONSOLE_BUFFER` | `200` | Console messages kept per tab |
| `CDP_URL` | – | Connect to existing Chrome instance |
| `CHROME_BINARY` | – | Path to Chrome/Chromium |
| `CHROME_FLAGS` | – | Extra Chrome flags (space-separated) |
| `SEEBRO_URL` | `http://127.0.0.1:$BRIDGE_PORT` | Server used by `seebro mcp` |
| `SEEBRO_NO_AUTOSTART` | – | `true` stops `seebro mcp` from launching a server |

Full list in the [configuration docs](docs/ARCHITECTURE.md).

### Commands

| Command | Purpose |
|---|---|
| `seebro` | Start the server (HTTP API + `/mcp`) |
| `seebro mcp` | MCP over stdio (proxies to the server, starts it if needed) |
| `seebro config init` / `seebro config show` | Create / show the config file |
| `seebro --version` | Print version |

## Stealth Mode

Seebro patches common automation fingerprints:

- Removes `navigator.webdriver` flag
- Spoofs realistic plugins and languages
- Hides CDP artifacts
- Optional: canvas noise, WebGL vendor spoofing, font metric fuzzing

`light` mode (default) handles most sites. `full` mode adds fingerprint randomization but can break some pages.

## Architecture

```
┌─────────────┐  HTTP / MCP      ┌──────────┐                  ┌─────────┐
│   Agent     │  ── JSON ───►    │  Seebro  │  ── CDP ───►     │ Chrome  │
│  (any kind) │   req/res        │  (Go)    │   DevTools       │         │
└─────────────┘                  └──────────┘                  └─────────┘
```

Seebro is a translation layer. Agents speak HTTP or MCP, Chrome speaks CDP. Seebro handles the conversion, adds stealth, records network and console activity, manages sessions, and compresses output to save tokens.

## Security Notice

**Seebro gives agents real browser access with your real accounts.**

When you log into sites, those sessions persist. Any agent with Seebro access can act as you – read messages, make purchases, access sensitive data. The network log can contain auth headers and the cookies tools expose session cookies.

Essential precautions:
- **Set `BRIDGE_TOKEN`** – without it, anyone on your network has access (this includes `/mcp`)
- **Treat `~/.seebro/` as sensitive** – it contains your browser profile and downloads
- **Start with test accounts** – don't connect production accounts until you trust your agent
- **Firewall appropriately** – Seebro binds to all interfaces by default

Think of it as handing someone an unlocked laptop. Powerful when intentional. Dangerous when careless.

## Dependencies

| Package | Purpose | License |
|---------|---------|---------|
| [chromedp](https://github.com/chromedp/chromedp) | CDP driver | MIT |
| [cdproto](https://github.com/chromedp/cdproto) | CDP types | MIT |
| [gobwas/ws](https://github.com/gobwas/ws) | WebSocket | MIT |

Everything else, including the MCP server, is Go standard library.

## Requirements

- Go 1.25+ (or download a [release binary](https://github.com/ChrisFeldmeier/seebro/releases))
- Chrome or Chromium

## Development

```bash
git clone https://github.com/ChrisFeldmeier/seebro.git
cd seebro
go build -o seebro .
go test ./...                       # unit tests, no Chrome needed
go test -tags integration ./...     # with a real headless Chrome
```

## Contributors

<a href="https://github.com/ChrisFeldmeier">
  <img src="https://github.com/ChrisFeldmeier.png" width="60" style="border-radius:50%" alt="Chris Feldmeier"/><br/>
  <sub><b>Chris Feldmeier</b> · Creator</sub>
</a>

## OpenClaw Integration

Seebro works natively with [OpenClaw](https://openclaw.ai), the open-source AI assistant. Use it as your agent's browser backend for efficient, low-cost web automation.



---

<p align="center">
  <a href="https://star-history.com/#seebro/seebro&Date">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=seebro/seebro&type=Date&theme=dark" />
      <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/svg?repos=seebro/seebro&type=Date" />
      <img alt="Star History" src="https://api.star-history.com/svg?repos=seebro/seebro&type=Date" width="600"/>
    </picture>
  </a>
</p>
