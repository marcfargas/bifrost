package main

import (
	"context"

	"github.com/marcfargas/bifrost/internal/shim"
	"github.com/spf13/cobra"
)

var shimOpts shim.Options

var shimCmd = &cobra.Command{
	Use:   "shim",
	Short: "Run the MCP stdio shim (used by Claude Code)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return shim.Run(context.Background(), shimOpts)
	},
}

func init() {
	shimCmd.Flags().StringVar(&shimOpts.ProjectName, "project-name", "", "Project name to register with the hub")
	shimCmd.Flags().StringVar(&shimOpts.DisplayName, "display-name", "", "Display name for this agent")
}
