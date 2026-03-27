package detect

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDetectGoProject(t *testing.T) {
	dir := t.TempDir()
	gomod := "module github.com/example/myapp\n\ngo 1.22\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0644); err != nil {
		t.Fatal(err)
	}

	info := DetectProject(dir)

	if info.Name != "myapp" {
		t.Errorf("expected name %q, got %q", "myapp", info.Name)
	}
	if !slices.Contains(info.Capabilities, "go") {
		t.Errorf("expected capabilities to contain %q, got %v", "go", info.Capabilities)
	}
}

func TestDetectNodeProject(t *testing.T) {
	dir := t.TempDir()
	pkgjson := `{
		"name": "my-frontend",
		"dependencies": {
			"react": "^18.0.0",
			"vite": "^5.0.0"
		},
		"devDependencies": {
			"typescript": "^5.0.0"
		}
	}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgjson), 0644); err != nil {
		t.Fatal(err)
	}

	info := DetectProject(dir)

	if info.Name != "my-frontend" {
		t.Errorf("expected name %q, got %q", "my-frontend", info.Name)
	}
	for _, cap := range []string{"node", "react", "vite", "typescript"} {
		if !slices.Contains(info.Capabilities, cap) {
			t.Errorf("expected capabilities to contain %q, got %v", cap, info.Capabilities)
		}
	}
}

func TestDetectFallbackToDirName(t *testing.T) {
	dir := t.TempDir()

	info := DetectProject(dir)

	expected := filepath.Base(dir)
	if info.Name != expected {
		t.Errorf("expected name %q, got %q", expected, info.Name)
	}
}
