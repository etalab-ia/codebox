//go:build !windows

package codebox

import (
	"os/exec"
	"syscall"
)

func detachProcess(cmd *exec.Cmd) {
	// A new session has no controlling terminal, so the child outlives the CLI
	// and is unaffected by SIGHUP/SIGINT reaching the terminal's process group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
