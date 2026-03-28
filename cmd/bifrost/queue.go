package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var queueCmd = &cobra.Command{
	Use:   "queue",
	Short: "Show sync status (pending event deliveries per agent and peer)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return showQueue()
	},
}

func showQueue() error {
	ctx := context.Background()

	conn, err := tryHubConnect()
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}
	defer conn.Close()

	mux := newCLIMux(ctx, conn)

	resp, err := mux.rpcCall(ctx, "hub.sync_status", nil)
	if err != nil {
		return fmt.Errorf("sync status: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("hub error: %s", resp.Error.Message)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	var result struct {
		Agents map[string]int `json:"agents"`
		Peers  map[string]int `json:"peers"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if len(result.Agents) == 0 && len(result.Peers) == 0 {
		fmt.Println("No pending deliveries.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	if len(result.Agents) > 0 {
		fmt.Fprintln(w, "AGENT PENDING DELIVERIES")
		fmt.Fprintf(w, "  AGENT ID\tPENDING CONVERSATIONS\n")
		keys := sortedKeys(result.Agents)
		for _, k := range keys {
			fmt.Fprintf(w, "  %s\t%d\n", k, result.Agents[k])
		}
	}

	if len(result.Agents) > 0 && len(result.Peers) > 0 {
		fmt.Fprintln(w, "")
	}

	if len(result.Peers) > 0 {
		fmt.Fprintln(w, "PEER PENDING DELIVERIES")
		fmt.Fprintf(w, "  PEER ID\tPENDING CONVERSATIONS\n")
		keys := sortedKeys(result.Peers)
		for _, k := range keys {
			fmt.Fprintf(w, "  %s\t%d\n", k, result.Peers[k])
		}
	}

	w.Flush()
	return nil
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
