#!/usr/bin/env python3
"""Load test for a running Seebro server (stdlib only).

Scenarios (each worker opens its own tab on the chosen engine):
  small  – small local pages: navigate, snapshot, text
  large  – a large local page (~25k elements): navigate, snapshot, text, markdown, viewport snapshot
  flow   – click through a local shop: category → product → add to cart → cart → checkout form → order
  real   – one gentle pass over public sites (Wikipedia, GitHub, Hacker News)

Example:
  python3 scripts/loadtest.py --site http://127.0.0.1:8765 --engine obscura --workers 4 --duration 60 --scenario flow

Create the test site once and serve it:
  python3 scripts/loadtest.py --write-site /tmp/seebro-site
  python3 -m http.server 8765 --bind 127.0.0.1 --directory /tmp/seebro-site
"""
import argparse, json, os, random, re, statistics, subprocess, sys, threading, time, urllib.request

def api(base, token, method, path, body=None, timeout=90):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method)
    if token:
        req.add_header("Authorization", "Bearer " + token)
    if data:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read()
            ctype = r.headers.get("Content-Type", "")
            return r.status, (json.loads(raw) if "json" in ctype else raw.decode("utf-8", "replace"))
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, raw.decode("utf-8", "replace")
    except Exception as e:  # timeouts, connection errors
        return 0, {"error": str(e)}

class Stats:
    def __init__(self):
        self.lock = threading.Lock()
        self.ops = {}       # op -> [ms]
        self.errors = {}    # op -> count
        self.samples = {}   # op -> first error
        self.flows = 0
        self.flow_fail = 0

    def add(self, op, ms, ok, err=None):
        with self.lock:
            self.ops.setdefault(op, []).append(ms)
            if not ok:
                self.errors[op] = self.errors.get(op, 0) + 1
                self.samples.setdefault(op, str(err)[:160])

class Worker(threading.Thread):
    def __init__(self, n, args, stats, stop):
        super().__init__(daemon=True)
        self.n, self.a, self.stats, self.stop = n, args, stats, stop
        self.tab = None

    def call(self, op, method, path, body=None, check=None):
        t0 = time.perf_counter()
        status, res = api(self.a.base, self.a.token, method, path, body)
        ms = (time.perf_counter() - t0) * 1000
        ok = status == 200 and (check is None or check(res))
        self.stats.add(op, ms, ok, res if not ok else None)
        return ok, res

    def q(self, path):
        return path + ("&" if "?" in path else "?") + "tabId=" + self.tab

    def snapshot(self, op="snapshot", extra=""):
        ok, res = self.call(op, "GET", self.q("/snapshot?filter=interactive&format=compact" + extra), check=lambda r: isinstance(r, str))
        return res if ok else ""

    def ref(self, snap, pattern):
        for line in snap.splitlines():
            if re.search(pattern, line):
                return line.split(":", 1)[0]
        return None

    def nav(self, url, op="navigate"):
        return self.call(op, "POST", "/navigate", {"tabId": self.tab, "url": url, "timeout": 60})[0]

    def wait(self, op, **cond):
        cond.setdefault("timeout", 20000)
        return self.call(op, "POST", "/wait", dict(cond, tabId=self.tab))[0]

    def act(self, op, **a):
        return self.call(op, "POST", "/action", dict(a, tabId=self.tab))[0]

    # ── scenarios ────────────────────────────────────────────
    def small(self):
        url = random.choice([self.a.site + "/index.html", self.a.site + "/page2.html"])
        self.nav(url)
        self.snapshot()
        self.call("text", "GET", self.q("/text"))

    def large(self):
        self.nav(self.a.site + "/big.html", "navigate_large")
        self.snapshot("snapshot_large")
        self.snapshot("snapshot_large_viewport", "&viewport=true")
        self.call("text_large", "GET", self.q("/text"))
        self.call("markdown_large", "GET", self.q("/text?mode=markdown&maxChars=30000"))

    def flow(self):
        s = self.a.site + "/shop"
        if not self.nav(s + "/index.html", "flow_home"):
            return False
        snap = self.snapshot("flow_snapshot")
        cat = self.ref(snap, r':link "(Laptops|Phones|Audio|Cameras)"')
        if not cat or not self.act("flow_click_category", kind="click", ref=cat):
            return False
        if not self.wait("flow_wait_products", selector=".card a"):
            return False
        snap = self.snapshot("flow_snapshot")
        prods = [l.split(":", 1)[0] for l in snap.splitlines() if re.search(r':link "(Ultra|Pro|Air|Max|Mini) ', l)]
        if not prods or not self.act("flow_click_product", kind="click", ref=random.choice(prods)):
            return False
        if not self.wait("flow_wait_product", text=["$"], selector="#add"):
            return False
        self.act("flow_select_qty", kind="select", selector="#qty", value=str(random.randint(1, 3)))
        if not self.act("flow_add_to_cart", kind="click", selector="#add") or not self.wait("flow_wait_added", text="Added!"):
            return False
        snap = self.snapshot("flow_snapshot")
        cart = self.ref(snap, r':link "Cart')
        if not cart or not self.act("flow_click_cart", kind="click", ref=cart) or not self.wait("flow_wait_cart", text="Total:"):
            return False
        self.call("flow_cart_text", "GET", self.q("/text"))
        snap = self.snapshot("flow_snapshot")
        co = self.ref(snap, r':link "Checkout"')
        if not co or not self.act("flow_click_checkout", kind="click", ref=co) or not self.wait("flow_wait_checkout", selector="#email"):
            return False
        ok, _ = self.call("flow_fill_form", "POST", "/actions", {"tabId": self.tab, "stopOnError": True, "actions": [
            {"kind": "fill", "selector": "#name", "text": f"Load Tester {self.n}"},
            {"kind": "fill", "selector": "#email", "text": f"lt{self.n}@example.com"},
            {"kind": "fill", "selector": "#country", "text": "Austria"},
            {"kind": "fill", "selector": "#terms", "text": "true"},
            {"kind": "click", "selector": "button[type=submit]"},
        ]}, check=lambda r: isinstance(r, dict) and r.get("failed") == 0)
        return ok and self.wait("flow_wait_order", text="Order confirmed")

    def real(self):
        for url in ["https://news.ycombinator.com/", "https://en.wikipedia.org/wiki/Germany", "https://github.com/chromedp/chromedp"]:
            name = url.split("/")[2].split(".")[-2]
            self.nav(url, "real_nav_" + name)
            self.snapshot("real_snapshot_" + name)
            self.snapshot("real_snapshot_viewport_" + name, "&viewport=true")
            self.call("real_text_" + name, "GET", self.q("/text?maxChars=30000"))

    def run(self):
        body = {"action": "new", "url": "about:blank"}
        if self.a.engine:
            body["engine"] = self.a.engine
        t0 = time.perf_counter()
        status, res = api(self.a.base, self.a.token, "POST", "/tab", body)
        self.stats.add("tab_new", (time.perf_counter() - t0) * 1000, status == 200, res)
        if status != 200:
            return
        self.tab = res["tabId"]
        try:
            while not self.stop.is_set():
                sc = self.a.scenario if self.a.scenario != "mixed" else random.choice(["small", "large", "flow"])
                if sc == "flow":
                    ok = self.flow()
                    with self.stats.lock:
                        self.stats.flows += 1
                        self.stats.flow_fail += 0 if ok else 1
                else:
                    getattr(self, sc)()
                if self.a.scenario == "real":
                    break
        finally:
            api(self.a.base, self.a.token, "POST", "/tab", {"action": "close", "tabId": self.tab})

SHOP_HEAD = '<!doctype html><html><head><meta charset="utf-8"><title>{t}</title><style>body{{font-family:sans-serif}} .card{{border:1px solid #ccc;margin:4px;padding:6px;display:inline-block;width:180px}}</style></head><body><nav><a href="/shop/index.html">Home</a> | <a href="/shop/cart.html">Cart (<span id="cc">0</span>)</a></nav>'
SHOP_TAIL = '<script>const cart = JSON.parse(localStorage.getItem("cart")||"[]"); document.getElementById("cc").textContent = cart.length;</script></body></html>'

def write_site(site):
    """Generate the local test site: small pages, a JS shop and a large page."""
    rnd = random.Random(7)
    shop = os.path.join(site, "shop")
    os.makedirs(os.path.join(shop, "api"), exist_ok=True)
    cats = ["Laptops", "Phones", "Audio", "Cameras"]
    products = [{"id": i, "name": f"{rnd.choice(['Ultra','Pro','Air','Max','Mini'])} {c[:-1]} {i}", "cat": c, "price": round(rnd.uniform(49, 1999), 2)}
                for i, c in enumerate([c for c in cats for _ in range(25)], start=1)]
    json.dump(products, open(os.path.join(shop, "api", "products.json"), "w"))
    h, t = SHOP_HEAD, SHOP_TAIL
    pages = {
        "index.html": h.format(t="Shop Home") + "<h1>Shop</h1><ul>" + "".join(f'<li><a href="/shop/category.html?c={c}">{c}</a></li>' for c in cats) + "</ul>" + t,
        "category.html": h.format(t="Category") + """<h1 id="h">Category</h1><div id="list">Loading…</div><script>
const c = new URLSearchParams(location.search).get("c"); document.getElementById("h").textContent = c;
fetch("/shop/api/products.json").then(r => r.json()).then(ps => { document.getElementById("list").innerHTML = ps.filter(p => p.cat === c).map(p =>
  `<div class="card"><a href="/shop/product.html?id=${p.id}">${p.name}</a><div>$${p.price}</div></div>`).join(""); });
</script>""" + t,
        "product.html": h.format(t="Product") + """<h1 id="name">…</h1><p id="price"></p>
<label>Quantity <select id="qty"><option>1</option><option>2</option><option>3</option></select></label>
<button id="add">Add to cart</button> <span id="msg"></span><script>
const id = +new URLSearchParams(location.search).get("id");
fetch("/shop/api/products.json").then(r => r.json()).then(ps => { const p = ps.find(p => p.id === id);
  document.getElementById("name").textContent = p.name; document.getElementById("price").textContent = "$" + p.price;
  document.getElementById("add").onclick = () => { const cart = JSON.parse(localStorage.getItem("cart") || "[]");
    cart.push({id, qty: +document.getElementById("qty").value, price: p.price, name: p.name});
    localStorage.setItem("cart", JSON.stringify(cart)); document.getElementById("cc").textContent = cart.length;
    document.getElementById("msg").textContent = "Added!"; }; });
</script>""" + t,
        "cart.html": h.format(t="Cart") + """<h1>Cart</h1><table id="items"></table><p id="total"></p><a id="checkout" href="/shop/checkout.html">Checkout</a><script>
const items = JSON.parse(localStorage.getItem("cart") || "[]");
document.getElementById("items").innerHTML = "<tr><th>Item</th><th>Qty</th><th>Price</th></tr>" + items.map(i => `<tr><td>${i.name}</td><td>${i.qty}</td><td>${i.price}</td></tr>`).join("");
document.getElementById("total").textContent = "Total: $" + items.reduce((s, i) => s + i.qty * i.price, 0).toFixed(2);
</script>""" + t,
        "checkout.html": h.format(t="Checkout") + """<h1>Checkout</h1><form id="f"><label>Name <input id="name" required></label><label>Email <input id="email" type="email" required></label>
<label>Country <select id="country"><option>Germany</option><option>Austria</option><option>Switzerland</option></select></label>
<label><input type="checkbox" id="terms"> Accept terms</label><button type="submit">Place order</button></form><p id="result"></p><script>
document.getElementById("f").onsubmit = (e) => { e.preventDefault();
  if (!document.getElementById("terms").checked) { document.getElementById("result").textContent = "Please accept the terms"; return; }
  const n = JSON.parse(localStorage.getItem("cart") || "[]").length; localStorage.setItem("cart", "[]");
  document.getElementById("result").textContent = "Order confirmed for " + document.getElementById("name").value + " (" + n + " items)"; };
</script>""" + t,
    }
    for name, html in pages.items():
        open(os.path.join(shop, name), "w").write(html)
    rows = "".join(f"<tr><td>{i}</td><td>Item {i}</td><td><a href='#r{i}'>detail {i}</a></td><td>{rnd.random():.4f}</td></tr>" for i in range(300))
    paras = "".join(f"<section><h2>Section {s}</h2>" + "".join(f"<p>Paragraph {s}.{k} " + " ".join(f"<a href='/big.html#{s}-{k}-{j}'>link{j}</a>" for j in range(8)) + " lorem ipsum dolor sit amet consectetur.</p>" for k in range(10)) + "</section>" for s in range(40))
    form = "<form>" + "".join(f"<label>Field {i} <input name='f{i}'></label>" for i in range(60)) + "<button>Send</button></form>"
    open(os.path.join(site, "big.html"), "w").write(f"<!doctype html><html><head><title>Big page</title></head><body><h1>Big page</h1>{form}<table>{rows}</table>{paras}</body></html>")
    open(os.path.join(site, "index.html"), "w").write("<!doctype html><title>Small page</title><h1>Small page</h1><p>Hello.</p><a href='/page2.html'>Next page</a><form><input name='q' placeholder='Search'><button>Go</button></form>")
    open(os.path.join(site, "page2.html"), "w").write("<!doctype html><title>Page Two</title><h1>Second page</h1><p>Back test</p><a href='/index.html'>Back</a>")
    print("site written to", site)

def tree_rss_mb(pid):
    try:
        out = subprocess.run(["ps", "-A", "-o", "pid=,ppid=,rss="], capture_output=True, text=True).stdout
    except Exception:
        return 0
    kids, rss = {}, {}
    for line in out.splitlines():
        p, pp, r = line.split()
        kids.setdefault(int(pp), []).append(int(p))
        rss[int(p)] = int(r)
    total, stack = 0, [pid]
    while stack:
        p = stack.pop()
        total += rss.get(p, 0)
        stack += kids.get(p, [])
    return total / 1024

def pct(xs, p):
    xs = sorted(xs)
    return xs[min(len(xs) - 1, int(round(p / 100 * (len(xs) - 1))))]

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:9867")
    ap.add_argument("--token", default=os.environ.get("BRIDGE_TOKEN", ""))
    ap.add_argument("--site", default="http://127.0.0.1:8765")
    ap.add_argument("--engine", default="", help="chrome | obscura (default: server's primary)")
    ap.add_argument("--scenario", default="flow", choices=["small", "large", "flow", "real", "mixed"])
    ap.add_argument("--workers", type=int, default=1)
    ap.add_argument("--duration", type=float, default=30)
    ap.add_argument("--pid", type=int, default=0, help="Seebro server PID for memory sampling")
    ap.add_argument("--json", default="", help="write the summary as JSON to this file")
    ap.add_argument("--write-site", default="", metavar="DIR", help="generate the test site into DIR and exit")
    a = ap.parse_args()
    if a.write_site:
        write_site(a.write_site)
        return

    stats, stop = Stats(), threading.Event()
    peak = [0.0]
    def sampler():
        while not stop.is_set():
            if a.pid:
                peak[0] = max(peak[0], tree_rss_mb(a.pid))
            time.sleep(0.5)
    threading.Thread(target=sampler, daemon=True).start()

    workers = [Worker(i, a, stats, stop) for i in range(a.workers)]
    t0 = time.time()
    for w in workers:
        w.start()
    if a.scenario == "real":
        for w in workers:
            w.join()
    else:
        time.sleep(a.duration)
        stop.set()
        for w in workers:
            w.join(timeout=120)
    stop.set()
    wall = time.time() - t0

    total_ops = sum(len(v) for v in stats.ops.values())
    total_err = sum(stats.errors.values())
    summary = {"engine": a.engine or "primary", "scenario": a.scenario, "workers": a.workers, "seconds": round(wall, 1),
               "ops": total_ops, "errors": total_err, "ops_per_sec": round(total_ops / wall, 1),
               "flows": stats.flows, "flow_failures": stats.flow_fail,
               "flows_per_min": round(stats.flows / wall * 60, 1) if stats.flows else 0,
               "peak_rss_mb": round(peak[0]), "per_op": {}}
    print(f"\n== {summary['engine']} · {a.scenario} · {a.workers} worker(s) · {wall:.0f}s ==")
    print(f"{'operation':34s} {'n':>5s} {'err':>4s} {'p50 ms':>8s} {'p95 ms':>8s} {'max ms':>8s}")
    for op in sorted(stats.ops):
        xs = stats.ops[op]
        row = {"n": len(xs), "errors": stats.errors.get(op, 0), "p50": round(statistics.median(xs)),
               "p95": round(pct(xs, 95)), "max": round(max(xs))}
        summary["per_op"][op] = row
        print(f"{op:34s} {row['n']:5d} {row['errors']:4d} {row['p50']:8d} {row['p95']:8d} {row['max']:8d}")
    print(f"ops {total_ops}  errors {total_err}  ops/s {summary['ops_per_sec']}", end="")
    if stats.flows:
        print(f"  flows {stats.flows} (failed {stats.flow_fail}, {summary['flows_per_min']}/min)", end="")
    if a.pid:
        print(f"  peak RSS {summary['peak_rss_mb']} MB", end="")
    print()
    for op, err in stats.samples.items():
        print(f"  first error in {op}: {err}")
    if a.json:
        json.dump(summary, open(a.json, "w"), indent=2)

if __name__ == "__main__":
    main()
