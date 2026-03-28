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
	"strings"
	"testing"
	"time"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/hub"
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
