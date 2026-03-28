package main

import (
	"fmt"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/spf13/cobra"
)

var (
	version = "dev"     // set via ldflags: -ldflags "-X main.version=x.y.z"
	commit  = "unknown" // set via ldflags: -ldflags "-X main.commit=abc1234"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version and protocol version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("bifrost %s (%s) protocol %s\n", version, commit, protocol.ProtocolVersion)
	},
}
