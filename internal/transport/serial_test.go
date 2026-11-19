package transport

import (
	"errors"
	"strings"
	"testing"

	"go.bug.st/serial"
	"go.bug.st/serial/enumerator"
)

func TestFilterSerialCandidates(t *testing.T) {
	in := []*enumerator.PortDetails{
		{Name: "/dev/cu.usbserial-0001", IsUSB: true, VID: "10C4"},
		{Name: "/dev/tty.usbserial-0001", IsUSB: true, VID: "10C4"},
		{Name: "/dev/cu.Bluetooth-Incoming-Port"},
		{Name: "/dev/cu.usbmodem101", IsUSB: true, VID: "303a"},
		{Name: "/dev/cu.usbmodemX", IsUSB: true, VID: "05AC"},
	}
	got := filterCandidates(in)
	if len(got) != 2 || got[0].Path != "/dev/cu.usbserial-0001" || got[1].Path != "/dev/cu.usbmodem101" {
		t.Fatalf("%+v", got)
	}
}

func TestSerialOpenErrorMessages(t *testing.T) {
	cases := map[serial.PortErrorCode]string{
		serial.PortBusy: "is another program using it", serial.PortNotFound: "not found",
		serial.PermissionDenied: "permission denied",
	}
	for code, want := range cases {
		err := serialOpenError("/dev/cu.x", code, errors.New("raw"))
		if !strings.Contains(err.Error(), "/dev/cu.x") || !strings.Contains(err.Error(), want) {
			t.Errorf("%v", err)
		}
	}
}
