// cmd/bifrost/main.go
package main

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "bifrost",
	Short: "Cross-agent communication hub for Claude Code",
}

func main() {
	rootCmd.AddCommand(hubCmd)
	rootCmd.AddCommand(shimCmd)
	rootCmd.AddCommand(agentsCmd)
	rootCmd.AddCommand(agentCmd)
	rootCmd.AddCommand(sendCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(updateCmd)
	rootCmd.AddCommand(newPeerCmd())
	rootCmd.AddCommand(newLogsCmd())
	rootCmd.AddCommand(queueCmd)
	rootCmd.AddCommand(tasksCmd)
	rootCmd.AddCommand(conversationsCmd)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
