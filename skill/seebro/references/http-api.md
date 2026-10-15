# Seebro HTTP API

Base URL `http://localhost:9867` (`BRIDGE_PORT`). If a token is set, send `Authorization: Bearer $TOKEN`
with every request. GET endpoints take query parameters. POST endpoints take a JSON body. Every
endpoint accepts `tabId` (default: the last tab used). Errors come back as `{"error": "…"}` with a
4xx/5xx status: 423 = tab locked by another owner (`X-Seebro-Owner`), 504 = tab unresponsive (reset it).
Requests from web pages (with an `Origin` header) are rejected unless allowed via `BRIDGE_ALLOWED_ORIGINS`.

```bash
S=http://localhost:9867; A="Authorization: Bearer $BRIDGE_TOKEN"
TAB=$(curl -s -H "$A" -X POST $S/tab -d '{"action":"new","url":"https://example.com"}' | jq -r .tabId)
curl -s -H "$A" "$S/snapshot?tabId=$TAB&filter=interactive&format=compact"
curl -s -H "$A" -X POST $S/action -d "{\"tabId\":\"$TAB\",\"kind\":\"click\",\"ref\":\"e0\"}"
```

| MCP tool | Endpoint | Parameters |
|---|---|---|
| tabs_list | `GET /tabs` | |
| tab_new / tab_close / tab_focus / tab_reset | `POST /tab` | `action` new\|close\|focus\|reset, `url`, `context`, `tabId` |
| contexts | `GET /contexts`, `POST /context` | `action: close`, `name` |
| tab_lock / tab_unlock | `POST /tab/lock`, `/tab/unlock` | `tabId`, `owner`, `timeoutSec` |
| navigate | `POST /navigate` | `url`, `type` url\|back\|forward\|reload, `waitUntil` domcontentloaded\|load\|networkidle, `ignoreCache`, `newTab`, `context`, `timeout` (s), `waitTitle`, `blockImages`, `blockMedia` |
| navigate_goal | `POST /goal` | `task`, `startUrl`, `decider` jev\|local, `maxSteps`, `maxSeconds`, `query`, `inputs` {label: text}, `noTyping`, `record`, `recordPath`, `model` |
| get_text | `GET /text` | `mode` readability\|markdown\|raw, `maxChars`, `offset` (→ `nextOffset`) |
| snapshot | `GET /snapshot` | `filter=interactive`, `viewport`, `links`, `format` compact\|text\|yaml, `diff`, `selector`, `maxNodes`, `maxTokens`, `engine=ax`, `depth` (ax), `output=file`, `path` |
| screenshot | `GET /screenshot` | `format` jpeg\|png\|webp, `quality`, `fullPage`, `ref`/`selector`, `maxDim`, `maxHeight`, `raw=true` (image bytes), `output=file`, `path` |
| inspect_element | `GET /element` | `ref`/`selector`, `html`, `styles` true\|all |
| wait_for | `POST /wait` | `text` (string or list), `selector`, `gone`, `url`, `networkIdle`, `timeout` (ms). Returns 408 on timeout |
| click, type_text, fill, press_key, hover, select_option, scroll, drag, upload_file, click_at | `POST /action` | `kind` + `ref`/`selector` + `text`, `key`, `value`, `scrollX`/`scrollY`, `x`/`y`, `count`, `toRef`/`toSelector`, `files`, `waitNav`, `fast` |
| fill_form, batch_actions | `POST /actions` | `actions: [{kind, …}]`, `stopOnError` |
| evaluate | `POST /evaluate` | `expression` (promises awaited); with `ref`/`selector` the expression is `el => …` |
| network_requests | `GET /network` | `type` (comma list), `filter`, `method`, `status` 2xx\|4xx\|failed\|404, `all`, `headers`, `limit`, `offset` |
| network_request | `GET /network/request` | `id`, `body=false`, `maxBytes`, `saveTo`, `base64` |
| websocket_messages | `GET /network/websocket` | `filter`, `dir` sent\|received\|sse, `all`, `limit` |
| export_har | `GET /network/har` | `path`, `bodies`, `all` |
| performance_metrics | `GET /performance` | |
| console_messages | `GET /console` | `level` (comma list), `all`, `limit` |
| clear_logs | `POST /network/clear`, `POST /console/clear` | `tabId` |
| handle_dialog | `GET /dialog`, `POST /dialog` | `action` accept\|dismiss, `promptText` |
| emulate | `POST /emulate` | `viewport`, `userAgent`, `colorScheme`, `reducedMotion`, `geolocation`, `network`, `cpuThrottling`, `extraHeaders`, `timezone`, `locale` |
| save_pdf | `GET /pdf` | `path`, `landscape`, `printBackground`, `raw=true` |
| downloads | `GET /downloads` | |
| cookies_get / cookies_set | `GET /cookies`, `POST /cookies` | `url`, `name` / `url`, `cookies: [{name, value, domain, path, secure, httpOnly, sameSite, expires}]` |
| health / stealth_status | `GET /health`, `GET /stealth/status` | |
| fingerprint_rotate | `POST /fingerprint/rotate` | `os` windows\|mac\|random, `browser`, `screen`, `language` |
| — | `POST /shutdown` | stops server and Chrome (deliberately not exposed as an MCP tool) |
| — | `POST /mcp` | MCP over Streamable HTTP (JSON-RPC) |

Action kinds for `/action` and `/actions`: `click`, `dblclick`, `clickAt`, `type`, `fill`, `press`,
`focus`, `hover`, `select`, `scroll`, `drag`, `upload`, `humanClick`, `humanType`.

## Environment

| Variable | Default | Purpose |
|---|---|---|
| `BRIDGE_PORT` | 9867 | HTTP port |
| `TYPESAFE_API_KEY`, `OPENROUTER_API_KEY`, `AI_GATEWAY_API_KEY` | – | Jev decider for `/goal` (`TYPESAFE_BASE_URL`, `JEV_PROVIDER`, `JEV_MODEL` optional) |
| `BRIDGE_BIND` | 127.0.0.1 | Listen address (non-loopback requires a token) |
| `BRIDGE_ALLOWED_ORIGINS` | – | Web origins allowed to call the API |
| `BRIDGE_TOKEN` | – | Bearer token (set it!) |
| `BRIDGE_HEADLESS` | false (`seebro mcp` autostart: true) | Hide the Chrome window |
| `BRIDGE_PROFILE` | `~/.seebro/chrome-profile` | Persistent profile |
| `BRIDGE_DOWNLOAD_DIR` | `~/.seebro/downloads` | Where downloads land |
| `BRIDGE_DIALOG` | manual | `accept` / `dismiss` answers JS dialogs automatically |
| `BRIDGE_NETWORK_BUFFER` | 300 | Requests remembered per tab |
| `BRIDGE_CONSOLE_BUFFER` | 200 | Console messages remembered per tab |
| `BRIDGE_TAB_IDLE` | 30m headless, off headed | Close unused tabs |
| `BRIDGE_RESTORE` | false headless | Reopen last session's tabs |
| `BRIDGE_SITE_ISOLATION` | false | true makes cross-origin iframes unreachable |
| `BRIDGE_LOG_LEVEL` | info | debug logs every request |
| `SEEBRO_URL` | `http://127.0.0.1:$BRIDGE_PORT` | Server that `seebro mcp` talks to |
| `SEEBRO_NO_AUTOSTART` | – | `true`: `seebro mcp` never starts a server |
