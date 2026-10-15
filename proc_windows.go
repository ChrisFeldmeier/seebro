//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// detach starts the child without a console so it outlives the MCP client.
func detach(cmd *exec.Cmd) {
	const createNewProcessGroup, detachedProcess = 0x00000200, 0x00000008
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}
