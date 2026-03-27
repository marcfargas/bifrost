package shim

import (
	"fmt"
	"os"
	"os/exec"
)

// autoStartHub launches `bifrost hub start` as a detached background process.
// It returns immediately without waiting for the process.
func autoStartHub() error {
	exePath, err := os.Executable()
	if err != nil {
		exePath = "bifrost"
	}

	cmd := exec.Command(exePath, "hub", "start")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	detachProcess(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("autostart hub: %w", err)
	}

	// Release the child so we don't hold it as a zombie.
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("autostart hub: release: %w", err)
	}

	return nil
}
