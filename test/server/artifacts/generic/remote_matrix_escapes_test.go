package generic_test

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// rawLineUpstream starts a plain TCP upstream that records each request
// line exactly as its bytes arrive, before any HTTP parser decodes or
// refuses them, and answers every request 200 with matrixBody. It returns
// the upstream's URL and a function that hands back and forgets the lines
// recorded so far.
func rawLineUpstream(t *testing.T) (string, func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	var lines []string
	serve := func(conn net.Conn) {
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
		mu.Lock()
		lines = append(lines, strings.TrimSuffix(line, "\r\n"))
		mu.Unlock()
		body := ""
		if !strings.HasPrefix(line, http.MethodHead+" ") {
			body = matrixBody
		}
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n"+
			"Connection: close\r\n\r\n%s", len(matrixBody), body)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(conn)
		}
	}()
	return "http://" + ln.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := lines
		lines = nil
		return out
	}
}

// TestAccGenericRemoteSendsEscapedMatrixParametersUpstream (#1420): a
// matrix value that decodes to a backslash, NUL, CR or LF reaches the
// upstream as %5C, %00, %0D or %0A after the path, the bytes 7.x sends
// (RemoteRepoBase.buildRequestMatrixParams, then HttpRepo.encodeIfNeeded's
// URIUtil.encodeQuery, 7.161.26), and no request line on the wire carries
// a raw control byte. Before, such a value was left off and the bare path
// asked for. A value whose decoded form holds a "." or ".." piece between
// "/", "\" or NUL is still left off: 7.x sends its "/" and dots as they
// are, so the upstream resolves another file, a fault not followed, and
// escaped it would be a step for an upstream that decodes before it
// resolves. So is one whose piece starts with a dot segment followed by
// ";", "?" or "#", which such an upstream would read as a dot segment once
// it strips path parameters or splits off the query or fragment; main sent
// those.
func TestAccGenericRemoteSendsEscapedMatrixParametersUpstream(t *testing.T) {
	w := newRemoteWorld(t)
	upstream, take := rawLineUpstream(t)
	w.c.URL = upstream
	w.update()
	for i, tc := range []struct{ params, want string }{
		{";x=a%5Cb", ";x=a%5Cb"},
		{";x=a%5C,b,c", ";x=a%5C,b,c"},
		{";x=a%00b", ";x=a%00b"},
		{";x=a%0Db", ";x=a%0Db"},
		{";x=a%0Ab", ";x=a%0Ab"},
		{";x=1%0D%0AHost:%20evil", ";x=1%0D%0AHost:%20evil"},
		{";x=..%5C..b;y=..%00", ";x=..%5C..b;y=..%00"},
		{";x=1%2F..%2F..%2Fsecret", ""},
		{";x=a%5C..%5Cb", ""},
		{";x=a%00..", ""},
		{";x=a%5C.", ""},
		{";x=1%2F..;y=1", ""},
		{";x=1%2F..%3Fq", ""},
		{";x=1%2F..%23f", ""},
		{";x=1%2F.;y=1", ""},
		{";x=1%2F..b;y=1", ";x=1%2F..b;y=1"},
	} {
		path := fmt.Sprintf("/e%d/file.bin", i)
		for _, method := range []string{http.MethodHead, http.MethodGet} {
			got := w.request(method, "/artifactory/remote"+path+tc.params, "reader", "")
			if got.Code != http.StatusOK {
				t.Errorf("%s %s answered %d %q, want 200", method, path+tc.params, got.Code,
					got.Body)
			}
			if method == http.MethodGet && got.Body.String() != matrixBody {
				t.Errorf("GET %s answered %q, want %q", path+tc.params, got.Body, matrixBody)
			}
		}
		lines := take()
		if len(lines) == 0 {
			t.Errorf("%s did not ask the upstream", path+tc.params)
		}
		for _, line := range lines {
			method, rest, _ := strings.Cut(line, " ")
			if (method != http.MethodHead && method != http.MethodGet) ||
				rest != path+tc.want+" HTTP/1.1" {
				t.Errorf("%s asked the upstream %q, want %s%s", path+tc.params, line, path,
					tc.want)
			}
			if strings.IndexFunc(line, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
				t.Errorf("request line %q carries a raw control byte", line)
			}
		}
	}
}
