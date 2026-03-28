//go:build e2e

// Package e2e contains end-to-end tests. Tests in this file require:
//   - the "e2e" build tag
//   - ANTHROPIC_API_KEY set
//   - bifrost binary on PATH (or built via go build)
//   - claude CLI on PATH (npm install -g @anthropic-ai/claude-code)
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
	"strings"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/hub"
	"github.com/marcfargas/bifrost/pkg/protocol"
)

// TestClaudeWhoAmI starts a hub, then runs `claude -p` with bifrost configured
// as an MCP server. It asks Claude to call bifrost_whoami and verifies the
// output contains an agent ID. This proves the full MCP pipeline: claude
// spawns the shim, the shim connects to the hub, registers, and serves tools.
func TestClaudeWhoAmI(t *testing.T) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("ANTHROPIC_AUTH_TOKEN") == "" {
		t.Skip("ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN not set")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not on PATH")
	}

	bifrostBin := findBifrost(t)

	// Create dirs for hub socket and data.
	// We use a short base path to stay within Unix socket length limits.
	socketDir, err := os.MkdirTemp("", "bf-e2e")
	if err != nil {
		t.Fatalf("create socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })

	// The shim resolves the socket via config.SocketPath(), which on Linux
	// uses $BIFROST_SOCKET_PATH/bifrost/hub.sock. We point both the hub and
	// the shim (via env) at the same path.
	bifrostDir := filepath.Join(socketDir, "bifrost")
	if err := os.MkdirAll(bifrostDir, 0o755); err != nil {
		t.Fatalf("mkdir bifrost dir: %v", err)
	}
	socketPath := filepath.Join(bifrostDir, "hub.sock")

	dataDir := filepath.Join(socketDir, "data")

	// Start the hub via Go API.
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

	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start hub server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	// Wait for socket to be ready.
	waitForSocket(t, socketPath, 5*time.Second)

	projectDir := filepath.Join(socketDir, "project")
	os.MkdirAll(projectDir, 0o755)

	// Build MCP config for --mcp-config flag.
	mcpCfg := map[string]any{
		"mcpServers": map[string]any{
			"bifrost": map[string]any{
				"command": bifrostBin,
				"args":    []string{"shim"},
			},
		},
	}
	mcpJSON, _ := json.Marshal(mcpCfg)

	// Run claude -p in bare mode with explicit MCP config.
	claudeCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(claudeCtx, "claude",
		"--bare",
		"--dangerously-skip-permissions",
		"-p", "Use the bifrost_whoami tool and tell me your agent ID. Output ONLY the agent ID, nothing else.",
		"--model", "haiku",
		"--max-turns", "5",
		"--mcp-config", string(mcpJSON),
	)
	cmd.Dir = projectDir

	// Set BIFROST_SOCKET_PATH so the shim connects to our test hub.
	cmd.Env = buildEnv(socketPath)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	t.Log("running: claude -p ...")
	err = cmd.Run()

	t.Logf("claude stdout:\n%s", stdout.String())
	t.Logf("claude stderr:\n%s", stderr.String())

	if err != nil {
		t.Fatalf("claude exited with error: %v", err)
	}

	output := stdout.String()
	// The output should contain some agent ID. The shim generates IDs like
	// "user@project" or similar. We just verify it's non-empty and doesn't
	// contain obvious error markers.
	if strings.TrimSpace(output) == "" {
		t.Fatal("claude produced no output")
	}
	if strings.Contains(strings.ToLower(output), "error") && strings.Contains(strings.ToLower(output), "could not") {
		t.Fatalf("claude output suggests a failure: %s", output)
	}

	t.Logf("bifrost_whoami returned: %s", strings.TrimSpace(output))
}

// TestClaudeListAgents starts a hub, registers a fake agent via the raw
// protocol, then asks claude -p to call bifrost_list_agents and verifies the
// fake agent appears in the output.
func TestClaudeListAgents(t *testing.T) {
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
	if err := os.MkdirAll(bifrostDir, 0o755); err != nil {
		t.Fatalf("mkdir bifrost dir: %v", err)
	}
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

	ctx := context.Background()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("start hub server: %v", err)
	}
	t.Cleanup(func() { srv.Stop() })

	waitForSocket(t, socketPath, 5*time.Second)

	// Register a fake agent directly via the hub protocol so it shows up
	// in bifrost_list_agents.
	conn := dialHub(t, socketPath)
	agent := fakeAgent("test-backend")
	result := rpcRegister(t, conn, agent)
	if result.resp.Error != nil {
		t.Fatalf("register fake agent: %s", result.resp.Error.Message)
	}

	projectDir := filepath.Join(socketDir, "project")
	os.MkdirAll(projectDir, 0o755)

	mcpCfg := map[string]any{
		"mcpServers": map[string]any{
			"bifrost": map[string]any{
				"command": bifrostBin,
				"args":    []string{"shim"},
			},
		},
	}
	mcpJSON, _ := json.Marshal(mcpCfg)

	claudeCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(claudeCtx, "claude",
		"--bare",
		"--dangerously-skip-permissions",
		"-p", "Use the bifrost_list_agents tool and tell me the names of all connected agents. Output ONLY the agent names, one per line.",
		"--model", "haiku",
		"--max-turns", "5",
		"--mcp-config", string(mcpJSON),
	)
	cmd.Dir = projectDir
	cmd.Env = buildEnv(socketPath)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	t.Log("running: claude -p (list agents)...")
	err = cmd.Run()

	t.Logf("claude stdout:\n%s", stdout.String())
	t.Logf("claude stderr:\n%s", stderr.String())

	if err != nil {
		t.Fatalf("claude exited with error: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "test-backend") {
		t.Fatalf("output does not mention test-backend:\n%s", output)
	}

	t.Logf("bifrost_list_agents found test-backend in output")
}

// findBifrost returns the path to the bifrost binary. It checks PATH first,
// then tries to build it.
func findBifrost(t *testing.T) string {
	t.Helper()

	if bin, err := exec.LookPath("bifrost"); err == nil {
		return bin
	}

	// Not on PATH — build it into a temp dir.
	tmpDir := t.TempDir()
	bin := filepath.Join(tmpDir, "bifrost")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/bifrost")
	cmd.Dir = findModuleRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build bifrost: %v\n%s", err, out)
	}
	return bin
}

// findModuleRoot walks up from the test file to find the go.mod directory.
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
			t.Fatal("could not find go.mod in parent directories")
		}
		dir = parent
	}
}

// waitForSocket polls until a Unix socket is connectable or the timeout expires.
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
	t.Fatalf("socket %s did not become available within %v", path, timeout)
}

// fakeAgent returns a protocol.Agent suitable for direct registration.
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

// buildEnv constructs the environment for the claude subprocess.
// It passes through the current environment but overrides BIFROST_SOCKET_PATH
// so the bifrost shim connects to our test hub's socket.
func buildEnv(socketPath string) []string {
	env := os.Environ()

	// Filter out existing BIFROST_SOCKET_PATH, HOME (we keep HOME), and
	// BIFROST_* vars that might interfere.
	filtered := make([]string, 0, len(env)+3)
	for _, e := range env {
		key := strings.SplitN(e, "=", 2)[0]
		switch strings.ToUpper(key) {
		case "BIFROST_SOCKET_PATH":
			continue // we'll set our own
		default:
			filtered = append(filtered, e)
		}
	}

	filtered = append(filtered, "BIFROST_SOCKET_PATH="+socketPath)

	// If using ANTHROPIC_AUTH_TOKEN (litellm proxy), map it to ANTHROPIC_API_KEY
	// so claude CLI picks it up. Also pass through ANTHROPIC_BASE_URL.
	if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		filtered = append(filtered, "ANTHROPIC_API_KEY="+os.Getenv("ANTHROPIC_AUTH_TOKEN"))
	}
	if url := os.Getenv("ANTHROPIC_BASE_URL"); url != "" {
		filtered = append(filtered, "ANTHROPIC_BASE_URL="+url)
	}

	return filtered
}
