//go:build windows

package hub

import (
	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/transport"
)

// newLocalListener returns a TCP-backed PipeListener on Windows.
func newLocalListener(_ *config.Config) (transport.Listener, error) {
	return transport.NewPipeListener()
}
