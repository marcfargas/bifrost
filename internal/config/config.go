package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/BurntSushi/toml"
)

// Duration wraps time.Duration to support TOML string parsing ("30s", "10m", etc.).
type Duration struct {
	time.Duration
}

// UnmarshalText implements encoding.TextUnmarshaler for TOML string durations.
func (d *Duration) UnmarshalText(text []byte) error {
	dur, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", string(text), err)
	}
	d.Duration = dur
	return nil
}

// MarshalText implements encoding.TextMarshaler for TOML string durations.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.Duration.String()), nil
}

// Config is the top-level configuration structure for Bifrost.
type Config struct {
	Hub           HubConfig           `toml:"hub"`
	Storage       StorageConfig       `toml:"storage"`
	Retention     RetentionConfig     `toml:"retention"`
	Conversations ConversationsConfig `toml:"conversations"`
	DND           DNDConfig           `toml:"dnd"`
	Federation    FederationConfig    `toml:"federation"`
	Logging       LoggingConfig       `toml:"logging"`
}

// HubConfig holds settings for the hub server.
type HubConfig struct {
	GracePeriod          Duration    `toml:"grace_period"`
	HeartbeatInterval    Duration    `toml:"heartbeat_interval"`
	HousekeepingInterval Duration    `toml:"housekeeping_interval"`
	Local                LocalConfig `toml:"local"`
	TCP                  TCPConfig   `toml:"tcp"`
	MCP                  MCPConfig   `toml:"mcp"`
}

// LocalConfig holds settings for the local (Unix socket / named pipe) transport.
type LocalConfig struct {
	// SocketPath overrides the default socket/pipe path when non-empty.
	SocketPath string `toml:"socket_path"`
}

// TCPConfig holds settings for the optional TCP transport.
type TCPConfig struct {
	Enabled bool   `toml:"enabled"`
	Listen  string `toml:"listen"`
}

// MCPConfig holds settings for the optional MCP server.
type MCPConfig struct {
	Enabled bool `toml:"enabled"`
	Port    int  `toml:"port"`
}

// StorageConfig holds settings for on-disk storage.
type StorageConfig struct {
	DataDir     string `toml:"data_dir"`
	MaxFileSize int64  `toml:"max_file_size"`
}

// RetentionConfig defines how long various data types are kept.
type RetentionConfig struct {
	Messages       Duration `toml:"messages"`
	CompletedTasks Duration `toml:"completed_tasks"`
	Attachments    Duration `toml:"attachments"`
	Conversations  Duration `toml:"conversations"`
}

// ConversationsConfig holds conversation-level settings.
type ConversationsConfig struct {
	InactivityTimeout Duration `toml:"inactivity_timeout"`
}

// DNDConfig holds do-not-disturb settings.
type DNDConfig struct {
	ReminderInterval    Duration `toml:"reminder_interval"`
	UrgentBreaksThrough bool     `toml:"urgent_breaks_through"`
}

// FederationConfig holds settings for peer federation.
type FederationConfig struct {
	Libp2p Libp2pConfig `toml:"libp2p"`
	MDNS   MDNSConfig   `toml:"mdns"`
	Direct DirectConfig `toml:"direct"`
}

// Libp2pConfig holds libp2p federation settings.
type Libp2pConfig struct {
	Enabled bool `toml:"enabled"`
}

// MDNSConfig holds mDNS discovery settings.
type MDNSConfig struct {
	Enabled bool `toml:"enabled"`
}

// DirectConfig holds direct TCP federation settings.
type DirectConfig struct {
	Enabled bool   `toml:"enabled"`
	Listen  string `toml:"listen"`
}

// LoggingConfig holds logging settings.
type LoggingConfig struct {
	Level string `toml:"level"`
	File  string `toml:"file"`
}

// LoadFrom reads the config from the given path.
// If the file does not exist, defaults are returned without error.
func LoadFrom(path string) (Config, error) {
	cfg := Defaults()

	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("stat config file: %w", err)
	}

	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, fmt.Errorf("decode config file %s: %w", path, err)
	}

	return cfg, nil
}

// Load reads the config from the platform-appropriate path.
func Load() (Config, error) {
	return LoadFrom(FilePath())
}

// FilePath returns the platform-appropriate config file path.
//
//   - Windows:  %APPDATA%\bifrost\config.toml
//   - macOS:    ~/Library/Application Support/bifrost/config.toml
//   - Linux:    $XDG_CONFIG_HOME/bifrost/config.toml (fallback ~/.config/)
func FilePath() string {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("APPDATA")
		if base == "" {
			base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
		}
		return filepath.Join(base, "bifrost", "config.toml")
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "bifrost", "config.toml")
	default:
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "bifrost", "config.toml")
	}
}

// DataDir returns the platform-appropriate data directory.
//
//   - Windows:  %LOCALAPPDATA%\bifrost
//   - macOS:    ~/Library/Application Support/bifrost
//   - Linux:    $XDG_DATA_HOME/bifrost (fallback ~/.local/share/)
func DataDir() string {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
		}
		return filepath.Join(base, "bifrost")
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Application Support", "bifrost")
	default:
		base := os.Getenv("XDG_DATA_HOME")
		if base == "" {
			home, _ := os.UserHomeDir()
			base = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(base, "bifrost")
	}
}

// SocketPath returns the platform-appropriate hub unix socket path.
// Unix sockets work on all platforms (Linux, macOS, Windows 10+).
//
//   - Windows:  %LOCALAPPDATA%/bifrost/hub.sock
//   - Linux:    $XDG_RUNTIME_DIR/bifrost/hub.sock (fallback /tmp/bifrost-$UID/)
//   - macOS:    $TMPDIR/bifrost/hub.sock
func SocketPath() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("LOCALAPPDATA"), "bifrost", "hub.sock")
	case "darwin":
		return filepath.Join(os.TempDir(), "bifrost", "hub.sock")
	default:
		runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
		if runtimeDir != "" {
			return filepath.Join(runtimeDir, "bifrost", "hub.sock")
		}
		return filepath.Join(os.TempDir(), fmt.Sprintf("bifrost-%d", os.Getuid()), "hub.sock")
	}
}

// PIDFilePath returns the path to the hub PID file.
func PIDFilePath() string {
	return filepath.Join(DataDir(), "hub.pid")
}
