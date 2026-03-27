//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func detachHubProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
}

func killHubProcess(proc *os.Process) error {
	return proc.Signal(syscall.SIGTERM)
}
