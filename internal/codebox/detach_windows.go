//go:build windows

package codebox

import (
	"os/exec"
	"syscall"
)

// detachedProcess is not exported by the standard library's syscall package.
// See Process Creation Flags (WinBase.h).
const detachedProcess = 0x00000008

func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// The Windows counterpart of Setsid: the child leaves the parent's
		// console and process group, so it outlives the CLI and is unaffected
		// by the terminal's Ctrl+C. CREATE_NEW_PROCESS_GROUP disables Ctrl+C
		// for the new group; DETACHED_PROCESS drops the inherited console, which
		// is what survives the terminal window closing. Stdio is always
		// redirected to a log file, so the child needs no console.
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	}
}
