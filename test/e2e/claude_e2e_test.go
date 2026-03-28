//go:build e2e

// Package e2e contains end-to-end tests that require:
//   - the "e2e" build tag
//   - ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN set
//   - claude CLI on PATH
//
// Run: go test -tags e2e ./test/e2e/ -run TestClaude -v -timeout 120s
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/hub"
	"github.com/marcfargas/bifrost/internal/transport"
	"github.com/marcfargas/bifrost/pkg/protocol"
)

// testEnv holds a running hub and paths for claude E2E tests.
type testEnv struct {
	srv        *hub.Server
	socketPath string
	socketDir  string
	bifrostBin string
	pluginDir  string
}

func setupClaudeTest(t *testing.T) *testEnv {
	t.Helper()

	if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("ANTHROPIC_AUTH_TOKEN") == "" {
		t.Skip("ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN not set")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not on PATH")
	}

	bifrostBin := findBifrost(t)

	socketDir, err := os.MkdirTemp("", "bf-e2e")
	if err != nil {
		t.Fatalf("create socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })

	bifrostDir := filepath.Join(socketDir, "bifrost")
	os.MkdirAll(bifrostDir, 0o755)
	socketPath := filepath.Join(bifrostDir, "hub.sock")
	dataDir := filepath.Join(socketDir, "data")

	cfg := config.Defaults()
	cfg.Hub.Local.SocketPath = socketPath
	cfg.Storage.DataDir = dataDir
	cfg.Federation.Libp2p.Enabled = false
	cfg.Federation.MDNS.Enabled = false
	cfg.Federation.Direct.Enabled = false

	srv, err := hub.NewServer(&cfg, nil)
	if err != nil {
		t.Fatalf("create hub server: %v", err)
	}

	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start hub server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	waitForSocket(t, socketPath, 5*time.Second)

	// Resolve plugin dir (relative to module root).
	pluginDir := filepath.Join(findModuleRoot(t), "plugin")

	return &testEnv{
		srv:        srv,
		socketPath: socketPath,
		socketDir:  socketDir,
		bifrostBin: bifrostBin,
		pluginDir:  pluginDir,
	}
}

// runClaude runs claude -p in bare mode with the given extra args.
func (te *testEnv) runClaude(t *testing.T, prompt string, extraArgs ...string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	args := []string{
		"--bare",
		"--dangerously-skip-permissions",
		"-p", prompt,
		"--model", "haiku",
		"--max-turns", "5",
	}
	args = append(args, extraArgs...)

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = te.socketDir
	cmd.Env = te.buildEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	t.Logf("running: claude %s", strings.Join(args, " "))
	err := cmd.Run()

	t.Logf("stdout:\n%s", stdout.String())
	if stderr.Len() > 0 {
		t.Logf("stderr:\n%s", stderr.String())
	}

	if err != nil {
		t.Fatalf("claude exited with error: %v", err)
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		t.Fatal("claude produced no output")
	}

	return output
}

func (te *testEnv) buildEnv() []string {
	env := os.Environ()
	filtered := make([]string, 0, len(env)+3)
	for _, e := range env {
		key := strings.SplitN(e, "=", 2)[0]
		if strings.ToUpper(key) == "BIFROST_SOCKET_PATH" {
			continue
		}
		filtered = append(filtered, e)
	}

	filtered = append(filtered, "BIFROST_SOCKET_PATH="+te.socketPath)

	if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		filtered = append(filtered, "ANTHROPIC_API_KEY="+os.Getenv("ANTHROPIC_AUTH_TOKEN"))
	}
	if url := os.Getenv("ANTHROPIC_BASE_URL"); url != "" {
		filtered = append(filtered, "ANTHROPIC_BASE_URL="+url)
	}

	return filtered
}

func (te *testEnv) mcpConfigFlag() string {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"bifrost": map[string]any{
				"command": te.bifrostBin,
				"args":    []string{"shim"},
			},
		},
	}
	data, _ := json.Marshal(cfg)
	return string(data)
}

// --- Tests ---

// TestClaudeMCP_WhoAmI validates the MCP server mode (--mcp-config).
func TestClaudeMCP_WhoAmI(t *testing.T) {
	te := setupClaudeTest(t)

	output := te.runClaude(t,
		"Use the bifrost_whoami tool and tell me your agent ID. Output ONLY the agent ID, nothing else.",
		"--mcp-config", te.mcpConfigFlag(),
	)

	t.Logf("bifrost_whoami via MCP config: %s", output)
}

// TestClaudePlugin_WhoAmI validates the plugin mode (--plugin-dir).
// The plugin's .mcp.json uses "command": "bifrost" which must be on PATH.
// Known issue: --plugin-dir may not resolve MCP commands via PATH.
func TestClaudePlugin_WhoAmI(t *testing.T) {
	t.Skip("plugin MCP command resolution via --plugin-dir not yet working — use --mcp-config instead")
	te := setupClaudeTest(t)

	output := te.runClaude(t,
		"Use the bifrost_whoami tool and tell me your agent ID. Output ONLY the agent ID, nothing else.",
		"--plugin-dir", te.pluginDir,
	)

	// Verify the tool actually ran (output should be a hex agent ID, not an error).
	if strings.Contains(output, "don't have access") || strings.Contains(output, "not available") {
		t.Fatalf("plugin mode did not load bifrost tools:\n%s", output)
	}

	t.Logf("bifrost_whoami via plugin: %s", output)
}

// TestClaudeMCP_ListAgents registers a fake agent then asks claude to list agents.
func TestClaudeMCP_ListAgents(t *testing.T) {
	te := setupClaudeTest(t)

	// Register a fake agent directly via the hub socket.
	conn := dialHub(t, te.socketPath)
	agent := fakeAgent("test-backend")
	result := rpcRegister(t, conn, agent)
	if result.resp.Error != nil {
		t.Fatalf("register fake agent: %s", result.resp.Error.Message)
	}

	output := te.runClaude(t,
		"Use the bifrost_list_agents tool and tell me the names of all connected agents. Output ONLY the agent names, one per line.",
		"--mcp-config", te.mcpConfigFlag(),
	)

	if !strings.Contains(output, "test-backend") {
		t.Fatalf("output does not mention test-backend:\n%s", output)
	}

	t.Logf("bifrost_list_agents found test-backend")
}

// TestClaudeMCP_SendMessage has Claude send a message to a fake agent connected
// via raw socket. Verifies the message arrives on the fake agent's connection —
// proving the full path: Claude → shim MCP → hub → socket → recipient.
func TestClaudeMCP_SendMessage(t *testing.T) {
	te := setupClaudeTest(t)

	// Register fake agent and start reading notifications.
	conn := dialHub(t, te.socketPath)
	agent := fakeAgent("msg-receiver")
	result := rpcRegister(t, conn, agent)
	if result.resp.Error != nil {
		t.Fatalf("register fake agent: %s", result.resp.Error.Message)
	}

	// Start goroutine to read notifications from the fake agent's connection.
	// Drain the ping notification first, then wait for the real message.
	type received struct {
		body string
		err  error
	}
	msgCh := make(chan received, 1)
	go func() {
		for {
			raw, err := readRawNotification(conn)
			if err != nil {
				msgCh <- received{err: err}
				return
			}
			// Parse notification envelope.
			var notif struct {
				Params struct {
					Type    string `json:"type"`
					Payload struct {
						Body string `json:"body"`
						From string `json:"from"`
					} `json:"payload"`
				} `json:"params"`
			}
			json.Unmarshal(raw, &notif)
			if notif.Params.Type == "message.new" {
				msgCh <- received{body: notif.Params.Payload.Body}
				return
			}
			// Skip ping, agent.registered, etc.
		}
	}()

	// Ask Claude to send a message.
	output := te.runClaude(t,
		"Use bifrost_send to send a message to agent:msg-receiver with body 'hello from claude'. Use type CONTEXT. Output ONLY 'sent' when done.",
		"--mcp-config", te.mcpConfigFlag(),
	)
	t.Logf("claude output: %s", output)

	// Verify the fake agent received the message.
	select {
	case msg := <-msgCh:
		if msg.err != nil {
			t.Fatalf("fake agent receive error: %v", msg.err)
		}
		if !strings.Contains(msg.body, "hello from claude") {
			t.Fatalf("expected 'hello from claude' in body, got: %s", msg.body)
		}
		t.Logf("fake agent received: %s", msg.body)
	case <-time.After(5 * time.Second):
		t.Fatal("fake agent did not receive message within 5s")
	}
}

// TestClaudeMCP_CreateAndListTask has Claude create a task, then list tasks
// to verify the full task lifecycle works through MCP.
func TestClaudeMCP_CreateAndListTask(t *testing.T) {
	te := setupClaudeTest(t)

	// Register fake assignee.
	conn := dialHub(t, te.socketPath)
	agent := fakeAgent("task-worker")
	result := rpcRegister(t, conn, agent)
	if result.resp.Error != nil {
		t.Fatalf("register fake agent: %s", result.resp.Error.Message)
	}
	// Drain notifications in background so the socket doesn't block.
	go func() {
		for {
			if _, err := readRawNotification(conn); err != nil {
				return
			}
		}
	}()

	// Create a task.
	output := te.runClaude(t,
		"Use bifrost_create_task to create a task assigned to agent:task-worker with title 'Implement login' and description 'Add OAuth2 support'. Then use bifrost_list_tasks to list all tasks. Output the task ID and status.",
		"--mcp-config", te.mcpConfigFlag(),
	)

	if !strings.Contains(strings.ToLower(output), "requested") && !strings.Contains(strings.ToLower(output), "implement login") {
		t.Fatalf("output doesn't show the created task:\n%s", output)
	}

	t.Logf("task created and listed: %s", output)
}

// TestClaudeMCP_TwoAgentConversation runs two claude -p instances concurrently.
// Agent A sends a message, Agent B receives and replies.
func TestClaudeMCP_TwoAgentConversation(t *testing.T) {
	te := setupClaudeTest(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	mcpFlag := te.mcpConfigFlag()

	// Agent B runs first — it will register, then poll for messages.
	// We give it a prompt that makes it call bifrost_list_agents, confirming it's connected,
	// then we'll have agent A send to it.

	// Run Agent A: send message to whatever agent B registers as.
	// Agent B's display name will be based on the temp dir, unpredictable.
	// Instead: register a fake "relay" agent, have Claude A send to it,
	// then verify the relay received it. Then have Claude B send to Claude A's agent.

	// Simpler: run them sequentially sharing the same hub.
	// A sends to B (B is a fake socket agent), B's message arrives.
	// Then run Claude B which sends back to A (A is now a fake socket agent).

	// Step 1: Claude A sends to fake-b.
	connB := dialHub(t, te.socketPath)
	rpcRegister(t, connB, fakeAgent("agent-bravo"))
	msgFromA := make(chan string, 1)
	go func() {
		for {
			raw, err := readRawNotification(connB)
			if err != nil {
				return
			}
			var notif struct {
				Params struct {
					Type    string `json:"type"`
					Payload struct {
						Body string `json:"body"`
					} `json:"payload"`
				} `json:"params"`
			}
			json.Unmarshal(raw, &notif)
			if notif.Params.Type == "message.new" {
				msgFromA <- notif.Params.Payload.Body
				return
			}
		}
	}()

	argsA := []string{"--mcp-config", mcpFlag}
	outputA := te.runClaude(t,
		"Use bifrost_send to send a QUESTION to agent:agent-bravo with body 'What is your status?'. Output 'sent' when done.",
		argsA...,
	)
	t.Logf("Agent A output: %s", outputA)

	select {
	case body := <-msgFromA:
		t.Logf("Agent Bravo received from A: %s", body)
	case <-ctx.Done():
		t.Fatal("Agent Bravo did not receive message from A")
	}

	// Step 2: Now register fake-a, run Claude B which sends back.
	connA := dialHub(t, te.socketPath)
	rpcRegister(t, connA, fakeAgent("agent-alpha"))
	msgFromB := make(chan string, 1)
	go func() {
		for {
			raw, err := readRawNotification(connA)
			if err != nil {
				return
			}
			var notif struct {
				Params struct {
					Type    string `json:"type"`
					Payload struct {
						Body string `json:"body"`
					} `json:"payload"`
				} `json:"params"`
			}
			json.Unmarshal(raw, &notif)
			if notif.Params.Type == "message.new" {
				msgFromB <- notif.Params.Payload.Body
				return
			}
		}
	}()

	outputB := te.runClaude(t,
		"Use bifrost_send to send an ANSWER to agent:agent-alpha with body 'All systems operational'. Output 'sent' when done.",
		argsA...,
	)
	t.Logf("Agent B output: %s", outputB)

	select {
	case body := <-msgFromB:
		t.Logf("Agent Alpha received from B: %s", body)
	case <-ctx.Done():
		t.Fatal("Agent Alpha did not receive message from B")
	}

	t.Log("Two-agent conversation complete: A→B and B→A both delivered")
}

// TestClaudeMCP_ChannelPush verifies that channel notifications are pushed to
// the agent. A fake sender sends a message to the claude agent, and we verify
// claude sees it as a <channel> tag in its conversation.
//
// Known limitation: --bare mode with --mcp-config may not support --channels
// because --channels server:X looks up servers from user config, not --mcp-config.
// This test documents the desired behavior and will pass once Claude Code
// supports channel registration via --mcp-config.
func TestClaudeMCP_ChannelPush(t *testing.T) {
	// Channels require claude.ai OAuth login — API key auth (used in CI) is not supported.
	// See: https://code.claude.com/docs/en/channels ("They require claude.ai login.
	// Console and API key authentication is not supported.")
	// This test works locally with interactive claude sessions that are logged in.
	// Run manually: go test -tags e2e ./test/e2e/ -run TestClaudeMCP_ChannelPush -v
	t.Skip("channel push requires claude.ai login — cannot test with API key auth in CI")
	te := setupClaudeTest(t)

	// Build the fakesender binary.
	senderBin := filepath.Join(t.TempDir(), "fakesender")
	if runtime.GOOS == "windows" {
		senderBin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", senderBin, "./test/e2e/cmd/fakesender")
	cmd.Dir = findModuleRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fakesender: %v\n%s", err, out)
	}

	// Launch fakesender in background — it will register, wait 3 seconds,
	// then send a message to whatever agent claude registers as.
	// We need to guess claude's agent name. The shim uses project_name from
	// the working dir. We'll target by listing agents first.

	// Strategy: run fakesender targeting "agent:*" (broadcast) — but bifrost
	// doesn't support wildcards in bifrost_send "to" field for non-"*" targets.
	// Instead: use the hub store. We know the claude agent's ID will be based
	// on hostname+path. Easier: just target by project name.
	// The shim's project name comes from the working dir basename.
	// Our test runs in socketDir, so project_name = basename of socketDir.

	// Even simpler: have claude call bifrost_whoami first to get its own ID,
	// then have fakesender target that ID. But fakesender runs before claude...

	// Simplest approach: fakesender sends to "*" (broadcast). Claude will
	// receive it as a channel notification. But messages.go handles "*" by
	// calling routeBroadcast which skips the sender.

	// Best approach: register fakesender, then in claude's prompt ask it to
	// call bifrost_whoami AND wait for a message. Meanwhile fakesender
	// sends to the agent name claude registers as.

	// Actually — let's just have fakesender send to a well-known name.
	// We set --display-name for the shim... except we don't control that
	// through --mcp-config.

	// Real approach: start claude, have it call bifrost_whoami to register,
	// THEN start fakesender targeting that agent. But we don't know the ID
	// until claude runs.

	// Pragmatic: fakesender sends to ALL agents via broadcast "*".
	// The hub will deliver to all online agents except fakesender.
	// Claude should see it as a <channel> notification.

	// Start fakesender with 3 second delay, broadcasting.
	senderCtx, senderCancel := context.WithCancel(context.Background())
	defer senderCancel()
	sender := exec.CommandContext(senderCtx, senderBin,
		te.socketPath, "*", "3", "CHANNEL_PUSH_TEST_MESSAGE")
	sender.Env = te.buildEnv()
	sender.Stderr = os.Stderr
	if err := sender.Start(); err != nil {
		t.Fatalf("start fakesender: %v", err)
	}
	defer sender.Process.Kill()

	// Run claude with --channels so it receives push notifications.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The --dangerously-load-development-channels flag needs the server in
	// .mcp.json (project config), not --mcp-config. Write a .mcp.json in
	// the working directory so claude discovers it.
	mcpDotJSON, _ := json.MarshalIndent(map[string]any{
		"mcpServers": map[string]any{
			"bifrost": map[string]any{
				"command": te.bifrostBin,
				"args":    []string{"shim"},
			},
		},
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(te.socketDir, ".mcp.json"), mcpDotJSON, 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}

	args := []string{
		"--dangerously-skip-permissions",
		"--dangerously-load-development-channels", "server:bifrost",
		"-p", "First call bifrost_whoami to register. Then wait 5 seconds using the Bash tool (run 'sleep 5'). After waiting, report any <channel> notifications you received from bifrost. If you see a message containing 'CHANNEL_PUSH_TEST_MESSAGE', output exactly 'PUSH_RECEIVED'. If no channel messages, output 'NO_PUSH'.",
		"--model", "haiku",
		"--max-turns", "8",
	}

	claudeCmd := exec.CommandContext(ctx, "claude", args...)
	claudeCmd.Dir = te.socketDir
	claudeCmd.Env = te.buildEnv()

	var stdout, stderr bytes.Buffer
	claudeCmd.Stdout = &stdout
	claudeCmd.Stderr = &stderr

	t.Log("running claude with --channels for push test...")
	err := claudeCmd.Run()

	t.Logf("stdout:\n%s", stdout.String())
	if stderr.Len() > 0 {
		t.Logf("stderr:\n%s", stderr.String())
	}

	if err != nil {
		t.Fatalf("claude exited with error: %v", err)
	}

	output := stdout.String()
	if strings.Contains(output, "PUSH_RECEIVED") {
		t.Log("Channel push notification verified!")
	} else if strings.Contains(output, "NO_PUSH") {
		t.Fatal("Claude did not receive channel push notification")
	} else {
		t.Logf("Unexpected output (checking for partial match):\n%s", output)
		if strings.Contains(output, "CHANNEL_PUSH_TEST_MESSAGE") {
			t.Log("Channel push content found in output (partial match)")
		} else {
			t.Fatal("Could not verify channel push delivery")
		}
	}
}

// readRawNotification reads one JSON-RPC notification from a transport.Conn.
func readRawNotification(conn *transport.Conn) ([]byte, error) {
	var raw json.RawMessage
	if err := conn.Receive(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// --- Helpers ---

func findBifrost(t *testing.T) string {
	t.Helper()
	if bin, err := exec.LookPath("bifrost"); err == nil {
		return bin
	}
	tmpDir := t.TempDir()
	bin := filepath.Join(tmpDir, "bifrost")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/bifrost")
	cmd.Dir = findModuleRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build bifrost: %v\n%s", err, out)
	}
	return bin
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}

func waitForSocket(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.Dial("unix", path)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("socket %s not available within %v", path, timeout)
}

func fakeAgent(id string) protocol.Agent {
	now := time.Now()
	return protocol.Agent{
		AgentID:         id,
		Username:        "ci",
		Hostname:        "ci-host",
		LocalPath:       "/tmp/fake",
		ProjectName:     id,
		DisplayName:     "ci@" + id,
		ProtocolVersion: protocol.ProtocolVersion,
		ConnectedAt:     now,
		LastSeen:        now,
	}
}
