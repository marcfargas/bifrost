package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/spf13/cobra"
)

var conversationsCmd = &cobra.Command{
	Use:   "conversations",
	Short: "List active conversations",
	RunE: func(cmd *cobra.Command, args []string) error {
		return listConversations()
	},
}

func listConversations() error {
	ctx := context.Background()

	conn, err := tryHubConnect()
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}
	defer conn.Close()

	mux := newCLIMux(ctx, conn)

	resp, err := mux.rpcCall(ctx, "hub.list_conversations", nil)
	if err != nil {
		return fmt.Errorf("list conversations: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("hub error: %s", resp.Error.Message)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	var convs []protocol.Conversation
	if err := json.Unmarshal(data, &convs); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if len(convs) == 0 {
		fmt.Println("No conversations.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ID\tPARTICIPANTS\tTASK\tLAST ACTIVITY\tCLOSED\n")
	for _, c := range convs {
		participants := strings.Join(c.Participants, ", ")
		if len(participants) > 30 {
			participants = participants[:27] + "..."
		}
		taskID := c.TaskID
		if taskID == "" {
			taskID = "-"
		} else {
			taskID = truncateID(taskID, 16)
		}
		closed := "no"
		if c.Closed {
			closed = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			truncateID(c.ConversationID, 16),
			participants,
			taskID,
			c.LastActivity.Format("2006-01-02 15:04:05"),
			closed,
		)
	}
	w.Flush()
	return nil
}
