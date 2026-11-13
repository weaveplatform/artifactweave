package upstream

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
)

// rawUpstream is a plain TCP server that records each request line exactly
// as its bytes arrive, before any HTTP parser decodes or refuses them, and
// answers 200 "ok", or a 302 to the Location redirects names for the
// request target.
type rawUpstream struct {
	mu        sync.Mutex
	lines     []string
	redirects map[string]string
}

func newRawUpstream(t *testing.T, redirects map[string]string) (*rawUpstream, *url.URL) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	s := &rawUpstream{redirects: redirects}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	return s, &url.URL{Scheme: "http", Host: ln.Addr().String(), Path: "/repo"}
}

func (s *rawUpstream) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		return
	}
	for {
		header, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if header == "\r\n" {
			break
		}
	}
	line = strings.TrimSuffix(line, "\r\n")
	s.mu.Lock()
	s.lines = append(s.lines, line)
	s.mu.Unlock()
	answer := "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"
	if fields := strings.Fields(line); len(fields) == 3 && s.redirects[fields[1]] != "" {
		answer = "HTTP/1.1 302 Found\r\nLocation: " + s.redirects[fields[1]] +
			"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
	}
	_, _ = conn.Write([]byte(answer))
}

func (s *rawUpstream) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.lines
	s.lines = nil
	return out
}

// securedClient is a client of base with the credential layer's request
// checks and destination policy, which run CheckedURL on every request and
// redirect, as Resolver.Options builds them.
func securedClient(t *testing.T, base *url.URL) *Client {
	t.Helper()
	security := &sourceSecurity{
		base:   base,
		policy: &destinationPolicy{private: map[string]bool{remote.Origin(base): true}},
	}
	return clientFor(t, repo.Config{URL: base.String()}, remote.Options{
		Prepare:       security.prepare,
		CheckURL:      security.checkURL,
		WrapTransport: security.transport,
	})
}

// TestAccMatrixEscapesTravelAndTheRestStaysStrict (#1420): a request's
// matrix string reaches the upstream with a backslash, NUL, CR or LF as
// %5C, %00, %0D or %0A, the bytes 7.x's encodeIfNeeded sends (7.161.26),
// and the request line on the wire carries no raw CR, LF, NUL or other
// control byte. The repository URL and the item path before it are still
// refused for any of them, escaped or not, and for a dot segment, and so is
// a matrix string an upstream would decode into a step, or a Path that
// does not end with the Matrix it names: none of those reaches the
// network. A redirect keeps the matrix rule only for a target whose path
// ends with the same matrix string.
func TestAccMatrixEscapesTravelAndTheRestStaysStrict(t *testing.T) {
	up, base := newRawUpstream(t, map[string]string{
		"/repo/hop/file.bin;x=a%5Cb":   "/moved/file.bin;x=a%5Cb",
		"/repo/hop2/file.bin;x=a%5Cb":  "/mo%5Cved/file.bin;x=a%5Cb",
		"/repo/hop3/file.bin;x=a%5Cb":  "/moved/file.bin;y=a%5Cb",
		"/repo/hop4/file.bin;x=a%0Db":  "/moved/file.bin;x=a%0Db%2F..",
		"/repo/plain/file.bin;x=1%2Fb": "/moved/file.bin;x=1%2Fb",
	})
	client := securedClient(t, base)
	send := func(path, matrix string) error {
		res, err := client.Do(t.Context(), remote.Request{
			Method: http.MethodGet, Path: path + matrix, Matrix: matrix,
		})
		if err == nil {
			_ = body(t, res)
		}
		return err
	}
	for _, matrix := range []string{
		";x=a%5Cb", ";x=a%00b", ";x=a%0Db", ";x=a%0Ab", ";x=1%0D%0AHost:%20evil",
		";x=..%00;y=a%5C..b", ";x=1;y%2B=2",
	} {
		if err := send("a/file.bin", matrix); err != nil {
			t.Errorf("GET a/file.bin%s: %v", matrix, err)
			continue
		}
		lines := up.take()
		want := "GET /repo/a/file.bin" + matrix + " HTTP/1.1"
		if len(lines) != 1 || lines[0] != want {
			t.Errorf("GET a/file.bin%s sent %q, want %q", matrix, lines, want)
		}
		for _, line := range lines {
			if strings.IndexFunc(line, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
				t.Errorf("request line %q carries a raw control byte", line)
			}
		}
	}

	for _, bad := range []string{
		"a%5Cb", "a%5cb", "a%00b", "a%0Db", "a%0Ab", "..", ".", "%2e%2e", "a%2F..%2Fb",
	} {
		for _, path := range []string{bad + "/file.bin", "a/" + bad} {
			if err := send(path, ";x=1"); !errors.Is(err, remote.ErrURL) {
				t.Errorf("GET %s;x=1 = %v, want ErrURL", path, err)
			}
		}
	}
	for _, path := range []string{`a\b`, "a\x00b", "a\rb", "a\nb"} {
		if err := send(path, ";x=a%5Cb"); !errors.Is(err, remote.ErrURL) {
			t.Errorf("GET %q = %v, want ErrURL", path, err)
		}
	}
	for _, matrix := range []string{";x=%2F..", ";x=a%5C..%5Cb", ";x=%00..", "x=1", ";x=a\\b"} {
		if err := send("a/file.bin", matrix); !errors.Is(err, remote.ErrURL) {
			t.Errorf("GET a/file.bin%s = %v, want ErrURL", matrix, err)
		}
	}
	res, err := client.Do(t.Context(), remote.Request{
		Method: http.MethodGet, Path: "a/file.bin;x=a%5Cb", Matrix: ";y=1",
	})
	if !errors.Is(err, remote.ErrURL) {
		t.Errorf("a Path that does not end with its Matrix = %v, want ErrURL", err)
		if err == nil {
			_ = body(t, res)
		}
	}
	if lines := up.take(); len(lines) != 0 {
		t.Errorf("refused requests reached the upstream: %q", lines)
	}

	for _, bad := range []string{"re%5Cpo", "re%00po", "re%0Dpo", "re%0Apo", "x/../repo"} {
		u := *base
		u.Path, _ = url.PathUnescape("/" + bad)
		u.RawPath = "/" + bad
		badClient := securedClient(t, &u)
		res, err := badClient.Do(t.Context(), remote.Request{
			Method: http.MethodGet, Path: "a/file.bin;x=a%5Cb", Matrix: ";x=a%5Cb",
		})
		if !errors.Is(err, remote.ErrSecurity) {
			t.Errorf("repository URL /%s = %v, want ErrSecurity", bad, err)
			if err == nil {
				_ = body(t, res)
			}
		}
	}
	if lines := up.take(); len(lines) != 0 {
		t.Errorf("a refused repository URL reached the upstream: %q", lines)
	}

	if err := send("hop/file.bin", ";x=a%5Cb"); err != nil {
		t.Errorf("redirect to the same matrix string: %v", err)
	}
	if lines := up.take(); len(lines) != 2 || lines[1] != "GET /moved/file.bin;x=a%5Cb HTTP/1.1" {
		t.Errorf("redirect to the same matrix string sent %q", lines)
	}
	for _, path := range []string{"hop2/file.bin", "hop3/file.bin"} {
		if err := send(path, ";x=a%5Cb"); !errors.Is(err, remote.ErrSecurity) {
			t.Errorf("redirect from %s = %v, want ErrSecurity", path, err)
		}
	}
	if err := send("hop4/file.bin", ";x=a%0Db"); !errors.Is(err, remote.ErrSecurity) {
		t.Errorf("redirect from hop4 = %v, want ErrSecurity", err)
	}
	if lines := up.take(); len(lines) != 3 {
		t.Errorf("refused redirects reached the upstream: %q", lines)
	}
	if err := send("plain/file.bin", ";x=1%2Fb"); err != nil {
		t.Errorf("redirect of a plain matrix string: %v", err)
	}
	if lines := up.take(); len(lines) != 2 || lines[1] != "GET /moved/file.bin;x=1%2Fb HTTP/1.1" {
		t.Errorf("redirect of a plain matrix string sent %q", lines)
	}
}
