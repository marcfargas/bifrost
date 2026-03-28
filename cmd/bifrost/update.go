package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const githubLatestReleaseURL = "https://api.github.com/repos/marcfargas/bifrost/releases/latest"

type githubRelease struct {
	TagName string `json:"tag_name"`
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update bifrost to the latest release from GitHub",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runUpdate(cmd)
	},
}

func runUpdate(cmd *cobra.Command) error {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Fetch latest release tag from GitHub.
	latest, err := fetchLatestVersion(ctx)
	if err != nil {
		return fmt.Errorf("fetch latest release: %w", err)
	}

	// Compare with current binary version (strip leading 'v' from tag).
	latestVersion := strings.TrimPrefix(latest, "v")
	currentVersion := strings.TrimPrefix(version, "v")

	if currentVersion == latestVersion {
		fmt.Fprintf(cmd.OutOrStdout(), "bifrost is already up to date (v%s)\n", currentVersion)
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Updating bifrost from v%s to v%s...\n", currentVersion, latestVersion)

	// Determine the path to the running binary so we can replace it.
	selfPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("determine executable path: %w", err)
	}
	selfPath, err = filepath.EvalSymlinks(selfPath)
	if err != nil {
		return fmt.Errorf("resolve executable symlink: %w", err)
	}

	// Download the release archive to a temp directory.
	tmpDir, err := os.MkdirTemp("", "bifrost-update-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath, err := downloadReleaseArchive(ctx, latestVersion, tmpDir)
	if err != nil {
		return fmt.Errorf("download release archive: %w", err)
	}

	// Extract the binary from the archive.
	extractedBinary, err := extractBinary(archivePath, tmpDir)
	if err != nil {
		return fmt.Errorf("extract binary: %w", err)
	}

	// Replace the running binary: rename old → .old, new → current path.
	oldPath := selfPath + ".old"
	_ = os.Remove(oldPath) // clean up any leftover from a previous update

	if err := os.Rename(selfPath, oldPath); err != nil {
		return fmt.Errorf("rename current binary: %w", err)
	}

	if err := os.Rename(extractedBinary, selfPath); err != nil {
		// Try to restore the original on failure.
		_ = os.Rename(oldPath, selfPath)
		return fmt.Errorf("replace binary: %w", err)
	}

	// Remove the backup.
	_ = os.Remove(oldPath)

	fmt.Fprintf(cmd.OutOrStdout(), "Updated to v%s\n", latestVersion)
	return nil
}

// fetchLatestVersion queries the GitHub releases API and returns the tag name
// of the latest release (e.g. "v0.3.1").
func fetchLatestVersion(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubLatestReleaseURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
	}

	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("empty tag_name in GitHub response")
	}
	return rel.TagName, nil
}

// downloadReleaseArchive downloads the release archive for the current
// OS/arch into destDir and returns the local archive path.
// Archive name follows goreleaser convention: bifrost_VERSION_OS_ARCH.tar.gz (or .zip on Windows).
func downloadReleaseArchive(ctx context.Context, version, destDir string) (string, error) {
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}

	archiveName := fmt.Sprintf("bifrost_%s_%s_%s%s", version, goos, goarch, ext)
	url := fmt.Sprintf("https://github.com/marcfargas/bifrost/releases/download/v%s/%s", version, archiveName)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: HTTP %d", archiveName, resp.StatusCode)
	}

	archivePath := filepath.Join(destDir, archiveName)
	f, err := os.Create(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", fmt.Errorf("write archive: %w", err)
	}

	return archivePath, nil
}

// extractBinary extracts the bifrost binary from the archive at archivePath
// into destDir and returns the path to the extracted binary.
func extractBinary(archivePath, destDir string) (string, error) {
	if strings.HasSuffix(archivePath, ".zip") {
		return extractFromZip(archivePath, destDir)
	}
	return extractFromTarGz(archivePath, destDir)
}

func extractFromTarGz(archivePath, destDir string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar: %w", err)
		}

		if !isBifrostBinary(hdr.Name) {
			continue
		}

		destPath := filepath.Join(destDir, filepath.Base(hdr.Name))
		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}

		if _, err := io.Copy(out, tr); err != nil { //nolint:gosec
			out.Close()
			return "", fmt.Errorf("extract binary: %w", err)
		}
		out.Close()
		return destPath, nil
	}

	return "", fmt.Errorf("bifrost binary not found in archive")
}

func extractFromZip(archivePath, destDir string) (string, error) {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer r.Close()

	for _, f := range r.File {
		if !isBifrostBinary(f.Name) {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return "", err
		}

		destPath := filepath.Join(destDir, filepath.Base(f.Name))
		out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return "", err
		}

		if _, err := io.Copy(out, rc); err != nil { //nolint:gosec
			rc.Close()
			out.Close()
			return "", fmt.Errorf("extract binary: %w", err)
		}
		rc.Close()
		out.Close()
		return destPath, nil
	}

	return "", fmt.Errorf("bifrost binary not found in zip archive")
}

// isBifrostBinary returns true if the archive entry name is the bifrost binary
// (handles both "bifrost" and "bifrost.exe", ignoring directory prefixes).
func isBifrostBinary(name string) bool {
	base := filepath.Base(name)
	return base == "bifrost" || base == "bifrost.exe"
}
