package release

import (
	"fmt"
	"testing"
)

func TestParseVersionOutput(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantVer   string
		wantProto string
		wantErr   bool
	}{
		{
			name:      "standard format",
			input:     "bifrost v0.1.0 (protocol v1.0.0)",
			wantVer:   "0.1.0",
			wantProto: "1.0.0",
		},
		{
			name:      "with trailing newline",
			input:     "bifrost v1.2.3 (protocol v1.0.0)\n",
			wantVer:   "1.2.3",
			wantProto: "1.0.0",
		},
		{
			name:    "garbage input",
			input:   "not a version string",
			wantErr: true,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := parseVersionOutput(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.Version != tt.wantVer {
				t.Errorf("version: got %q, want %q", info.Version, tt.wantVer)
			}
			if info.ProtocolVersion != tt.wantProto {
				t.Errorf("protocol: got %q, want %q", info.ProtocolVersion, tt.wantProto)
			}
		})
	}
}

func TestVersionMatch(t *testing.T) {
	tests := []struct {
		installed string
		expected  string
		want      bool
	}{
		{"0.1.0", "0.1.0", true},
		{"0.1.0", "0.2.0", false},
		{" 0.1.0 ", "0.1.0", true}, // whitespace tolerance
		{"1.0.0", "1.0.1", false},
	}

	for _, tt := range tests {
		t.Run(tt.installed+"_vs_"+tt.expected, func(t *testing.T) {
			got := VersionMatch(tt.installed, tt.expected)
			if got != tt.want {
				t.Errorf("VersionMatch(%q, %q) = %v, want %v", tt.installed, tt.expected, got, tt.want)
			}
		})
	}
}

func TestDownloadURLTemplate(t *testing.T) {
	// Verify the URL template produces valid URLs.
	url := fmt.Sprintf(DownloadURLTemplate, GitHubRepo, "0.1.0", "0.1.0", "linux", "amd64", "")
	expected := "https://github.com/marcfargas/bifrost/releases/download/v0.1.0/bifrost_0.1.0_linux_amd64"
	if url != expected {
		t.Errorf("got %q, want %q", url, expected)
	}

	urlWin := fmt.Sprintf(DownloadURLTemplate, GitHubRepo, "0.1.0", "0.1.0", "windows", "amd64", ".exe")
	expectedWin := "https://github.com/marcfargas/bifrost/releases/download/v0.1.0/bifrost_0.1.0_windows_amd64.exe"
	if urlWin != expectedWin {
		t.Errorf("got %q, want %q", urlWin, expectedWin)
	}
}

func TestCheckBinary(t *testing.T) {
	// This test only runs if bifrost is actually on PATH.
	// We use it as a smoke test for the real binary.
	info, err := CheckBinary()
	if err != nil {
		t.Skip("bifrost not on PATH, skipping: " + err.Error())
	}
	if info.Version == "" {
		t.Error("CheckBinary returned empty version")
	}
}
