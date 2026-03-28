package hub

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/federation"
	"github.com/marcfargas/bifrost/internal/transport"
	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/marcfargas/bifrost/pkg/store"
)

// Server is the hub daemon. It owns the listener, connection manager, and
// core Hub, and drives the RPC accept loop.
type Server struct {
	cfg        *config.Config
	hub        *core.Hub
	connMgr    *ConnManager
	listener   transport.Listener
	handler    *Handler
	cancel     context.CancelFunc
	wg         sync.WaitGroup
	logger     *slog.Logger
	logFile    *os.File                    // non-nil when hub.log was opened by NewServer
	mcpHTTP    *transport.MCPHTTPTransport // nil if MCP HTTP is disabled
	fedManager *federation.Manager         // nil if federation is disabled
}

// NewServer creates a Server: opens the SQLite store, creates the core Hub,
// creates a ConnManager, registers it as a Notifier, and wires up the Handler.
// If logger is nil, slog.Default() is used.
func NewServer(cfg *config.Config, logger *slog.Logger) (*Server, error) {
	dataDir := cfg.Storage.DataDir
	if dataDir == "" {
		dataDir = config.DataDir()
	}

	dbPath := filepath.Join(dataDir, "hub.db")
	s, err := store.NewSQLite(dbPath)
	if err != nil {
		return nil, fmt.Errorf("server: open store: %w", err)
	}

	maxFileSize := strconv.FormatInt(cfg.Storage.MaxFileSize, 10)
	h, err := core.NewHubWithConfig(s, dataDir, maxFileSize)
	if err != nil {
		return nil, fmt.Errorf("server: create hub: %w", err)
	}
	cm := NewConnManager()
	h.AddNotifier(cm)

	// Rotate hub.log if it exceeds the configured size threshold.
	logPath := filepath.Join(dataDir, "hub.log")
	maxLogSize := cfg.Logging.LogMaxSize
	if maxLogSize == 0 {
		maxLogSize = 1048576 // 1 MiB default
	}
	if fi, err := os.Stat(logPath); err == nil && fi.Size() > maxLogSize {
		_ = os.Rename(logPath, logPath+".1")
	}

	// Open hub.log for append so operational events are persisted regardless
	// of whether the hub runs in foreground or via autostart.
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("server: open hub.log: %w", err)
	}

	if logger == nil {
		// Write to both stderr and hub.log so `bifrost logs -f` works regardless
		// of how the hub was started.
		w := io.MultiWriter(os.Stderr, logFile)
		logger = slog.New(slog.NewTextHandler(w, nil))
	}

	handler := NewHandler(h, cm)
	handler.SetLogger(logger)

	return &Server{
		cfg:     cfg,
		hub:     h,
		connMgr: cm,
		handler: handler,
		logger:  logger,
		logFile: logFile,
	}, nil
}

// SetPeerManager sets the peer manager used by peer.* RPC handlers.
func (s *Server) SetPeerManager(pm PeerManager) {
	s.handler.SetPeerManager(pm)
}

// Hub returns the underlying core.Hub for wiring federation or other components.
func (s *Server) Hub() *core.Hub {
	return s.hub
}

// Start begins the hub server in the background. It starts the local listener,
// MCP HTTP transport (if enabled), housekeeping, and the accept loop. The server
// runs until Stop is called or ctx is cancelled.
// This is the non-blocking form used by tests; production code uses Run instead.
func (s *Server) Start(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel

	// Start local listener (platform-appropriate).
	ln, err := newLocalListener(s.cfg)
	if err != nil {
		cancel()
		return fmt.Errorf("server: start listener: %w", err)
	}
	s.listener = ln

	// Start MCP HTTP transport if enabled.
	if err := s.startMCPHTTP(runCtx); err != nil {
		_ = ln.Close()
		cancel()
		return err
	}

	// Start federation manager if any transport is enabled.
	if err := s.startFederation(runCtx); err != nil {
		s.stopMCPHTTP()
		_ = ln.Close()
		cancel()
		return err
	}

	// Start housekeeping goroutine.
	s.wg.Go(func() {
		runHousekeeping(runCtx, s.cfg, s.hub)
	})

	// Start accept loop goroutine.
	s.wg.Go(func() {
		s.acceptLoop(runCtx)
	})

	return nil
}

// Stop shuts down the hub server started with Start.
func (s *Server) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.stopMCPHTTP()
	s.stopFederation()
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.wg.Wait()
	_ = s.hub.Store().Close()
	if s.logFile != nil {
		_ = s.logFile.Close()
	}
}

// MCPHTTPPort returns the actual port the MCP HTTP transport is listening on.
// Returns 0 if MCP HTTP is not enabled or not yet started.
func (s *Server) MCPHTTPPort() int {
	if s.mcpHTTP == nil {
		return 0
	}
	return s.mcpHTTP.Port()
}

// Run starts the hub daemon. It blocks until a signal is received or ctx is
// cancelled, then performs a clean shutdown.
func (s *Server) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	defer cancel()

	// Start local listener (platform-appropriate).
	ln, err := newLocalListener(s.cfg)
	if err != nil {
		return fmt.Errorf("server: start listener: %w", err)
	}
	s.listener = ln

	// Write PID file with PID and listener address.
	if err := s.writePIDFile(ln.Addr()); err != nil {
		_ = ln.Close()
		return fmt.Errorf("server: write PID file: %w", err)
	}

	// Start MCP HTTP transport if enabled.
	if err := s.startMCPHTTP(runCtx); err != nil {
		_ = ln.Close()
		s.removePIDFile()
		return err
	}

	// Start federation manager if any transport is enabled.
	if err := s.startFederation(runCtx); err != nil {
		s.stopMCPHTTP()
		_ = ln.Close()
		s.removePIDFile()
		return err
	}

	// Start housekeeping goroutine.
	s.wg.Go(func() {
		runHousekeeping(runCtx, s.cfg, s.hub)
	})

	// Start accept loop goroutine.
	s.wg.Go(func() {
		s.acceptLoop(runCtx)
	})

	// Wait for SIGINT/SIGTERM or context cancellation.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case <-sigCh:
	case <-runCtx.Done():
	}

	// Clean shutdown.
	cancel()
	s.stopMCPHTTP()
	s.stopFederation()
	_ = s.listener.Close()
	s.wg.Wait()
	_ = s.hub.Store().Close()
	if s.logFile != nil {
		_ = s.logFile.Close()
	}
	s.removePIDFile()

	return nil
}

// startFederation creates and starts the federation manager if any transport is
// enabled in the config. It wires the manager to both the core Hub (for message
// forwarding) and the RPC handler (for peer.* RPCs).
// If federation has already been set on the hub (e.g. by a test or external
// caller before Start), this method is a no-op to avoid overwriting that setup.
func (s *Server) startFederation(ctx context.Context) error {
	// If federation was wired externally (e.g. tests), don't overwrite it.
	if s.hub.Federation() != nil {
		s.logger.Debug("federation already configured externally, skipping auto-setup")
		return nil
	}

	fedCfg := s.cfg.Federation
	if !fedCfg.Libp2p.Enabled && !fedCfg.MDNS.Enabled && !fedCfg.Direct.Enabled {
		s.logger.Info("federation disabled (no transports enabled)")
		return nil
	}

	// Determine the local peer ID and create enabled transports.
	var localPeerID string
	var libp2pTransport *federation.Libp2pTransport

	if fedCfg.Libp2p.Enabled {
		dataDir := s.cfg.Storage.DataDir
		if dataDir == "" {
			dataDir = config.DataDir()
		}
		keyPath := filepath.Join(dataDir, "libp2p.key")
		lt, err := federation.NewLibp2pTransport(federation.Libp2pConfig{
			Logger:  s.logger,
			KeyPath: keyPath,
		})
		if err != nil {
			return fmt.Errorf("federation: create libp2p transport: %w", err)
		}
		libp2pTransport = lt
		localPeerID = lt.HostID()
	} else {
		// Derive a stable peer ID from hostname + data dir without libp2p.
		hostname, _ := os.Hostname()
		dataDir := s.cfg.Storage.DataDir
		if dataDir == "" {
			dataDir = config.DataDir()
		}
		sum := sha256.Sum256([]byte(hostname + dataDir))
		localPeerID = fmt.Sprintf("%x", sum[:8])
	}

	mgr := federation.NewManager(s.hub, s.hub.Store(), s.cfg, localPeerID, s.logger)

	if libp2pTransport != nil {
		mgr.AddTransport(libp2pTransport)
	}

	if fedCfg.MDNS.Enabled {
		mgr.AddTransport(federation.NewMDNSTransport(federation.MDNSTransportConfig{
			HubID:  localPeerID,
			Logger: s.logger,
		}))
	}

	if fedCfg.Direct.Enabled {
		mgr.AddTransport(federation.NewDirectTransport(federation.DirectTransportConfig{
			HubID:  localPeerID,
			Listen: fedCfg.Direct.Listen,
			Logger: s.logger,
		}))
	}

	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("federation: start manager: %w", err)
	}

	s.fedManager = mgr
	s.hub.SetFederation(mgr)
	s.handler.SetPeerManager(&federationPeerAdapter{
		mgr:     mgr,
		libp2pt: libp2pTransport,
		store:   s.hub.Store(),
	})

	s.logger.Info("federation started", "peer_id", localPeerID,
		"libp2p", fedCfg.Libp2p.Enabled,
		"mdns", fedCfg.MDNS.Enabled,
		"direct", fedCfg.Direct.Enabled)

	return nil
}

// stopFederation gracefully stops the federation manager if it was started.
func (s *Server) stopFederation() {
	if s.fedManager != nil {
		if err := s.fedManager.Stop(); err != nil {
			s.logger.Error("federation manager stop error", "err", err)
		}
	}
}

// federationPeerAdapter adapts *federation.Manager to the PeerManager interface
// required by the hub RPC handler. It delegates peer.new / peer.join to the
// libp2p transport (when available) and list operations to the store.
type federationPeerAdapter struct {
	mgr     *federation.Manager
	libp2pt *federation.Libp2pTransport // nil when libp2p is not enabled
	store   store.Store
}

// PeerNew generates a magic code and announces this hub on the DHT via libp2p.
func (a *federationPeerAdapter) PeerNew(ctx context.Context) (string, error) {
	if a.libp2pt == nil {
		return "", fmt.Errorf("libp2p transport is not enabled")
	}
	return a.libp2pt.PeerNew(ctx)
}

// PeerJoin connects to a peer hub identified by a magic code.
func (a *federationPeerAdapter) PeerJoin(ctx context.Context, code string) (string, error) {
	if a.libp2pt == nil {
		return "", fmt.Errorf("libp2p transport is not enabled")
	}
	conn, err := a.libp2pt.PeerJoin(ctx, code)
	if err != nil {
		return "", err
	}
	return a.mgr.ConnectViaPeerConn(ctx, conn, protocol.PeerTransport("libp2p"))
}

// ListPeers returns all known federation peers from the store.
func (a *federationPeerAdapter) ListPeers(ctx context.Context) ([]*protocol.Peer, error) {
	return a.store.ListPeers(ctx)
}

// ListRemoteAgents returns agents registered on a specific peer hub.
func (a *federationPeerAdapter) ListRemoteAgents(ctx context.Context, peerHub string) ([]*protocol.Agent, error) {
	return a.store.ListAgents(ctx, store.AgentFilter{PeerHub: peerHub})
}

// startMCPHTTP starts the MCP HTTP transport when cfg.Hub.MCP.Enabled is true.
func (s *Server) startMCPHTTP(ctx context.Context) error {
	if !s.cfg.Hub.MCP.Enabled {
		s.logger.Info("MCP HTTP transport disabled")
		return nil
	}

	s.mcpHTTP = transport.NewMCPHTTPTransport(s.hub, s.cfg.Hub.MCP, s.logger)
	if err := s.mcpHTTP.Start(ctx); err != nil {
		return fmt.Errorf("hub: start MCP HTTP: %w", err)
	}

	// Register the MCP HTTP transport as a notifier so the hub can
	// deliver notifications to HTTP-connected agents.
	s.hub.AddNotifier(s.mcpHTTP)

	return nil
}

// stopMCPHTTP gracefully stops the MCP HTTP transport if it was started.
func (s *Server) stopMCPHTTP() {
	if s.mcpHTTP != nil {
		if err := s.mcpHTTP.Stop(); err != nil {
			s.logger.Error("MCP HTTP transport stop error", "err", err)
		}
	}
}

// acceptLoop accepts incoming connections and spawns a handler goroutine for each.
func (s *Server) acceptLoop(ctx context.Context) {
	for {
		conn, err := s.listener.Accept(ctx)
		if err != nil {
			// Context cancelled or listener closed — stop.
			return
		}
		s.wg.Add(1)
		go func(c *transport.Conn) {
			defer s.wg.Done()
			s.handleConnection(ctx, c)
		}(conn)
	}
}

// handleConnection reads RPCRequests in a loop, dispatches to the handler, and
// writes responses back. Returns when the connection is closed or an error occurs.
// It also watches the context so that server shutdown can interrupt blocked reads
// (necessary on Windows where closing the client side of a Unix socket may not
// immediately unblock the server-side read).
func (s *Server) handleConnection(ctx context.Context, conn *transport.Conn) {
	defer conn.Close()

	// Close the connection when context is cancelled to unblock Receive.
	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	for {
		var req RPCRequest
		if err := conn.Receive(&req); err != nil {
			// EOF or closed connection — exit silently.
			return
		}
		resp := s.handler.Handle(ctx, conn, &req)
		if resp != nil {
			if err := conn.Send(resp); err != nil {
				return
			}
		}
	}
}

// writePIDFile writes the current process PID and the listener address to the
// PID file, creating parent directories as needed.
func (s *Server) writePIDFile(addr string) error {
	path := config.PIDFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir pid dir: %w", err)
	}
	content := strconv.Itoa(os.Getpid()) + "\n" + addr + "\n"
	return os.WriteFile(path, []byte(content), 0o600)
}

// removePIDFile removes the PID file, ignoring errors.
func (s *Server) removePIDFile() {
	_ = os.Remove(config.PIDFilePath())
}
