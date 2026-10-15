package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// `seebro mcp` — MCP over stdio for Claude Code / Claude Desktop.
//
// The process is a small proxy: it forwards each JSON-RPC line to the
// /mcp endpoint of a running Seebro server and starts that server in the
// background if none is reachable. All MCP clients therefore share one
// Chrome instead of each spawning their own.

func runMCPStdio() {
	base := strings.TrimRight(envOr("SEEBRO_URL", "http://127.0.0.1:"+port), "/")
	client := &http.Client{Timeout: 3 * time.Minute}
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, "seebro mcp: "+format+"\n", a...) }

	if err := ensureServer(base, logf); err != nil {
		logf("%v", err)
	}

	out := bufio.NewWriter(os.Stdout)
	var outMu sync.Mutex
	write := func(data []byte) {
		outMu.Lock()
		defer outMu.Unlock()
		_, _ = out.Write(bytes.TrimSpace(data))
		_ = out.WriteByte('\n')
		_ = out.Flush()
	}

	var wg sync.WaitGroup
	in := bufio.NewReaderSize(os.Stdin, 1<<16)
	for {
		line, err := in.ReadBytes('\n')
		if line = bytes.TrimSpace(line); len(line) > 0 {
			wg.Add(1)
			// Requests run concurrently so a long wait_for doesn't block ping/cancel.
			go func(msg []byte) {
				defer wg.Done()
				if resp := forwardMCP(client, base, msg, logf); resp != nil {
					write(resp)
				}
			}(line)
		}
		if err != nil {
			if err != io.EOF {
				logf("stdin: %v", err)
			}
			break
		}
	}
	wg.Wait()
}

// forwardMCP posts one message to the server and returns the response line
// (nil for notifications). Transport errors become JSON-RPC errors.
func forwardMCP(client *http.Client, base string, msg []byte, logf func(string, ...any)) []byte {
	var head struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(msg, &head)

	post := func() (*http.Response, error) {
		req, err := http.NewRequest("POST", base+"/mcp", bytes.NewReader(msg))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}

	resp, err := post()
	if err != nil {
		// Server went away (stopped, crashed, laptop slept) — start it again once.
		if e := ensureServer(base, logf); e == nil {
			resp, err = post()
		}
	}
	if err != nil {
		return rpcErrorLine(head.ID, -32000, "seebro server unreachable: "+err.Error())
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	switch {
	case resp.StatusCode == http.StatusAccepted || len(bytes.TrimSpace(body)) == 0:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return rpcErrorLine(head.ID, -32002, "the seebro server at "+base+" has no /mcp endpoint (older version) — restart it with the new binary")
	case resp.StatusCode == http.StatusUnauthorized:
		return rpcErrorLine(head.ID, -32001, "seebro rejected the token: set BRIDGE_TOKEN (or token in ~/.seebro/config.json) to the server's token")
	case resp.StatusCode >= 400 && !json.Valid(body):
		return rpcErrorLine(head.ID, -32000, fmt.Sprintf("seebro HTTP %d: %s", resp.StatusCode, truncate(string(body), 500)))
	}
	if len(head.ID) == 0 || string(head.ID) == "null" {
		return nil // notification: never answer, even if the server did
	}
	return body
}

func rpcErrorLine(id json.RawMessage, code int, msg string) []byte {
	if len(id) == 0 {
		return nil
	}
	b, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
	return b
}

// ensureServer checks /health and, for a local URL, launches `seebro` in the
// background when nothing answers.
func ensureServer(base string, logf func(string, ...any)) error {
	switch status := probe(base); status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("server at %s requires a different token", base)
	case 0:
		// not reachable — try to start it below
	default:
		return fmt.Errorf("server at %s answered HTTP %d", base, status)
	}

	u, err := url.Parse(base)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		return fmt.Errorf("server at %s not reachable", base)
	}
	if os.Getenv("SEEBRO_NO_AUTOSTART") == "true" {
		return fmt.Errorf("server at %s not reachable (autostart disabled)", base)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return err
	}
	logPath := filepath.Join(stateDir, "seebro.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(exe)
	cmd.Env = os.Environ()
	if os.Getenv("BRIDGE_HEADLESS") == "" {
		cmd.Env = append(cmd.Env, "BRIDGE_HEADLESS=true")
	}
	if p := u.Port(); p != "" {
		cmd.Env = append(cmd.Env, "BRIDGE_PORT="+p)
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start seebro: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	logf("started seebro server (pid %d, log %s)", pid, logPath)

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if probe(base) == http.StatusOK {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("seebro did not become ready within 45s, see %s", logPath)
}

// probe returns the /health status code, or 0 if the server is unreachable.
func probe(base string) int {
	req, err := http.NewRequest("GET", base+"/health", nil)
	if err != nil {
		return 0
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}
