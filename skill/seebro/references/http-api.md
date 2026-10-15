# Seebro HTTP API

Base URL `http://localhost:9867` (`BRIDGE_PORT`). If a token is set, send `Authorization: Bearer $TOKEN`
with every request. GET endpoints take query parameters. POST endpoints take a JSON body. Every
endpoint accepts `tabId`. Errors come back as `{"error": "…"}` with a 4xx/5xx status.

```bash
S=http://localhost:9867; A="Authorization: Bearer $BRIDGE_TOKEN"
TAB=$(curl -s -H "$A" -X POST $S/tab -d '{"action":"new","url":"https://example.com"}' | jq -r .tabId)
curl -s -H "$A" "$S/snapshot?tabId=$TAB&filter=interactive&format=compact"
curl -s -H "$A" -X POST $S/action -d "{\"tabId\":\"$TAB\",\"kind\":\"click\",\"ref\":\"e0\"}"
```

| MCP tool | Endpoint | Parameters |
|---|---|---|
| tabs_list | `GET /tabs` | |
| tab_new / tab_close / tab_focus | `POST /tab` | `action` new\|close\|focus, `url`, `tabId` |
| tab_lock / tab_unlock | `POST /tab/lock`, `/tab/unlock` | `tabId`, `owner`, `timeoutSec` |
| navigate | `POST /navigate` | `url`, `type` url\|back\|forward\|reload, `ignoreCache`, `newTab`, `timeout` (s), `waitTitle`, `blockImages`, `blockMedia` |
| get_text | `GET /text` | `mode=raw` |
| snapshot | `GET /snapshot` | `filter=interactive`, `format` compact\|text\|yaml, `diff`, `depth`, `selector`, `maxTokens`, `output=file`, `path` |
| screenshot | `GET /screenshot` | `format` jpeg\|png\|webp, `quality`, `fullPage`, `ref`/`selector`, `raw=true` (image bytes), `output=file`, `path` |
| inspect_element | `GET /element` | `ref`/`selector`, `html`, `styles` true\|all |
| wait_for | `POST /wait` | `text` (string or list), `selector`, `gone`, `url`, `networkIdle`, `timeout` (ms). Returns 408 on timeout |
| click, type_text, fill, press_key, hover, select_option, scroll, drag, upload_file, click_at | `POST /action` | `kind` + `ref`/`selector` + `text`, `key`, `value`, `scrollX`/`scrollY`, `x`/`y`, `count`, `toRef`/`toSelector`, `files`, `waitNav`, `fast` |
| fill_form, batch_actions | `POST /actions` | `actions: [{kind, …}]`, `stopOnError` |
| evaluate | `POST /evaluate` | `expression` (promises awaited); with `ref`/`selector` the expression is `el => …` |
| network_requests | `GET /network` | `type` (comma list), `filter`, `method`, `status` 2xx\|4xx\|failed\|404, `all`, `headers`, `limit`, `offset` |
| network_request | `GET /network/request` | `id`, `body=false`, `maxBytes`, `saveTo`, `base64` |
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
| `BRIDGE_TOKEN` | – | Bearer token (set it!) |
| `BRIDGE_HEADLESS` | false (`seebro mcp` autostart: true) | Hide the Chrome window |
| `BRIDGE_PROFILE` | `~/.seebro/chrome-profile` | Persistent profile |
| `BRIDGE_DOWNLOAD_DIR` | `~/.seebro/downloads` | Where downloads land |
| `BRIDGE_DIALOG` | manual | `accept` / `dismiss` answers JS dialogs automatically |
| `BRIDGE_NETWORK_BUFFER` | 300 | Requests remembered per tab |
| `BRIDGE_CONSOLE_BUFFER` | 200 | Console messages remembered per tab |
| `SEEBRO_URL` | `http://127.0.0.1:$BRIDGE_PORT` | Server that `seebro mcp` talks to |
| `SEEBRO_NO_AUTOSTART` | – | `true`: `seebro mcp` never starts a server |
