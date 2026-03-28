package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

var queueCmd = &cobra.Command{
	Use:   "queue",
	Short: "Show message queue (messages waiting for offline agents or unreachable peers)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return showQueue()
	},
}

type queueEntry struct {
	Target string    `json:"target"`
	Count  int       `json:"count"`
	Oldest time.Time `json:"oldest"`
}

func showQueue() error {
	ctx := context.Background()

	conn, err := tryHubConnect()
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}
	defer conn.Close()

	mux := newCLIMux(ctx, conn)

	resp, err := mux.rpcCall(ctx, "hub.queue_status", nil)
	if err != nil {
		return fmt.Errorf("queue status: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("hub error: %s", resp.Error.Message)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	var result struct {
		Agents []queueEntry `json:"agents"`
		Peers  []queueEntry `json:"peers"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if len(result.Agents) == 0 && len(result.Peers) == 0 {
		fmt.Println("Queue is empty.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)

	if len(result.Agents) > 0 {
		fmt.Fprintln(w, "AGENT QUEUE")
		fmt.Fprintf(w, "  AGENT ID\tCOUNT\tOLDEST\n")
		for _, e := range result.Agents {
			fmt.Fprintf(w, "  %s\t%d\t%s\n",
				e.Target, e.Count, e.Oldest.Format("2006-01-02 15:04:05"))
		}
	}

	if len(result.Agents) > 0 && len(result.Peers) > 0 {
		fmt.Fprintln(w, "")
	}

	if len(result.Peers) > 0 {
		fmt.Fprintln(w, "PEER QUEUE")
		fmt.Fprintf(w, "  PEER ID\tCOUNT\tOLDEST\n")
		for _, e := range result.Peers {
			fmt.Fprintf(w, "  %s\t%d\t%s\n",
				e.Target, e.Count, e.Oldest.Format("2006-01-02 15:04:05"))
		}
	}

	w.Flush()
	return nil
}
