package cdntest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

// FrontDoorOptions configures StartFrontDoor.
type FrontDoorOptions struct {
	// Origin is the storage endpoint the edge forwards to, the account URL
	// without a trailing slash, for example Azurite's
	// "http://127.0.0.1:10000/devstoreaccount1". The request's path
	// ("/<container>/<blob name>") and query, the SAS, are appended.
	Origin string
	// CacheTTL is the route's cache expiry cap: the longest a whole-object
	// response stays cached under its full URL, query included, shortened by
	// the response's Cache-Control max-age. Zero means no caching.
	CacheTTL time.Duration
	// Host is the address the edge listens on; empty means "localhost".
	Host string
	// Now is the cache's clock; nil means time.Now.
	Now func() time.Time
	// Client forwards requests to the origin; nil means a plain client.
	Client *http.Client
}

// frontDoorEdge models Azure Front Door in front of a storage account with
// query_string_caching_behavior = UseQueryString (decision 0029). It checks
// nothing: the origin checks the SAS in the query. A whole-object GET the
// origin answers 200 is cached under the full request URI, so only a client
// holding the same SAS URL gets the cached copy, and keeps getting it for
// the response's max-age or the route's cap, whichever is shorter, even
// after the SAS has expired: the exposure decision 0029 bounds. A response
// marked no-store or private is not cached. Ranged and conditional requests
// always go to the origin; real Front Door caches in 8 MiB chunks, which is
// not modelled.
type frontDoorEdge struct {
	origin string
	ttl    time.Duration
	now    func() time.Time
	client *http.Client

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	header  http.Header
	body    []byte
	expires time.Time
}

// forwarded are the request headers the edge passes to the origin. Not
// Authorization: storage refuses a request carrying both a SAS and another
// credential, and a client never sends one to a CDN host.
var forwarded = []string{
	"Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since",
}

// hopByHop are the response headers that describe one connection, not the
// object, and are not cached or copied.
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Transfer-Encoding": true, "Date": true,
}

// StartFrontDoor listens and serves a fake Front Door until Close.
func StartFrontDoor(o FrontDoorOptions) (*Edge, error) {
	if o.Origin == "" {
		return nil, errors.New("cdntest: a Front Door edge needs an origin")
	}
	if o.Host == "" {
		o.Host = "localhost"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: time.Minute}
	}
	ln, err := (&net.ListenConfig{}).Listen(
		context.Background(),
		"tcp",
		net.JoinHostPort(o.Host, "0"),
	)
	if err != nil {
		return nil, fmt.Errorf("cdntest: listen: %w", err)
	}
	e := &Edge{
		done: make(chan struct{}),
		frontDoor: &frontDoorEdge{
			origin: strings.TrimSuffix(o.Origin, "/"), ttl: o.CacheTTL, now: o.Now,
			client: o.Client, cache: map[string]cached{},
		},
	}
	e.URL = "http://" + net.JoinHostPort(o.Host, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))
	r := chi.NewRouter()
	r.Get("/*", e.serve)
	r.Head("/*", e.serve)
	e.srv = &http.Server{Handler: r, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		defer close(e.done)
		_ = e.srv.Serve(ln)
	}()
	return e, nil
}

// answer serves r from the cache or the origin and reports whether the
// cache answered.
func (f *frontDoorEdge) answer(w http.ResponseWriter, r *http.Request) bool {
	key := r.URL.RequestURI()
	cacheable := f.ttl > 0 && r.Header.Get("Range") == "" &&
		r.Header.Get("If-None-Match") == "" && r.Header.Get("If-Modified-Since") == ""
	if cacheable {
		if c, ok := f.lookup(key); ok {
			copyHeader(w.Header(), c.header)
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				_, _ = w.Write(c.body)
			}
			return true
		}
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, f.origin+key, nil)
	if err != nil {
		http.Error(w, "origin: "+err.Error(), http.StatusBadGateway)
		return false
	}
	for _, h := range forwarded {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := f.client.Do(req)
	if err != nil {
		http.Error(w, "origin: "+err.Error(), http.StatusBadGateway)
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "origin: "+err.Error(), http.StatusBadGateway)
		return false
	}
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	if life := f.lifetime(resp.Header.Get("Cache-Control")); cacheable && life > 0 &&
		r.Method == http.MethodGet && resp.StatusCode == http.StatusOK {
		f.store(key, cached{header: resp.Header.Clone(), body: body}, life)
	}
	return false
}

// lifetime is how long a response with Cache-Control cc stays cached: the
// route's cap, shortened by max-age, and zero for no-store or private.
func (f *frontDoorEdge) lifetime(cc string) time.Duration {
	life := f.ttl
	for d := range strings.SplitSeq(cc, ",") {
		name, value, _ := strings.Cut(strings.TrimSpace(strings.ToLower(d)), "=")
		switch name {
		case "no-store", "private":
			return 0
		case "max-age":
			if sec, err := strconv.Atoi(value); err == nil {
				life = min(life, time.Duration(sec)*time.Second)
			}
		}
	}
	return life
}

func (f *frontDoorEdge) lookup(key string) (cached, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.cache[key]
	if ok && !f.now().Before(c.expires) {
		delete(f.cache, key)
		return cached{}, false
	}
	return c, ok
}

func (f *frontDoorEdge) store(key string, c cached, life time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c.expires = f.now().Add(life)
	f.cache[key] = c
}

func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		if !hopByHop[k] {
			dst[k] = append([]string(nil), vs...)
		}
	}
}
