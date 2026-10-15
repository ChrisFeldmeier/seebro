<p align="center">
  <img src="assets/seebro-headless.png" alt="Seebro" width="200"/>
</p>

<p align="center">
  <strong>Give your AI agent eyes.</strong><br/>
  ~9 MB binary. HTTP API + MCP server. 10x cheaper than screenshots.<br/>
  Whole navigations in one call, steered by a tiny decision model.<br/><br/>
  🪼 <em>See the web through your agent's eyes</em>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go"/>
  <img src="https://img.shields.io/badge/~9MB_binary-FFD700?style=flat-square" alt="~9MB"/>
  <img src="https://img.shields.io/badge/REST_API-00ff88?style=flat-square" alt="REST"/>
  <img src="https://img.shields.io/badge/MCP-46_tools-7c3aed?style=flat-square" alt="MCP"/>
  <img src="https://img.shields.io/badge/MIT-888?style=flat-square" alt="MIT"/>
</p>

---

## The Problem

Browser automation tools are designed for humans, not agents. Playwright, Puppeteer and Selenium return massive DOM dumps or screenshots that burn through tokens. Most agent-specific solutions are locked into their own ecosystems.

Seebro takes a different approach: **compact text and element refs over HTTP and MCP**. One small Go binary controls Chrome (or the lightweight obscura engine). Any agent can drive it with plain HTTP calls, and Claude (or any other MCP client) can use it as a native toolset. For plain navigation the agent doesn't even have to steer step by step: `/goal` hands "get me to the pricing page" to a small decision model and returns when it's there.

```bash
# Interactive elements on screen — ~650 tokens for a Wikipedia article
curl "localhost:9867/snapshot?filter=interactive&format=compact&viewport=true"

# Act on them by ref
curl -X POST localhost:9867/action -d '{"kind":"click","ref":"e12"}'

# Or hand over a whole navigation: one call, several clicks
curl -X POST localhost:9867/goal -d '{"task":"Find the pricing page","startUrl":"https://vercel.com"}'

# Or let Claude drive it directly
claude mcp add seebro -- seebro mcp
```

## Features at a Glance

| Area | What you get |
|---|---|
| **Reading** | Page text as clean text or **markdown** (headings, lists, links, tables) with paging; interactive-element snapshots with **stable refs**, a **viewport-only** mode and diffs; full accessibility tree; element inspection (attributes, box, HTML, computed styles) |
| **iframes** | Snapshots, text and actions reach into **iframes, including cross-origin** ones (payment forms, embedded logins, editors) |
| **Interaction** | Click, double click, click at coordinates, type (fast `insertText`), fill (React/Vue-safe), whole forms in one call, keys and combos (`Enter`, `Control+A`), hover, select, scroll, drag & drop (HTML5 + mouse), file upload, human-like mouse and typing |
| **Navigation** | Load URLs, back / forward / reload, `waitUntil` load / network idle, wait for text / element / URL (reacts instantly to DOM changes), multiple tabs |
| **Goal navigation** | `POST /goal` / `navigate_goal`: a whole multi-step navigation in **one call**. The Jev decision model (TypeSafe, OpenRouter or Vercel AI Gateway) picks each click, search or dropdown choice; an offline matcher works without a key and takes over when a model call fails. Stop gates, no-effect recovery, bot-wall detection and a step trace with confidences and cost |
| **Engines** | Chrome (default) or **obscura**, a Rust headless browser using ~20x less memory; both together in one server, chosen per tab |
| **DevTools** | Network log with headers, POST data and response bodies, **WebSocket and Server-Sent Events**, console and uncaught exceptions, JS dialogs, **HAR export**, **performance metrics and Web Vitals** |
| **Emulation** | Mobile viewports and touch, dark mode, reduced motion, geolocation, network and CPU throttling, user agent, extra headers, timezone, locale |
| **Output** | Screenshots (JPEG/PNG/WebP; viewport, full page or element; auto-scaled for LLMs), **screen recording to MP4** in real time with every click and input highlighted, PDF export, downloads (CSV/PDF exports land in a known folder) |
| **Scripting** | JavaScript in the page with awaited promises: `fetch('/api/…')` runs with the page's own login |
| **Sessions** | Persistent Chrome profile (logins survive restarts), cookies, **isolated incognito-like contexts** for parallel agents or second accounts |
| **Multi-agent** | One shared browser for all MCP clients, tab locks that are actually enforced, per-session identity |
| **Robustness** | Hung or crashed tabs are detected and can be reset; idle tabs are closed automatically; JS dialogs never block a call; Cloudflare challenge and block pages are recognised instead of clicked around on |
| **Stealth** | Passes common bot checks: no `webdriver` flag, no "HeadlessChrome" in user agent or client hints, real plugins, native-looking overrides, live self-check |
| **Security** | Loopback by default, web pages can't reach the API (CSRF/DNS-rebinding guard), token enforced beyond loopback |
| **Agent integration** | HTTP API, MCP server (stdio + Streamable HTTP, 46 tools), Claude skill |

## How It Compares

| | Seebro | Typical Browser Tools |
|---|---|---|
| **Token cost** | ~650 (on-screen elements) / ~800 (text) | 10,000+ (full DOM/screenshot) |
| **Interface** | HTTP + MCP – works with any agent | Framework-specific |
| **Output format** | Compact refs, text, markdown | Raw HTML or pixels |
| **Multi-step navigation** | One `/goal` call, a small model decides each step (~$0.0002–0.0006 per step) | One large-model round trip per click |
| **Engines** | Chrome or obscura (32 MB idle), per tab | One browser |
| **Bot detection** | Built-in, self-checking stealth | Manual configuration |
| **Session handling** | Persistent profile + isolated contexts | Usually ephemeral |
| **Deployment** | Single ~9 MB binary | Complex dependencies |

## Performance

Measured on the same machine against the previous release (Chrome 155, headless):

| Operation | Before | Now |
|---|---|---|
| Interactive snapshot, Wikipedia article (27k a11y nodes) | 1,248 ms, 10 MB transferred from Chrome | **77 ms** |
| …same, only elements on screen (`viewport=true`) | – (83 KB ≈ 20k tokens) | **73 ms, 2.6 KB ≈ 650 tokens** |
| Interactive snapshot, GitHub repo page | 251 ms | **18 ms** |
| Navigate to a page without `<title>` | 2,394 ms | **108 ms** |
| Type 300 characters | 495 ms | **7 ms** |
| Full-page screenshot, Wikipedia (16,384 px) | timeout, tab hung for good | **673 ms** |
| Text of Hacker News | lines glued together | correct line breaks |

## Two Engines: Chrome and obscura

Seebro can drive two browsers with the same API, MCP tools and refs:

| | Chrome (default) | [obscura](https://github.com/h4ckf0r0day/obscura) |
|---|---|---|
| What it is | Google Chrome / Chromium | Rust headless browser (V8, own DOM/layout), Apache-2.0 |
| Install | Chrome on the machine | `scripts/install-obscura.sh` (no Chrome needed) |
| Memory, idle | 654 MB | **32 MB** |
| Memory, 8 parallel shop flows | 1,869 MB | **251 MB** |
| Small pages, 4 workers | 136 ops/s | **163 ops/s** |
| Shop click-through, 1 / 4 / 8 workers | 155 / 345 / 425 flows/min | 153 / 334 / 328 flows/min |
| Large page (25k elements), 4 workers | **38.5 ops/s** | 9.9 ops/s |
| Mixed load, 8 workers | **92.8 ops/s** | 33.8 ops/s |
| Real sites (navigate) | GitHub 2.4 s, Wikipedia 1.1 s | GitHub 14.8 s, Wikipedia 2.9 s |
| iframes, JS dialogs, scrolling, WebSocket capture, metrics, contexts | yes | no |

Measured with `scripts/loadtest.py` on a MacBook Pro (Intel Core i9-9980HK, 8 cores / 16 threads), local test site, 30–60 s per run, 0 errors in every run.

**Rule of thumb:** obscura for many parallel, simple-to-medium pages where memory matters (scraping, reading, standard forms); Chrome for large or script-heavy pages, iframes, dialogs and anything visual. All obscura pages share one JavaScript thread, so throughput stops growing beyond ~4 parallel tabs; `BRIDGE_OBSCURA_WORKERS` adds processes but helps only a little (8-worker mixed load: 33.8 → 41.8 ops/s).

```bash
BRIDGE_ENGINE=obscura seebro                       # obscura only, no Chrome
curl -X POST localhost:9867/tab -d '{"action":"new","url":"https://example.com","engine":"obscura"}'   # hybrid
```

MCP: `tab_new {url, engine: "obscura"}`. `health` lists the available engines.

## Goal Navigation with Jev

Normally an agent drives Seebro one call at a time: snapshot → think → click → wait → snapshot. Every hop costs a full round trip through a large model, and every intermediate page passes through its context. `POST /goal` (MCP: `navigate_goal`) hands the whole navigation to a **small decision model** instead and returns when the goal is reached.

**Set up** (once): give Seebro one Jev key. Without a key `/goal` still works with the offline matcher.

```bash
AI_GATEWAY_API_KEY=vck_… seebro          # Vercel AI Gateway (needs paid gateway credits)
# or TYPESAFE_API_KEY=…  /  OPENROUTER_API_KEY=sk-or-…
```

**Use:**

```bash
curl -X POST localhost:9867/goal -H "Authorization: Bearer $TOKEN" -d '{
  "task": "Go to the source code repository of the Go programming language on GitHub",
  "startUrl": "https://go.dev/"
}'
```

```json
{
  "status": "done",
  "decider": "auto(vercel:typesafe-ai/jev, fallback local)",
  "final_url": "https://github.com/golang/go",
  "elapsed_ms": 8042,
  "steps": [
    {"step": 1, "executed_action": "click_e128", "description": "link \"GitHub\" -> https://github.com/golang",
     "top_probability": 0.98, "goal_done": 0.04, "stuck": 0.62, "decided_by": "vercel:typesafe-ai/jev",
     "outcome": "navigated to https://github.com/golang"},
    {"step": 2, "executed_action": "click_e160", "description": "link \"go\" -> /golang/go",
     "top_probability": 0.98, "goal_done": 0.46, "outcome": "navigated to https://github.com/golang/go"},
    {"step": 3, "proposed_action": "done", "top_probability": 0.73, "goal_done": 0.91, "outcome": "agent declared done"}
  ],
  "usage": {"decider_calls": 3, "input_tokens": 23949, "output_tokens": 3565, "est_cost_usd": 0.001006}
}
```

From Claude: `navigate_goal {tabId, task, startUrl}`, then `get_text` on the page it ends on.

### How a step works

Each step:

1. Seebro's snapshot engine lists the interactive elements; they become named options (`click_e73`, `search_e4`, `type_e9` / `submit_e9`, `select_e12`, plus `scroll_down`, `scroll_up`, `back`, `done`). Noise (edit links, citation markers, `javascript:`) and duplicate targets are dropped; when a page has more than 240 options, those that match the task survive the cap first.
2. One decider call answers three independent questions: **which action?** (a probability per option), **is the goal reached?**, **are we stuck?**
3. Stop gates run *before* acting (goal > 0.85, stuck > 0.85 after step 2, `done`). An action that had no visible effect and is proposed again is replaced by the next most probable one.
4. Seebro executes the action, waits for the navigation or page update, and records the outcome in the history the decider sees next. The page it observes afterwards is reused for the next step, so a step costs one snapshot.

Search boxes get the `query` (default: the task's keywords) as real key presses and Enter; a search field hidden in a collapsed header is filled and its form submitted instead. Dropdowns get a second question, "which option?". Ordinary text fields are only filled from `inputs`, never with a guess. Don't put passwords in `inputs`; do logins yourself (or once in headed mode, the profile keeps the session).

The run stops on Cloudflare challenge or block pages (`status: "blocked"` with evidence and guidance) instead of clicking around on them. Other end states: `goal_achieved` or `done` (arrived), `stuck`, `max_steps`, `max_seconds`, `error`.

### Options

| Field | Meaning |
|---|---|
| `task` | What to achieve (required) |
| `startUrl` | Navigate here first; otherwise start from the tab's current page |
| `decider` | `auto` (default), `jev` or `local`, see [Deciders](#deciders) |
| `maxSteps`, `maxSeconds` | Budgets (default 24 steps, 180 s) |
| `query` | Text for search boxes (default: the task's keywords) |
| `inputs` | `{"email": "a@b.c"}`: text for fields whose label contains the key. Without an entry nothing is typed into ordinary fields. |
| `noTyping` | Only click, never type |
| `record`, `recordPath` | Record the run as an MP4 |

### Deciders

Everything runs inside the Seebro binary: its own Chrome, its own snapshot engine, and plain HTTPS calls to the model. No Playwright, Node or jev-browser is needed.

| `decider` | Behaviour |
|---|---|
| `auto` (default) | Jev when a key is configured, otherwise the offline matcher. If a Jev call fails after its retries (network, rate limit, credits used up, malformed answer), the matcher decides that step and the run continues. |
| `jev` | Jev only; a failed call ends the run with `status: "error"` and the provider's message |
| `local` | Offline keyword matcher: follows links named after the task's words in order and uses search boxes. Free and instant, fine for "go to X, then Y" and searches, no judgment |

Each step's `decided_by` and `fallback_reason` show who answered and why.

**Jev** is TypeSafe's decision model: it answers typed questions (pick one option, is this statement true?) with probabilities instead of generating text. Through the Vercel AI Gateway a step took 0.5–1 s and cost $0.0002–0.0006 (the billed cost is reported in `usage`). Set one key in Seebro's environment:

| Variable | |
|---|---|
| `TYPESAFE_API_KEY` | TypeSafe API (`TYPESAFE_BASE_URL` to override the endpoint) |
| `OPENROUTER_API_KEY` | OpenRouter decisions API (`sk-or-…`) |
| `AI_GATEWAY_API_KEY` | Vercel AI Gateway (`vck_…`, model `typesafe-ai/jev`; needs paid gateway credits, free tier is refused) |
| `JEV_PROVIDER`, `JEV_MODEL` | Force a provider (`typesafe`, `openrouter`, `vercel`); pick a model snapshot (default `jev-latest`) |

### Results

**Measured** (Wikipedia, same decisions for every contestant, median of 3, same machine and Chrome):

| Task | jev-browser (Playwright) | Seebro `/goal` | Seebro, step by step |
|---|---|---|---|
| Coffee → Espresso | 2,436 ms | **2,155 ms** | 2,288 ms |
| Search "Ristretto" | 1,939 ms | **1,640 ms** | 1,610 ms |
| Coffee → Espresso → Ristretto | 4,215 ms | **3,184 ms** | 2,652 ms |

With the real Jev model (Vercel AI Gateway) Seebro solved all of these open tasks without help, each in one call: Espresso → inventor's article (2.6 s, 2 steps), Hacker News "newest" (3.4 s), Vercel pricing page (3.0 s, $0.0004 billed), go.dev → github.com/golang/go (8.6 s, 3 steps), python.org → "Using Python on macOS" docs (9.0 s, 7 steps), Wikipedia search for Mount Everest (2 steps).

The browser side of `/goal` is 1.1–1.3× faster than jev-browser. The real gain is in the agent: a three-hop navigation is **one tool call** instead of about nine, and none of the intermediate pages pass through the large model's context.

### When to use it

- **Good fit:** getting to a page ("pricing", "newest submissions", "the docs page about X"), following a known path, searching a site, the same navigation across many sites (monitoring, scraping) where a large model per click would be slow and expensive.
- **Not a fit:** reading and judging content, filling forms with real data, logins, purchases or messages. Let the agent do those itself with the normal tools; `/goal` is for getting there.

The loop design, the questions and the bot-wall detector are ported from [jev-browser](https://github.com/jkudish/jev-browser) (MIT, © Joey Kudish).

## Binary Size & Footprint

Release builds (`-ldflags "-s -w"`, as produced by goreleaser):

| Platform | Size |
|---|---|
| macOS arm64 | 9.3 MB |
| macOS amd64 | 9.8 MB |
| Linux arm64 | 9.0 MB |
| Linux amd64 | 9.6 MB |
| Windows amd64 | 9.9 MB |

At runtime the Seebro process uses about 15 MB of RAM; Chrome runs as separate processes. Network, console and WebSocket logs are fixed-size per-tab ring buffers (300 / 200 / 200 entries); response bodies stay in Chrome (capped at 5 MB per resource, 25 MB per tab) and are fetched only when asked for. Chrome itself starts with background services disabled (~9% less memory than a default Chrome) and idle tabs are closed after 30 minutes in headless mode. Goal navigation, the Jev client and the recorder add no dependencies; recording needs `ffmpeg` on the machine, obscura is a separate optional binary.

## Getting Started

### Option 1: Docker

```bash
docker run -d -p 9867:9867 -e BRIDGE_TOKEN=your-secret --security-opt seccomp=unconfined seebro/seebro
curl -H "Authorization: Bearer your-secret" http://localhost:9867/health
```

### Option 2: Build from Source

```bash
go build -ldflags "-s -w" -o seebro .
BRIDGE_HEADLESS=true ./seebro
```

### Option 3: Let Your Agent Handle It

With [OpenClaw](https://openclaw.ai) or Claude, ask your agent to set up Seebro. The [skill file](skill/seebro/SKILL.md) has everything it needs.

## MCP Server (Claude & other MCP clients)

Seebro has a built-in [Model Context Protocol](https://modelcontextprotocol.io) server with **46 tools**, in the same binary. No extra install, runtime or dependency.

### Setup

**Claude Code** (stdio, recommended):

```bash
claude mcp add --scope user seebro -- /path/to/seebro mcp
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

- `seebro mcp` is a thin proxy mode of the same binary that forwards JSON-RPC to `POST /mcp`.
- If no server answers on `localhost`, it **starts one in the background** (headless, log in `~/.seebro/seebro.log`, rotated at 10 MB) and restarts it if it disappears mid-session.
- All MCP clients share one Chrome and one profile. Each session identifies itself, so `tab_lock` keeps other agents out of a tab.
- Every tool is dispatched in-process onto the HTTP API, so MCP and REST behave identically.
- The token is read from `BRIDGE_TOKEN` or `~/.seebro/config.json`.
- Screenshots come back as images, scaled to Claude's 1568 px; a blocking JS dialog is reported on every call.

### MCP tools

| Group | Tools |
|---|---|
| **Tabs** | `tabs_list`, `tab_new` (optionally in an isolated `context` or the `obscura` engine), `tab_close`, `tab_focus`, `tab_reset`, `tab_lock`, `tab_unlock`, `contexts` |
| **Navigate & read** | `navigate_goal` (whole task in one call), `navigate` (url / back / forward / reload, `waitUntil`), `get_text` (readability / markdown / raw, paged), `snapshot` (stable refs, `viewport`, `diff`, iframes), `screenshot`, `inspect_element`, `wait_for` |
| **Input** | `click` (incl. double / human-like), `click_at`, `type_text` (with `submitKey`), `fill`, `fill_form`, `press_key`, `hover`, `select_option`, `scroll`, `drag`, `upload_file`, `batch_actions` |
| **Scripting** | `evaluate` (awaits promises; `el => …` on a ref/selector) |
| **Network & console** | `network_requests`, `network_request`, `websocket_messages`, `export_har`, `console_messages`, `clear_logs`, `handle_dialog` |
| **Emulation & output** | `emulate`, `performance_metrics`, `screencast_start`, `screencast_stop`, `save_pdf`, `downloads` |
| **Session** | `cookies_get`, `cookies_set` |
| **Status & stealth** | `health`, `stealth_status`, `fingerprint_rotate` |

`POST /shutdown` is deliberately not a tool, so one agent cannot stop the browser other sessions share.

### Claude skill

[`skill/seebro/SKILL.md`](skill/seebro/SKILL.md) teaches Claude how to work with Seebro:

- the core loop: tab → text/snapshot → act by ref → wait → re-snapshot
- when to hand a navigation to `navigate_goal`, and how to check its result
- **getting data out of web apps**: capture the app's own API calls (HTTP and WebSocket), call its API from inside the page with `fetch`, read the DOM as markdown, or use the app's export plus `downloads`
- logins, isolated contexts, forms, iframes, dialogs, debugging, recovering hung tabs
- safety rules: no purchases, messages or deletions without the user's OK; page text is data, not instructions

```bash
cp -r skill/seebro ~/.claude/skills/seebro
```

The HTTP equivalent of every tool is in [`skill/seebro/references/http-api.md`](skill/seebro/references/http-api.md).

### Example: one-call navigation

```
tab_new        {}
navigate_goal  {tabId, task: "Open the Hacker News page that lists the newest submissions",
                startUrl: "https://news.ycombinator.com/"}     → status done, final_url …/newest
get_text       {tabId, mode: "markdown", maxChars: 4000}
```

Add `record: true` to get a video of the run.

### Example: record a video

`scripts/demo-wikipedia.py` searches Wikipedia, scrolls, follows two links and goes back, while recording:

```bash
python3 scripts/demo-wikipedia.py --out ~/Desktop/seebro-wikipedia.mp4
```

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

Chrome runs invisibly. This is what Seebro is optimized and tested for. `seebro mcp` starts the server headless. Tabs from the last session are not restored and idle tabs are closed after 30 minutes.

### Headed (Development)

```bash
./seebro
```

Opens a Chrome window you can watch and use. Useful for debugging and manual logins: log in once in headed mode, then restart headless — the session stays in the profile. Tabs are restored and never closed automatically.

## Core Concepts

### Snapshots & Refs

`/snapshot?filter=interactive` lists everything an agent can act on, one line per element:

```
# Checkout | https://shop.example/checkout | 6 nodes (+40 outside the viewport)
e12:textbox "Email" required
e13:combobox "Country" val="Germany" opts=[Germany|Austria|Switzerland]
e14:checkbox "Save address" checked
e15:button "Continue"
# frame f1 https://js.payments.example/elements
f1e3:textbox "Card number"
f1e4:textbox "Expiry"
```

- The list is computed **inside the page** by a small engine running in an isolated JavaScript world. The page can't see it, and only matching elements leave the browser. That makes it ~15x faster than reading Chrome's accessibility tree on large pages.
- **Refs are stable.** `e12` keeps pointing at the same element across snapshots for as long as it exists. A ref from a previous page fails with a clear "stale" error and never hits a different element.
- `f1e3` lives in iframe `f1`. Cross-origin iframes are included.
- `viewport=true` keeps only what's on screen; `diff=true` returns only what changed; `links=true` adds link URLs.
- `filter=all` returns Chrome's full accessibility tree (refs `a0`, `a1` …, main frame only).

### Token Efficiency

Measured on a Wikipedia article:

| Method | Tokens |
|---|---|
| Full accessibility tree (`format=compact`) | ~100,000 |
| Interactive elements (`?filter=interactive&format=compact`) | ~20,000 |
| Interactive elements on screen (`&viewport=true`) | ~650 |
| Text, first page (`/text?maxChars=30000`) | ~7,500 |
| Screenshot of the viewport (1366×681) | ~1,250 |

### Session Persistence

Log in once, stay logged in. Seebro maintains a Chrome profile at `~/.seebro/chrome-profile/` that keeps cookies and storage across restarts. Isolated contexts (`"context": "name"` when opening a tab) live only in memory.

### Network, Console & WebSocket Recording

Every tab records its HTTP requests, WebSocket frames, Server-Sent Events and console output from the moment Seebro attaches. Lists default to "since the last navigation" (`all=true` includes earlier entries still in the buffer).

## API Reference

All endpoints accept `tabId`. Without it, the last tab used is taken. GET endpoints take query parameters, POST endpoints a JSON body. Errors are `{"error": "…"}`.

### Reading State

| Endpoint | Purpose |
|----------|---------|
| `GET /health` | Connection status |
| `GET /tabs` | Open tabs with lock owner, open dialog, health and context |
| `GET /snapshot` | Elements with refs (see options below) |
| `GET /text` | Page text: `mode=readability\|markdown\|raw`, `maxChars`, `offset` (returns `nextOffset`) |
| `GET /screenshot` | Image: `format=jpeg\|png\|webp`, `quality`, `fullPage`, `ref`/`selector`, `maxDim`, `maxHeight`, `raw=true`, `output=file&path=` |
| `GET /element` | One element: attributes, text, value, box, visibility, `html=true`, `styles=true\|all` |
| `GET /cookies` | Cookies of the current page (`url`, `name`) |
| `GET /stealth/status` | Live bot-detection self-check with score |

### Taking Actions

| Endpoint | Purpose |
|----------|---------|
| `POST /navigate` | `url`, or `type: back\|forward\|reload`; `waitUntil: domcontentloaded\|load\|networkidle`; `newTab` (+ `context`) |
| `POST /action` | Single action (kinds below) |
| `POST /actions` | Several actions (`stopOnError`) |
| `POST /goal` | Whole navigation in one call: `task`, `startUrl`, `decider` (`auto`/`jev`/`local`), `maxSteps`, `maxSeconds`, `query`, `inputs`, `noTyping`, `record`; returns status, final URL, step trace, usage (see [Goal Navigation](#goal-navigation-with-jev)) |
| `POST /evaluate` | JavaScript; promises awaited; with `ref`/`selector` runs `el => …` |
| `POST /wait` | `text` (one or many), `selector` (or `gone`), `url`, `networkIdle`, `timeout`; 408 on timeout |
| `POST /tab` | `new` (+ `url`, `context`, `engine: chrome\|obscura`), `close`, `focus`, `reset` |
| `POST /tab/lock`, `/tab/unlock` | Reserve a tab; others get 423 until it is released or expires |
| `POST /cookies` | Set cookies |
| `POST /fingerprint/rotate` | New user agent, platform, screen, CPU cores |
| `POST /shutdown` | Graceful shutdown |

**Action kinds** for `/action` and `/actions`:

| Kind | Does |
|---|---|
| `click`, `dblclick` | Click / double-click by `ref` or `selector` |
| `clickAt` | Click at `x`/`y` (`count` for multi-click) |
| `type` | Focus and type `text` (fast insert, last key as a real key press) |
| `fill` | Set value of input, textarea, select, checkbox/radio or contenteditable with input/change events |
| `press` | Key names and combos: `Enter`, `Tab`, `Escape`, `ArrowDown`, `Control+A`, `Meta+Shift+K` |
| `focus`, `hover`, `select`, `scroll` | As named (`scrollX`/`scrollY` for page scroll) |
| `drag` | From `ref`/`selector` to `toRef`/`toSelector` or `x`/`y` |
| `upload` | Set `files` on an `<input type=file>` |
| `humanClick`, `humanType` | Human-like mouse path / typing rhythm |

### DevTools

| Endpoint | Purpose |
|----------|---------|
| `GET /network` | Requests since the last navigation; filter by `type`, `filter` (URL), `method`, `status` (`2xx`, `4xx`, `failed`, `404`); `headers=true`, `limit`, `offset`, `all` |
| `GET /network/request` | One request by `id`: headers, POST data, body (`maxBytes`, `saveTo`, `base64`) |
| `GET /network/websocket` | WebSocket frames and SSE events (`filter`, `dir=sent\|received\|sse`, `limit`) |
| `GET /network/har` | HAR 1.2 export (`bodies=true`, `path=…har`) |
| `POST /network/clear`, `POST /console/clear` | Reset a tab's logs |
| `GET /console` | Console messages, exceptions, browser log (`level`, `limit`, `all`) |
| `GET /dialog`, `POST /dialog` | Open JS dialog; `accept` / `dismiss` (`promptText`) |
| `POST /emulate` | `viewport` (`390x844x3,mobile,touch`), `userAgent`, `colorScheme`, `reducedMotion`, `geolocation`, `network` (`Slow 3G` … `Offline`), `cpuThrottling`, `extraHeaders`, `timezone`, `locale` |
| `GET /performance` | TTFB, FCP, LCP, CLS, load, plus JS heap, DOM nodes, layout counts |
| `GET /pdf` | Print to PDF (`path`, `landscape`, `printBackground`) |
| `POST /screencast/start`, `POST /screencast/stop` | Record the tab as a real-time MP4 (needs ffmpeg); clicks and typing are highlighted while recording (`path`, `maxWidth`, `maxHeight`, `quality`) |
| `GET /downloads` | Downloads and files in the download folder |
| `GET /contexts`, `POST /context` | List isolated contexts; `close` one (and its tabs) |
| `POST /mcp` | MCP endpoint (Streamable HTTP, JSON-RPC) |

### Snapshot Options

| Parameter | Effect |
|-----------|--------|
| `filter=interactive` | Interactive elements from the in-page engine (recommended) |
| `viewport=true` | Only elements currently on screen |
| `links=true` | Add link URLs |
| `diff=true` | Only elements added, changed or removed since the last snapshot |
| `selector=CSS` | Only inside this element |
| `maxNodes=N` | Stop after N elements (default 5000) |
| `maxTokens=N` | Truncate to a token budget |
| `format=compact\|text\|yaml` | Output format (JSON by default) |
| `filter` omitted / `engine=ax` | Chrome's accessibility tree (`depth=N` to limit) |
| `output=file`, `path=` | Save to disk |

## Configuration

Environment variables (a config file at `~/.seebro/config.json` is also supported, see `seebro config init`):

| Variable | Default | Purpose |
|----------|---------|---------|
| `BRIDGE_PORT` | `9867` | Server port |
| `BRIDGE_BIND` | `127.0.0.1` | Listen address. Anything but loopback requires a token (generated if unset) |
| `BRIDGE_TOKEN` | – | Bearer token |
| `BRIDGE_ALLOWED_ORIGINS` | – | Web origins allowed to call the API from a browser (comma-separated) |
| `BRIDGE_ALLOWED_HOSTS` | – | Extra `Host` names accepted on loopback (reverse proxies) |
| `BRIDGE_HEADLESS` | `false` | Run without a window (`seebro mcp` autostart: `true`) |
| `BRIDGE_PROFILE` | `~/.seebro/chrome-profile` | Browser profile |
| `BRIDGE_STATE_DIR` | `~/.seebro` | Logs, screenshots, PDFs, saved state |
| `BRIDGE_STEALTH` | `light` | `light` or `full` (adds fingerprint noise) |
| `BRIDGE_SITE_ISOLATION` | `false` | `true` keeps Chrome's site isolation (cross-origin iframes become unreachable) |
| `BRIDGE_LEAN` | `true` | `false` keeps Chrome's background services |
| `BRIDGE_TAB_IDLE` | `30m` headless, off headed | Close tabs unused for this long (`0` = never) |
| `BRIDGE_RESTORE` | `false` headless, `true` headed | Reopen the last session's tabs |
| `BRIDGE_DIALOG` | – | `accept` / `dismiss` answers JS dialogs automatically |
| `BRIDGE_DOWNLOAD_DIR` | `~/.seebro/downloads` | Download folder |
| `BRIDGE_NETWORK_BUFFER` | `300` | Requests kept per tab |
| `BRIDGE_CONSOLE_BUFFER` | `200` | Console messages kept per tab |
| `BRIDGE_BLOCK_IMAGES` / `BRIDGE_BLOCK_MEDIA` | `false` | Skip images / all media |
| `BRIDGE_NO_ANIMATIONS` | `false` | Freeze CSS animations |
| `BRIDGE_TIMEZONE` | – | Timezone for all tabs |
| `BRIDGE_LOG_LEVEL` | `info` | `debug` also logs every successful request |
| `BRIDGE_ENGINE` | `chrome` | `chrome`, `obscura` (no Chrome) or `auto` (Chrome if installed) |
| `OBSCURA_BINARY` | – | Path to obscura (default: next to seebro, `~/.seebro/bin`, PATH) |
| `BRIDGE_OBSCURA_WORKERS` | `1` | obscura worker processes |
| `BRIDGE_OBSCURA_STEALTH` | `true` | obscura's own fingerprint + TLS impersonation |
| `BRIDGE_OBSCURA_PRIVATE_NETWORK` | `true` | Let obscura open localhost/LAN URLs like Chrome does |
| `TYPESAFE_API_KEY` / `OPENROUTER_API_KEY` / `AI_GATEWAY_API_KEY` | – | Jev key for `/goal` (any one) |
| `JEV_PROVIDER`, `JEV_MODEL` | auto, `jev-latest` | Force `typesafe`, `openrouter` or `vercel`; model snapshot |
| `TYPESAFE_BASE_URL`, `AI_GATEWAY_EVALUATION_URL`, `OPENROUTER_DECISIONS_URL` | provider defaults | Override a Jev endpoint (proxies, tests) |
| `FFMPEG_BINARY` | `ffmpeg` | ffmpeg used to encode screen recordings |
| `CDP_URL` | – | Use an already running Chrome |
| `CHROME_BINARY`, `CHROME_FLAGS` | – | Chrome path, extra flags |
| `SEEBRO_URL` | `http://127.0.0.1:$BRIDGE_PORT` | Server used by `seebro mcp` |
| `SEEBRO_NO_AUTOSTART` | – | `true` stops `seebro mcp` from launching a server |
| `SEEBRO_OWNER` | `mcp-<pid>` | Identity of an MCP session for tab locks |

More in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

### Commands

| Command | Purpose |
|---|---|
| `seebro` | Start the server (HTTP API + `/mcp`) |
| `seebro mcp` | MCP over stdio (proxies to the server, starts it if needed) |
| `seebro config init` / `seebro config show` | Create / show the config file |
| `seebro --version` | Print version |

## Stealth Mode

Automation signals are removed at the browser level, where they leave no JavaScript traces:

- `navigator.webdriver` is `false`, as in a normal Chrome (`--disable-blink-features=AutomationControlled`)
- user agent and client hints say `Chrome`, not `HeadlessChrome`
- real plugins, mime types and `window.chrome` (new headless mode)
- the injected script lives in a closure (no globals), and every override keeps native descriptors and `toString()`

`light` (default) changes nothing else. `full` adds seeded canvas, WebGL and font noise, WebRTC relay and hardware values that stay stable within a session. `GET /stealth/status` runs the same checks bot detectors use and scores them. On bot.sannysoft.com every check passes.

## Architecture

```
┌─────────────┐  HTTP / MCP      ┌──────────────────────┐   CDP    ┌───────────────────┐
│   Agent     │  ── JSON ───►    │        Seebro        │ ───────► │ Chrome            │
│  (any kind) │   req/res        │         (Go)         │ ───────► │ obscura (optional)│
└─────────────┘                  │  snapshot engine     │          └───────────────────┘
                                 │  recorder, stealth   │  HTTPS   ┌───────────────────┐
                                 │  /goal loop ─────────┼────────► │ Jev decision model│
                                 └──────────────────────┘          │ (optional)        │
                                                                   └───────────────────┘
```

Seebro is a translation layer. Agents speak HTTP or MCP, browsers speak CDP. Seebro adds an in-page snapshot engine, records network and console activity, handles stealth, sessions and tab health, compresses everything to save tokens, and can run whole navigations itself with a decision model. Details: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Security Notice

**Seebro gives agents real browser access with your real accounts.** Anyone who can call it can act as you: read messages, make purchases, read cookies and auth headers from the network log.

What Seebro does for you:
- listens on **127.0.0.1** only, unless you set `BRIDGE_BIND`; then a token is mandatory and generated if missing
- **rejects requests from web pages** (any `Origin` or cross-site `Sec-Fetch-Site` not explicitly allowed) and foreign `Host` headers, so a site you visit can't drive Seebro through your browser (CSRF, DNS rebinding)
- answers CORS only for origins you allow — never `*`
- refuses to write output files into hidden files or folders (`~/.ssh`, dotfiles) outside its state directory
- enforces tab locks between agents

What you should do:
- set `BRIDGE_TOKEN` if other users or containers share the machine
- treat `~/.seebro/` as sensitive — it contains your browser profile
- start with test accounts until you trust your agent

Think of it as handing someone an unlocked laptop. Powerful when intentional. Dangerous when careless.

## Dependencies

| Package | Purpose | License |
|---------|---------|---------|
| [chromedp](https://github.com/chromedp/chromedp) | CDP driver | MIT |
| [cdproto](https://github.com/chromedp/cdproto) | CDP types | MIT |
| [gobwas/ws](https://github.com/gobwas/ws) | WebSocket | MIT |

Everything else, including the MCP server and the Jev client, is Go standard library. The goal-navigation loop and bot-wall detector are ported from [jev-browser](https://github.com/jkudish/jev-browser) (MIT); see [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).

## Requirements

- Go 1.25+ (or download a [release binary](https://github.com/ChrisFeldmeier/seebro/releases))
- Chrome or Chromium (or obscura alone: `scripts/install-obscura.sh`)
- Optional: `ffmpeg` for screen recordings (not in the Docker image), a Jev key for model-steered `/goal`

## Development

```bash
git clone https://github.com/ChrisFeldmeier/seebro.git
cd seebro
go build -o seebro .
go test ./...                       # ~200 unit tests, no Chrome or API key needed
go test -tags integration ./...     # with a real headless Chrome

# load test (see the script header for scenarios)
python3 scripts/loadtest.py --write-site /tmp/seebro-site
python3 -m http.server 8765 --bind 127.0.0.1 --directory /tmp/seebro-site &
python3 scripts/loadtest.py --engine obscura --scenario flow --workers 4 --duration 60

# recorded demo: Wikipedia search, scrolling, links, back
python3 scripts/demo-wikipedia.py --out /tmp/seebro-wikipedia.mp4
```

| Script | Purpose |
|---|---|
| `scripts/loadtest.py` | Load test for both engines (small, large, click-through, mixed, real sites) |
| `scripts/demo-wikipedia.py` | Automated tour that produces a video |
| `scripts/install-obscura.sh` | Install the obscura engine into `~/.seebro/bin` |
| `scripts/install-autostart.sh` | Run Seebro at login (launchd / systemd) |
| `scripts/check.sh` | The CI checks, locally |

## Contributors

<a href="https://github.com/ChrisFeldmeier">
  <img src="https://github.com/ChrisFeldmeier.png" width="60" style="border-radius:50%" alt="Chris Feldmeier"/><br/>
  <sub><b>Chris Feldmeier</b> · Creator</sub>
</a>

## OpenClaw Integration

Seebro works natively with [OpenClaw](https://openclaw.ai), the open-source AI assistant. Use it as your agent's browser backend for efficient, low-cost web automation.



---

<p align="center">
  <a href="https://star-history.com/#ChrisFeldmeier/seebro&Date">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=ChrisFeldmeier/seebro&type=Date&theme=dark" />
      <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/svg?repos=ChrisFeldmeier/seebro&type=Date" />
      <img alt="Star History" src="https://api.star-history.com/svg?repos=ChrisFeldmeier/seebro&type=Date" width="600"/>
    </picture>
  </a>
</p>
