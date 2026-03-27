package federation

import (
	"strings"
	"testing"
)

func TestGenerateMagicCode(t *testing.T) {
	mc, err := GenerateMagicCode()
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(mc.Code, MagicCodePrefix+"-") {
		t.Errorf("expected prefix %s-, got %s", MagicCodePrefix, mc.Code)
	}

	segments := strings.Split(mc.Code, "-")
	if len(segments) != MagicCodeSegments+1 { // prefix + 4 segments
		t.Errorf("expected %d segments, got %d: %s", MagicCodeSegments+1, len(segments), mc.Code)
	}

	if mc.PrivKey == nil || mc.PubKey == nil {
		t.Error("keys should not be nil")
	}

	if len(mc.Seed) != 10 {
		t.Errorf("expected 10-byte seed, got %d", len(mc.Seed))
	}
}

func TestDeriveMagicCodeRoundTrip(t *testing.T) {
	mc1, err := GenerateMagicCode()
	if err != nil {
		t.Fatal(err)
	}

	mc2, err := DeriveMagicCode(mc1.Code)
	if err != nil {
		t.Fatal(err)
	}

	// Same seed should produce same keys.
	if !mc1.PubKey.Equals(mc2.PubKey) {
		t.Error("public keys should match after round-trip")
	}

	priv1Raw, _ := mc1.PrivKey.Raw()
	priv2Raw, _ := mc2.PrivKey.Raw()
	if string(priv1Raw) != string(priv2Raw) {
		t.Error("private keys should match after round-trip")
	}
}

func TestDeriveMagicCodeCaseInsensitive(t *testing.T) {
	mc1, err := GenerateMagicCode()
	if err != nil {
		t.Fatal(err)
	}

	// Lowercase version should work.
	mc2, err := DeriveMagicCode(strings.ToLower(mc1.Code))
	if err != nil {
		t.Fatal(err)
	}

	if !mc1.PubKey.Equals(mc2.PubKey) {
		t.Error("case-insensitive derivation should produce same keys")
	}
}

func TestDeriveMagicCodeInvalid(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{"empty", ""},
		{"no prefix", "XXXX-YYYY-ZZZZ-WWWW"},
		{"wrong prefix", "RAINBOW-XXXX-YYYY-ZZZZ"},
		{"just prefix", "BIFROST-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DeriveMagicCode(tt.code)
			if err == nil {
				t.Errorf("expected error for code %q", tt.code)
			}
		})
	}
}

func TestMagicCodeUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := range 100 {
		mc, err := GenerateMagicCode()
		if err != nil {
			t.Fatal(err)
		}
		if seen[mc.Code] {
			t.Errorf("duplicate code on iteration %d: %s", i, mc.Code)
		}
		seen[mc.Code] = true
	}
}
