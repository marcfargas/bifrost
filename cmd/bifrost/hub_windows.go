//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func detachHubProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

func killHubProcess(proc *os.Process) error {
	return proc.Kill()
}
