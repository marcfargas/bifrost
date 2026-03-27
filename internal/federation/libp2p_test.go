package federation

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/marcfargas/bifrost/pkg/protocol"
)

func TestLibp2pTransportCreateAndStop(t *testing.T) {
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}

	lt, err := NewLibp2pTransport(Libp2pConfig{
		PrivKey:              priv,
		Logger:               slog.Default(),
		ListenAddrs:          []string{"/ip4/127.0.0.1/tcp/0"},
		SkipDefaultBootstrap: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if lt.Name() != "libp2p" {
		t.Errorf("expected name libp2p, got %s", lt.Name())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	incoming := make(chan PeerConn, 4)
	if err := lt.Start(ctx, incoming); err != nil {
		t.Fatal(err)
	}

	hostID := lt.HostID()
	if hostID == "" {
		t.Error("host ID should not be empty")
	}

	if err := lt.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// TestLibp2pDirectStreamExchange tests bidirectional PeerEnvelope exchange
// over libp2p streams using StreamPeerConn.
//
// Note: libp2p uses lazy stream negotiation — the receiver's stream handler
// only fires when the initiator writes data, not when the stream is opened.
// So B must Send before A can Receive.
func TestLibp2pDirectStreamExchange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create two minimal libp2p hosts.
	hA, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer hA.Close()

	hB, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer hB.Close()

	// Set up stream handler on A using our protocol ID.
	incomingA := make(chan PeerConn, 1)
	hA.SetStreamHandler(BifrostProtocolID, func(s network.Stream) {
		peerID := s.Conn().RemotePeer().String()
		incomingA <- NewStreamPeerConn(peerID, s)
	})

	// B connects to A.
	piA := peer.AddrInfo{ID: hA.ID(), Addrs: hA.Addrs()}
	if err := hB.Connect(ctx, piA); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// B opens a bifrost stream.
	s, err := hB.NewStream(ctx, hA.ID(), BifrostProtocolID)
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	connB := NewStreamPeerConn(hA.ID().String(), s)
	defer connB.Close()

	// B sends an envelope — this triggers lazy protocol negotiation and
	// makes A's stream handler fire.
	env := &protocol.PeerEnvelope{
		Method:  "peer.heartbeat",
		ID:      "test-1",
		Version: "1.0",
		From:    hB.ID().String(),
		Payload: []byte(`{"timestamp":"2026-03-28T00:00:00Z"}`),
	}
	if err := connB.Send(ctx, env); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Wait for A to get the incoming stream.
	var connA PeerConn
	select {
	case connA = <-incomingA:
		defer connA.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for incoming stream on A")
	}

	// Verify peer IDs.
	if connB.PeerID() != hA.ID().String() {
		t.Errorf("B's conn peer ID: expected %s, got %s", hA.ID(), connB.PeerID())
	}
	if connA.PeerID() != hB.ID().String() {
		t.Errorf("A's conn peer ID: expected %s, got %s", hB.ID(), connA.PeerID())
	}

	// A receives the envelope that B already sent.
	received, err := connA.Receive(ctx)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}

	if received.Method != "peer.heartbeat" {
		t.Errorf("expected method peer.heartbeat, got %s", received.Method)
	}
	if received.ID != "test-1" {
		t.Errorf("expected ID test-1, got %s", received.ID)
	}
	if received.From != hB.ID().String() {
		t.Errorf("expected from %s, got %s", hB.ID(), received.From)
	}

	// A sends a response back to B.
	resp := &protocol.PeerEnvelope{
		Method:  "peer.heartbeat.ack",
		ID:      "test-1-ack",
		Version: "1.0",
		From:    hA.ID().String(),
	}
	if err := connA.Send(ctx, resp); err != nil {
		t.Fatalf("send response: %v", err)
	}

	receivedResp, err := connB.Receive(ctx)
	if err != nil {
		t.Fatalf("receive response: %v", err)
	}
	if receivedResp.Method != "peer.heartbeat.ack" {
		t.Errorf("expected method peer.heartbeat.ack, got %s", receivedResp.Method)
	}
}

// TestLibp2pTransportConnect tests that the Transport.Connect method works
// by having B connect to A through the full transport layer.
func TestLibp2pTransportConnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	logger := slog.Default()

	// Create host A using raw libp2p (no DHT) with our stream handler.
	hA, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer hA.Close()

	incomingA := make(chan PeerConn, 1)
	hA.SetStreamHandler(BifrostProtocolID, func(s network.Stream) {
		incomingA <- NewStreamPeerConn(s.Conn().RemotePeer().String(), s)
	})

	// Create host B using our transport.
	privB, _, _ := crypto.GenerateEd25519Key(nil)
	ltB, err := NewLibp2pTransport(Libp2pConfig{
		PrivKey:              privB,
		Logger:               logger,
		ListenAddrs:          []string{"/ip4/127.0.0.1/tcp/0"},
		SkipDefaultBootstrap: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ltB.Stop()

	incomingB := make(chan PeerConn, 4)
	if err := ltB.Start(ctx, incomingB); err != nil {
		t.Fatal(err)
	}

	// B connects to A using full multiaddr.
	fullAddr := hA.Addrs()[0].String() + "/p2p/" + hA.ID().String()
	conn, err := ltB.Connect(ctx, fullAddr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	if conn.PeerID() != hA.ID().String() {
		t.Errorf("expected peer ID %s, got %s", hA.ID(), conn.PeerID())
	}

	// Write something to trigger lazy protocol negotiation on A's side.
	env := &protocol.PeerEnvelope{
		Method:  "peer.sync_agents",
		ID:      "init-1",
		Version: "1.0",
		From:    ltB.HostID(),
	}
	if err := conn.Send(ctx, env); err != nil {
		t.Fatalf("send: %v", err)
	}

	// A should receive the incoming stream.
	select {
	case inc := <-incomingA:
		inc.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for incoming stream on A")
	}
}

func TestLibp2pTransportImplementsInterface(t *testing.T) {
	// Compile-time check that Libp2pTransport implements Transport.
	var _ Transport = (*Libp2pTransport)(nil)
}
