package outbound

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

// peer answers /warm at once and /slow after answer (or never, when answer is
// zero), recording the connection each /slow exchange arrived on.
type peer struct {
	*httptest.Server
	mu    sync.Mutex
	warm  []string
	slow  []string
	clock time.Duration
}

func newPeer(t *testing.T, answer time.Duration) *peer {
	t.Helper()
	p := &peer{clock: answer}
	routes := chi.NewRouter()
	routes.HandleFunc("/warm", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.warm = append(p.warm, r.RemoteAddr)
		p.mu.Unlock()
		_, _ = io.WriteString(w, "warm")
	})
	routes.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		p.mu.Lock()
		p.slow = append(p.slow, r.RemoteAddr)
		p.mu.Unlock()
		if p.clock == 0 {
			<-r.Context().Done()
			return
		}
		select {
		case <-time.After(p.clock):
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	})
	p.Server = httptest.NewServer(routes)
	t.Cleanup(p.Close)
	return p
}

func (p *peer) exchanges() (warm, slow []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.warm...), append([]string(nil), p.slow...)
}

// replicationMethods are the methods the replication push client sends.
var replicationMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodPost,
}

// A reused connection times the peer's answer from the request, not from
// when it went idle. Before #1866 the stale idle deadline failed the first
// response read: net/http replayed GET and HEAD on a second connection, and
// failed a write the peer had received with a timeout.
func TestAccReusedConnectionTimesTheAnswerFromTheRequest(t *testing.T) {
	const timeout = 2 * time.Second
	for _, method := range replicationMethods {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			p := newPeer(t, timeout*85/100)
			client := testClient(t, parsed(t, p.URL), nil, nil, timeout, nil)
			readResponse(t)(client.Do(request(t, http.MethodGet, p.URL+"/warm", "")))
			// Idle for less than the pool's limit of half the timeout, so the
			// request reuses the connection; the answer comes within the
			// request's timeout but after the idle connection's.
			time.Sleep(timeout * 3 / 10)
			response, err := client.Do(request(t, method, p.URL+"/slow", "replicated body"))
			if err != nil {
				t.Fatal("answer within the socket timeout failed", err)
			}
			readResponse(t)(response, nil)
			if warm, slow := p.exchanges(); len(slow) != 1 || slow[0] != warm[0] {
				t.Fatal("answer on a reused connection was replayed", warm, slow)
			}
		})
	}
}

// A socket timeout on a reused connection fails the one request. Before
// #1866 net/http replayed a timed-out GET or HEAD on a fresh connection;
// writes were never replayed, because Client.Do drops GetBody and the
// idempotency headers and net/http replays only GET, HEAD, OPTIONS and TRACE.
func TestAccReusedConnectionTimeoutIsNotReplayed(t *testing.T) {
	const timeout = 500 * time.Millisecond
	for _, method := range replicationMethods {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			p := newPeer(t, 0)
			client := testClient(t, parsed(t, p.URL), nil, nil, timeout, nil)
			readResponse(t)(client.Do(request(t, http.MethodGet, p.URL+"/warm", "")))
			// The idle deadline would expire before the header timeout, and
			// the pool still hands the connection out.
			time.Sleep(timeout / 5)
			upload := request(t, method, p.URL+"/slow", "replicated body")
			upload.Header.Set("Idempotency-Key", "caller-key")
			if _, err := client.Do(upload); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			if _, slow := p.exchanges(); len(slow) != 1 {
				t.Fatal("timed-out request reached the peer", len(slow), "times")
			}
		})
	}
}

// closeOnHandOver returns ctx with a trace that runs closePool once, when
// net/http has taken the request's connection back but not yet handed over
// its bodyless answer, and then holds net/http there so the round trip sees
// the closed connection first. Its PutIdleConn hook runs in that window.
func closeOnHandOver(ctx context.Context, closePool func(), hold time.Duration) context.Context {
	var once sync.Once
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{PutIdleConn: func(error) {
		once.Do(func() {
			closePool()
			time.Sleep(hold)
		})
	}})
}

// A pool that closes a connection while net/http hands over a bodyless
// answer fails the request as a connection failure the caller may retry.
// Before #1878 SafeError answered ErrRequest, a refused request.
func TestAccPoolClosingTheConnectionUnderAnAnswerIsAConnectionFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		timeout time.Duration
		close   func(*Client)
	}{
		// Client.Close releases the connection, as at shutdown.
		"close": {timeout: time.Second, close: (*Client).Close},
		// The pool's idle limit, half of 40 ms, passes during the hold.
		"idle-limit": {timeout: 40 * time.Millisecond, close: func(*Client) {}},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPeer(t, 0)
			lost := 0
			// The hold makes the closed connection win; an answer that still
			// wins on a starved scheduler is retried, never misclassified.
			for range 5 {
				client := testClient(t, parsed(t, p.URL), nil, nil, tc.timeout, nil)
				head := request(t, http.MethodHead, p.URL+"/warm", "")
				head = head.WithContext(closeOnHandOver(t.Context(), func() { tc.close(client) }, 100*time.Millisecond))
				response, err := client.Do(head)
				if err == nil {
					readResponse(t)(response, nil)
					continue
				}
				if !errors.Is(err, ErrConnection) {
					t.Fatal("pool close classified as", err)
				}
				lost++
			}
			if lost == 0 {
				t.Fatal("no request saw its connection close")
			}
		})
	}
}

func TestUnitIdleConnTimeoutIsHalfTheSocketTimeout(t *testing.T) {
	for socket, want := range map[time.Duration]time.Duration{
		0: 90 * time.Second, time.Nanosecond: time.Nanosecond, 100 * time.Millisecond: 50 * time.Millisecond,
		15 * time.Second: 7500 * time.Millisecond, time.Hour: 90 * time.Second,
	} {
		if got := IdleConnTimeout(socket); got != want {
			t.Fatal(socket, got, want)
		}
	}
	client := testClient(t, parsed(t, "http://127.0.0.1:1"), nil, nil, time.Second, nil)
	if client.transport.IdleConnTimeout != 500*time.Millisecond {
		t.Fatal("client pool ignores the socket timeout", client.transport.IdleConnTimeout)
	}
}

type deadlineConn struct {
	net.Conn
	read, write error
}

func (c deadlineConn) SetReadDeadline(time.Time) error  { return c.read }
func (c deadlineConn) SetWriteDeadline(time.Time) error { return c.write }

func TestUnitSocketTimeoutConnNeedsItsDeadlines(t *testing.T) {
	if conn := (deadlineConn{}); SocketTimeoutConn(conn, 0) != conn {
		t.Fatal("no socket timeout still wrapped the connection")
	}
	refused := errors.New("refused")
	for _, conn := range []deadlineConn{{write: refused}, {read: refused}} {
		if _, err := SocketTimeoutConn(conn, time.Second).Write([]byte("x")); !errors.Is(err, refused) {
			t.Fatal(conn, err)
		}
	}
	if _, err := SocketTimeoutConn(deadlineConn{read: refused}, time.Second).Read(nil); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	local, remote := net.Pipe()
	defer local.Close()
	if err := remote.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := SocketTimeoutConn(local, time.Second).Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("write failure lost its cause", err)
	}
}
