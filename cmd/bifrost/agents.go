package main

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

var agentsCmd = &cobra.Command{
	Use:   "agents",
	Short: "List agents connected to the hub",
	RunE: func(cmd *cobra.Command, args []string) error {
		return listAgents()
	},
}

func listAgents() error {
	ctx := context.Background()

	conn, err := tryHubConnect()
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}
	defer conn.Close()

	mux := newCLIMux(ctx, conn)

	resp, err := mux.rpcCall(ctx, "hub.list_agents", nil)
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("hub error: %s", resp.Error.Message)
	}

	agents, err := parseAgentList(resp.Result)
	if err != nil {
		return fmt.Errorf("parse agent list: %w", err)
	}

	if len(agents) == 0 {
		fmt.Println("No agents connected.")
		return nil
	}

	printAgentTable(agents)
	return nil
}
