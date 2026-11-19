package device

import "time"

// Keep tests fast: the fake reboots (or not) immediately after commit.
func init() { rebootGrace = 50 * time.Millisecond }
