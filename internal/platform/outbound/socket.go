package outbound

import (
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// SocketTimeoutConn applies a socket inactivity timeout to conn: each read and
// each write must make progress within timeout. A timeout of zero or less
// returns conn unchanged. A transport using it pairs it with IdleConnTimeout
// for its pool and with ResponseHeaderTimeout set to the same timeout, as
// Client and the remote repository client do.
func SocketTimeoutConn(conn net.Conn, timeout time.Duration) net.Conn {
	if timeout <= 0 {
		return conn
	}
	return idleConn{Conn: conn, timeout: timeout}
}

// IdleConnTimeout is the pool limit for connections under SocketTimeoutConn:
// half the socket timeout, at most 90 seconds, and 90 seconds when there is no
// socket timeout. A pooled connection's read loop waits under the deadline
// set when the connection went idle, and the next request's write moves it.
// Handing out only connections idle for less than half the timeout keeps that
// deadline out of reach between taking a connection and writing to it.
func IdleConnTimeout(socketTimeout time.Duration) time.Duration {
	limit := 90 * time.Second
	if socketTimeout > 0 {
		limit = min(limit, max(socketTimeout/2, time.Nanosecond))
	}
	return limit
}

type idleConn struct {
	net.Conn
	timeout time.Duration
}

func (c idleConn) Read(body []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, fmt.Errorf("set socket read deadline: %w", err)
	}
	n, err := c.Conn.Read(body)
	if err == nil {
		return n, nil
	}
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	return n, fmt.Errorf("read socket: %w", err)
}

func (c idleConn) Write(body []byte) (int, error) {
	now := time.Now()
	if err := c.SetWriteDeadline(now.Add(c.timeout)); err != nil {
		return 0, fmt.Errorf("set socket write deadline: %w", err)
	}
	// net/http's read loop is already blocked in a read on a reused
	// connection, under the deadline it set when the connection went idle.
	// Left alone, that deadline expires while the peer is still answering:
	// net/http replays an idempotent request whose first response read fails
	// on a reused connection (one call, two exchanges), and fails any other
	// request early (#1866). Twice the timeout leaves the header wait to
	// ResponseHeaderTimeout, which starts when the write completes and fails
	// without a replay, while every pending read stays bounded.
	if err := c.SetReadDeadline(now.Add(2 * c.timeout)); err != nil {
		return 0, fmt.Errorf("set socket read deadline: %w", err)
	}
	n, err := c.Conn.Write(body)
	if err != nil {
		return n, fmt.Errorf("write socket: %w", err)
	}
	return n, nil
}
