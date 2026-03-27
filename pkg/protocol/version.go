package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

const (
	ProtocolMajor = 1
	ProtocolMinor = 0
	ProtocolPatch = 0
)

// ProtocolVersion is the canonical string representation of the current protocol version.
var ProtocolVersion = fmt.Sprintf("%d.%d.%d", ProtocolMajor, ProtocolMinor, ProtocolPatch)

// HubID derives a stable 16-character hex identifier from a peer ID string.
// It is computed as the first 8 bytes (16 hex chars) of the SHA-256 digest of
// the raw peerID bytes.
func HubID(peerID string) string {
	sum := sha256.Sum256([]byte(peerID))
	return hex.EncodeToString(sum[:8])
}

// CompatibleWith returns true if the given version string is compatible with the
// current protocol version. Compatibility requires the same major version.
func CompatibleWith(version string) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 1 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	return major == ProtocolMajor
}
