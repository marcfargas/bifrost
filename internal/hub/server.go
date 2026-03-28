package hub

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/transport"
	"github.com/marcfargas/bifrost/pkg/core"
	"github.com/marcfargas/bifrost/pkg/store"
)

// Server is the hub daemon. It owns the listener, connection manager, and
// core Hub, and drives the RPC accept loop.
type Server struct {
	cfg      *config.Config
	hub      *core.Hub
	connMgr  *ConnManager
	listener transport.Listener
	handler  *Handler
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	logger   *slog.Logger
	mcpHTTP  *transport.MCPHTTPTransport // nil if MCP HTTP is disabled
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

	if logger == nil {
		logger = slog.Default()
	}

	return &Server{
		cfg:     cfg,
		hub:     h,
		connMgr: cm,
		handler: NewHandler(h, cm),
		logger:  logger,
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
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.wg.Wait()
	_ = s.hub.Store().Close()
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
	_ = s.listener.Close()
	s.wg.Wait()
	_ = s.hub.Store().Close()
	s.removePIDFile()

	return nil
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
func (s *Server) handleConnection(ctx context.Context, conn *transport.Conn) {
	defer conn.Close()
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

