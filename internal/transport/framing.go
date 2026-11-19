// Package transport carries Meshtastic stream framing and connections.
package transport

import (
	"bufio"
	"errors"
	"io"
)

// MaxPayload is the largest protobuf payload a frame may carry.
const MaxPayload = 512

const (
	start1 = 0x94
	start2 = 0xC3
)

// ErrFrameTooLarge is returned when a payload exceeds MaxPayload.
var ErrFrameTooLarge = errors.New("frame payload exceeds 512 bytes")

// WriteFrame writes payload with the 0x94 0xC3 + big-endian length header.
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxPayload {
		return ErrFrameTooLarge
	}
	buf := make([]byte, 0, 4+len(payload))
	buf = append(buf, start1, start2, byte(len(payload)>>8), byte(len(payload)))
	buf = append(buf, payload...)
	_, err := w.Write(buf)
	return err
}

// FrameReader extracts frames from a byte stream. Bytes outside frames
// (device debug log output) are copied to the log writer.
type FrameReader struct {
	r   *bufio.Reader
	log io.Writer
}

// NewFrameReader returns a FrameReader over r. A nil log discards non-frame bytes.
func NewFrameReader(r io.Reader, log io.Writer) *FrameReader {
	if log == nil {
		log = io.Discard
	}
	return &FrameReader{r: bufio.NewReader(r), log: log}
}

// Next returns the next frame payload. It returns io.EOF when input ends
// between frames and io.ErrUnexpectedEOF when it ends inside a frame.
func (f *FrameReader) Next() ([]byte, error) {
	// Seek the start marker, logging everything else.
	for {
		b, err := f.r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != start1 {
			f.logByte(b)
			continue
		}
	second:
		b, err = f.r.ReadByte()
		if err != nil {
			return nil, unexpected(err)
		}
		if b == start1 { // false start; this byte may begin a real marker
			f.logByte(start1)
			goto second
		}
		if b != start2 {
			f.logByte(start1)
			f.logByte(b)
			continue
		}
		var hdr [2]byte
		if _, err := io.ReadFull(f.r, hdr[:]); err != nil {
			return nil, unexpected(err)
		}
		n := int(hdr[0])<<8 | int(hdr[1])
		if n > MaxPayload { // corrupt header: drop it and resync
			continue
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(f.r, payload); err != nil {
			return nil, unexpected(err)
		}
		return payload, nil
	}
}

func (f *FrameReader) logByte(b byte) { _, _ = f.log.Write([]byte{b}) }

func unexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}
