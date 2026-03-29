package federation

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	"github.com/multiformats/go-multiaddr"
	"github.com/multiformats/go-multihash"
)

const (
	// BifrostProtocolID is the libp2p protocol identifier for bifrost federation.
	BifrostProtocolID = protocol.ID("/bifrost/federation/1.0.0")

	// BifrostDHTNamespace is the DHT namespace for magic code announcements.
	BifrostDHTNamespace = "/bifrost/peer/"
)

// Libp2pTransport implements federation over libp2p with Noise encryption and DHT discovery.
type Libp2pTransport struct {
	host                 host.Host
	dht                  *dht.IpfsDHT
	logger               *slog.Logger
	privKey              crypto.PrivKey
	bootstrap            []string
	skipDefaultBootstrap bool
	incoming             chan<- PeerConn
	mu                   sync.Mutex
	cancel               context.CancelFunc
}

// Libp2pConfig holds configuration for the libp2p transport.
type Libp2pConfig struct {
	PrivKey              crypto.PrivKey // persistent identity key for this hub
	KeyPath              string         // path to persist the identity key; ignored if PrivKey is set
	Bootstrap            []string       // additional DHT bootstrap peers (multiaddr strings)
	ListenAddrs          []string       // listen addresses (empty = defaults)
	Logger               *slog.Logger
	SkipDefaultBootstrap bool // skip connecting to default IPFS bootstrap peers (for testing)
}

// loadOrGenerateKey loads a private key from path, or generates a new one and
// saves it. If path is empty, a fresh ephemeral key is generated.
func loadOrGenerateKey(path string) (crypto.PrivKey, error) {
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			priv, err := crypto.UnmarshalPrivateKey(data)
			if err != nil {
				return nil, fmt.Errorf("unmarshal libp2p key from %s: %w", path, err)
			}
			return priv, nil
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read libp2p key file %s: %w", path, err)
		}
	}

	// Key file absent (or no path given) — generate a new one.
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		return nil, fmt.Errorf("generate identity key: %w", err)
	}

	if path != "" {
		data, err := crypto.MarshalPrivateKey(priv)
		if err != nil {
			return nil, fmt.Errorf("marshal libp2p key: %w", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, fmt.Errorf("write libp2p key to %s: %w", path, err)
		}
	}

	return priv, nil
}

// NewLibp2pTransport creates a new libp2p federation transport.
func NewLibp2pTransport(cfg Libp2pConfig) (*Libp2pTransport, error) {
	if cfg.PrivKey == nil {
		priv, err := loadOrGenerateKey(cfg.KeyPath)
		if err != nil {
			return nil, err
		}
		cfg.PrivKey = priv
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	listenAddrs := cfg.ListenAddrs
	if len(listenAddrs) == 0 {
		listenAddrs = []string{
			"/ip4/0.0.0.0/tcp/0",
			"/ip4/0.0.0.0/udp/0/quic-v1",
			"/ip6/::/tcp/0",
			"/ip6/::/udp/0/quic-v1",
		}
	}

	var multiaddrs []multiaddr.Multiaddr
	for _, addr := range listenAddrs {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			return nil, fmt.Errorf("parse listen addr %s: %w", addr, err)
		}
		multiaddrs = append(multiaddrs, ma)
	}

	h, err := libp2p.New(
		libp2p.Identity(cfg.PrivKey),
		libp2p.ListenAddrs(multiaddrs...),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
		libp2p.EnableRelay(),
		libp2p.EnableNATService(),
	)
	if err != nil {
		return nil, fmt.Errorf("create libp2p host: %w", err)
	}

	return &Libp2pTransport{
		host:                 h,
		logger:               cfg.Logger,
		privKey:              cfg.PrivKey,
		bootstrap:            cfg.Bootstrap,
		skipDefaultBootstrap: cfg.SkipDefaultBootstrap,
	}, nil
}

// Name returns "libp2p".
func (t *Libp2pTransport) Name() string { return "libp2p" }

// Start begins listening for incoming bifrost streams and bootstraps the DHT.
func (t *Libp2pTransport) Start(ctx context.Context, incoming chan<- PeerConn) error {
	t.mu.Lock()
	ctx, t.cancel = context.WithCancel(ctx)
	t.incoming = incoming
	t.mu.Unlock()

	// Set stream handler for incoming connections first, before any
	// DHT/bootstrap work that might trigger inbound streams.
	t.host.SetStreamHandler(BifrostProtocolID, func(s network.Stream) {
		peerID := s.Conn().RemotePeer().String()
		t.logger.Info("incoming libp2p stream", "peer_id", peerID)
		conn := NewStreamPeerConn(peerID, s)
		incoming <- conn
	})

	// Initialize DHT.
	kadDHT, err := dht.New(ctx, t.host, dht.Mode(dht.ModeAutoServer))
	if err != nil {
		return fmt.Errorf("create DHT: %w", err)
	}
	t.dht = kadDHT

	// Bootstrap DHT.
	if err := t.dht.Bootstrap(ctx); err != nil {
		return fmt.Errorf("bootstrap DHT: %w", err)
	}

	// Connect to custom bootstrap peers.
	for _, addr := range t.bootstrap {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			t.logger.Warn("invalid bootstrap addr", "addr", addr, "error", err)
			continue
		}
		pi, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			t.logger.Warn("invalid bootstrap peer info", "addr", addr, "error", err)
			continue
		}
		go func(pi peer.AddrInfo) {
			if err := t.host.Connect(ctx, pi); err != nil {
				t.logger.Debug("bootstrap connect failed", "peer", pi.ID, "error", err)
			}
		}(*pi)
	}

	// Connect to default IPFS bootstrap peers (skip for local-only/test scenarios).
	if !t.skipDefaultBootstrap {
		for _, addr := range dht.DefaultBootstrapPeers {
			pi, err := peer.AddrInfoFromP2pAddr(addr)
			if err != nil {
				continue
			}
			go func(pi peer.AddrInfo) {
				if err := t.host.Connect(ctx, pi); err != nil {
					t.logger.Debug("default bootstrap connect failed", "peer", pi.ID, "error", err)
				}
			}(*pi)
		}
	}

	t.logger.Info("libp2p transport started",
		"peer_id", t.host.ID().String(),
		"addrs", t.host.Addrs())

	return nil
}

// Connect opens a stream to a remote peer by multiaddr or peer ID string.
func (t *Libp2pTransport) Connect(ctx context.Context, address string) (PeerConn, error) {
	// Try parsing as a full multiaddr first.
	ma, err := multiaddr.NewMultiaddr(address)
	if err == nil {
		pi, err := peer.AddrInfoFromP2pAddr(ma)
		if err == nil {
			if err := t.host.Connect(ctx, *pi); err != nil {
				return nil, fmt.Errorf("connect to multiaddr: %w", err)
			}
			s, err := t.host.NewStream(ctx, pi.ID, BifrostProtocolID)
			if err != nil {
				return nil, fmt.Errorf("open stream: %w", err)
			}
			return NewStreamPeerConn(pi.ID.String(), s), nil
		}
	}

	// Try parsing as a peer ID and finding via DHT.
	pid, err := peer.Decode(address)
	if err != nil {
		return nil, fmt.Errorf("invalid peer address %q: not a multiaddr or peer ID", address)
	}

	// Find the peer via DHT.
	pi, err := t.dht.FindPeer(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("find peer %s via DHT: %w", pid, err)
	}

	if err := t.host.Connect(ctx, pi); err != nil {
		return nil, fmt.Errorf("connect to peer %s: %w", pid, err)
	}

	s, err := t.host.NewStream(ctx, pid, BifrostProtocolID)
	if err != nil {
		return nil, fmt.Errorf("open stream to %s: %w", pid, err)
	}

	return NewStreamPeerConn(pid.String(), s), nil
}

// Stop shuts down the libp2p host and DHT.
func (t *Libp2pTransport) Stop() error {
	t.mu.Lock()
	cancel := t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if t.dht != nil {
		if err := t.dht.Close(); err != nil {
			t.logger.Debug("DHT close error", "error", err)
		}
	}
	return t.host.Close()
}

// magicCodeCID creates a CID from a magic code's public key for DHT provider records.
func magicCodeCID(mc *MagicCode) (cid.Cid, error) {
	rendID, err := peer.IDFromPublicKey(mc.PubKey)
	if err != nil {
		return cid.Undef, fmt.Errorf("derive rendezvous peer ID: %w", err)
	}
	// Create a CID from the peer ID hash — this is what we announce/look up on the DHT.
	mh, err := multihash.Sum([]byte(BifrostDHTNamespace+rendID.String()), multihash.SHA2_256, -1)
	if err != nil {
		return cid.Undef, fmt.Errorf("create multihash: %w", err)
	}
	return cid.NewCidV1(cid.Raw, mh), nil
}

// AnnounceMagicCode publishes the magic code's peer ID on the DHT so joiners can find this hub.
func (t *Libp2pTransport) AnnounceMagicCode(ctx context.Context, mc *MagicCode) error {
	c, err := magicCodeCID(mc)
	if err != nil {
		return err
	}

	if err := t.dht.Provide(ctx, c, true); err != nil {
		return fmt.Errorf("DHT provide: %w", err)
	}

	t.logger.Info("announced magic code on DHT",
		"cid", c.String(),
		"local_peer_id", t.host.ID().String())

	return nil
}

// FindByMagicCode looks up a hub that announced the given magic code on the DHT.
func (t *Libp2pTransport) FindByMagicCode(ctx context.Context, mc *MagicCode) (peer.ID, error) {
	c, err := magicCodeCID(mc)
	if err != nil {
		return "", err
	}

	providers, err := t.dht.FindProviders(ctx, c)
	if err != nil {
		return "", fmt.Errorf("find providers: %w", err)
	}

	for _, pi := range providers {
		if pi.ID != t.host.ID() {
			return pi.ID, nil
		}
	}

	return "", fmt.Errorf("no hub found for magic code")
}

// PeerNew generates a magic code and announces this hub on the DHT.
// Returns the formatted magic code string.
func (t *Libp2pTransport) PeerNew(ctx context.Context) (string, error) {
	mc, err := GenerateMagicCode()
	if err != nil {
		return "", err
	}

	if err := t.AnnounceMagicCode(ctx, mc); err != nil {
		return "", fmt.Errorf("announce: %w", err)
	}

	return mc.Code, nil
}

// PeerJoin finds and connects to a hub that announced the given magic code.
func (t *Libp2pTransport) PeerJoin(ctx context.Context, code string) (PeerConn, error) {
	mc, err := DeriveMagicCode(code)
	if err != nil {
		return nil, fmt.Errorf("derive magic code: %w", err)
	}

	pid, err := t.FindByMagicCode(ctx, mc)
	if err != nil {
		return nil, fmt.Errorf("find peer: %w", err)
	}

	// Find and connect to the peer.
	pi, err := t.dht.FindPeer(ctx, pid)
	if err != nil {
		return nil, fmt.Errorf("find peer addrs: %w", err)
	}

	if err := t.host.Connect(ctx, pi); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	s, err := t.host.NewStream(ctx, pid, BifrostProtocolID)
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}

	t.logger.Info("joined peer via magic code", "peer_id", pid.String())

	return NewStreamPeerConn(pid.String(), s), nil
}

// HostID returns the libp2p host's peer ID string.
func (t *Libp2pTransport) HostID() string {
	return t.host.ID().String()
}
