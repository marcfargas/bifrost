package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	hubpkg "github.com/marcfargas/bifrost/internal/hub"
)

// TestMCPHTTPLifecycle verifies the full lifecycle of a direct MCP HTTP
// connection: initialize session, register agent, list agents.
func TestMCPHTTPLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start hub with MCP HTTP enabled on a random port.
	cfg := config.Defaults()
	cfg.Hub.MCP.Enabled = true
	cfg.Hub.MCP.Port = 0 // let OS pick a free port
	cfg.Storage.DataDir = t.TempDir()

	srv, err := hubpkg.NewServer(&cfg, nil)
	if err != nil {
		t.Fatalf("create hub: %v", err)
	}

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start hub: %v", err)
	}
	defer srv.Stop()

	port := srv.MCPHTTPPort()
	if port == 0 {
		t.Fatal("MCPHTTPPort returned 0, expected a real port")
	}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)

	// --- Phase 1: Initialize MCP session ---

	initReq := mcpJSONRPCRequest("initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "test-client",
			"version": "1.0.0",
		},
	}, 1)

	resp, sessionID := doMCPPost(t, baseURL, "", initReq)
	if sessionID == "" {
		t.Fatal("expected Mcp-Session-Id header in initialize response")
	}

	// Verify the server declared claude/channel capability.
	var initResult struct {
		Result struct {
			Capabilities struct {
				Experimental map[string]any `json:"experimental"`
			} `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &initResult); err != nil {
		t.Fatalf("parse init response: %v (raw: %s)", err, resp)
	}
	if _, ok := initResult.Result.Capabilities.Experimental["claude/channel"]; !ok {
		t.Error("expected claude/channel in experimental capabilities")
	}

	// Send initialized notification.
	notif := mcpJSONRPCNotification("notifications/initialized", nil)
	doMCPPost(t, baseURL, sessionID, notif)

	// --- Phase 2: Register agent via bifrost_whoami ---

	whoamiReq := mcpJSONRPCRequest("tools/call", map[string]any{
		"name": "bifrost_whoami",
		"arguments": map[string]any{
			"project_name": "test-project-alpha",
			"local_path":   "/tmp/test/alpha",
		},
	}, 2)

	whoamiResp, _ := doMCPPost(t, baseURL, sessionID, whoamiReq)
	mcpAssertNoError(t, whoamiResp, "bifrost_whoami")

	// --- Phase 3: List agents (should see self) ---

	listReq := mcpJSONRPCRequest("tools/call", map[string]any{
		"name":      "bifrost_list_agents",
		"arguments": map[string]any{},
	}, 3)

	listResp, _ := doMCPPost(t, baseURL, sessionID, listReq)
	mcpAssertNoError(t, listResp, "bifrost_list_agents")

	if !strings.Contains(string(listResp), "test-project-alpha") {
		t.Errorf("expected to see test-project-alpha in agent list, got: %s", listResp)
	}
}

// TestMCPHTTPDisabledByDefault verifies MCP HTTP is not started when disabled.
func TestMCPHTTPDisabledByDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := config.Defaults()
	cfg.Hub.MCP.Enabled = false
	cfg.Hub.MCP.Port = 0
	cfg.Storage.DataDir = t.TempDir()

	srv, err := hubpkg.NewServer(&cfg, nil)
	if err != nil {
		t.Fatalf("create hub: %v", err)
	}

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start hub: %v", err)
	}
	defer srv.Stop()

	// MCPHTTPPort should return 0 when MCP is disabled.
	if port := srv.MCPHTTPPort(); port != 0 {
		t.Errorf("MCPHTTPPort = %d, want 0 when MCP is disabled", port)
	}
}

// TestMCPHTTPToolRegistration connects to MCP HTTP, initializes a session,
// and verifies that the bifrost tools are listed.
func TestMCPHTTPToolRegistration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cfg := config.Defaults()
	cfg.Hub.MCP.Enabled = true
	cfg.Hub.MCP.Port = 0
	cfg.Storage.DataDir = t.TempDir()

	srv, err := hubpkg.NewServer(&cfg, nil)
	if err != nil {
		t.Fatalf("create hub: %v", err)
	}

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start hub: %v", err)
	}
	defer srv.Stop()

	port := srv.MCPHTTPPort()
	baseURL := fmt.Sprintf("http://127.0.0.1:%d/mcp", port)

	// Initialize session.
	initReq := mcpJSONRPCRequest("initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test-client", "version": "1.0.0"},
	}, 1)
	_, sessionID := doMCPPost(t, baseURL, "", initReq)
	doMCPPost(t, baseURL, sessionID, mcpJSONRPCNotification("notifications/initialized", nil))

	// List tools.
	listToolsReq := mcpJSONRPCRequest("tools/list", nil, 2)
	toolsResp, _ := doMCPPost(t, baseURL, sessionID, listToolsReq)

	// Parse tools list.
	var toolsResult struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(toolsResp, &toolsResult); err != nil {
		t.Fatalf("parse tools/list response: %v (raw: %s)", err, toolsResp)
	}

	// Verify all expected bifrost tools are registered.
	expectedTools := []string{
		"bifrost_whoami",
		"bifrost_list_agents",
		"bifrost_send",
		"bifrost_list_conversations",
		"bifrost_create_task",
		"bifrost_update_task",
		"bifrost_get_task",
		"bifrost_list_tasks",
		"bifrost_subscribe",
		"bifrost_list_channels",
		"bifrost_dnd",
		"bifrost_peer",
	}

	registered := make(map[string]bool)
	for _, tool := range toolsResult.Result.Tools {
		registered[tool.Name] = true
	}

	for _, name := range expectedTools {
		if !registered[name] {
			t.Errorf("expected tool %q to be registered", name)
		}
	}
}

// --- Test helpers ---

func mcpJSONRPCRequest(method string, params any, id int) []byte {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"id":      id,
	}
	if params != nil {
		req["params"] = params
	}
	data, _ := json.Marshal(req)
	return data
}

func mcpJSONRPCNotification(method string, params any) []byte {
	req := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	data, _ := json.Marshal(req)
	return data
}

func doMCPPost(t *testing.T, baseURL, sessionID string, body []byte) ([]byte, string) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	sid := resp.Header.Get("Mcp-Session-Id")

	// MCP Streamable HTTP returns SSE-wrapped responses.
	// Parse "data: {...}\n" lines to extract the JSON payload.
	data := extractSSEData(raw)
	if data == nil {
		data = raw // fall back to raw if not SSE
	}

	return data, sid
}

// extractSSEData parses an SSE body and returns the JSON from the first
// "data:" line. Returns nil if no data line is found.
func extractSSEData(raw []byte) []byte {
	text := string(raw)
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if payload, ok := strings.CutPrefix(line, "data: "); ok {
			payload = strings.TrimSpace(payload)
			if payload != "" {
				return []byte(payload)
			}
		}
	}
	return nil
}

func mcpAssertNoError(t *testing.T, resp []byte, toolName string) {
	t.Helper()

	// MCP tools/call response shape:
	// {"jsonrpc":"2.0","id":N,"result":{"content":[...],"isError":false}}
	var result struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		t.Fatalf("parse %s response: %v (raw: %s)", toolName, err, resp)
	}
	if result.Error != nil {
		t.Errorf("%s returned JSON-RPC error: %s", toolName, result.Error.Message)
	}
	if result.Result.IsError {
		t.Errorf("%s returned tool error: %s", toolName, resp)
	}
}
