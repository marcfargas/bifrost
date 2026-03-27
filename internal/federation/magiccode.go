// Package federation — magic code generation and derivation for peer pairing.
package federation

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/crypto"
)

const (
	// MagicCodePrefix is the human-readable prefix for magic codes.
	MagicCodePrefix = "BIFROST"
	// MagicCodeSegmentLen is the length of each segment in a formatted magic code.
	MagicCodeSegmentLen = 4
	// MagicCodeSegments is the number of segments (excluding prefix) in a magic code.
	MagicCodeSegments = 4
)

// MagicCode holds a generated magic code and the derived libp2p key pair.
type MagicCode struct {
	Code    string         // formatted: BIFROST-AXKM-TNVR-Q7PD-HZLW
	Seed    []byte         // raw 16-byte seed
	PrivKey crypto.PrivKey // ed25519 private key derived from seed
	PubKey  crypto.PubKey  // ed25519 public key
}

// GenerateMagicCode creates a new magic code with a random seed.
func GenerateMagicCode() (*MagicCode, error) {
	seed := make([]byte, 10) // 10 bytes = 80 bits entropy = 16 base32 chars
	if _, err := rand.Read(seed); err != nil {
		return nil, fmt.Errorf("generate seed: %w", err)
	}
	return magicCodeFromSeed(seed)
}

// DeriveMagicCode reconstructs the key pair from a formatted magic code string.
func DeriveMagicCode(code string) (*MagicCode, error) {
	seed, err := decodeMagicCode(code)
	if err != nil {
		return nil, err
	}
	return magicCodeFromSeed(seed)
}

// magicCodeFromSeed derives a deterministic ed25519 key pair from a 16-byte seed.
func magicCodeFromSeed(seed []byte) (*MagicCode, error) {
	// Derive a 32-byte ed25519 seed from the 16-byte magic code seed.
	// Use SHA-256 to expand the seed deterministically.
	h := sha256.Sum256(append([]byte("bifrost-magic-v1:"), seed...))
	edSeed := h[:]

	// Create ed25519 key pair from the derived seed.
	edPriv := ed25519.NewKeyFromSeed(edSeed)
	privKey, pubKey, err := crypto.KeyPairFromStdKey(&edPriv)
	if err != nil {
		return nil, fmt.Errorf("derive keypair: %w", err)
	}

	return &MagicCode{
		Code:    formatMagicCode(seed),
		Seed:    seed,
		PrivKey: privKey,
		PubKey:  pubKey,
	}, nil
}

// formatMagicCode encodes a seed as a human-friendly code: BIFROST-XXXX-XXXX-XXXX-XXXX
func formatMagicCode(seed []byte) string {
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(seed)
	// Take first 16 chars (covers 10 bytes = 16 base32 chars), then segment.
	if len(encoded) > MagicCodeSegmentLen*MagicCodeSegments {
		encoded = encoded[:MagicCodeSegmentLen*MagicCodeSegments]
	}

	var segments []string
	segments = append(segments, MagicCodePrefix)
	for i := 0; i < len(encoded); i += MagicCodeSegmentLen {
		end := i + MagicCodeSegmentLen
		if end > len(encoded) {
			end = len(encoded)
		}
		segments = append(segments, encoded[i:end])
	}
	return strings.Join(segments, "-")
}

// decodeMagicCode parses a formatted magic code back to the raw seed.
func decodeMagicCode(code string) ([]byte, error) {
	code = strings.ToUpper(strings.TrimSpace(code))

	if !strings.HasPrefix(code, MagicCodePrefix+"-") {
		return nil, fmt.Errorf("invalid magic code: must start with %s-", MagicCodePrefix)
	}

	// Remove prefix and dashes.
	rest := strings.TrimPrefix(code, MagicCodePrefix+"-")
	encoded := strings.ReplaceAll(rest, "-", "")

	if len(encoded) == 0 {
		return nil, fmt.Errorf("invalid magic code: empty after prefix")
	}

	// Pad to multiple of 8 for base32.
	for len(encoded)%8 != 0 {
		encoded += "="
	}

	seed, err := base32.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid magic code: base32 decode: %w", err)
	}

	return seed, nil
}
