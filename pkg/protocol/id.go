package protocol

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// AgentIDFrom derives a deterministic agent ID from a hostname and local path.
// It returns the first 16 hex characters of the SHA-256 hash of
// "hostname:localPath", giving a stable 64-bit identifier.
func AgentIDFrom(hostname, localPath string) string {
	input := fmt.Sprintf("%s:%s", hostname, localPath)
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])[:16]
}

// NewShortID generates a random 8-character hex string (4 random bytes).
func NewShortID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("protocol: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}

// NewID generates a random 16-character hex string (8 random bytes).
func NewID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("protocol: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}
