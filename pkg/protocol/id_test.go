package protocol

import (
	"strings"
	"testing"
)

// TestAgentIDFromDeterministic verifies that the same inputs always yield the same ID.
func TestAgentIDFromDeterministic(t *testing.T) {
	id1 := AgentIDFrom("myhost", "/home/user/project")
	id2 := AgentIDFrom("myhost", "/home/user/project")
	if id1 != id2 {
		t.Errorf("AgentIDFrom is not deterministic: %q != %q", id1, id2)
	}
}

// TestAgentIDFromDifferentInputs verifies that different inputs produce different IDs.
func TestAgentIDFromDifferentInputs(t *testing.T) {
	cases := [][2]string{
		{"host-a", "/path/one"},
		{"host-b", "/path/one"},
		{"host-a", "/path/two"},
		{"host-b", "/path/two"},
	}
	seen := make(map[string]struct{})
	for _, c := range cases {
		id := AgentIDFrom(c[0], c[1])
		if len(id) != 16 {
			t.Errorf("AgentIDFrom(%q, %q) returned %d chars, want 16", c[0], c[1], len(id))
		}
		// Verify it's valid hex.
		for _, ch := range id {
			if !strings.ContainsRune("0123456789abcdef", ch) {
				t.Errorf("AgentIDFrom returned non-hex char %q in %q", ch, id)
			}
		}
		if _, dup := seen[id]; dup {
			t.Errorf("AgentIDFrom collision for inputs %v: %q", c, id)
		}
		seen[id] = struct{}{}
	}
}

// TestNewShortIDUnique generates 1000 short IDs and verifies no collisions and correct format.
func TestNewShortIDUnique(t *testing.T) {
	const iterations = 1000
	seen := make(map[string]struct{}, iterations)
	for i := range iterations {
		id := NewShortID()
		if len(id) != 8 {
			t.Fatalf("NewShortID returned %d chars, want 8", len(id))
		}
		for _, ch := range id {
			if !strings.ContainsRune("0123456789abcdef", ch) {
				t.Fatalf("NewShortID returned non-hex char %q in %q", ch, id)
			}
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("NewShortID collision after %d iterations: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}

// TestNewIDUnique generates 1000 IDs and verifies no collisions and correct format.
func TestNewIDUnique(t *testing.T) {
	const iterations = 1000
	seen := make(map[string]struct{}, iterations)
	for i := range iterations {
		id := NewID()
		if len(id) != 16 {
			t.Fatalf("NewID returned %d chars, want 16", len(id))
		}
		for _, ch := range id {
			if !strings.ContainsRune("0123456789abcdef", ch) {
				t.Fatalf("NewID returned non-hex char %q in %q", ch, id)
			}
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("NewID collision after %d iterations: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}

// TestProtocolVersionCompat verifies CompatibleWith behaviour.
func TestProtocolVersionCompat(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{ProtocolVersion, true},
		{"1.0.0", true},
		{"1.99.0", true},
		{"2.0.0", false},
		{"0.9.9", false},
		{"bad", false},
		{"", false},
	}
	for _, c := range cases {
		got := CompatibleWith(c.version)
		if got != c.want {
			t.Errorf("CompatibleWith(%q) = %v, want %v", c.version, got, c.want)
		}
	}
}
