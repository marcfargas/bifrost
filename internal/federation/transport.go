// Package federation implements hub-to-hub communication for cross-network
// agent collaboration. It defines the transport interface that libp2p, mDNS,
// and direct TCP transports implement.
package federation

import (
	"context"
	"encoding/json"
	"io"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

// PeerConn represents a bidirectional connection to a peer hub.
type PeerConn interface {
	// PeerID returns the unique identifier for the remote peer.
	PeerID() string

	// Send sends an envelope to the peer.
	Send(ctx context.Context, env *protocol.PeerEnvelope) error

	// Receive blocks until an envelope is received from the peer.
	Receive(ctx context.Context) (*protocol.PeerEnvelope, error)

	// Close terminates the connection.
	Close() error
}

// Transport is a federation transport that can initiate and accept peer connections.
type Transport interface {
	// Name returns the transport identifier (e.g., "libp2p", "mdns", "direct").
	Name() string

	// Start begins listening for incoming peer connections.
	// Accepted connections are sent to the provided channel.
	Start(ctx context.Context, incoming chan<- PeerConn) error

	// Connect initiates an outgoing connection to a peer.
	// The address format is transport-specific.
	Connect(ctx context.Context, address string) (PeerConn, error)

	// Stop shuts down the transport.
	Stop() error
}

// StreamPeerConn adapts any io.ReadWriteCloser into a PeerConn using JSON encoding.
// Used by both direct TCP and libp2p transports.
type StreamPeerConn struct {
	peerID  string
	rwc     io.ReadWriteCloser
	encoder *json.Encoder
	decoder *json.Decoder
}

// NewStreamPeerConn creates a PeerConn from a bidirectional stream.
func NewStreamPeerConn(peerID string, rwc io.ReadWriteCloser) *StreamPeerConn {
	return &StreamPeerConn{
		peerID:  peerID,
		rwc:     rwc,
		encoder: json.NewEncoder(rwc),
		decoder: json.NewDecoder(rwc),
	}
}

func (c *StreamPeerConn) PeerID() string { return c.peerID }

func (c *StreamPeerConn) Send(_ context.Context, env *protocol.PeerEnvelope) error {
	return c.encoder.Encode(env)
}

func (c *StreamPeerConn) Receive(_ context.Context) (*protocol.PeerEnvelope, error) {
	var env protocol.PeerEnvelope
	if err := c.decoder.Decode(&env); err != nil {
		return nil, err
	}
	return &env, nil
}

func (c *StreamPeerConn) Close() error {
	return c.rwc.Close()
}
