package transport

import (
	"io"
	"net"
	"strings"
	"testing"
)

func TestTCPAddr(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.5": "192.168.1.5:4403", "node.local:1234": "node.local:1234",
		"fe80::1": "[fe80::1]:4403", "[fe80::1]": "[fe80::1]:4403", "[fe80::1]:99": "[fe80::1]:99",
	} {
		if got := tcpAddr(in); got != want {
			t.Errorf("tcpAddr(%q)=%q want %q", in, got, want)
		}
	}
}

func TestDialTCPConnects(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			defer c.Close()
			io.Copy(io.Discard, c)
		}
	}()
	c, err := DialTCP(ctxT(t), ln.Addr().String(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Kind() != KindTCP {
		t.Fatalf("kind %v", c.Kind())
	}
}

func TestDialTCPErrorNamesAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	_, err = DialTCP(ctxT(t), addr, io.Discard)
	if err == nil || !strings.Contains(err.Error(), addr) {
		t.Fatalf("%v", err)
	}
}
