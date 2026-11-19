package transport

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func frame(p string) []byte { return append([]byte{0x94, 0xC3, 0, byte(len(p))}, p...) }

func TestWriteFrame(t *testing.T) {
	var b bytes.Buffer
	if err := WriteFrame(&b, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b.Bytes(), frame("hello")) {
		t.Fatalf("got % x", b.Bytes())
	}
}

func TestWriteFrameRejectsOversize(t *testing.T) {
	if err := WriteFrame(io.Discard, make([]byte, 513)); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatal(err)
	}
}

func TestFrameReaderSkipsLogBytes(t *testing.T) {
	in := append([]byte("boot log\r\n"), frame("a")...)
	in = append(in, "more\n"...)
	in = append(in, frame("b")...)
	var log bytes.Buffer
	fr := NewFrameReader(bytes.NewReader(in), &log)
	for _, want := range []string{"a", "b"} {
		p, err := fr.Next()
		if err != nil || string(p) != want {
			t.Fatalf("got %q %v, want %q", p, err, want)
		}
	}
	if _, err := fr.Next(); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
	if log.String() != "boot log\r\nmore\n" {
		t.Fatalf("log %q", log.String())
	}
}

func TestFrameReaderResyncsOnOversizeLength(t *testing.T) {
	in := append([]byte{0x94, 0xC3, 0x02, 0x01}, frame("ok")...)
	p, err := NewFrameReader(bytes.NewReader(in), io.Discard).Next()
	if err != nil || string(p) != "ok" {
		t.Fatalf("got %q %v", p, err)
	}
}

func TestFrameReaderFalseStart(t *testing.T) {
	in := append([]byte{0x94, 'x'}, frame("ok")...)
	p, err := NewFrameReader(bytes.NewReader(in), io.Discard).Next()
	if err != nil || string(p) != "ok" {
		t.Fatalf("got %q %v", p, err)
	}
}

func TestFrameReaderFalseStartReexaminesByte(t *testing.T) {
	in := append([]byte{0x94}, frame("ok")...) // 0x94 0x94 0xC3 ...
	var log bytes.Buffer
	p, err := NewFrameReader(bytes.NewReader(in), &log).Next()
	if err != nil || string(p) != "ok" {
		t.Fatalf("got %q %v", p, err)
	}
	if !bytes.Equal(log.Bytes(), []byte{0x94}) {
		t.Fatalf("log % x", log.Bytes())
	}
}

func TestFrameReaderTruncated(t *testing.T) {
	in := []byte{0x94, 0xC3, 0, 5, 'a', 'b'}
	if _, err := NewFrameReader(bytes.NewReader(in), io.Discard).Next(); err != io.ErrUnexpectedEOF {
		t.Fatal(err)
	}
}

func TestFrameReaderTruncatedHeader(t *testing.T) {
	for _, in := range [][]byte{{0x94}, {0x94, 0xC3}, {0x94, 0xC3, 0}} {
		if _, err := NewFrameReader(bytes.NewReader(in), io.Discard).Next(); err != io.ErrUnexpectedEOF {
			t.Fatalf("% x: %v", in, err)
		}
	}
}

func FuzzFrameReader(f *testing.F) {
	f.Add(frame("seed"))
	f.Fuzz(func(t *testing.T, in []byte) {
		fr := NewFrameReader(bytes.NewReader(in), io.Discard)
		for {
			p, err := fr.Next()
			if err != nil {
				return
			}
			if len(p) > MaxPayload {
				t.Fatalf("payload %d > max", len(p))
			}
		}
	})
}
