package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/marcfargas/bifrost/pkg/protocol"
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
	} else {
		printAgentTable(agents)
	}

	// Print peer summary.
	fmt.Println()
	if err := printPeerSummary(ctx, mux); err != nil {
		// Non-fatal: hub may not have federation enabled.
		fmt.Printf("Peers: (unavailable: %s)\n", err)
	}

	return nil
}

// printPeerSummary queries peer.list and prints a short connected/disconnected summary.
func printPeerSummary(ctx context.Context, mux *cliMux) error {
	resp, err := mux.rpcCall(ctx, "peer.list", nil)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("%s", resp.Error.Message)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	var result struct {
		Peers []protocol.Peer `json:"peers"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if len(result.Peers) == 0 {
		fmt.Println("Peers: none")
		return nil
	}

	var connected, disconnected int
	for _, p := range result.Peers {
		if p.Status == protocol.PeerStatusConnected {
			connected++
		} else {
			disconnected++
		}
	}

	fmt.Printf("Peers: %d connected, %d disconnected\n", connected, disconnected)
	for _, p := range result.Peers {
		name := p.DisplayName
		if name == "" {
			name = p.PeerID
			if len(name) > 16 {
				name = name[:16]
			}
		}
		fmt.Printf("  %-20s  %-12s  %s\n", name, string(p.Status), p.LastSeen.Format("2006-01-02 15:04:05"))
	}

	return nil
}
