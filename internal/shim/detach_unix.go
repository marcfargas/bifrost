//go:build !windows

package shim

import (
	"os/exec"
	"syscall"
)

// detachProcess configures cmd to run as a new session leader so it survives
// after the parent exits.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
}
