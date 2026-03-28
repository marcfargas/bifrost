package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/spf13/cobra"
)

var tasksCmd = &cobra.Command{
	Use:   "tasks",
	Short: "List all tasks with their status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return listTasks()
	},
}

func listTasks() error {
	ctx := context.Background()

	conn, err := tryHubConnect()
	if err != nil {
		return fmt.Errorf("connect to hub: %w", err)
	}
	defer conn.Close()

	mux := newCLIMux(ctx, conn)

	resp, err := mux.rpcCall(ctx, "task.list", nil)
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("hub error: %s", resp.Error.Message)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}

	var tasks []protocol.Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if len(tasks) == 0 {
		fmt.Println("No tasks.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ID\tSTATUS\tTITLE\tASSIGNEE\tREQUESTER\tUPDATED\n")
	for _, t := range tasks {
		title := t.Title
		if len(title) > 40 {
			title = title[:37] + "..."
		}
		assignee := t.Assignee
		if len(assignee) > 20 {
			assignee = assignee[:17] + "..."
		}
		requester := t.Requester
		if len(requester) > 20 {
			requester = requester[:17] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			truncateID(t.TaskID, 16),
			string(t.Status),
			title,
			assignee,
			requester,
			t.UpdatedAt.Format("2006-01-02 15:04:05"),
		)
	}
	w.Flush()
	return nil
}
