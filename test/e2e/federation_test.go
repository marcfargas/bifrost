//go:build e2e

// Package e2e contains end-to-end tests that require external binaries
// (bifrost, claude CLI) and network access. These tests are not run in CI.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFederatedE2E spins up two bifrost hubs, peers them, then runs two
// claude -p instances (one per hub) that exchange messages and a task.
//
// Requirements:
//   - bifrost binary built and on PATH
//   - claude CLI installed
//   - ANTHROPIC_API_KEY set
//
// Run: go test -tags e2e ./test/e2e/ -run TestFederatedE2E -v -timeout 300s
func TestFederatedE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	bifrost, err := exec.LookPath("bifrost")
	if err != nil {
		t.Skip("bifrost binary not on PATH")
	}

	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude CLI not on PATH")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	// Create temp dirs for each hub
	dirA := t.TempDir()
	dirB := t.TempDir()

	// Configure Hub A — direct transport on a random port, no libp2p/mDNS
	cfgA := filepath.Join(dirA, "config.toml")
	writeConfig(t, cfgA, 17450, 17451, false)

	// Configure Hub B
	cfgB := filepath.Join(dirB, "config.toml")
	writeConfig(t, cfgB, 17460, 17461, false)

	// --- Step 1: Start both hub processes ---
	//
	// Each hub runs as a long-lived process. They listen for agent connections
	// on their respective MCP ports and for peer connections on their direct
	// transport ports.

	hubA := exec.CommandContext(ctx, bifrost, "hub", "start",
		"--config", cfgA,
		"--data-dir", filepath.Join(dirA, "data"),
	)
	hubA.Env = append(os.Environ(), "BIFROST_LOG_LEVEL=debug")
	hubA.Stdout = os.Stdout
	hubA.Stderr = os.Stderr
	if err := hubA.Start(); err != nil {
		t.Fatalf("start hub A: %v", err)
	}
	defer hubA.Process.Kill() //nolint:errcheck

	hubB := exec.CommandContext(ctx, bifrost, "hub", "start",
		"--config", cfgB,
		"--data-dir", filepath.Join(dirB, "data"),
	)
	hubB.Env = append(os.Environ(), "BIFROST_LOG_LEVEL=debug")
	hubB.Stdout = os.Stdout
	hubB.Stderr = os.Stderr
	if err := hubB.Start(); err != nil {
		t.Fatalf("start hub B: %v", err)
	}
	defer hubB.Process.Kill() //nolint:errcheck

	// Wait for hubs to be ready (they need to bind ports and initialize stores)
	time.Sleep(2 * time.Second)

	// --- Step 2: Peer the hubs ---
	//
	// Hub A generates a pairing invitation (magic code), then Hub B joins
	// using that code. This establishes the bidirectional federation link.

	peerNewOut := runBifrost(t, ctx, bifrost, "--config", cfgA, "peer", "new")
	t.Logf("peer new output: %s", peerNewOut)

	code := extractMagicCode(t, peerNewOut)
	t.Logf("magic code: %s", code)

	peerJoinOut := runBifrost(t, ctx, bifrost, "--config", cfgB, "peer", "join", code)
	t.Logf("peer join output: %s", peerJoinOut)

	// Wait for peering to establish
	time.Sleep(2 * time.Second)

	// Verify peers are connected
	peerListA := runBifrost(t, ctx, bifrost, "--config", cfgA, "peer", "list")
	t.Logf("hub A peers: %s", peerListA)

	peerListB := runBifrost(t, ctx, bifrost, "--config", cfgB, "peer", "list")
	t.Logf("hub B peers: %s", peerListB)

	// --- Step 3: Set up project directories with MCP configs ---
	//
	// Each claude agent runs in its own project directory with a .mcp.json
	// that points to the local hub's bifrost shim.

	projectA := filepath.Join(dirA, "project-alpha")
	os.MkdirAll(projectA, 0o755)                                                   //nolint:errcheck
	os.WriteFile(filepath.Join(projectA, "README.md"), []byte("# Alpha\n"), 0o644) //nolint:errcheck

	projectB := filepath.Join(dirB, "project-beta")
	os.MkdirAll(projectB, 0o755)                                                  //nolint:errcheck
	os.WriteFile(filepath.Join(projectB, "README.md"), []byte("# Beta\n"), 0o644) //nolint:errcheck

	mcpA := map[string]any{
		"mcpServers": map[string]any{
			"bifrost": map[string]any{
				"command": bifrost,
				"args":    []string{"shim", "--config", cfgA},
			},
		},
	}
	writeMCPConfig(t, projectA, mcpA)

	mcpB := map[string]any{
		"mcpServers": map[string]any{
			"bifrost": map[string]any{
				"command": bifrost,
				"args":    []string{"shim", "--config", cfgB},
			},
		},
	}
	writeMCPConfig(t, projectB, mcpB)

	// --- Step 4: Launch two claude instances that exchange messages ---
	//
	// Agent Alpha sends a message to project-beta and waits for a reply.
	// Agent Beta listens for incoming messages and replies.
	// Both agents use bifrost_send to communicate across hubs.

	agentA := exec.CommandContext(ctx, claudeBin,
		"-p", "You are Agent Alpha. Your project is 'project-alpha'. "+
			"Use bifrost_send to send a message to project-beta saying: "+
			"'Hello from Alpha! Can you tell me your project name?'. "+
			"Wait for a reply. When you receive a reply, respond with 'E2E_ALPHA_SUCCESS'.",
		"--model", "haiku",
		"--max-turns", "5",
		"--max-cost-usd", "0.25",
		"--channels", "plugin:bifrost",
	)
	agentA.Dir = projectA
	agentA.Env = append(os.Environ(), "ANTHROPIC_API_KEY="+os.Getenv("ANTHROPIC_API_KEY"))
	var outA bytes.Buffer
	agentA.Stdout = &outA
	agentA.Stderr = os.Stderr

	agentB := exec.CommandContext(ctx, claudeBin,
		"-p", "You are Agent Beta. Your project is 'project-beta'. "+
			"When you receive a message from another agent, reply using bifrost_send "+
			"telling them your project name is 'project-beta'. "+
			"After replying, respond with 'E2E_BETA_SUCCESS'.",
		"--model", "haiku",
		"--max-turns", "5",
		"--max-cost-usd", "0.25",
		"--channels", "plugin:bifrost",
	)
	agentB.Dir = projectB
	agentB.Env = append(os.Environ(), "ANTHROPIC_API_KEY="+os.Getenv("ANTHROPIC_API_KEY"))
	var outB bytes.Buffer
	agentB.Stdout = &outB
	agentB.Stderr = os.Stderr

	// Run both agents concurrently
	errChA := make(chan error, 1)
	errChB := make(chan error, 1)

	go func() { errChA <- agentA.Run() }()
	go func() { errChB <- agentB.Run() }()

	// Wait for both to complete (with timeout)
	select {
	case err := <-errChA:
		if err != nil {
			t.Logf("agent A exited with error (may be normal): %v", err)
		}
	case <-time.After(120 * time.Second):
		t.Log("agent A timed out")
	}

	select {
	case err := <-errChB:
		if err != nil {
			t.Logf("agent B exited with error (may be normal): %v", err)
		}
	case <-time.After(120 * time.Second):
		t.Log("agent B timed out")
	}

	// --- Step 5: Verify outputs ---
	outputA := outA.String()
	outputB := outB.String()

	t.Logf("Agent A output:\n%s", outputA)
	t.Logf("Agent B output:\n%s", outputB)

	if !strings.Contains(outputA, "E2E_ALPHA_SUCCESS") {
		t.Error("Agent A did not report success")
	}
	if !strings.Contains(outputB, "E2E_BETA_SUCCESS") {
		t.Error("Agent B did not report success")
	}
}

// writeConfig generates a TOML config file for a bifrost hub.
func writeConfig(t *testing.T, path string, directPort, mcpPort int, libp2pEnabled bool) {
	t.Helper()
	content := fmt.Sprintf(`
[hub]
grace_period = "5s"
heartbeat_interval = "5s"

[federation.libp2p]
enabled = %v

[federation.mdns]
enabled = false

[federation.direct]
enabled = true
listen = "127.0.0.1:%d"

[hub.mcp]
enabled = false
port = %d

[logging]
level = "debug"
`, libp2pEnabled, directPort, mcpPort)

	os.MkdirAll(filepath.Dir(path), 0o755) //nolint:errcheck
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeMCPConfig writes a .mcp.json file into a project directory.
func writeMCPConfig(t *testing.T, projectDir string, cfg map[string]any) {
	t.Helper()
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(filepath.Join(projectDir, ".mcp.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// runBifrost executes a bifrost CLI command and returns its combined output.
func runBifrost(t *testing.T, ctx context.Context, bin string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("bifrost %v failed: %v\noutput: %s", args, err, string(out))
	}
	return string(out)
}

// extractMagicCode parses the "BIFROST-..." magic code from peer new output.
func extractMagicCode(t *testing.T, output string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "BIFROST-") {
			return line
		}
	}
	t.Fatal("could not find magic code in output")
	return ""
}
