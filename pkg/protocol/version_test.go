package protocol

import (
	"errors"
	"testing"
)

func TestCompatibleWith(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{ProtocolVersion, true},
		{"1.0.0", true},
		{"1.99.0", true},
		{"1.0.1", true},
		{"2.0.0", false},
		{"0.1.0", false},
		{"", false},
		{"garbage", false},
	}
	for _, tt := range tests {
		got := CompatibleWith(tt.version)
		if got != tt.want {
			t.Errorf("CompatibleWith(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

func TestCheckPeerVersionCompatible(t *testing.T) {
	if err := CheckPeerVersion(ProtocolVersion); err != nil {
		t.Errorf("self should be compatible: %v", err)
	}
	if err := CheckPeerVersion("1.99.0"); err != nil {
		t.Errorf("same major should be compatible: %v", err)
	}
}

func TestCheckPeerVersionIncompatible(t *testing.T) {
	err := CheckPeerVersion("2.0.0")
	if err == nil {
		t.Error("different major should be incompatible")
	}
	var vme *VersionMismatchError
	if !errors.As(err, &vme) {
		t.Errorf("expected VersionMismatchError, got %T", err)
	}
	if vme.Local != ProtocolVersion {
		t.Errorf("Local = %q, want %q", vme.Local, ProtocolVersion)
	}
	if vme.Remote != "2.0.0" {
		t.Errorf("Remote = %q, want %q", vme.Remote, "2.0.0")
	}
}

func TestCheckPeerVersionEmpty(t *testing.T) {
	if err := CheckPeerVersion(""); err == nil {
		t.Error("empty version should fail")
	}
}

func TestCheckPeerVersionGarbage(t *testing.T) {
	if err := CheckPeerVersion("not-a-version"); err == nil {
		t.Error("garbage version should fail")
	}
}
