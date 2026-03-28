package shim

import (
	"context"
	"fmt"
	"net"
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

	for range maxAttempts {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		conn, err = tryConnect()
		if err == nil {
			return conn, nil
		}
	}

	return nil, fmt.Errorf("could not connect to hub after autostart: %w", err)
}

// tryConnect dials the unix domain socket at config.SocketPath().
// Works on all platforms (Linux, macOS, Windows 10+).
func tryConnect() (*transport.Conn, error) {
	addr := config.SocketPath()
	nc, err := net.Dial("unix", addr)
	if err != nil {
		return nil, err
	}
	return transport.NewConn(nc), nil
}
