package shim

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerTools adds all Bifrost MCP tools to the server.
func registerTools(server *mcp.Server, mux *hubMux, agent *protocol.Agent, nw *notificationWriter) {
	registerListAgents(server, mux)
	registerWhoAmI(server, agent)
	registerSend(server, mux, agent)
	registerListConversations(server, mux)
	registerCreateTask(server, mux, agent)
	registerUpdateTask(server, mux, agent)
	registerGetTask(server, mux)
	registerListTasks(server, mux)
	registerSubscribe(server, mux, agent)
	registerListChannels(server, mux)
	registerDND(server, mux, agent, nw)
	registerPeer(server, mux)
}

// registerListAgents registers the bifrost_list_agents tool.
func registerListAgents(server *mcp.Server, mux *hubMux) {
	type listAgentsArgs struct {
		Status string `json:"status,omitempty" jsonschema:"optional filter by agent status (online, idle, offline, dnd)"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "bifrost_list_agents",
		Description: "List agents connected to the Bifrost hub. Optionally filter by status.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args listAgentsArgs) (*mcp.CallToolResult, any, error) {
		params := map[string]any{}
		if args.Status != "" {
			params["status"] = args.Status
		}

		resp, err := mux.rpcCall(ctx, "hub.list_agents", params)
		if err != nil {
			return nil, nil, fmt.Errorf("hub call failed: %w", err)
		}
		if resp.Error != nil {
			return errorResult(resp.Error.Message), nil, nil
		}

		text, err := formatJSON(resp.Result)
		if err != nil {
			return errorResult("failed to format response"), nil, nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, nil, nil
	})
}

// registerWhoAmI registers the bifrost_whoami tool.
func registerWhoAmI(server *mcp.Server, agent *protocol.Agent) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "bifrost_whoami",
		Description: "Show the current agent's identity in the Bifrost network.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
		text, err := formatJSON(agent)
		if err != nil {
			return errorResult("failed to format identity"), nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, nil, nil
	})
}

// registerSend registers the bifrost_send tool.
func registerSend(server *mcp.Server, mux *hubMux, agent *protocol.Agent) {
	type sendArgs struct {
		To       string `json:"to" jsonschema:"recipient address: agent:name, channel:name, or task:id"`
		Body     string `json:"body" jsonschema:"message body text"`
		Type     string `json:"type,omitempty" jsonschema:"message type: QUESTION, ANSWER, CONTEXT, STATUS, ERROR (default ANSWER)"`
		Subject  string `json:"subject,omitempty" jsonschema:"optional message subject line"`
		Priority string `json:"priority,omitempty" jsonschema:"message priority: low, normal, urgent (default normal)"`
		InReplyTo string `json:"in_reply_to,omitempty" jsonschema:"optional message ID this replies to"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "bifrost_send",
		Description: "Send a message to another agent, channel, or task in the Bifrost network.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args sendArgs) (*mcp.CallToolResult, any, error) {
		msgType := args.Type
		if msgType == "" {
			msgType = "ANSWER"
		}
		priority := args.Priority
		if priority == "" {
			priority = "normal"
		}

		msg := protocol.Message{
			ID:        protocol.NewID(),
			From:      agent.AgentID,
			To:        args.To,
			Body:      args.Body,
			Type:      protocol.MessageType(strings.ToUpper(msgType)),
			Subject:   args.Subject,
			Priority:  protocol.Priority(priority),
			InReplyTo: args.InReplyTo,
		}

		resp, err := mux.rpcCall(ctx, "msg.send", msg)
		if err != nil {
			return nil, nil, fmt.Errorf("hub call failed: %w", err)
		}
		if resp.Error != nil {
			// If the error includes available agents, show them.
			result := resp.Error.Message
			if resp.Error.Data != nil {
				if agents, err := formatJSON(resp.Error.Data); err == nil {
					result += "\n\nAvailable agents:\n" + agents
				}
			}
			return errorResult(result), nil, nil
		}

		text, err := formatJSON(resp.Result)
		if err != nil {
			return errorResult("message sent but failed to format response"), nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Message sent.\n" + text}},
		}, nil, nil
	})
}

// registerListConversations registers the bifrost_list_conversations tool.
func registerListConversations(server *mcp.Server, mux *hubMux) {
	type listConvsArgs struct {
		ActiveOnly bool `json:"active_only,omitempty" jsonschema:"if true, show only active conversations"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "bifrost_list_conversations",
		Description: "List conversations in the Bifrost hub.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args listConvsArgs) (*mcp.CallToolResult, any, error) {
		params := map[string]any{}
		if args.ActiveOnly {
			params["active_only"] = true
		}

		resp, err := mux.rpcCall(ctx, "hub.list_conversations", params)
		if err != nil {
			return nil, nil, fmt.Errorf("hub call failed: %w", err)
		}
		if resp.Error != nil {
			return errorResult(resp.Error.Message), nil, nil
		}

		text, err := formatJSON(resp.Result)
		if err != nil {
			return errorResult("failed to format response"), nil, nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, nil, nil
	})
}

// registerCreateTask registers the bifrost_create_task tool.
func registerCreateTask(server *mcp.Server, mux *hubMux, agent *protocol.Agent) {
	type createTaskArgs struct {
		Assignee    string   `json:"assignee" jsonschema:"agent address to assign the task to"`
		Title       string   `json:"title" jsonschema:"short task title"`
		Description string   `json:"description,omitempty" jsonschema:"full task description"`
		Files       []string `json:"files,omitempty" jsonschema:"optional list of local file paths to attach"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "bifrost_create_task",
		Description: "Create a new task assigned to another agent in the Bifrost network.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args createTaskArgs) (*mcp.CallToolResult, any, error) {
		if args.Assignee == "" {
			return errorResult("assignee is required"), nil, nil
		}
		if args.Title == "" {
			return errorResult("title is required"), nil, nil
		}

		params := map[string]any{
			"requester":   agent.AgentID,
			"assignee":    args.Assignee,
			"title":       args.Title,
			"description": args.Description,
		}
		if len(args.Files) > 0 {
			params["files"] = args.Files
		}

		resp, err := mux.rpcCall(ctx, "task.create", params)
		if err != nil {
			return nil, nil, fmt.Errorf("hub call failed: %w", err)
		}
		if resp.Error != nil {
			return errorResult(resp.Error.Message), nil, nil
		}

		text, err := formatJSON(resp.Result)
		if err != nil {
			return errorResult("task created but failed to format response"), nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Task created.\n" + text}},
		}, nil, nil
	})
}

// registerUpdateTask registers the bifrost_update_task tool.
func registerUpdateTask(server *mcp.Server, mux *hubMux, agent *protocol.Agent) {
	type updateTaskArgs struct {
		TaskID      string `json:"task_id" jsonschema:"ID of the task to update"`
		Status      string `json:"status,omitempty" jsonschema:"new status: accepted, in_progress, completed, failed, rejected"`
		Description string `json:"description,omitempty" jsonschema:"updated task description"`
		Summary     string `json:"summary,omitempty" jsonschema:"completion summary"`
		Reason      string `json:"reason,omitempty" jsonschema:"reason for rejection or failure"`
		Files       []string `json:"files,omitempty" jsonschema:"optional list of local file paths to attach"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "bifrost_update_task",
		Description: "Update the status or details of a task in the Bifrost network.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args updateTaskArgs) (*mcp.CallToolResult, any, error) {
		if args.TaskID == "" {
			return errorResult("task_id is required"), nil, nil
		}

		params := map[string]any{
			"agent_id": agent.AgentID,
			"task_id":  args.TaskID,
		}
		if args.Status != "" {
			params["status"] = args.Status
		}
		if args.Description != "" {
			params["description"] = args.Description
		}
		if args.Summary != "" {
			params["summary"] = args.Summary
		}
		if args.Reason != "" {
			params["reason"] = args.Reason
		}
		if len(args.Files) > 0 {
			params["files"] = args.Files
		}

		resp, err := mux.rpcCall(ctx, "task.update", params)
		if err != nil {
			return nil, nil, fmt.Errorf("hub call failed: %w", err)
		}
		if resp.Error != nil {
			return errorResult(resp.Error.Message), nil, nil
		}

		text, err := formatJSON(resp.Result)
		if err != nil {
			return errorResult("task updated but failed to format response"), nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "Task updated.\n" + text}},
		}, nil, nil
	})
}

// registerGetTask registers the bifrost_get_task tool.
func registerGetTask(server *mcp.Server, mux *hubMux) {
	type getTaskArgs struct {
		TaskID string `json:"task_id" jsonschema:"ID of the task to retrieve"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "bifrost_get_task",
		Description: "Get full details of a task by ID, including status, description, and attachment list.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args getTaskArgs) (*mcp.CallToolResult, any, error) {
		if args.TaskID == "" {
			return errorResult("task_id is required"), nil, nil
		}

		resp, err := mux.rpcCall(ctx, "task.get", map[string]any{"task_id": args.TaskID})
		if err != nil {
			return nil, nil, fmt.Errorf("hub call failed: %w", err)
		}
		if resp.Error != nil {
			return errorResult(resp.Error.Message), nil, nil
		}

		// Parse the task to produce a human-friendly formatted output.
		taskData, err := json.Marshal(resp.Result)
		if err != nil {
			return errorResult("failed to marshal task"), nil, nil
		}

		var task protocol.Task
		if err := json.Unmarshal(taskData, &task); err != nil {
			// Fall back to raw JSON if we can't parse it.
			text, _ := formatJSON(resp.Result)
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: text}},
			}, nil, nil
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "Task: %s\n", task.TaskID)
		fmt.Fprintf(&sb, "Title: %s\n", task.Title)
		fmt.Fprintf(&sb, "Status: %s\n", task.Status)
		fmt.Fprintf(&sb, "Requester: %s\n", task.Requester)
		fmt.Fprintf(&sb, "Assignee: %s\n", task.Assignee)
		if task.Description != "" {
			fmt.Fprintf(&sb, "Description:\n  %s\n", strings.ReplaceAll(task.Description, "\n", "\n  "))
		}
		if task.Summary != "" {
			fmt.Fprintf(&sb, "Summary:\n  %s\n", strings.ReplaceAll(task.Summary, "\n", "\n  "))
		}
		if task.Reason != "" {
			fmt.Fprintf(&sb, "Reason: %s\n", task.Reason)
		}
		fmt.Fprintf(&sb, "Created: %s\n", task.CreatedAt.Format("2006-01-02 15:04:05 UTC"))
		fmt.Fprintf(&sb, "Updated: %s\n", task.UpdatedAt.Format("2006-01-02 15:04:05 UTC"))
		if len(task.Attachments) > 0 {
			fmt.Fprintf(&sb, "Attachments (%d):\n", len(task.Attachments))
			for _, att := range task.Attachments {
				fmt.Fprintf(&sb, "  - %s\n", att)
			}
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: sb.String()}},
		}, nil, nil
	})
}

// registerListTasks registers the bifrost_list_tasks tool.
func registerListTasks(server *mcp.Server, mux *hubMux) {
	type listTasksArgs struct {
		Status    string `json:"status,omitempty" jsonschema:"filter by status: requested, accepted, in_progress, completed, failed, rejected"`
		Requester string `json:"requester,omitempty" jsonschema:"filter by requester agent ID"`
		Assignee  string `json:"assignee,omitempty" jsonschema:"filter by assignee agent ID"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "bifrost_list_tasks",
		Description: "List tasks in the Bifrost network, optionally filtered by status, requester, or assignee.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args listTasksArgs) (*mcp.CallToolResult, any, error) {
		params := map[string]any{}
		if args.Status != "" {
			params["status"] = args.Status
		}
		if args.Requester != "" {
			params["requester"] = args.Requester
		}
		if args.Assignee != "" {
			params["assignee"] = args.Assignee
		}

		resp, err := mux.rpcCall(ctx, "task.list", params)
		if err != nil {
			return nil, nil, fmt.Errorf("hub call failed: %w", err)
		}
		if resp.Error != nil {
			return errorResult(resp.Error.Message), nil, nil
		}

		// Parse task list and format as a table.
		taskData, err := json.Marshal(resp.Result)
		if err != nil {
			return errorResult("failed to marshal task list"), nil, nil
		}

		var tasks []protocol.Task
		if err := json.Unmarshal(taskData, &tasks); err != nil || len(tasks) == 0 {
			if err == nil && len(tasks) == 0 {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: "No tasks found."}},
				}, nil, nil
			}
			// Fall back to raw JSON.
			text, _ := formatJSON(resp.Result)
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: text}},
			}, nil, nil
		}

		var sb strings.Builder
		fmt.Fprintf(&sb, "%-26s  %-12s  %-20s  %s\n", "TASK ID", "STATUS", "ASSIGNEE", "TITLE")
		fmt.Fprintf(&sb, "%s\n", strings.Repeat("-", 80))
		for _, t := range tasks {
			title := t.Title
			if len(title) > 30 {
				title = title[:27] + "..."
			}
			assignee := t.Assignee
			if len(assignee) > 20 {
				assignee = assignee[:17] + "..."
			}
			fmt.Fprintf(&sb, "%-26s  %-12s  %-20s  %s\n", t.TaskID, string(t.Status), assignee, title)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: sb.String()}},
		}, nil, nil
	})
}

// registerSubscribe registers the bifrost_subscribe tool.
func registerSubscribe(server *mcp.Server, mux *hubMux, agent *protocol.Agent) {
	type subscribeArgs struct {
		Target string `json:"target" jsonschema:"channel or task target to subscribe to (e.g. channel:general, task:id)"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "bifrost_subscribe",
		Description: "Subscribe to a channel or task target to receive notifications from it.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args subscribeArgs) (*mcp.CallToolResult, any, error) {
		if args.Target == "" {
			return errorResult("target is required"), nil, nil
		}

		params := map[string]any{
			"agent_id": agent.AgentID,
			"target":   args.Target,
		}

		resp, err := mux.rpcCall(ctx, "channel.subscribe", params)
		if err != nil {
			return nil, nil, fmt.Errorf("hub call failed: %w", err)
		}
		if resp.Error != nil {
			return errorResult(resp.Error.Message), nil, nil
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Subscribed to %s.", args.Target)}},
		}, nil, nil
	})
}

// registerListChannels registers the bifrost_list_channels tool.
func registerListChannels(server *mcp.Server, mux *hubMux) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "bifrost_list_channels",
		Description: "List all known channels in the Bifrost network.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
		resp, err := mux.rpcCall(ctx, "channel.list", map[string]any{})
		if err != nil {
			return nil, nil, fmt.Errorf("hub call failed: %w", err)
		}
		if resp.Error != nil {
			return errorResult(resp.Error.Message), nil, nil
		}

		text, err := formatJSON(resp.Result)
		if err != nil {
			return errorResult("failed to format response"), nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, nil, nil
	})
}

// registerDND registers the bifrost_dnd tool.
func registerDND(server *mcp.Server, mux *hubMux, agent *protocol.Agent, nw *notificationWriter) {
	type dndArgs struct {
		Enabled bool   `json:"enabled" jsonschema:"true to enable DND, false to disable"`
		Reason  string `json:"reason,omitempty" jsonschema:"optional reason shown to other agents while in DND mode"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "bifrost_dnd",
		Description: "Enable or disable Do Not Disturb mode. While enabled, incoming messages are queued and a reminder loop notifies you of pending messages.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args dndArgs) (*mcp.CallToolResult, any, error) {
		params := map[string]any{
			"agent_id": agent.AgentID,
			"enabled":  args.Enabled,
		}
		if args.Reason != "" {
			params["reason"] = args.Reason
		}

		resp, err := mux.rpcCall(ctx, "dnd.set", params)
		if err != nil {
			return nil, nil, fmt.Errorf("hub call failed: %w", err)
		}
		if resp.Error != nil {
			return errorResult(resp.Error.Message), nil, nil
		}

		if args.Enabled {
			globalDND.start(ctx, mux, agent.AgentID, nw)
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "DND mode enabled. Incoming messages will be queued."}},
			}, nil, nil
		}

		globalDND.stop()

		// Report how many messages were flushed.
		result := resp.Result
		resultJSON, _ := formatJSON(result)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "DND mode disabled.\n" + resultJSON}},
		}, nil, nil
	})
}

// registerPeer registers the bifrost_peer tool.
func registerPeer(server *mcp.Server, mux *hubMux) {
	type peerArgs struct {
		Action string `json:"action" jsonschema:"action to perform: 'new' to generate a magic code, 'join' to connect using a code"`
		Code   string `json:"code,omitempty" jsonschema:"magic code (required for 'join' action)"`
	}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "bifrost_peer",
		Description: "Manage federation peering. Use 'new' to generate a magic code, or 'join' with a code to connect to a remote hub.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args peerArgs) (*mcp.CallToolResult, any, error) {
		switch args.Action {
		case "new":
			resp, err := mux.rpcCall(ctx, "peer.new", map[string]any{})
			if err != nil {
				return nil, nil, fmt.Errorf("hub call failed: %w", err)
			}
			if resp.Error != nil {
				return errorResult(resp.Error.Message), nil, nil
			}

			data, jsonErr := json.Marshal(resp.Result)
			if jsonErr != nil {
				return errorResult("failed to marshal response"), nil, nil
			}

			var result struct {
				Code string `json:"code"`
			}
			if jsonErr := json.Unmarshal(data, &result); jsonErr != nil {
				return errorResult("failed to parse response"), nil, nil
			}

			msg := fmt.Sprintf("Magic code generated: %s\nShare this code with the other hub. They should run: bifrost peer join %s\nThe code expires in 24 hours.", result.Code, result.Code)
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: msg}},
			}, nil, nil

		case "join":
			if args.Code == "" {
				return errorResult("'code' is required for join action"), nil, nil
			}

			resp, err := mux.rpcCall(ctx, "peer.join", map[string]any{"code": args.Code})
			if err != nil {
				return nil, nil, fmt.Errorf("hub call failed: %w", err)
			}
			if resp.Error != nil {
				return errorResult(resp.Error.Message), nil, nil
			}

			data, jsonErr := json.Marshal(resp.Result)
			if jsonErr != nil {
				return errorResult("failed to marshal response"), nil, nil
			}

			var result struct {
				PeerID string             `json:"peer_id"`
				Agents []protocol.Agent   `json:"agents"`
			}
			if jsonErr := json.Unmarshal(data, &result); jsonErr != nil {
				return errorResult("failed to parse response"), nil, nil
			}

			var sb strings.Builder
			fmt.Fprintf(&sb, "Connected to peer hub: %s\n", result.PeerID)
			fmt.Fprintf(&sb, "%d remote agents available.\n", len(result.Agents))
			if len(result.Agents) > 0 {
				sb.WriteString("Remote agents:\n")
				for _, a := range result.Agents {
					name := a.AgentID
					if len(a.Aliases) > 0 {
						name = a.Aliases[0]
					}
					fmt.Fprintf(&sb, "  - %s (%s)\n", name, a.Status)
				}
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: sb.String()}},
			}, nil, nil

		default:
			return errorResult(fmt.Sprintf("unknown action %q, must be 'new' or 'join'", args.Action)), nil, nil
		}
	})
}

// errorResult creates a CallToolResult with IsError set.
func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
		IsError: true,
	}
}

// formatJSON marshals v as indented JSON text.
func formatJSON(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
