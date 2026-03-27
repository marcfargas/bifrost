//go:build windows

package shim

import (
	"os/exec"
	"syscall"
)

// detachProcess configures cmd to run in a new process group so it survives
// after the parent exits.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
