package shim

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/transport"
)

// connectToHub tries to connect to the hub. If no hub is running, it
// auto-starts one and retries.
func connectToHub(ctx context.Context) (*transport.Conn, error) {
	conn, err := tryConnect()
	if err == nil {
		return conn, nil
	}

	// Hub is not running — auto-start it.
	if startErr := autoStartHub(); startErr != nil {
		return nil, fmt.Errorf("hub not running and autostart failed: %w", startErr)
	}

	// Retry connection with backoff.
	const (
		maxAttempts = 20
		interval    = 100 * time.Millisecond
	)

	for attempt := range maxAttempts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		conn, err = tryConnect()
		if err == nil {
			return conn, nil
		}

		_ = attempt
	}

	return nil, fmt.Errorf("could not connect to hub after autostart: %w", err)
}

// tryConnect attempts a single connection to the hub.
func tryConnect() (*transport.Conn, error) {
	if runtime.GOOS == "windows" {
		return tryConnectWindows()
	}
	return tryConnectUnix()
}

// tryConnectUnix dials the Unix domain socket at config.SocketPath().
func tryConnectUnix() (*transport.Conn, error) {
	addr := config.SocketPath()
	nc, err := net.Dial("unix", addr)
	if err != nil {
		return nil, err
	}
	return transport.NewConn(nc), nil
}

// tryConnectWindows reads the PID file to get the TCP address, verifies the
// hub process is alive, and connects via TCP.
func tryConnectWindows() (*transport.Conn, error) {
	pidPath := config.PIDFilePath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return nil, fmt.Errorf("read pid file: %w", err)
	}

	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) < 2 {
		return nil, fmt.Errorf("malformed pid file: expected pid and address")
	}

	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return nil, fmt.Errorf("invalid pid in pid file: %w", err)
	}

	// Verify process is alive.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil, fmt.Errorf("hub process %d not found: %w", pid, err)
	}
	// On Windows, FindProcess always succeeds; we rely on the connection
	// attempt itself to fail if the process is gone.
	_ = proc

	addr := strings.TrimSpace(lines[1])
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return transport.NewConn(nc), nil
}
