package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage agents in the hub",
}

var agentRmCmd = &cobra.Command{
	Use:   "rm <name-or-id>",
	Short: "Remove an agent from the hub",
	Long: `Remove an agent from the hub by name or ID.

Use --all-offline to remove all agents with status "offline".`,
	Args: func(cmd *cobra.Command, args []string) error {
		allOffline, _ := cmd.Flags().GetBool("all-offline")
		if allOffline {
			return nil
		}
		if len(args) != 1 {
			return fmt.Errorf("requires exactly one argument (name or id), or use --all-offline")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		allOffline, _ := cmd.Flags().GetBool("all-offline")
		if allOffline {
			return removeAllOfflineAgents()
		}
		return removeAgent(args[0])
	},
}

func init() {
	agentRmCmd.Flags().Bool("all-offline", false, "Remove all agents with status offline")
	agentCmd.AddCommand(agentRmCmd)
}

func removeAgent(nameOrID string) error {
	ctx := context.Background()

	conn, err := tryHubConnect()
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}
	defer conn.Close()

	mux := newCLIMux(ctx, conn)

	params := map[string]string{"name": nameOrID}
	resp, err := mux.rpcCall(ctx, "hub.remove_agent", params)
	if err != nil {
		return fmt.Errorf("remove agent: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("hub error: %s", resp.Error.Message)
	}

	// Parse the result: {"agent_id": "...", "name": "..."}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	var result struct {
		AgentID string `json:"agent_id"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	fmt.Printf("Removed agent %s (%s)\n", result.Name, result.AgentID)
	return nil
}

func removeAllOfflineAgents() error {
	ctx := context.Background()

	conn, err := tryHubConnect()
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}
	defer conn.Close()

	mux := newCLIMux(ctx, conn)

	params := map[string]bool{"all_offline": true}
	resp, err := mux.rpcCall(ctx, "hub.remove_agent", params)
	if err != nil {
		return fmt.Errorf("remove agents: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("hub error: %s", resp.Error.Message)
	}

	// Parse the result: {"removed": [{"agent_id": "...", "name": "..."}, ...]}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	var result struct {
		Removed []struct {
			AgentID string `json:"agent_id"`
			Name    string `json:"name"`
		} `json:"removed"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if len(result.Removed) == 0 {
		fmt.Println("No offline agents to remove.")
		return nil
	}

	for _, a := range result.Removed {
		fmt.Printf("Removed agent %s (%s)\n", a.Name, a.AgentID)
	}
	return nil
}
