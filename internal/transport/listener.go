package transport

import (
	"context"
	"encoding/json"
	"io"
	"sync"
)

// Conn wraps an io.ReadWriteCloser with JSON encoding/decoding and
// mutex-protected sends.
type Conn struct {
	rwc     io.ReadWriteCloser
	encoder *json.Encoder
	decoder *json.Decoder
	mu      sync.Mutex
}

// NewConn creates a Conn wrapping the given ReadWriteCloser.
func NewConn(rwc io.ReadWriteCloser) *Conn {
	return &Conn{
		rwc:     rwc,
		encoder: json.NewEncoder(rwc),
		decoder: json.NewDecoder(rwc),
	}
}

// Send encodes v as JSON and writes it to the connection. Thread-safe.
func (c *Conn) Send(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.encoder.Encode(v)
}

// Receive decodes the next JSON value from the connection into v.
func (c *Conn) Receive(v any) error {
	return c.decoder.Decode(v)
}

// Close closes the underlying connection.
func (c *Conn) Close() error {
	return c.rwc.Close()
}

// Listener is the interface implemented by platform-specific local transports.
type Listener interface {
	// Accept waits for and returns a new connection. Blocks until one arrives
	// or ctx is cancelled.
	Accept(ctx context.Context) (*Conn, error)
	// Close stops the listener.
	Close() error
	// Addr returns the address the listener is bound to.
	Addr() string
}
