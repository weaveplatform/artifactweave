package peers

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// Link is the TCP forwarder through which one instance reaches another. It
// forwards bytes unchanged, so the far instance sees a request whose Host is
// the link's address and answers with its own configured base URL.
type Link struct {
	// From is the instance whose configuration allows the link's URL; To is
	// the instance it forwards to.
	From, To *Instance
	// URL is http://<the link's loopback address>.
	URL string

	addr     string
	listener net.Listener
	served   sync.WaitGroup

	mu          sync.Mutex
	partitioned bool
	closed      bool
	conns       map[net.Conn]struct{}
}

func newLink(t *testing.T, from, to *Instance) *Link {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	l := &Link{From: from, To: to, URL: "http://" + addr, addr: addr, listener: listener, conns: map[net.Conn]struct{}{}}
	t.Cleanup(l.close)
	return l
}

// Partition cuts the link: it resets every connection it carries and every
// connection it accepts until Heal. A reset rather than silently dropped
// traffic makes the instance's call fail at once, so a scenario needs no
// client timeout to see the partition.
func (l *Link) Partition() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.partitioned = true
	for conn := range l.conns {
		reset(conn)
		delete(l.conns, conn)
	}
}

// Heal lets the link carry new connections again.
func (l *Link) Heal() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.partitioned = false
}

// Do sends a request through the link as To's administrator. A partitioned
// link answers with a transport error, which Do returns.
func (l *Link) Do(method, path, body string) (int, string, error) {
	return l.To.cluster.request(l.URL, l.To, method, path, body)
}

// serve starts accepting once To has its final address; connections that
// arrive before wait in the listener's backlog.
func (l *Link) serve() {
	target := l.To.addr
	l.served.Go(func() {
		for {
			conn, err := l.listener.Accept()
			if err != nil {
				return
			}
			if !l.track(conn) {
				reset(conn)
				continue
			}
			l.served.Go(func() { l.forward(conn, target) })
		}
	})
}

func (l *Link) forward(conn net.Conn, target string) {
	defer l.untrack(conn)
	upstream, err := net.DialTimeout("tcp", target, 5*time.Second)
	if err != nil {
		reset(conn)
		return
	}
	if !l.track(upstream) {
		reset(upstream)
		reset(conn)
		return
	}
	defer l.untrack(upstream)
	copied := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		// Half-close keeps HTTP's request/response exchange intact when one
		// side finishes writing first.
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		copied <- struct{}{}
	}
	go pipe(upstream, conn)
	go pipe(conn, upstream)
	<-copied
	<-copied
}

// track records conn so Partition and close can reset it, and reports
// false when the link is cut or closed.
func (l *Link) track(conn net.Conn) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.partitioned || l.closed {
		return false
	}
	l.conns[conn] = struct{}{}
	return true
}

func (l *Link) untrack(conn net.Conn) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.conns[conn]; ok {
		delete(l.conns, conn)
		_ = conn.Close()
	}
}

// close stops accepting, resets what the link carries and waits for its
// goroutines, so a closed link holds no port and no connection.
func (l *Link) close() {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	for conn := range l.conns {
		reset(conn)
		delete(l.conns, conn)
	}
	l.mu.Unlock()
	_ = l.listener.Close()
	l.served.Wait()
}

// reset closes conn with an RST rather than a FIN, as a cut network path
// looks to the peer that next writes.
func reset(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
}
