package hub

import (
	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/transport"
)

// newLocalListener returns a Unix socket SocketListener on non-Windows platforms.
func newLocalListener(cfg *config.Config) (transport.Listener, error) {
	socketPath := config.SocketPath()
	if cfg.Hub.Local.SocketPath != "" {
		socketPath = cfg.Hub.Local.SocketPath
	}
	return transport.NewSocketListener(socketPath)
}
