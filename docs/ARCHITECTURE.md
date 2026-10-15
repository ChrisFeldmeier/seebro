# Seebro Architecture

## Overview

Seebro is a single Go binary (~9 MB, ~15 MB RAM) that launches Chrome and exposes it to AI agents
through an HTTP API and an MCP server. Agents never speak CDP; they get compact text and refs.

```
┌──────────────┐  HTTP / MCP   ┌──────────────────────────┐      CDP      ┌──────────────┐
│  Agent / LLM │ ────────────▶ │  Seebro (Go)             │ ────────────▶ │ Chrome        │
│  Claude, …   │ ◀──────────── │  handlers · monitors ·   │ ◀──────────── │ (headless or  │
└──────────────┘  JSON / text  │  snapshot engine · MCP   │   one session │  headed)      │
                               └──────────────────────────┘   per tab     └──────────────┘
```

## Design Principles

1. **Text over pixels** — readable text and interactive-element lists instead of screenshots.
2. **Measure in the page, transfer little** — the snapshot engine runs inside the page and returns
   only what an agent can act on; bodies, trees and logs stay in Chrome until asked for.
3. **Bounded memory** — every per-tab log is a fixed-size ring buffer; idle tabs are closed.
4. **One browser, many agents** — `seebro mcp` processes share one server; locks and isolated
   contexts keep agents apart.
5. **Safe by default** — loopback only, browser-originated requests rejected, token required
   beyond loopback.

## File Structure

```
main.go               Chrome launch flags, routes, shutdown, startup self-check
config.go             Env/config-file settings, embedded JS
security.go           Bind address, origin/host guard, token generation, safe output paths
middleware.go         Logging (by status), CORS for allowed origins, auth
bridge.go             Tab registry, tab creation/attachment, default (last used) tab
contexts.go           Isolated browser contexts, tab-lock enforcement
monitor.go            Per-tab recorder: network, console, WebSocket/SSE, dialogs, health
tabhealth.go          Hung/crashed tab detection, reset, idle-tab cleanup
snapshot.js           In-page snapshot engine (runs in an isolated world)
snapshot_engine.go    Isolated worlds per frame, frame offsets, ref resolution
snapshot.go           Snapshot node type, text/compact formats, diff, accessibility-tree parsing
handler_snapshot.go   /snapshot: engine for interactive, accessibility tree for full
text.js               Text extraction (readability, raw, markdown)
screenshot.go         Screenshots without captureBeyondViewport (viewport enlarging instead)
handlers.go           navigate (incl. history), text, screenshot, evaluate, tabs, locks, shutdown
handler_actions.go    /action, /actions, ref resolution for actions
actions_extra.go      fill, press (key names), dblclick, clickAt, drag, upload
cdp.go                Navigation/readiness polling, typing via insertText, element helpers
handler_devtools.go   network log, console, dialogs, wait, emulate, element, pdf, downloads
handler_insights.go   WebSocket/SSE messages, HAR export, performance metrics
handler_cookies.go    Cookies
handler_stealth.go    Live stealth checks, fingerprint rotation
stealth_ua.go         Headless user-agent/client-hint override, stealth checks
stealth.js            Minimal stealth script (light) and fingerprint noise (full)
human.go              Human-like mouse paths and typing
mcp.go                MCP JSON-RPC server, tool catalogue (in-process dispatch onto the API)
mcp_stdio.go          `seebro mcp`: stdio proxy, server autostart, log rotation
goal.go               `POST /goal`: goal-directed navigation loop, action space, local decider
jev.go                Jev decision-model client (TypeSafe/OpenRouter), questions, filters, bot-wall detector
state.go              Save/restore tabs (restore is off in headless by default)
skill/seebro/         Claude skill and HTTP reference
```

## Engines

`BRIDGE_ENGINE` picks the primary browser: `chrome` (default), `obscura` or `auto` (Chrome if
installed, else obscura). With Chrome as primary, tabs opened with `{"engine":"obscura"}` run in a
second browser that Seebro starts on first use.

obscura (Apache-2.0, https://github.com/h4ckf0r0day/obscura) is a Rust headless browser with its
own DOM, V8 runtime, layout and CDP server. Seebro runs `obscura serve` as a sidecar on a free
loopback port (binary from `OBSCURA_BINARY`, next to seebro, `~/.seebro/bin` or PATH; see
`scripts/install-obscura.sh`) and drives it with the same chromedp code.

Engine-specific behaviour (`obscura.go`, `engineOf`, `engineFromCtx`):
- obscura tabs get no Chrome stealth script or UA override (obscura has its own consistent
  fingerprint, including TLS).
- The snapshot engine runs in the page's main world (non-enumerable `__sb`) in a single CDP call
  with `lite` mode: `textContent` instead of `innerText`, no `getBoundingClientRect` /
  `getComputedStyle`. In obscura those layout calls cost 6–13 ms per element and isolated worlds
  are much slower to create.
- `/text` uses a structural walker (`lite`) instead of `innerText`.
- No frame-tree or box-model calls (obscura doesn't load iframes; box models force layout).
- Clicks on `<a download>` are fetched inside the page and saved to the download folder.
- All pages share one V8 thread in obscura: CDP calls wait while page scripts run, so latency on
  script-heavy pages varies.

## Snapshots and refs

Two engines produce the same `A11yNode` list and output formats:

| | Snapshot engine (`filter=interactive`, default) | Accessibility tree (`filter=all` or `engine=ax`) |
|---|---|---|
| Source | `snapshot.js` in a per-frame isolated world | `Accessibility.getFullAXTree` |
| Cost on Wikipedia | ~80 ms, only matching elements | ~1.2 s, 10 MB JSON for 27k nodes |
| Refs | `e12`, `f1e3` (frame 1) — stable per element | `a0, a1 …` — positional, per snapshot |
| Frames | all frames incl. cross-origin | main frame |
| Extras | `viewport=true`, `links=true`, options, states | full semantic tree, depth |

**Isolated worlds.** `Page.createIsolatedWorld` gives the engine the page's DOM without its
JavaScript: the page cannot see or tamper with it. Each world is bound to the frame's document
(`loaderId`) because execution-context ids repeat after a cross-site navigation (new renderer process).

**Stable refs.** The engine keeps a `WeakMap` element → id per document, so the same element keeps
its ref across snapshots. Ids keep increasing within a tab (the next id is carried over to the next
document), so a ref from a previous page can never resolve to an element on the current one; it
fails with "stale — take a new snapshot".

**Ref resolution.** An action with `ref` asks the frame's world for the element
(`__sb.el(id)`), turns the object into a backend node id (`DOM.describeNode`) and from there the
existing CDP actions run unchanged. Coordinate-based input (double click, drag, human click) adds
the frame's offset (`DOM.getFrameOwner` + box model).

**Cross-origin iframes.** Chrome runs with `--disable-site-isolation-trials` (opt out with
`BRIDGE_SITE_ISOLATION=true`), so all frames of a tab share one CDP session and get their own world.

## Recording (monitor.go)

Each tab has a `tabMonitor` fed by CDP events:

| Buffer | Default size | Notes |
|---|---|---|
| Network requests | 300 (`BRIDGE_NETWORK_BUFFER`) | headers kept; bodies/POST data fetched from Chrome on demand |
| Console + exceptions + browser log | 200 (`BRIDGE_CONSOLE_BUFFER`) | |
| WebSocket frames + SSE events | 200 | payloads truncated to 4 KB |

Entries carry a navigation counter, so lists default to "since the last navigation". Chrome's own
body buffers are capped at 5 MB per resource / 25 MB per tab.

Dialogs (`alert`/`confirm`/`prompt`) are recorded; actions and `evaluate` watch for them and return
immediately instead of blocking. `beforeunload` is always accepted; `BRIDGE_DIALOG=accept|dismiss`
answers all dialogs automatically.

## Tab health

- A call that times out triggers a 1.5 s probe; if the renderer doesn't answer, the tab is marked
  `unresponsive` and the caller gets 504 with instructions to `reset` it.
- `Inspector.targetCrashed` marks a tab `crashed`.
- `POST /tab {"action":"reset"}` closes the tab and opens a fresh one at the same URL.
- Idle tabs (default 30 min headless, off headed; `BRIDGE_TAB_IDLE`) are closed, except the
  initial tab and locked tabs.

## Screenshots

`captureBeyondViewport` wedges the renderer on some pages (Chrome 155 headless, e.g. Wikipedia).
Full-page and tall-element captures therefore enlarge the viewport with
`Emulation.setDeviceMetricsOverride`, capture, and restore the previous emulation. `maxDim`
downsizes via the clip scale; MCP uses 1568 px by default.

## Stealth

Browser level, no JS traces:
- `--disable-blink-features=AutomationControlled` → `navigator.webdriver === false`
- user agent and client hints without "Headless" via `Emulation.setUserAgentOverride` (headless only)
- real plugins, mimeTypes and `window.chrome` from new headless

`stealth.js` (wrapped in a closure, no globals) only aligns notification permissions in light mode;
`full` adds seeded canvas/WebGL/font noise, WebRTC relay and hardware values. All overrides keep
native descriptors and `toString()`. `GET /stealth/status` runs the checks detectors use.

## Request pipeline

```
Request → Logging → Guard (origin, Sec-Fetch-Site, host) → CORS (allowed origins) → Auth → Handler
```

- Listens on `BRIDGE_BIND` (default `127.0.0.1`). Binding elsewhere without a token generates one.
- Requests from web pages (`Origin`, `Sec-Fetch-Site: cross-site/same-site`) are rejected unless
  the origin is in `BRIDGE_ALLOWED_ORIGINS`; foreign `Host` headers are rejected on loopback
  (DNS rebinding).
- File outputs (`path`, `saveTo`) may not write into hidden files/directories outside the state dir.
- Tab locks: mutating calls on a locked tab need the owner (`X-Seebro-Owner`, sent per session by
  `seebro mcp`), otherwise 423.

## Goal navigation (goal.go, jev.go)

`POST /goal` runs a task as a loop. The loop drives the tab through the same mux as MCP
(`apiBrowser`, in-process), so it uses the snapshot engine, actions, waits and recording unchanged.

Per step: observe (interactive snapshot with links + 1,500 chars of page text) → build the action
space (`buildActionSpace`) → ask the decider `action` (choice), `goal_done` and `stuck` (noul) in one
call → stop gates → execute → `Settle` → observe again. The observation after an action is reused as
the next step's view, so a step costs one snapshot. `Arm` marks the document with a `beforeunload`
flag before acting; `Settle` waits while that flag says a navigation is starting, until the new URL
has a non-loading document, or 400 ms of quiet when nothing navigates.

The decider is an interface: `jevClient` (TypeSafe `/v1/systemone` or OpenRouter
`/api/alpha/decisions`, retries on 408/409/429/5xx, answers validated before use) or `localDecider`
(deterministic keyword walk). Tests drive the loop with a fake browser and scripted deciders.
The design is ported from jev-browser (MIT).

## MCP

`POST /mcp` speaks JSON-RPC (Streamable HTTP, JSON responses). Each tool maps to an HTTP route and
is dispatched in-process through the same mux (`httptest.ResponseRecorder`), so MCP and REST cannot
drift apart. `seebro mcp` is a stdio proxy that forwards each line to `/mcp`, starts the server in
the background when needed (headless, log rotated at 10 MB) and identifies itself for tab locks.

## State

```
~/.seebro/
├── chrome-profile/   Persistent profile (cookies, storage)
├── config.json       port, token, headless, …
├── sessions.json     Tabs saved on shutdown (restored when BRIDGE_RESTORE / headed)
├── downloads/        Browser downloads
├── screenshots/ snapshots/ pdfs/
└── seebro.log        Server log when started by `seebro mcp`
```
