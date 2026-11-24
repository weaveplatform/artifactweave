package peers

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

// unrouted tags a connection a relay carries before it knows which instance
// it leads to; no cut names it, so only close resets it.
const unrouted = -1

const (
	// dialTimeout bounds a relay's connection to the instance or ingress it
	// carries a connection to; both are on the loopback interface.
	dialTimeout = 5 * time.Second
	// requestHeadTimeout bounds how long an egress proxy waits for a
	// request's head, so a client that connects and sends nothing cannot
	// hold the proxy's close.
	requestHeadTimeout = 30 * time.Second
)

// relay accepts connections on a loopback listener and carries each one to
// an instance. It is an instance's ingress, a TCP forwarder whose address is
// the instance's base URL, or its egress, the forward proxy the instance's
// own server calls other instances through. It tracks every connection it
// carries, with the instance the connection leads to, so a cut or close can
// reset it.
type relay struct {
	addr     string
	listener net.Listener
	served   sync.WaitGroup

	mu     sync.Mutex
	closed bool
	// cut holds the instances an egress refuses to carry connections to.
	cut   map[int]bool
	conns map[net.Conn]int
}

func listenRelay(t *testing.T) *relay {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &relay{addr: listener.Addr().String(), listener: listener, cut: map[int]bool{}, conns: map[net.Conn]int{}}
	t.Cleanup(r.close)
	return r
}

// serve starts accepting, handing each connection to handle. Connections
// that arrive before it wait in the listener's backlog, so a relay can
// listen before the instance it leads to has its final address.
func (r *relay) serve(handle func(net.Conn)) {
	r.served.Go(func() {
		for {
			conn, err := r.listener.Accept()
			if err != nil {
				return
			}
			if !r.track(conn, unrouted) {
				reset(conn)
				continue
			}
			r.served.Go(func() {
				defer r.untrack(conn)
				handle(conn)
			})
		}
	})
}

// serveIngress forwards every connection, byte for byte, to the instance
// listening at target. The instance therefore sees the Host its base URL
// names.
func (r *relay) serveIngress(target string) {
	r.serve(func(conn net.Conn) {
		upstream, ok := r.dial(conn, target, unrouted)
		if !ok {
			return
		}
		defer r.untrack(upstream)
		pipe(conn, conn, upstream)
	})
}

// serveEgress is an HTTP forward proxy that reaches only the addresses
// route knows, the instances' ingresses, and refuses the instances cut
// from it. It serves both forms the instance's clients send: the peer
// client CONNECTs to every destination, and the remote client sends an
// absolute-form request for a plain HTTP destination named by IP address.
func (r *relay) serveEgress(route func(addr string) (int, bool)) {
	r.serve(func(conn net.Conn) {
		_ = conn.SetReadDeadline(time.Now().Add(requestHeadTimeout))
		head := bufio.NewReader(conn)
		req, err := http.ReadRequest(head)
		if err != nil {
			reset(conn)
			return
		}
		_ = conn.SetReadDeadline(time.Time{})
		to, ok := route(req.URL.Host)
		if !ok {
			// Not an instance: the cluster's instances reach nothing else
			// through their egress.
			_, _ = io.WriteString(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
			return
		}
		if !r.track(conn, to) {
			reset(conn)
			return
		}
		upstream, ok := r.dial(conn, req.URL.Host, to)
		if !ok {
			return
		}
		defer r.untrack(upstream)
		if req.Method == http.MethodConnect {
			if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
				reset(conn)
				return
			}
			pipe(conn, head, upstream)
			return
		}
		// One request per proxy connection: Go's transport reuses a
		// connection to a proxy for any plain HTTP destination, and a
		// connection that could carry requests to several instances could
		// not be cut for one of them. Connection: close makes the ingress's
		// instance answer with it, and the client then opens a new one.
		req.Close = true
		req.Header.Del("Proxy-Connection")
		req.Header.Del("Proxy-Authorization")
		if err := req.Write(upstream); err != nil {
			reset(conn)
			return
		}
		_, _ = io.Copy(conn, upstream)
	})
}

// dial connects to target for conn, which leads to instance to, and tracks
// the new connection. When the dial fails, or the relay was cut from to or
// closed meanwhile, it resets conn and reports false.
func (r *relay) dial(conn net.Conn, target string, to int) (net.Conn, bool) {
	upstream, err := net.DialTimeout("tcp", target, dialTimeout)
	if err != nil {
		reset(conn)
		return nil, false
	}
	if !r.track(upstream, to) {
		reset(upstream)
		reset(conn)
		return nil, false
	}
	return upstream, true
}

// setCut cuts the relay from instance to, resetting every connection it
// carries there and refusing new ones, or lifts that cut.
func (r *relay) setCut(to int, cut bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !cut {
		delete(r.cut, to)
		return
	}
	r.cut[to] = true
	for conn, dest := range r.conns {
		if dest == to {
			reset(conn)
			delete(r.conns, conn)
		}
	}
}

// track records conn as leading to instance to, or moves it there, so a cut
// or close can reset it, and reports false when the relay is closed or cut
// from to.
func (r *relay) track(conn net.Conn, to int) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.cut[to] {
		return false
	}
	r.conns[conn] = to
	return true
}

func (r *relay) untrack(conn net.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.conns[conn]; ok {
		delete(r.conns, conn)
		_ = conn.Close()
	}
}

// close stops accepting, resets what the relay carries and waits for its
// goroutines, so a closed relay holds no port and no connection.
func (r *relay) close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	for conn := range r.conns {
		reset(conn)
		delete(r.conns, conn)
	}
	r.mu.Unlock()
	_ = r.listener.Close()
	r.served.Wait()
}

// pipe copies conn's bytes, read through in, to upstream and upstream's back
// to conn until both directions end.
func pipe(conn net.Conn, in io.Reader, upstream net.Conn) {
	copied := make(chan struct{}, 2)
	half := func(dst net.Conn, src io.Reader) {
		_, _ = io.Copy(dst, src)
		// Half-close keeps HTTP's request/response exchange intact when one
		// side finishes writing first.
		if tcp, ok := dst.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		copied <- struct{}{}
	}
	go half(upstream, in)
	go half(conn, upstream)
	<-copied
	<-copied
}

// reset closes conn with an RST rather than a FIN, as a cut network path
// looks to the peer that next writes. A reset rather than silently dropped
// traffic makes the instance's call fail at once, so a scenario needs no
// client timeout to see a partition.
func reset(conn net.Conn) {
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
}
