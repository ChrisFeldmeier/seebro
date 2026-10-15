#!/usr/bin/env python3
"""Record a video of Seebro browsing Wikipedia (search, scroll, follow links, go back).

  python3 scripts/demo-wikipedia.py --base http://127.0.0.1:9867 --out ~/Desktop/seebro-wikipedia.mp4

Uses only the HTTP API: /tab, /screencast/start, /navigate, /snapshot, /action, /wait,
/evaluate, /screencast/stop. Needs ffmpeg on the machine running Seebro.
"""
import argparse, json, os, re, sys, time, urllib.error, urllib.request

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:9867")
    ap.add_argument("--token", default=os.environ.get("BRIDGE_TOKEN", ""))
    ap.add_argument("--engine", default="")
    ap.add_argument("--out", default="")
    ap.add_argument("--query", default="Go (programming language)")
    a = ap.parse_args()

    def api(method, path, body=None, timeout=90):
        req = urllib.request.Request(a.base + path, data=json.dumps(body).encode() if body is not None else None, method=method)
        if a.token:
            req.add_header("Authorization", "Bearer " + a.token)
        req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                raw = r.read().decode()
                return json.loads(raw) if r.headers.get("Content-Type", "").startswith("application/json") else raw
        except urllib.error.HTTPError as e:
            raise SystemExit(f"{method} {path} failed: HTTP {e.code} {e.read().decode()[:300]}")

    def step(msg):
        print(f"[{time.time() - t0:5.1f}s] {msg}", flush=True)

    def snapshot():
        return api("GET", f"/snapshot?tabId={tab}&filter=interactive&format=compact&viewport=true")

    def ref(snap, pattern):
        for line in snap.splitlines():
            if re.search(pattern, line):
                return line.split(":", 1)[0]
        return None

    def smooth_scroll(px, pause=1.2):
        api("POST", "/evaluate", {"tabId": tab, "expression": f"window.scrollBy({{top: {px}, behavior: 'smooth'}}); true"})
        time.sleep(pause)

    def click_link(text):
        # Find the link anywhere on the page, glide it into view, then click it by ref.
        full = api("GET", f"/snapshot?tabId={tab}&filter=interactive&format=compact")
        r = ref(full, rf':link "{re.escape(text)}"')
        if not r:
            return False
        api("POST", "/evaluate", {"tabId": tab, "ref": r,
                                  "expression": "el => { el.scrollIntoView({behavior: 'smooth', block: 'center'}); return true; }"})
        time.sleep(1.3)
        api("POST", "/action", {"tabId": tab, "kind": "click", "ref": r})
        return True

    t0 = time.time()
    body = {"action": "new", "url": "about:blank"}
    if a.engine:
        body["engine"] = a.engine
    tab = api("POST", "/tab", body)["tabId"]
    api("POST", "/emulate", {"tabId": tab, "viewport": "1280x800"})
    api("POST", "/screencast/start", {"tabId": tab, "path": a.out, "maxWidth": 1280, "maxHeight": 800, "quality": 75})
    try:
        step("open Wikipedia")
        api("POST", "/navigate", {"tabId": tab, "url": "https://en.wikipedia.org/wiki/Main_Page", "waitUntil": "load"})
        time.sleep(1.5)

        step(f"search for {a.query!r}")
        box = ref(snapshot(), r':(searchbox|combobox|textbox) "Search Wikipedia')
        if not box:
            sys.exit("search box not found")
        api("POST", "/actions", {"tabId": tab, "stopOnError": True, "actions": [
            {"kind": "click", "ref": box},
            {"kind": "type", "ref": box, "text": a.query},
        ]})
        time.sleep(1.0)
        # The search field is re-rendered while typing, so press Enter on the focused element.
        api("POST", "/action", {"tabId": tab, "kind": "press", "key": "Enter"})
        api("POST", "/wait", {"tabId": tab, "url": "/wiki/Go_(programming_language)", "timeout": 20000})
        api("POST", "/wait", {"tabId": tab, "networkIdle": True, "timeout": 10000})
        time.sleep(1.5)

        step("read the article")
        for _ in range(3):
            smooth_scroll(450)

        step("follow the link to Rob Pike")
        if click_link("Rob Pike"):
            api("POST", "/wait", {"tabId": tab, "url": "Rob_Pike", "timeout": 20000})
            time.sleep(1.5)
            smooth_scroll(400)
            step("follow the link to Bell Labs")
            if click_link("Bell Labs"):
                api("POST", "/wait", {"tabId": tab, "url": "Bell_Labs", "timeout": 20000})
                time.sleep(1.5)
                smooth_scroll(500)

        step("go back twice")
        api("POST", "/navigate", {"tabId": tab, "type": "back"})
        time.sleep(1.5)
        api("POST", "/navigate", {"tabId": tab, "type": "back"})
        time.sleep(2.0)
    finally:
        res = api("POST", "/screencast/stop", {"tabId": tab}, timeout=180)
        api("POST", "/tab", {"action": "close", "tabId": tab})
    step(f"video: {res.get('path') or res}")
    print(json.dumps(res, indent=2))

if __name__ == "__main__":
    main()
