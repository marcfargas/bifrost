//go:build !windows

package transport

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// SocketListener implements Listener using a Unix domain socket.
type SocketListener struct {
	ln   net.Listener
	path string
}

// NewSocketListener creates a Unix domain socket listener at path.
// It creates parent directories (mode 0700), removes any stale socket file,
// starts listening, and chmods the socket to 0600.
func NewSocketListener(path string) (*SocketListener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("transport: mkdir %s: %w", dir, err)
	}

	// Remove stale socket file if present.
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("transport: remove stale socket %s: %w", path, err)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("transport: listen unix %s: %w", path, err)
	}

	if err := os.Chmod(path, 0600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("transport: chmod %s: %w", path, err)
	}

	return &SocketListener{ln: ln, path: path}, nil
}

// Accept waits for an incoming connection. Returns when ctx is cancelled
// or a connection arrives.
func (s *SocketListener) Accept(ctx context.Context) (*Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := s.ln.Accept()
		ch <- result{c, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return NewConn(r.conn), nil
	}
}

// Close stops the listener and removes the socket file.
func (s *SocketListener) Close() error {
	err := s.ln.Close()
	_ = os.Remove(s.path)
	return err
}

// Addr returns the path of the Unix domain socket.
func (s *SocketListener) Addr() string {
	return s.path
}
