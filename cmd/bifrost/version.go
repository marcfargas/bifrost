package main

import (
	"fmt"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/spf13/cobra"
)

var version = "dev" // set via ldflags: -ldflags "-X main.version=x.y.z"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version and protocol version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("bifrost %s (protocol %s)\n", version, protocol.ProtocolVersion)
	},
}
