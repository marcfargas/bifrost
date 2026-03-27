// Package federation — direct TCP/TLS transport for peer-to-peer hub federation.
package federation

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/marcfargas/bifrost/pkg/protocol"
)

// DirectTransport implements federation over direct TCP/TLS connections
// with token-based authentication. For known-address peering when
// libp2p DHT is not desired.
type DirectTransport struct {
	logger   *slog.Logger
	hubID    string
	listen   string
	tlsCert  string
	tlsKey   string
	tokens   map[string]string // peer_id -> token
	listener net.Listener
	incoming chan<- PeerConn
	mu       sync.RWMutex
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// DirectTransportConfig holds configuration for the direct TCP/TLS transport.
// Named DirectTransportConfig to avoid collision with config.DirectConfig.
type DirectTransportConfig struct {
	HubID   string
	Listen  string // e.g., "0.0.0.0:7434"
	TLSCert string // path to TLS cert (empty = no TLS)
	TLSKey  string // path to TLS key
	Logger  *slog.Logger
}

// NewDirectTransport creates a new direct TCP/TLS federation transport.
func NewDirectTransport(cfg DirectTransportConfig) *DirectTransport {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &DirectTransport{
		logger:  cfg.Logger,
		hubID:   cfg.HubID,
		listen:  cfg.Listen,
		tlsCert: cfg.TLSCert,
		tlsKey:  cfg.TLSKey,
		tokens:  make(map[string]string),
	}
}

// Name returns the transport identifier.
func (t *DirectTransport) Name() string { return "direct" }

// Start begins listening for incoming direct connections.
func (t *DirectTransport) Start(ctx context.Context, incoming chan<- PeerConn) error {
	ctx, t.cancel = context.WithCancel(ctx)
	t.incoming = incoming

	var listener net.Listener
	var err error

	if t.tlsCert != "" && t.tlsKey != "" {
		cert, err := tls.LoadX509KeyPair(t.tlsCert, t.tlsKey)
		if err != nil {
			return fmt.Errorf("direct: load TLS cert: %w", err)
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
		}
		listener, err = tls.Listen("tcp", t.listen, tlsCfg)
		if err != nil {
			return fmt.Errorf("direct: tls listen: %w", err)
		}
	} else {
		listener, err = net.Listen("tcp", t.listen)
		if err != nil {
			return fmt.Errorf("direct: listen: %w", err)
		}
	}

	t.listener = listener

	// Accept loop
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					t.logger.Error("direct accept error", "error", err)
					// If listener was closed externally, stop looping.
					return
				}
			}
			go t.handleIncoming(ctx, conn)
		}
	}()

	t.logger.Info("direct transport started", "listen", listener.Addr().String())
	return nil
}

// handleIncoming authenticates an incoming connection.
func (t *DirectTransport) handleIncoming(ctx context.Context, conn net.Conn) {
	// Read the initial auth envelope
	peerConn := NewStreamPeerConn("", conn)

	env, err := peerConn.Receive(ctx)
	if err != nil {
		t.logger.Error("direct: auth receive failed", "error", err)
		conn.Close()
		return
	}

	if env.Method != "peer.auth" {
		t.logger.Error("direct: expected peer.auth", "method", env.Method)
		conn.Close()
		return
	}

	if !protocol.CompatibleWith(env.Version) {
		errPayload, _ := json.Marshal(protocol.PeerResponse{
			ID:    env.ID,
			OK:    false,
			Error: fmt.Sprintf("incompatible protocol: local=%s remote=%s", protocol.ProtocolVersion, env.Version),
		})
		_ = peerConn.Send(ctx, &protocol.PeerEnvelope{
			Method:  "peer.error",
			ID:      env.ID,
			Version: protocol.ProtocolVersion,
			From:    t.hubID,
			Payload: errPayload,
		})
		conn.Close()
		return
	}

	// Validate token
	var authPayload struct {
		Token  string `json:"token"`
		PeerID string `json:"peer_id"`
	}
	if err := json.Unmarshal(env.Payload, &authPayload); err != nil {
		t.logger.Error("direct: auth payload unmarshal failed", "error", err)
		conn.Close()
		return
	}

	t.mu.RLock()
	expectedToken, known := t.tokens[authPayload.PeerID]
	t.mu.RUnlock()

	if known && authPayload.Token != expectedToken {
		t.logger.Warn("direct: invalid token", "peer_id", authPayload.PeerID)
		conn.Close()
		return
	}

	// If not known, this is a new pairing — accept and store the token
	if !known && authPayload.Token != "" {
		t.mu.Lock()
		t.tokens[authPayload.PeerID] = authPayload.Token
		t.mu.Unlock()
	}

	// Send auth response
	respPayload, _ := json.Marshal(struct {
		PeerID string `json:"peer_id"`
		Token  string `json:"token"`
	}{
		PeerID: t.hubID,
		Token:  t.localToken(authPayload.PeerID),
	})
	_ = peerConn.Send(ctx, &protocol.PeerEnvelope{
		Method:  "peer.auth_ok",
		ID:      env.ID,
		Version: protocol.ProtocolVersion,
		From:    t.hubID,
		Payload: respPayload,
	})

	// Re-create the PeerConn with the real peer ID
	authedConn := &directPeerConn{
		StreamPeerConn: peerConn,
		remotePeerID:   authPayload.PeerID,
	}

	t.incoming <- authedConn
}

// localToken returns or generates a persistent token for a peer.
func (t *DirectTransport) localToken(peerID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	tok, ok := t.tokens[peerID]
	if ok {
		return tok
	}
	tok = protocol.NewID() + protocol.NewID() // 32 hex chars
	t.tokens[peerID] = tok
	return tok
}

// SetToken stores a token for a peer (loaded from config on startup).
func (t *DirectTransport) SetToken(peerID, token string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tokens[peerID] = token
}

// Connect opens a direct TCP/TLS connection to a peer at the given address.
func (t *DirectTransport) Connect(ctx context.Context, address string) (PeerConn, error) {
	var conn net.Conn
	var err error

	dialer := net.Dialer{Timeout: 10 * time.Second}

	if t.tlsCert != "" {
		tlsCfg := &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // peers authenticate via tokens, not CA
			MinVersion:         tls.VersionTLS13,
		}
		conn, err = tls.DialWithDialer(&dialer, "tcp", address, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}

	if err != nil {
		return nil, fmt.Errorf("direct connect %s: %w", address, err)
	}

	peerConn := NewStreamPeerConn("", conn)

	// Send auth — use empty token for first connection (new pairing)
	t.mu.RLock()
	token := t.tokens[address] // may be empty for first connection
	t.mu.RUnlock()

	authPayload, _ := json.Marshal(struct {
		Token  string `json:"token"`
		PeerID string `json:"peer_id"`
	}{
		Token:  token,
		PeerID: t.hubID,
	})

	if err := peerConn.Send(ctx, &protocol.PeerEnvelope{
		Method:  "peer.auth",
		ID:      protocol.NewShortID(),
		Version: protocol.ProtocolVersion,
		From:    t.hubID,
		Payload: authPayload,
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("direct auth send: %w", err)
	}

	// Read auth response
	resp, err := peerConn.Receive(ctx)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("direct auth receive: %w", err)
	}

	if resp.Method == "peer.error" {
		var errResp protocol.PeerResponse
		_ = json.Unmarshal(resp.Payload, &errResp)
		conn.Close()
		return nil, fmt.Errorf("direct auth rejected: %s", errResp.Error)
	}

	var authResp struct {
		PeerID string `json:"peer_id"`
		Token  string `json:"token"`
	}
	_ = json.Unmarshal(resp.Payload, &authResp)

	// Store the token for future reconnections.
	// Key by both the peer's hub ID and the dial address so Connect can find it
	// on the next attempt regardless of which key is used for lookup.
	if authResp.Token != "" {
		t.mu.Lock()
		t.tokens[authResp.PeerID] = authResp.Token
		t.tokens[address] = authResp.Token
		t.mu.Unlock()
	}

	return &directPeerConn{
		StreamPeerConn: peerConn,
		remotePeerID:   authResp.PeerID,
	}, nil
}

// Stop shuts down the direct transport.
func (t *DirectTransport) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}
	if t.listener != nil {
		t.listener.Close()
	}
	t.wg.Wait()
	return nil
}

// Addr returns the listener address (for tests and config reporting).
func (t *DirectTransport) Addr() string {
	if t.listener != nil {
		return t.listener.Addr().String()
	}
	return t.listen
}

// directPeerConn wraps StreamPeerConn with the authenticated peer ID.
type directPeerConn struct {
	*StreamPeerConn
	remotePeerID string
}

func (c *directPeerConn) PeerID() string { return c.remotePeerID }
