//go:build windows

package transport

import (
	"context"
	"fmt"
	"net"
)

// PipeListener implements Listener using a localhost TCP connection.
// True named pipe support via go-winio can be added later; for now
// localhost TCP on an OS-assigned port provides equivalent local-only semantics.
type PipeListener struct {
	ln net.Listener
}

// NewPipeListener creates a TCP listener on 127.0.0.1 with an OS-assigned port.
func NewPipeListener() (*PipeListener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("transport: listen tcp: %w", err)
	}
	return &PipeListener{ln: ln}, nil
}

// Accept waits for an incoming connection. Returns when ctx is cancelled
// or a connection arrives.
func (p *PipeListener) Accept(ctx context.Context) (*Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := p.ln.Accept()
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

// Close stops the listener.
func (p *PipeListener) Close() error {
	return p.ln.Close()
}

// Addr returns the actual "127.0.0.1:PORT" string the listener is bound to.
func (p *PipeListener) Addr() string {
	return p.ln.Addr().String()
}
