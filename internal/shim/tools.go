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
func registerTools(server *mcp.Server, mux *hubMux, agent *protocol.Agent) {
	registerListAgents(server, mux)
	registerWhoAmI(server, agent)
	registerSend(server, mux, agent)
	registerListConversations(server, mux)
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
