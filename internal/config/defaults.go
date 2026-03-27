package config

import "time"

// Defaults returns a Config populated with sensible default values.
func Defaults() Config {
	return Config{
		Hub: HubConfig{
			GracePeriod:          Duration{30 * time.Second},
			HeartbeatInterval:    Duration{30 * time.Second},
			HousekeepingInterval: Duration{5 * time.Minute},
			Local:                LocalConfig{},
			TCP: TCPConfig{
				Enabled: false,
				Listen:  "0.0.0.0:7432",
			},
			MCP: MCPConfig{
				Enabled: false,
				Port:    7433,
			},
		},
		Storage: StorageConfig{
			DataDir:     "",    // resolved at runtime via DataDir()
			MaxFileSize: 10 * 1024 * 1024, // 10 MB
		},
		Retention: RetentionConfig{
			Messages:       Duration{30 * 24 * time.Hour},   // 30d
			CompletedTasks: Duration{90 * 24 * time.Hour},   // 90d
			Attachments:    Duration{30 * 24 * time.Hour},   // 30d
			Conversations:  Duration{90 * 24 * time.Hour},   // 90d
		},
		Conversations: ConversationsConfig{
			InactivityTimeout: Duration{10 * time.Minute},
		},
		DND: DNDConfig{
			ReminderInterval:    Duration{5 * time.Minute},
			UrgentBreaksThrough: true,
		},
		Federation: FederationConfig{
			Libp2p: Libp2pConfig{Enabled: true},
			MDNS:   MDNSConfig{Enabled: false},
			Direct: DirectConfig{
				Enabled: false,
				Listen:  "0.0.0.0:7434",
			},
		},
		Logging: LoggingConfig{
			Level: "info",
			File:  "",
		},
	}
}
