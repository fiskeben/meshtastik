package device

import (
	"errors"
	"strconv"
	"strings"
)

// ErrUnsupportedFirmware is returned when the node runs firmware older than 2.5
// or reports a version that cannot be parsed.
var ErrUnsupportedFirmware = errors.New("unsupported firmware")

// parseFirmware extracts major and minor from versions like "2.7.15.567b8ea" or "v2.6.11".
func parseFirmware(v string) (major, minor int, ok bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// supported reports whether v meets the 2.5 floor.
func supported(v string) bool {
	maj, min, ok := parseFirmware(v)
	return ok && (maj > 2 || (maj == 2 && min >= 5))
}
