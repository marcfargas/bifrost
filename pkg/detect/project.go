package detect

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

// ProjectInfo holds auto-detected project name and capabilities.
type ProjectInfo struct {
	Name         string
	Capabilities []string
}

// packageJSON is used to unmarshal the relevant fields from package.json.
type packageJSON struct {
	Name            string            `json:"name"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// DetectProject inspects dir and returns the detected ProjectInfo.
func DetectProject(dir string) ProjectInfo {
	info := ProjectInfo{
		Name:         filepath.Base(dir),
		Capabilities: []string{},
	}

	if pkg, ok := readPackageJSON(filepath.Join(dir, "package.json")); ok {
		if pkg.Name != "" {
			info.Name = pkg.Name
		}
		info.Capabilities = append(info.Capabilities, detectNodeCapabilities(pkg)...)
	}

	if name, ok := readGoMod(filepath.Join(dir, "go.mod")); ok {
		info.Name = name
		info.Capabilities = append(info.Capabilities, "go")
	}

	if fileExists(filepath.Join(dir, "pyproject.toml")) {
		info.Capabilities = append(info.Capabilities, "python")
	}

	if fileExists(filepath.Join(dir, "Cargo.toml")) {
		info.Capabilities = append(info.Capabilities, "rust")
	}

	return info
}

// readPackageJSON reads and parses a package.json file.
func readPackageJSON(path string) (packageJSON, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return packageJSON{}, false
	}
	var pkg packageJSON
	if err := json.Unmarshal(data, &pkg); err != nil {
		return packageJSON{}, false
	}
	return pkg, true
}

// readGoMod reads the first line of a go.mod file and extracts the last path
// segment of the module name.
func readGoMod(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	lines := strings.SplitN(string(data), "\n", 2)
	if len(lines) == 0 {
		return "", false
	}
	line := strings.TrimSpace(lines[0])
	// Expected format: "module github.com/user/repo"
	const prefix = "module "
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	modulePath := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	if modulePath == "" {
		return "", false
	}
	// Extract last path segment as project name.
	parts := strings.Split(modulePath, "/")
	return parts[len(parts)-1], true
}

// detectNodeCapabilities inspects package.json dependencies and returns a list
// of detected capability strings.
func detectNodeCapabilities(pkg packageJSON) []string {
	caps := []string{"node"}

	allDeps := make(map[string]string, len(pkg.Dependencies)+len(pkg.DevDependencies))
	maps.Copy(allDeps, pkg.Dependencies)
	maps.Copy(allDeps, pkg.DevDependencies)

	known := map[string]string{
		"typescript": "typescript",
		"react":      "react",
		"vue":        "vue",
		"vite":       "vite",
		"next":       "nextjs",
		"express":    "express",
	}

	for dep, cap := range known {
		if _, ok := allDeps[dep]; ok {
			caps = append(caps, cap)
		}
	}

	return caps
}

// fileExists returns true if the path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
