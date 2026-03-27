package protocol

import (
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
