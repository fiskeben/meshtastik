package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"go.bug.st/serial"
	"go.bug.st/serial/enumerator"
)

// SerialCandidate is a USB serial port that looks like a Meshtastic node.
type SerialCandidate struct{ Path, VID, PID, Product string }

// Vendor IDs of USB-UART bridges and MCUs used by Meshtastic hardware.
var knownVIDs = map[string]bool{
	"10C4": true, "1A86": true, "0403": true, "303A": true,
	"239A": true, "2886": true, "2E8A": true,
}

func filterCandidates(ports []*enumerator.PortDetails) []SerialCandidate {
	var out []SerialCandidate
	index := map[string]int{} // path with /dev/tty. normalised to /dev/cu.
	for _, p := range ports {
		if !p.IsUSB || !knownVIDs[strings.ToUpper(p.VID)] {
			continue
		}
		key := strings.Replace(p.Name, "/dev/tty.", "/dev/cu.", 1)
		cand := SerialCandidate{Path: p.Name, VID: p.VID, PID: p.PID, Product: p.Product}
		if i, dup := index[key]; dup {
			if strings.HasPrefix(p.Name, "/dev/cu.") {
				out[i] = cand
			}
			continue
		}
		index[key] = len(out)
		out = append(out, cand)
	}
	return out
}

// DetectSerial lists likely Meshtastic serial ports.
func DetectSerial() ([]SerialCandidate, error) {
	ports, err := enumerator.GetDetailedPortsList()
	if err != nil {
		return nil, fmt.Errorf("list serial ports: %w", err)
	}
	return filterCandidates(ports), nil
}

func serialOpenError(path string, code serial.PortErrorCode, err error) error {
	switch code {
	case serial.PortBusy:
		return fmt.Errorf("serial port %s is busy: is another program using it? (%w)", path, err)
	case serial.PortNotFound:
		return fmt.Errorf("serial port %s not found (%w)", path, err)
	case serial.PermissionDenied:
		return fmt.Errorf("serial port %s: permission denied (%w)", path, err)
	}
	return fmt.Errorf("open serial port %s: %w", path, err)
}

// OpenSerial opens path at 115200 8N1 and wakes the node's API.
func OpenSerial(ctx context.Context, path string, log io.Writer) (Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	port, err := serial.Open(path, &serial.Mode{
		BaudRate: 115200, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit,
		// DTR and RTS asserted, as pyserial does, so ESP32 auto-reset is not triggered.
		InitialStatusBits: &serial.ModemOutputBits{DTR: true, RTS: true},
	})
	if err != nil {
		var pe *serial.PortError
		if errors.As(err, &pe) {
			return nil, serialOpenError(path, pe.Code(), err)
		}
		return nil, fmt.Errorf("open serial port %s: %w", path, err)
	}
	if _, err := port.Write(bytes.Repeat([]byte{0xC3}, 32)); err != nil {
		port.Close()
		return nil, fmt.Errorf("wake serial port %s: %w", path, err)
	}
	select {
	case <-time.After(100 * time.Millisecond):
	case <-ctx.Done():
		port.Close()
		return nil, ctx.Err()
	}
	return NewStreamConn(port, KindSerial, log), nil
}
