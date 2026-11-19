package transport

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
)

const defaultTCPPort = "4403"

// tcpAddr adds the default port and IPv6 brackets where missing.
func tcpAddr(addr string) string {
	if strings.HasPrefix(addr, "[") {
		if strings.HasSuffix(addr, "]") {
			return addr + ":" + defaultTCPPort
		}
		return addr
	}
	if strings.Count(addr, ":") > 1 { // bare IPv6
		return "[" + addr + "]:" + defaultTCPPort
	}
	if strings.Contains(addr, ":") {
		return addr
	}
	return addr + ":" + defaultTCPPort
}

// DialTCP connects to a node's TCP API (default port 4403).
func DialTCP(ctx context.Context, addr string, log io.Writer) (Conn, error) {
	full := tcpAddr(addr)
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", full)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", full, err)
	}
	return NewStreamConn(nc, KindTCP, log), nil
}
