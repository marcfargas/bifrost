package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/marcfargas/bifrost/internal/config"
	"github.com/marcfargas/bifrost/internal/hub"
	"github.com/marcfargas/bifrost/pkg/protocol"
	"github.com/spf13/cobra"
)

// detachHubProcess is implemented per-platform in hub_unix.go / hub_windows.go.

var hubCmd = &cobra.Command{
	Use:   "hub",
	Short: "Manage the Bifrost hub daemon",
}

var hubStartDaemon bool

var hubStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the hub (foreground by default, -d for background)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if hubStartDaemon {
			return startHubDaemon()
		}
		return startHubForeground(cmd)
	},
}

var hubStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the running hub daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		return stopHub()
	},
}

var hubStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show hub status and connected agents",
	RunE: func(cmd *cobra.Command, args []string) error {
		return hubStatus()
	},
}

func init() {
	hubStartCmd.Flags().BoolVarP(&hubStartDaemon, "daemon", "d", false, "Start as background process")
	hubStartCmd.Flags().Bool("hub.mcp.enabled", false, "Enable MCP Streamable HTTP endpoint")
	hubStartCmd.Flags().Int("hub.mcp.port", 7433, "MCP HTTP listen port")
	hubCmd.AddCommand(hubStartCmd)
	hubCmd.AddCommand(hubStopCmd)
	hubCmd.AddCommand(hubStatusCmd)
}

// startHubForeground loads config, applies any CLI flag overrides, creates a
// server, and runs in the foreground.
func startHubForeground(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Apply CLI flag overrides.
	if cmd.Flags().Changed("hub.mcp.enabled") {
		v, _ := cmd.Flags().GetBool("hub.mcp.enabled")
		cfg.Hub.MCP.Enabled = v
	}
	if cmd.Flags().Changed("hub.mcp.port") {
		v, _ := cmd.Flags().GetInt("hub.mcp.port")
		cfg.Hub.MCP.Port = v
	}

	srv, err := hub.NewServer(&cfg, nil)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	return srv.Run(context.Background())
}

// startHubDaemon launches the hub as a detached background process.
func startHubDaemon() error {
	exePath, err := os.Executable()
	if err != nil {
		exePath = "bifrost"
	}

	cmd := exec.Command(exePath, "hub", "start")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	detachHubProcess(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start hub daemon: %w", err)
	}

	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release hub process: %w", err)
	}

	fmt.Printf("Hub started in background (pid %d)\n", cmd.Process.Pid)
	return nil
}

// stopHub reads the PID file and sends a termination signal to the hub process.
func stopHub() error {
	pidPath := config.PIDFilePath()
	data, err := os.ReadFile(pidPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("hub is not running (no PID file at %s)", pidPath)
		}
		return fmt.Errorf("read PID file: %w", err)
	}

	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return fmt.Errorf("invalid PID in file: %w", err)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	if err := killHubProcess(proc); err != nil {
		return fmt.Errorf("stop hub process %d: %w", pid, err)
	}

	fmt.Printf("Sent stop signal to hub (pid %d)\n", pid)
	return nil
}

// hubStatus connects to the hub and prints a summary of connected agents.
func hubStatus() error {
	ctx := context.Background()

	conn, err := tryHubConnect()
	if err != nil {
		fmt.Println("Hub status: not running")
		fmt.Printf("  Error: %v\n", err)
		return nil
	}
	defer conn.Close()

	mux := newCLIMux(ctx, conn)

	resp, err := mux.rpcCall(ctx, "hub.list_agents", nil)
	if err != nil {
		return fmt.Errorf("list agents: %w", err)
	}
	if resp.Error != nil {
		return fmt.Errorf("hub error: %s", resp.Error.Message)
	}

	agents, err := parseAgentList(resp.Result)
	if err != nil {
		return fmt.Errorf("parse agent list: %w", err)
	}

	fmt.Println("Hub status: running")
	fmt.Printf("  Agents: %d\n", len(agents))
	if len(agents) > 0 {
		fmt.Println()
		printAgentTable(agents)
	}

	return nil
}

// parseAgentList converts a raw result into a slice of protocol.Agent.
func parseAgentList(raw any) ([]protocol.Agent, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var agents []protocol.Agent
	if err := json.Unmarshal(data, &agents); err != nil {
		return nil, err
	}
	return agents, nil
}
