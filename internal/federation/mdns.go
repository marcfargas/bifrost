package federation

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"
)

const (
	// MDNSServiceName is the mDNS service type for bifrost hub discovery.
	MDNSServiceName = "_bifrost-hub._tcp"
	// MDNSBrowseInterval is how often to scan for new hubs on the LAN.
	MDNSBrowseInterval = 10 * time.Second
)

// MDNSTransport discovers bifrost hubs on the local network via mDNS/DNS-SD.
// Disabled by default because it has no authentication.
type MDNSTransport struct {
	logger   *slog.Logger
	hubID    string
	port     int
	listener net.Listener
	incoming chan<- PeerConn
	mu       sync.Mutex
	known    map[string]bool // peer addresses already connected
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// MDNSTransportConfig holds configuration for the mDNS transport.
type MDNSTransportConfig struct {
	HubID  string
	Logger *slog.Logger
}

// NewMDNSTransport creates a new mDNS federation transport.
func NewMDNSTransport(cfg MDNSTransportConfig) *MDNSTransport {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &MDNSTransport{
		logger: cfg.Logger,
		hubID:  cfg.HubID,
		known:  make(map[string]bool),
	}
}

func (t *MDNSTransport) Name() string { return "mdns" }

// Start begins advertising this hub via mDNS and browsing for others.
func (t *MDNSTransport) Start(ctx context.Context, incoming chan<- PeerConn) error {
	ctx, t.cancel = context.WithCancel(ctx)
	t.incoming = incoming

	// Start TCP listener for incoming connections from discovered peers.
	var err error
	t.listener, err = net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return fmt.Errorf("mdns: listen: %w", err)
	}
	t.port = t.listener.Addr().(*net.TCPAddr).Port

	// Accept incoming TCP connections.
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		for {
			conn, err := t.listener.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					t.logger.Error("mdns accept error", "error", err)
					continue
				}
			}
			peerConn := NewStreamPeerConn("mdns-"+conn.RemoteAddr().String(), conn)
			incoming <- peerConn
		}
	}()

	// Advertise this hub via mDNS.
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.advertise(ctx)
	}()

	// Browse for other hubs.
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.browse(ctx)
	}()

	t.logger.Info("mDNS transport started", "port", t.port, "hub_id", t.hubID)
	return nil
}

// advertise periodically broadcasts this hub's presence via UDP multicast.
func (t *MDNSTransport) advertise(ctx context.Context) {
	ticker := time.NewTicker(MDNSBrowseInterval)
	defer ticker.Stop()

	record := fmt.Sprintf("%s\t%s\t%d", MDNSServiceName, t.hubID, t.port)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			addr, err := net.ResolveUDPAddr("udp4", "224.0.0.251:5353")
			if err != nil {
				continue
			}
			conn, err := net.DialUDP("udp4", nil, addr)
			if err != nil {
				continue
			}
			conn.Write([]byte(record)) //nolint:errcheck
			conn.Close()
		}
	}
}

// browse listens for mDNS announcements from other bifrost hubs.
func (t *MDNSTransport) browse(ctx context.Context) {
	addr, err := net.ResolveUDPAddr("udp4", "224.0.0.251:5353")
	if err != nil {
		t.logger.Error("mdns: resolve multicast addr", "error", err)
		return
	}

	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		t.logger.Error("mdns: listen multicast", "error", err)
		return
	}
	defer conn.Close()

	buf := make([]byte, 1024)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(MDNSBrowseInterval)) //nolint:errcheck
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}

		record := string(buf[:n])
		var service, hubID string
		var port int
		_, err = fmt.Sscanf(record, "%s\t%s\t%d", &service, &hubID, &port)
		if err != nil || service != MDNSServiceName || hubID == t.hubID {
			continue
		}

		peerAddr := fmt.Sprintf("%s:%d", remoteAddr.IP.String(), port)

		t.mu.Lock()
		alreadyKnown := t.known[peerAddr]
		if !alreadyKnown {
			t.known[peerAddr] = true
		}
		t.mu.Unlock()

		if !alreadyKnown {
			t.logger.Info("discovered hub via mDNS", "hub_id", hubID, "addr", peerAddr)
			go func() {
				peerConn, err := t.Connect(ctx, peerAddr)
				if err != nil {
					t.logger.Warn("mdns connect failed", "addr", peerAddr, "error", err)
					t.mu.Lock()
					delete(t.known, peerAddr)
					t.mu.Unlock()
					return
				}
				t.incoming <- peerConn
			}()
		}
	}
}

// Connect opens a TCP connection to a discovered hub.
func (t *MDNSTransport) Connect(ctx context.Context, address string) (PeerConn, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("mdns connect %s: %w", address, err)
	}
	return NewStreamPeerConn("mdns-"+address, conn), nil
}

// Stop shuts down the mDNS transport.
func (t *MDNSTransport) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}
	if t.listener != nil {
		t.listener.Close() //nolint:errcheck
	}
	t.wg.Wait()
	return nil
}
