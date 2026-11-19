package device

import "testing"

func TestParseFirmware(t *testing.T) {
	cases := []struct {
		in       string
		maj, min int
		ok       bool
	}{
		{"2.7.15.567b8ea", 2, 7, true}, {"2.5.0", 2, 5, true}, {"v2.6.11", 2, 6, true},
		{"", 0, 0, false}, {"garbage", 0, 0, false}, {"2", 0, 0, false},
	}
	for _, c := range cases {
		maj, min, ok := parseFirmware(c.in)
		if maj != c.maj || min != c.min || ok != c.ok {
			t.Errorf("%q -> %d %d %v", c.in, maj, min, ok)
		}
	}
}
