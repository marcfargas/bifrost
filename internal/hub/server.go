package hub

import (
	"context"
	"fmt"
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
	cfg     *config.Config
	hub     *core.Hub
	connMgr *ConnManager
	listener transport.Listener
	handler  *Handler
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

// NewServer creates a Server: opens the SQLite store, creates the core Hub,
// creates a ConnManager, registers it as a Notifier, and wires up the Handler.
func NewServer(cfg *config.Config) (*Server, error) {
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

	return &Server{
		cfg:     cfg,
		hub:     h,
		connMgr: cm,
		handler:  NewHandler(h, cm),
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
	_ = s.listener.Close()
	s.wg.Wait()
	_ = s.hub.Store().Close()
	s.removePIDFile()

	return nil
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

