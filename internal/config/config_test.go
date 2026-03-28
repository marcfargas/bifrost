package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDefaultsAreComplete verifies that Defaults() returns a fully populated config.
func TestDefaultsAreComplete(t *testing.T) {
	cfg := Defaults()

	// Hub
	if cfg.Hub.GracePeriod.Duration != 30*time.Second {
		t.Errorf("GracePeriod: got %v, want 30s", cfg.Hub.GracePeriod)
	}
	if cfg.Hub.HeartbeatInterval.Duration != 30*time.Second {
		t.Errorf("HeartbeatInterval: got %v, want 30s", cfg.Hub.HeartbeatInterval)
	}
	if cfg.Hub.HousekeepingInterval.Duration != 5*time.Minute {
		t.Errorf("HousekeepingInterval: got %v, want 5m", cfg.Hub.HousekeepingInterval)
	}
	if cfg.Hub.TCP.Enabled {
		t.Error("TCP should be disabled by default")
	}
	if cfg.Hub.TCP.Listen != "0.0.0.0:7432" {
		t.Errorf("TCP.Listen: got %q, want 0.0.0.0:7432", cfg.Hub.TCP.Listen)
	}
	if cfg.Hub.MCP.Enabled {
		t.Error("MCP should be disabled by default")
	}
	if cfg.Hub.MCP.Port != 7433 {
		t.Errorf("MCP.Port: got %d, want 7433", cfg.Hub.MCP.Port)
	}

	// Storage
	const wantMaxFileSize = 10 * 1024 * 1024
	if cfg.Storage.MaxFileSize != wantMaxFileSize {
		t.Errorf("MaxFileSize: got %d, want %d", cfg.Storage.MaxFileSize, wantMaxFileSize)
	}

	// Retention
	if cfg.Retention.Messages.Duration != 30*24*time.Hour {
		t.Errorf("Retention.Messages: got %v, want 720h", cfg.Retention.Messages)
	}
	if cfg.Retention.CompletedTasks.Duration != 90*24*time.Hour {
		t.Errorf("Retention.CompletedTasks: got %v, want 2160h", cfg.Retention.CompletedTasks)
	}
	if cfg.Retention.Attachments.Duration != 30*24*time.Hour {
		t.Errorf("Retention.Attachments: got %v, want 720h", cfg.Retention.Attachments)
	}
	if cfg.Retention.Conversations.Duration != 90*24*time.Hour {
		t.Errorf("Retention.Conversations: got %v, want 2160h", cfg.Retention.Conversations)
	}

	// Conversations
	if cfg.Conversations.InactivityTimeout.Duration != 10*time.Minute {
		t.Errorf("InactivityTimeout: got %v, want 10m", cfg.Conversations.InactivityTimeout)
	}

	// DND
	if cfg.DND.ReminderInterval.Duration != 5*time.Minute {
		t.Errorf("DND.ReminderInterval: got %v, want 5m", cfg.DND.ReminderInterval)
	}
	if !cfg.DND.UrgentBreaksThrough {
		t.Error("DND.UrgentBreaksThrough should be true by default")
	}

	// Federation
	if !cfg.Federation.Libp2p.Enabled {
		t.Error("Federation.Libp2p should be enabled by default")
	}
	if cfg.Federation.MDNS.Enabled {
		t.Error("Federation.MDNS should be disabled by default")
	}
	if cfg.Federation.Direct.Enabled {
		t.Error("Federation.Direct should be disabled by default")
	}
	if cfg.Federation.Direct.Listen != "0.0.0.0:7434" {
		t.Errorf("Federation.Direct.Listen: got %q, want 0.0.0.0:7434", cfg.Federation.Direct.Listen)
	}

	// Logging
	if cfg.Logging.Level != "info" {
		t.Errorf("Logging.Level: got %q, want info", cfg.Logging.Level)
	}
}

// TestLoadMissingFileReturnsDefaults verifies that Load() returns defaults when the
// config file does not exist.
func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	// Override APPDATA / XDG_CONFIG_HOME / HOME to a temp dir that has no config file.
	tmp := t.TempDir()
	t.Setenv("APPDATA", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	// Also clear HOME so the fallback path also misses.
	t.Setenv("HOME", tmp)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	defaults := Defaults()
	if cfg.Hub.HeartbeatInterval != defaults.Hub.HeartbeatInterval {
		t.Errorf("expected default HeartbeatInterval, got %v", cfg.Hub.HeartbeatInterval)
	}
	if cfg.Logging.Level != defaults.Logging.Level {
		t.Errorf("expected default Logging.Level, got %q", cfg.Logging.Level)
	}
}

// TestLoadFromTOML writes a minimal TOML file and verifies values are loaded correctly.
func TestLoadFromTOML(t *testing.T) {
	tmp := t.TempDir()

	// Write a partial TOML override.
	tomlContent := `
[hub]
grace_period = "1m"
heartbeat_interval = "15s"

[logging]
level = "debug"
`
	cfgDir := filepath.Join(tmp, "bifrost")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfgFile := filepath.Join(cfgDir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte(tomlContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadFrom(cfgFile)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.Hub.GracePeriod.Duration != 1*time.Minute {
		t.Errorf("GracePeriod: got %v, want 1m", cfg.Hub.GracePeriod)
	}
	if cfg.Hub.HeartbeatInterval.Duration != 15*time.Second {
		t.Errorf("HeartbeatInterval: got %v, want 15s", cfg.Hub.HeartbeatInterval)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level: got %q, want debug", cfg.Logging.Level)
	}

	// Unspecified values should retain defaults.
	defaults := Defaults()
	if cfg.Hub.HousekeepingInterval != defaults.Hub.HousekeepingInterval {
		t.Errorf("HousekeepingInterval should be default, got %v", cfg.Hub.HousekeepingInterval)
	}
}

// TestDurationUnmarshal verifies Duration correctly parses various time strings.
func TestDurationUnmarshal(t *testing.T) {
	cases := []struct {
		input string
		want  time.Duration
	}{
		{"30s", 30 * time.Second},
		{"10m", 10 * time.Minute},
		{"1h", time.Hour},
		{"720h0m0s", 720 * time.Hour},
		{"2160h0m0s", 2160 * time.Hour},
	}

	for _, tc := range cases {
		var d Duration
		if err := d.UnmarshalText([]byte(tc.input)); err != nil {
			t.Errorf("UnmarshalText(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if d.Duration != tc.want {
			t.Errorf("UnmarshalText(%q): got %v, want %v", tc.input, d.Duration, tc.want)
		}
	}

	// Test round-trip via MarshalText.
	d := Duration{5 * time.Minute}
	text, err := d.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: unexpected error: %v", err)
	}
	var d2 Duration
	if err := d2.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText round-trip: %v", err)
	}
	if d2.Duration != d.Duration {
		t.Errorf("round-trip mismatch: got %v, want %v", d2.Duration, d.Duration)
	}
}

// TestDurationUnmarshalInvalid verifies that invalid duration strings return an error.
func TestDurationUnmarshalInvalid(t *testing.T) {
	var d Duration
	if err := d.UnmarshalText([]byte("not-a-duration")); err == nil {
		t.Error("expected error for invalid duration, got nil")
	}
}
