package outbound

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
)

func (c *Client) proxyConnection(ctx context.Context, network string, addresses []string) (net.Conn, error) {
	last := ErrConnection
	for _, address := range addresses {
		conn, err := c.direct(ctx, network, address)
		if err != nil {
			last = err
			continue
		}
		if c.proxy.Scheme != "https" {
			return conn, nil
		}
		// Authenticate the proxy's logical TLS name, never its checked physical
		// address or the destination identity. No destination client cert is sent.
		secure := tls.Client(conn, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: c.roots, ServerName: c.proxy.Hostname()})
		if err := secure.HandshakeContext(ctx); err != nil {
			closeErr := conn.Close()
			return nil, SafeError(errors.Join(err, closeErr))
		}
		return secure, nil
	}
	return nil, last
}

func (c *Client) tunnel(ctx context.Context, network, target string, proxies []string) (net.Conn, error) {
	conn, err := c.proxyConnection(ctx, network, proxies)
	if err != nil {
		return nil, err
	}
	if c.ntlm != nil {
		// Connect owns conn from here, including close on failure/cancellation.
		// It CONNECTs to the same checked IP, so the proxy never resolves DNS.
		tunnel, err := c.ntlm.Connect(ctx, conn, target, c.transport.MaxResponseHeaderBytes)
		if err != nil {
			// Cancellation closes the socket, which would otherwise surface as a
			// retryable connection failure against the next checked address.
			return nil, SafeError(errors.Join(ctx.Err(), err))
		}
		return tunnel, nil
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()
	// Always CONNECT to the checked IP, including a plain HTTP destination.
	// An absolute-form forward request could let the proxy resolve DNS again.
	request, err := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+target, nil)
	if err != nil {
		return nil, ErrSecurity
	}
	if c.proxy.User != nil {
		password, _ := c.proxy.User.Password()
		request.SetBasicAuth(c.proxy.User.Username(), password)
		request.Header.Set("Proxy-Authorization", request.Header.Get("Authorization"))
		request.Header.Del("Authorization")
	}
	if err := request.Write(conn); err != nil {
		return nil, SafeError(err)
	}
	reader := bufio.NewReader(io.LimitReader(conn, c.transport.MaxResponseHeaderBytes))
	//nolint:bodyclose // a successful CONNECT body is the caller-owned tunnel connection
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return nil, SafeError(err)
	}
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusProxyAuthRequired || response.StatusCode == http.StatusForbidden {
			return nil, ErrSecurity
		}
		return nil, ErrConnection
	}
	success = true
	return bufferedConn{Conn: conn, reader: io.MultiReader(reader, conn)}, nil
}

type bufferedConn struct {
	net.Conn
	reader io.Reader
}

func (c bufferedConn) Read(body []byte) (int, error) {
	n, err := c.reader.Read(body)
	if err == nil {
		return n, nil
	}
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	return n, fmt.Errorf("read outbound proxy tunnel: %w", err)
}
