//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach starts the child in its own session so it outlives the MCP client.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
