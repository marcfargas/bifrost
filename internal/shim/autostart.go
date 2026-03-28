package shim

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/marcfargas/bifrost/internal/config"
)

// autoStartHub launches `bifrost hub start` as a detached background process.
// It returns immediately without waiting for the process.
func autoStartHub() error {
	exePath, err := os.Executable()
	if err != nil {
		exePath = "bifrost"
	}

	// Ensure the data directory exists (hub needs it for the DB).
	dataDir := config.DataDir()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("autostart hub: create data dir %s: %w", dataDir, err)
	}

	// Ensure the socket directory exists.
	sockDir := filepath.Dir(config.SocketPath())
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		return fmt.Errorf("autostart hub: create socket dir %s: %w", sockDir, err)
	}

	// Log hub stderr to a file for debugging auto-start issues.
	logPath := filepath.Join(dataDir, "hub.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logFile = nil // non-fatal — hub runs without logging
	}

	cmd := exec.Command(exePath, "hub", "start")
	cmd.Stdout = nil
	cmd.Stderr = logFile
	cmd.Stdin = nil

	detachProcess(cmd)

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return fmt.Errorf("autostart hub: %w", err)
	}

	// Release the child so we don't hold it as a zombie.
	// The log file handle is inherited by the child process.
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("autostart hub: release: %w", err)
	}

	return nil
}
