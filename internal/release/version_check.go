// Package release provides utilities for checking and downloading the bifrost binary.
package release

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// GitHubRepo is the repository for bifrost releases.
	GitHubRepo = "marcfargas/bifrost"

	// DownloadURLTemplate is the pattern for release binary downloads.
	// Placeholders: repo, version, version, os, arch, extension.
	DownloadURLTemplate = "https://github.com/%s/releases/download/v%s/bifrost_%s_%s_%s%s"
)

// VersionInfo holds parsed version output from `bifrost version`.
type VersionInfo struct {
	Version         string
	ProtocolVersion string
}

// CheckBinary verifies that the bifrost binary is on PATH and returns its version.
// Returns an error if the binary is not found or cannot be executed.
func CheckBinary() (*VersionInfo, error) {
	path, err := exec.LookPath("bifrost")
	if err != nil {
		return nil, fmt.Errorf("bifrost binary not found on PATH: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return nil, fmt.Errorf("failed to run bifrost version: %w", err)
	}

	return parseVersionOutput(string(out))
}

// parseVersionOutput extracts version and protocol version from the output
// of `bifrost version`. Expected format:
//
//	bifrost v0.1.0 (protocol v1.0.0)
func parseVersionOutput(output string) (*VersionInfo, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, fmt.Errorf("unexpected version output format: %q", output)
	}
	// Format: "bifrost v0.1.0 (protocol v1.0.0)"
	var ver, proto string
	_, err := fmt.Sscanf(output, "bifrost v%s (protocol v%s", &ver, &proto)
	if err != nil || ver == "" {
		return nil, fmt.Errorf("unexpected version output format: %q", output)
	}
	// Remove trailing ')' from protocol version.
	proto = strings.TrimSuffix(proto, ")")
	return &VersionInfo{
		Version:         ver,
		ProtocolVersion: proto,
	}, nil
}

// VersionMatch checks if the installed binary version matches the expected version.
func VersionMatch(installed, expected string) bool {
	return strings.TrimSpace(installed) == strings.TrimSpace(expected)
}

// DownloadBinary downloads the bifrost binary for the current platform from
// GitHub releases and places it in the given directory.
// Returns the path to the downloaded binary.
func DownloadBinary(ctx context.Context, version, destDir string) (string, error) {
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}

	url := fmt.Sprintf(DownloadURLTemplate, GitHubRepo, version, version, goos, goarch, ext)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download bifrost v%s: %w", version, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download bifrost v%s: HTTP %d", version, resp.StatusCode)
	}

	binaryName := "bifrost" + ext
	destPath := filepath.Join(destDir, binaryName)

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create dest dir: %w", err)
	}

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		_ = os.Remove(destPath)
		return "", fmt.Errorf("write binary: %w", err)
	}

	return destPath, nil
}

// EnsureBinary checks that the correct version of bifrost is available.
// If missing or wrong version, it downloads from GitHub releases.
// Returns the path to the binary.
func EnsureBinary(ctx context.Context, expectedVersion string) (string, error) {
	info, err := CheckBinary()
	if err == nil && VersionMatch(info.Version, expectedVersion) {
		// Correct version already installed.
		path, _ := exec.LookPath("bifrost")
		return path, nil
	}

	// Need to download. Place in user's local bin directory.
	var destDir string
	switch runtime.GOOS {
	case "windows":
		destDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "bifrost", "bin")
	default: // linux, darwin
		home, _ := os.UserHomeDir()
		destDir = filepath.Join(home, ".local", "bin")
	}

	return DownloadBinary(ctx, expectedVersion, destDir)
}
