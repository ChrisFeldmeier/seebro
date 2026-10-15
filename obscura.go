package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// obscura engine: a Chrome-free headless browser (https://github.com/h4ckf0r0day/obscura,
// Apache-2.0) that speaks CDP. Seebro runs it as a sidecar process and drives
// it with the same code as Chrome.
//
// Two ways to use it:
//   - BRIDGE_ENGINE=obscura   obscura is the only browser (no Chrome needed)
//   - default (chrome)        Chrome is primary; tabs opened with
//                             {"engine":"obscura"} run in obscura, started on first use
//
// obscura is much lighter for simple pages but slower on very large ones and
// lacks some browser features (iframes, scrolling, downloads, dialogs); see
// engineCaps for what Seebro refuses up front on obscura tabs.

const (
	engineChrome  = "chrome"
	engineObscura = "obscura"
)

var (
	engineMode     = strings.ToLower(envOr("BRIDGE_ENGINE", engineChrome)) // chrome | obscura | auto
	obscuraBinary  = os.Getenv("OBSCURA_BINARY")
	obscuraStealth = os.Getenv("BRIDGE_OBSCURA_STEALTH") != "false"
	// Chrome can reach localhost too, so obscura gets the same access unless
	// the operator opts into its SSRF protection.
	obscuraPrivateNet = os.Getenv("BRIDGE_OBSCURA_PRIVATE_NETWORK") != "false"
	// obscura runs every page of a process on one V8 thread; worker processes
	// let tabs run in parallel (0 = obscura's default of 1).
	obscuraWorkers = envInt("BRIDGE_OBSCURA_WORKERS", 0)
)

// findObscura locates the obscura binary: OBSCURA_BINARY, next to seebro,
// ~/.seebro/bin, then PATH.
func findObscura() (string, error) {
	if obscuraBinary != "" {
		if _, err := os.Stat(obscuraBinary); err != nil {
			return "", fmt.Errorf("OBSCURA_BINARY %s: %w", obscuraBinary, err)
		}
		return obscuraBinary, nil
	}
	name := "obscura"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), name))
	}
	candidates = append(candidates, filepath.Join(stateDir, "bin", name))
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("obscura not found: set OBSCURA_BINARY, put it next to seebro or in ~/.seebro/bin (scripts/install-obscura.sh)")
}

// chromeAvailable reports whether a Chrome/Chromium executable can be found.
func chromeAvailable() bool {
	if chromeBinary != "" || cdpURL != "" {
		return true
	}
	for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "headless_shell"} {
		if _, err := exec.LookPath(n); err == nil {
			return true
		}
	}
	for _, p := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		`C:\Program Files\Google\Chrome\Application\chrome.exe`,
		`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
	} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// primaryEngine resolves BRIDGE_ENGINE=auto.
func primaryEngine() string {
	switch engineMode {
	case engineObscura:
		return engineObscura
	case "auto":
		if chromeAvailable() {
			return engineChrome
		}
		return engineObscura
	}
	return engineChrome
}

// obscuraProc is a running `obscura serve`.
type obscuraProc struct {
	cmd *exec.Cmd
	ws  string
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = l.Close() }()
	_, p, err := net.SplitHostPort(l.Addr().String())
	return p, err
}

// startObscura launches `obscura serve` on a free loopback port and waits
// until its CDP endpoint answers.
func startObscura() (*obscuraProc, error) {
	bin, err := findObscura()
	if err != nil {
		return nil, err
	}
	p, err := freePort()
	if err != nil {
		return nil, err
	}
	args := []string{"serve", "--host", "127.0.0.1", "--port", p, "--allow-file-access", "--quiet",
		"--storage-dir", filepath.Join(stateDir, "obscura-storage")}
	if obscuraStealth {
		args = append(args, "--stealth")
	}
	if obscuraPrivateNet {
		args = append(args, "--allow-private-network")
	}
	if obscuraWorkers > 1 {
		args = append(args, "--workers", strconv.Itoa(obscuraWorkers))
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(stateDir, "obscura.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start obscura: %w", err)
	}
	go func() { _ = cmd.Wait(); _ = logFile.Close() }()

	base := "http://127.0.0.1:" + p
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/json/version")
		if err == nil {
			var v struct {
				WS string `json:"webSocketDebuggerUrl"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&v)
			_ = resp.Body.Close()
			if v.WS != "" {
				slog.Info("obscura started", "pid", cmd.Process.Pid, "endpoint", v.WS, "binary", bin)
				return &obscuraProc{cmd: cmd, ws: v.WS}, nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	return nil, fmt.Errorf("obscura did not answer on port %s within 20s (see %s)", p, filepath.Join(stateDir, "obscura.log"))
}

func (o *obscuraProc) stop() {
	if o != nil && o.cmd != nil && o.cmd.Process != nil {
		_ = o.cmd.Process.Kill()
	}
}

// altEngine is the secondary obscura browser next to a primary Chrome.
type altEngine struct {
	once       sync.Once
	err        error
	proc       *obscuraProc
	browserCtx context.Context
	cancel     context.CancelFunc
}

// obscuraEngine returns the browser context for obscura tabs: the primary
// browser in obscura mode, otherwise the lazily started secondary one.
func (b *Bridge) obscuraEngine() (context.Context, error) {
	if b.primary == engineObscura {
		return b.browserCtx, nil
	}
	a := &b.alt
	a.once.Do(func() {
		a.proc, a.err = startObscura()
		if a.err != nil {
			return
		}
		allocCtx, allocCancel := chromedp.NewRemoteAllocator(context.Background(), a.proc.ws)
		bctx, bcancel := chromedp.NewContext(allocCtx)
		if err := chromedp.Run(bctx); err != nil {
			bcancel()
			allocCancel()
			a.proc.stop()
			a.err = fmt.Errorf("connect obscura: %w", err)
			return
		}
		a.browserCtx = withEngine(bctx, engineObscura)
		a.cancel = func() { bcancel(); allocCancel() }
		initial := string(chromedp.FromContext(bctx).Target.TargetID)
		b.mu.Lock()
		b.tabs[initial] = &TabEntry{ctx: a.browserCtx}
		b.tabEngine[initial] = engineObscura
		b.altInitial = initial
		b.mu.Unlock()
	})
	return a.browserCtx, a.err
}

// shutdownEngines stops the secondary obscura and, in obscura mode, the primary sidecar.
func (b *Bridge) shutdownEngines() {
	if b.alt.cancel != nil {
		b.alt.cancel()
	}
	b.alt.proc.stop()
	b.primaryProc.stop()
}

// engineOf reports which browser runs a tab.
func (b *Bridge) engineOf(tabID string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if e, ok := b.tabEngine[tabID]; ok {
		return e
	}
	return b.primary
}

// browserFor returns the browser-level context that owns a tab.
func (b *Bridge) browserFor(tabID string) context.Context {
	if b.engineOf(tabID) == engineObscura && b.primary != engineObscura && b.alt.browserCtx != nil {
		return b.alt.browserCtx
	}
	return b.browserCtx
}

// altTargets lists the secondary engine's page targets (nil if not running).
func (b *Bridge) altTargets() []*target.Info {
	if b.primary == engineObscura || b.alt.browserCtx == nil {
		return nil
	}
	var targets []*target.Info
	if err := chromedp.Run(b.alt.browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		targets, err = target.GetTargets().Do(ctx)
		return err
	})); err != nil {
		return nil
	}
	out := targets[:0]
	b.mu.Lock()
	for _, t := range targets {
		if t.Type == targetTypePage {
			b.tabEngine[string(t.TargetID)] = engineObscura
			out = append(out, t)
		}
	}
	b.mu.Unlock()
	return out
}

// engineUnsupported returns an error for features obscura does not provide,
// so callers get a clear message instead of a timeout or silent no-op.
func (b *Bridge) engineUnsupported(tabID, feature string) error {
	if b.engineOf(tabID) != engineObscura {
		return nil
	}
	return fmt.Errorf("%s is not supported by the obscura engine; open the tab with engine \"chrome\" for this", feature)
}

type engineKey struct{}

// withEngine tags a tab context with its engine so tab-level helpers
// (stealth injection, capability checks) can tell them apart.
func withEngine(ctx context.Context, engine string) context.Context {
	return context.WithValue(ctx, engineKey{}, engine)
}

func engineFromCtx(ctx context.Context) string {
	if e, ok := ctx.Value(engineKey{}).(string); ok {
		return e
	}
	return ""
}

// openTab opens a tab on an engine, optionally in an isolated context
// (contexts are a Chrome feature).
func (b *Bridge) openTab(engine, contextName, url string) (string, context.Context, context.CancelFunc, error) {
	if engine == "" {
		engine = b.primary
	}
	if contextName != "" {
		if engine != engineChrome || b.primary != engineChrome {
			return "", nil, nil, fmt.Errorf("isolated contexts need the chrome engine")
		}
		return b.CreateTabInContext(contextName, url)
	}
	return b.CreateTabOnEngine(engine, url)
}

// obscuraDownload handles clicks on <a download> in obscura, which has no
// download manager (the click would navigate to the file instead). The file is
// fetched inside the page (with its cookies) and saved to the download folder.
// handled=false means: not a download link, do a normal click.
func obscuraDownload(ctx context.Context, nodeID int64, selector string) (bool, map[string]any, error) {
	raw, err := callOnTarget(ctx, nodeID, selector, `async function() {
		if (this.tagName !== "A" || !this.hasAttribute("download") || !this.href) return null;
		const r = await fetch(this.href, {credentials: "include"});
		const buf = new Uint8Array(await r.arrayBuffer());
		if (buf.length > 50 * 1024 * 1024) throw new Error("download larger than 50 MB");
		let bin = "";
		for (let i = 0; i < buf.length; i += 0x8000) bin += String.fromCharCode.apply(null, buf.subarray(i, i + 0x8000));
		const name = this.getAttribute("download") || decodeURIComponent(new URL(this.href).pathname.split("/").pop() || "download");
		return {url: this.href, name, status: r.status, data: btoa(bin)};
	}`)
	if err != nil {
		return false, nil, err
	}
	var d struct {
		URL, Name, Data string
		Status          int
	}
	if string(raw) == "null" || json.Unmarshal(raw, &d) != nil || d.URL == "" {
		return false, nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(d.Data)
	if err != nil {
		return true, nil, err
	}
	name := filepath.Base(filepath.Clean("/" + d.Name)) // no path traversal from the page
	if name == "/" || name == "." {
		name = "download"
	}
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		return true, nil, err
	}
	path := filepath.Join(downloadDir, name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return true, nil, err
	}
	downloads.Lock()
	downloads.list = append(downloads.list, &downloadInfo{GUID: "obscura-" + name, URL: d.URL, File: name, State: "completed",
		Received: int64(len(data)), Total: int64(len(data)), Path: path})
	if len(downloads.list) > 50 {
		downloads.list = downloads.list[1:]
	}
	downloads.Unlock()
	return true, map[string]any{"clicked": true, "downloaded": path, "size": len(data), "status": d.Status}, nil
}
