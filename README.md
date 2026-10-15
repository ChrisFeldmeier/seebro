<p align="center">
  <img src="assets/seebro-headless.png" alt="Seebro" width="200"/>
</p>

<p align="center">
  <strong>Give your AI agent eyes.</strong><br/>
  12MB binary. HTTP API. 10x cheaper than screenshots.<br/><br/>
  🪼 <em>See the web through your agent's eyes</em>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go"/>
  <img src="https://img.shields.io/badge/12MB_binary-FFD700?style=flat-square" alt="12MB"/>
  <img src="https://img.shields.io/badge/REST_API-00ff88?style=flat-square" alt="REST"/>
  <img src="https://img.shields.io/badge/MIT-888?style=flat-square" alt="MIT"/>
</p>

---

## The Problem

Browser automation tools are designed for humans, not agents. Playwright, Puppeteer, Selenium – they all return massive DOM dumps or screenshots that burn through tokens. And most agent-specific solutions are locked into their own ecosystems.

Seebro takes a different approach: **accessibility trees over HTTP**.

```bash
# Get page content — ~800 tokens instead of 10,000+
curl localhost:9867/text

# Interact with elements using stable refs
curl -X POST localhost:9867/action -d '{"kind":"click","ref":"e5"}'
```

## How It Compares

| | Seebro | Typical Browser Tools |
|---|---|---|
| **Token cost** | ~800 (text) / ~3,600 (interactive) | 10,000+ (full DOM/screenshot) |
| **Interface** | HTTP – works with any agent | Framework-specific |
| **Output format** | Structured a11y tree | Raw HTML or pixels |
| **Bot detection** | Built-in stealth | Manual configuration |
| **Session handling** | Automatic persistence | Usually ephemeral |
| **Deployment** | Single 12MB binary | Complex dependencies |

**Bottom line:** 5-13x cheaper for typical browsing tasks, and it works with any agent that can make HTTP calls.

## Getting Started

### Option 1: Docker

```bash
docker run -d -p 9867:9867 --security-opt seccomp=unconfined seebro/seebro
curl http://localhost:9867/health
```

### Option 2: Build from Source

```bash
go build -o seebro .
BRIDGE_HEADLESS=true ./seebro
```

### Option 3: Let Your Agent Handle It

If you're using [OpenClaw](https://openclaw.ai), just ask your agent to set up Seebro. The [skill file](skill/seebro/SKILL.md) has everything it needs.

## Use with Claude (MCP)

Seebro includes an MCP server, so Claude Code, Claude Desktop and any other MCP client can drive the browser through 38 tools. These cover tabs, navigation, snapshots, clicks and forms, network and console inspection, dialogs, emulation, PDF, downloads and cookies.

```bash
# Claude Code — stdio; starts a headless Seebro in the background if none is running
claude mcp add seebro -- /path/to/seebro mcp

# or connect to an already running server over HTTP
claude mcp add --transport http seebro http://localhost:9867/mcp --header "Authorization: Bearer $BRIDGE_TOKEN"
```

Claude Desktop (`claude_desktop_config.json`):

```json
{ "mcpServers": { "seebro": { "command": "/path/to/seebro", "args": ["mcp"] } } }
```

`seebro mcp` is a small proxy (a few MB). It forwards JSON-RPC to `POST /mcp` on the running server, so all clients share one Chrome. The token comes from `BRIDGE_TOKEN` or `~/.seebro/config.json`. The Claude skill in [`skill/seebro`](skill/seebro/SKILL.md) teaches the agent how to work with Seebro, especially how to get data out of web apps. Copy it to `~/.claude/skills/seebro` to install it.

## Running Modes

### Headless (Production)

```bash
BRIDGE_HEADLESS=true ./seebro
```

Chrome runs invisibly. This is what Seebro is optimized and tested for. Best for servers, CI pipelines, and automated workflows.

### Headed (Development)

```bash
./seebro
```

Opens a Chrome window you can watch and interact with. Useful for debugging and manual logins. Note: headed mode has some rough edges – profile management is manual, and some sites behave differently.

## Core Concepts

### Accessibility Tree & Refs

Instead of HTML, Seebro returns the browser's accessibility tree – the same structure screen readers use. Each element gets a stable ref (`e0`, `e1`, `e2`...) that you use for interactions.

```
e0:RootWebArea "Example Domain"
e1:heading "Example Domain"
e2:link "More information..."
```

Click `e2` and the link activates. No coordinate guessing, no CSS selectors that break.

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

## API Reference

### Reading State

| Endpoint | Purpose |
|----------|---------|
| `GET /health` | Check connection status |
| `GET /tabs` | List open browser tabs |
| `GET /snapshot` | Get accessibility tree |
| `GET /text` | Extract readable content |
| `GET /screenshot` | Capture JPEG image |
| `GET /cookies` | Retrieve cookies |
| `GET /stealth/status` | Check stealth configuration |

### Taking Actions

| Endpoint | Purpose |
|----------|---------|
| `POST /navigate` | Load a URL |
| `POST /action` | Click, type, scroll, etc. |
| `POST /actions` | Batch multiple actions |
| `POST /evaluate` | Run JavaScript |
| `POST /tab` | Create or close tabs |
| `POST /tab/lock` | Reserve tab for exclusive use |
| `POST /tab/unlock` | Release tab reservation |
| `POST /cookies` | Set cookies |
| `POST /fingerprint/rotate` | Change browser fingerprint |
| `POST /shutdown` | Graceful shutdown |

### DevTools

| Endpoint | Purpose |
|----------|---------|
| `GET /network` | Requests since the last navigation (filter by `type`, `filter`, `method`, `status`) |
| `GET /network/request` | Headers, POST data and response body of one request (`id`) |
| `GET /console` | Console messages, uncaught exceptions, browser log |
| `POST /network/clear`, `POST /console/clear` | Reset the recorded logs of a tab |
| `GET /dialog`, `POST /dialog` | Inspect / accept / dismiss `alert`, `confirm`, `prompt` |
| `POST /wait` | Wait for text, selector (or its absence), URL or network idle |
| `POST /emulate` | Viewport/device, user agent, color scheme, geolocation, network + CPU throttling, headers, timezone, locale |
| `GET /element` | Attributes, text, box, outerHTML and computed styles of one element |
| `GET /pdf` | Print page to PDF |
| `GET /downloads` | Downloaded files (saved to `~/.seebro/downloads`) |
| `POST /mcp` | MCP endpoint (Streamable HTTP) |

Network and console data live in fixed-size per-tab buffers (300 requests and 200 messages by default). Response bodies are never copied, and Chrome serves them on request, so memory stays flat however long a tab stays open.

`/navigate` also takes `"type": "back" | "forward" | "reload"`. `/screenshot` takes `format=png|webp`, `fullPage=true`, or `ref`/`selector` to capture a single element. `/evaluate` awaits promises, and with `ref`/`selector` it runs `el => …` on that element. Action kinds now include `dblclick`, `clickAt`, `drag` and `upload`. `fill` fires input/change events, and `press` understands key names and combos such as `Enter` or `Control+A`.

### Snapshot Options

| Parameter | Effect |
|-----------|--------|
| `filter=interactive` | Only buttons, links, inputs (~75% fewer tokens) |
| `format=compact` | Minimal one-line-per-node output |
| `format=text` | Indented tree format |
| `diff=true` | Only changed elements since last call |
| `depth=N` | Limit tree depth |
| `selector=CSS` | Scope to element subtree |
| `maxTokens=N` | Truncate to token budget |

## Configuration

Environment variables control behavior:

| Variable | Default | Purpose |
|----------|---------|---------|
| `BRIDGE_PORT` | `9867` | Server port |
| `BRIDGE_TOKEN` | – | Auth token (set this in production!) |
| `BRIDGE_HEADLESS` | `false` | Run without visible window |
| `BRIDGE_PROFILE` | `~/.seebro/chrome-profile` | Browser profile location |
| `BRIDGE_STEALTH` | `light` | Stealth level: `light` or `full` |
| `BRIDGE_BLOCK_IMAGES` | `false` | Skip image loading |
| `BRIDGE_NO_ANIMATIONS` | `false` | Freeze CSS animations |
| `CDP_URL` | – | Connect to existing Chrome instance |
| `BRIDGE_DOWNLOAD_DIR` | `~/.seebro/downloads` | Where downloads are saved |
| `BRIDGE_DIALOG` | – | `accept` / `dismiss` to auto-answer JS dialogs (default: leave open for `/dialog`) |
| `BRIDGE_NETWORK_BUFFER` | `300` | Network requests kept per tab |
| `BRIDGE_CONSOLE_BUFFER` | `200` | Console messages kept per tab |
| `SEEBRO_URL` | `http://127.0.0.1:$BRIDGE_PORT` | Server used by `seebro mcp` |
| `SEEBRO_NO_AUTOSTART` | – | `true` stops `seebro mcp` from launching a server |

Full list in the [configuration docs](docs/ARCHITECTURE.md).

## Stealth Mode

Seebro patches common automation fingerprints:

- Removes `navigator.webdriver` flag
- Spoofs realistic plugins and languages
- Hides CDP artifacts
- Optional: canvas noise, WebGL vendor spoofing, font metric fuzzing

`light` mode (default) handles most sites. `full` mode adds fingerprint randomization but can break some pages.

## Architecture

```
┌─────────────┐                  ┌──────────┐                  ┌─────────┐
│   Agent     │  ── HTTP ───►    │  Seebro  │  ── CDP ───►     │ Chrome  │
│  (any kind) │   JSON req/res   │  (Go)    │   DevTools       │         │
└─────────────┘                  └──────────┘                  └─────────┘
```

Seebro is a translation layer. Agents speak HTTP, Chrome speaks CDP. Seebro handles the conversion, adds stealth, manages sessions, and compresses output to save tokens.

## Security Notice

**Seebro gives agents real browser access with your real accounts.**

When you log into sites, those sessions persist. Any agent with Seebro access can act as you – read messages, make purchases, access sensitive data.

Essential precautions:
- **Set `BRIDGE_TOKEN`** – without it, anyone on your network has access
- **Treat `~/.seebro/` as sensitive** – it contains your browser profile
- **Start with test accounts** – don't connect production accounts until you trust your agent
- **Firewall appropriately** – Seebro binds to all interfaces by default

Think of it as handing someone an unlocked laptop. Powerful when intentional. Dangerous when careless.

## Dependencies

| Package | Purpose | License |
|---------|---------|---------|
| [chromedp](https://github.com/chromedp/chromedp) | CDP driver | MIT |
| [cdproto](https://github.com/chromedp/cdproto) | CDP types | MIT |
| [gobwas/ws](https://github.com/gobwas/ws) | WebSocket | MIT |

Everything else is Go standard library.

## Requirements

- Go 1.25+ (or download a [release binary](https://github.com/ChrisFeldmeier/seebro/releases))
- Chrome or Chromium

## Development

```bash
git clone https://github.com/ChrisFeldmeier/seebro.git
cd seebro
go build -o seebro .
go test ./...
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
