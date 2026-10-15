package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Network exposure and browser-originated request protection.
//
// Seebro drives a browser with the user's real sessions, so a web page the
// user happens to visit must never be able to reach it. Browsers can send
// "simple" cross-origin requests (no CORS preflight) and load URLs via <img>,
// so CORS alone does not protect a localhost API. We therefore:
//   - listen on loopback by default (BRIDGE_BIND),
//   - reject requests that a browser marks as cross-site (Origin /
//     Sec-Fetch-Site) unless the origin is explicitly allowed,
//   - reject foreign Host headers on loopback (DNS rebinding),
//   - require a token whenever we listen beyond loopback.

var (
	bindAddr       = envOr("BRIDGE_BIND", "127.0.0.1")
	allowedOrigins = splitList(os.Getenv("BRIDGE_ALLOWED_ORIGINS"))
	allowedHosts   = splitList(os.Getenv("BRIDGE_ALLOWED_HOSTS"))
)

func isLoopbackHost(h string) bool {
	h = strings.Trim(h, "[]")
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func listenAddr() string { return net.JoinHostPort(bindAddr, port) }

// originAllowed reports whether a browser origin may call the API.
func originAllowed(origin string) bool {
	if origin == "http://localhost:"+port || origin == "http://127.0.0.1:"+port {
		return true // Seebro's own pages (welcome)
	}
	for _, o := range allowedOrigins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

func hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	if isLoopbackHost(host) {
		return true
	}
	for _, h := range allowedHosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

// guardMiddleware blocks cross-site browser requests and DNS rebinding.
// Non-browser clients (curl, SDKs, MCP) send neither header and pass.
func guardMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && !originAllowed(origin) {
			jsonErr(w, 403, fmt.Errorf("cross-origin request from %s blocked (allow it with BRIDGE_ALLOWED_ORIGINS)", origin))
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" && origin == "" {
			jsonErr(w, 403, fmt.Errorf("cross-site browser request blocked"))
			return
		}
		if isLoopbackHost(bindAddr) && !hostAllowed(r.Host) {
			jsonErr(w, 403, fmt.Errorf("host %q not allowed (DNS rebinding protection; see BRIDGE_ALLOWED_HOSTS)", r.Host))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ensureToken generates and persists a token when listening beyond loopback
// without one. Loopback-only servers may run without a token: the guard
// middleware keeps web pages out and other local processes are trusted.
func ensureToken() {
	if token != "" || isLoopbackHost(bindAddr) {
		return
	}
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		slog.Error("cannot generate token", "err", err)
		os.Exit(1)
	}
	token = hex.EncodeToString(buf)
	path := configFilePath()
	if err := saveConfigValue(path, "token", token); err != nil {
		slog.Warn("could not persist generated token", "path", path, "err", err)
	}
	// Printed once so container users can connect (like Jupyter does).
	slog.Warn("listening beyond loopback without BRIDGE_TOKEN: generated one", "token", token, "saved", path)
}

func configFilePath() string {
	return envOr("BRIDGE_CONFIG", filepath.Join(homeDir(), ".seebro", "config.json"))
}

// saveConfigValue sets one key in the JSON config file, keeping other keys.
func saveConfigValue(path, key string, value any) error {
	cfg := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}
	cfg[key] = value
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// outputPath validates a caller-supplied file path for writing. Paths inside
// the state dir are always fine; elsewhere hidden files and directories
// (dotfiles, ~/.ssh, …) are refused, and the extension must match when given.
func outputPath(p string, exts ...string) (string, error) {
	if p == "" {
		return "", nil
	}
	if strings.HasPrefix(p, "~/") {
		p = filepath.Join(homeDir(), p[2:])
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if len(exts) > 0 {
		ext := strings.ToLower(filepath.Ext(abs))
		ok := false
		for _, e := range exts {
			if ext == e {
				ok = true
			}
		}
		if !ok {
			return "", fmt.Errorf("path must end in %s", strings.Join(exts, ", "))
		}
	}
	if sd, err := filepath.Abs(stateDir); err == nil && strings.HasPrefix(abs, sd+string(filepath.Separator)) {
		return abs, nil
	}
	for _, part := range strings.Split(abs, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return "", fmt.Errorf("refusing to write into hidden path %q", abs)
		}
	}
	if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
		return "", fmt.Errorf("%s is a directory", abs)
	}
	return abs, nil
}
