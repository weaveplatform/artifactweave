package cdntest_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/pkg/cdn/cdntest"
)

// sasOrigin models storage behind Front Door: it serves one blob at
// /c/blob to a request whose query carries sig=good and an se (epoch
// seconds) still in the future, and refuses everything else with 403, as
// storage refuses a missing, tampered or expired SAS.
type sasOrigin struct {
	srv   *httptest.Server
	now   func() time.Time
	calls atomic.Int64
	mu    sync.Mutex
	auth  []string
}

var blobBody = bytes.Repeat([]byte("0123456789"), 100)

func newSASOrigin(t *testing.T, now func() time.Time) *sasOrigin {
	t.Helper()
	o := &sasOrigin{now: now}
	r := chi.NewRouter()
	r.Get("/c/blob", o.serve)
	r.Head("/c/blob", o.serve)
	o.srv = httptest.NewServer(r)
	t.Cleanup(o.srv.Close)
	return o
}

func (o *sasOrigin) serve(w http.ResponseWriter, r *http.Request) {
	o.calls.Add(1)
	o.mu.Lock()
	o.auth = append(o.auth, r.Header.Get("Authorization"))
	o.mu.Unlock()
	q := r.URL.Query()
	se, err := strconv.ParseInt(q.Get("se"), 10, 64)
	if q.Get("sig") != "good" || err != nil || !o.now().Before(time.Unix(se, 0)) {
		http.Error(w, "AuthenticationFailed", http.StatusForbidden)
		return
	}
	if v := q.Get("rscd"); v != "" {
		w.Header().Set("Content-Disposition", v)
	}
	if v := q.Get("rscc"); v != "" {
		w.Header().Set("Cache-Control", v)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(blobBody))
}

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func get(t *testing.T, method, u string, header map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func startFrontDoor(t *testing.T, c *clock, cacheTTL time.Duration) (*cdntest.Edge, *sasOrigin) {
	t.Helper()
	origin := newSASOrigin(t, c.now)
	edge, err := cdntest.StartFrontDoor(cdntest.FrontDoorOptions{
		Origin: origin.srv.URL + "/", CacheTTL: cacheTTL, Now: c.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = edge.Close() })
	return edge, origin
}

// TestFrontDoorPassesTheSASThrough checks the edge checks nothing itself:
// the origin accepts or refuses the SAS, and the client's Authorization
// header never reaches it.
func TestFrontDoorPassesTheSASThrough(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	edge, origin := startFrontDoor(t, c, 0)
	se := strconv.FormatInt(c.now().Add(time.Minute).Unix(), 10)
	signed := edge.URL + "/c/blob?se=" + se + "&sig=good&rscd=attachment"
	resp, body := get(t, http.MethodGet, signed, map[string]string{"Authorization": "Bearer x"})
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, blobBody) ||
		resp.Header.Get("Content-Disposition") != "attachment" {
		t.Fatalf("signed GET = %d %q", resp.StatusCode, resp.Header)
	}
	resp, _ = get(t, http.MethodGet, edge.URL+"/c/blob", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no SAS = %d, want the origin's 403", resp.StatusCode)
	}
	resp, _ = get(t, http.MethodGet, edge.URL+"/c/blob?se="+se+"&sig=tampered", nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("tampered SAS = %d", resp.StatusCode)
	}
	resp, body = get(t, http.MethodGet, signed, map[string]string{"Range": "bytes=10-19"})
	if resp.StatusCode != http.StatusPartialContent || string(body) != "0123456789" {
		t.Fatalf("ranged GET = %d %q", resp.StatusCode, body)
	}
	resp, body = get(t, http.MethodHead, signed, nil)
	if resp.StatusCode != http.StatusOK || len(body) != 0 {
		t.Fatalf("HEAD = %d, %d bytes", resp.StatusCode, len(body))
	}
	if origin.calls.Load() != 5 {
		t.Fatalf("origin answered %d requests, want all 5: nothing is cached", origin.calls.Load())
	}
	origin.mu.Lock()
	defer origin.mu.Unlock()
	for _, a := range origin.auth {
		if a != "" {
			t.Fatal("the edge forwarded an Authorization header")
		}
	}
	reqs := edge.Requests()
	if len(reqs) != 5 || !reqs[0].Authorization || reqs[0].Cached {
		t.Fatalf("requests %+v", reqs)
	}
}

// TestFrontDoorCachesOnTheFullURL shows the cache key includes the query: a
// cached blob reaches only a client holding the same SAS URL, and keeps
// reaching it after the SAS expires until the route's cache cap passes,
// the exposure decision 0029 bounds.
func TestFrontDoorCachesOnTheFullURL(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	edge, origin := startFrontDoor(t, c, 10*time.Minute)
	se := strconv.FormatInt(c.now().Add(time.Minute).Unix(), 10)
	signed := edge.URL + "/c/blob?se=" + se + "&sig=good"
	for range 2 {
		if resp, body := get(t, http.MethodGet, signed, nil); resp.StatusCode != http.StatusOK ||
			!bytes.Equal(body, blobBody) {
			t.Fatalf("GET = %d", resp.StatusCode)
		}
	}
	if origin.calls.Load() != 1 {
		t.Fatalf("origin answered %d, want 1: the second GET is a cache hit", origin.calls.Load())
	}
	if resp, body := get(t, http.MethodHead, signed, nil); resp.StatusCode != http.StatusOK ||
		len(body) != 0 {
		t.Fatalf("cached HEAD = %d, %d bytes", resp.StatusCode, len(body))
	}
	if resp, _ := get(
		t,
		http.MethodGet,
		edge.URL+"/c/blob",
		nil,
	); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("the path without its SAS = %d: the cache must key on the query", resp.StatusCode)
	}
	// Ranged and conditional requests skip the cache.
	if resp, _ := get(
		t,
		http.MethodGet,
		signed,
		map[string]string{"Range": "bytes=0-9"},
	); resp.StatusCode !=
		http.StatusPartialContent {
		t.Fatalf("ranged = %d", resp.StatusCode)
	}
	if origin.calls.Load() != 3 {
		t.Fatalf("origin answered %d, want 3", origin.calls.Load())
	}
	// Past the SAS's expiry the cached copy is still served; past the cap
	// the edge asks the origin again, which refuses the expired SAS.
	c.add(5 * time.Minute)
	if resp, _ := get(t, http.MethodGet, signed, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("within the cache cap after expiry = %d", resp.StatusCode)
	}
	c.add(6 * time.Minute)
	if resp, _ := get(t, http.MethodGet, signed, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("past the cache cap = %d, want the origin's 403", resp.StatusCode)
	}
	hits := 0
	for _, r := range edge.Requests() {
		if r.Cached {
			hits++
		}
	}
	if hits != 3 {
		t.Fatalf("%d cache hits, want 3", hits)
	}
}

func TestFrontDoorOriginErrors(t *testing.T) {
	if _, err := cdntest.StartFrontDoor(cdntest.FrontDoorOptions{}); err == nil {
		t.Fatal("an edge without an origin must not start")
	}
	if _, err := cdntest.StartFrontDoor(cdntest.FrontDoorOptions{
		Origin: "http://127.0.0.1:1", Host: "256.0.0.1",
	}); err == nil {
		t.Fatal("an address that cannot be listened on must fail")
	}
	// A closed origin answers 502.
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	edge, err := cdntest.StartFrontDoor(cdntest.FrontDoorOptions{Origin: dead.URL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = edge.Close() })
	if resp, _ := get(
		t,
		http.MethodGet,
		edge.URL+"/c/blob",
		nil,
	); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("dead origin = %d", resp.StatusCode)
	}
	// An origin URL the client cannot use answers 502 too.
	bad, err := cdntest.StartFrontDoor(cdntest.FrontDoorOptions{Origin: "http://[::1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bad.Close() })
	if resp, _ := get(
		t,
		http.MethodGet,
		bad.URL+"/c/blob",
		nil,
	); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("unparseable origin = %d", resp.StatusCode)
	}
}

// TestFrontDoorHonoursCacheControl checks the origin's max-age shortens the
// route's cap, and no-store or private keeps a response out of the cache.
func TestFrontDoorHonoursCacheControl(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	edge, origin := startFrontDoor(t, c, 10*time.Minute)
	se := strconv.FormatInt(c.now().Add(time.Hour).Unix(), 10)
	short := edge.URL + "/c/blob?se=" + se + "&sig=good&rscc=public%2C%20max-age%3D60"
	get(t, http.MethodGet, short, nil)
	c.add(30 * time.Second)
	get(t, http.MethodGet, short, nil)
	if origin.calls.Load() != 1 {
		t.Fatalf("within max-age: origin answered %d", origin.calls.Load())
	}
	c.add(31 * time.Second)
	get(t, http.MethodGet, short, nil)
	if origin.calls.Load() != 2 {
		t.Fatalf("past max-age: origin answered %d, want 2", origin.calls.Load())
	}
	for _, cc := range []string{"no-store", "private"} {
		u := edge.URL + "/c/blob?se=" + se + "&sig=good&rscc=" + cc
		before := origin.calls.Load()
		get(t, http.MethodGet, u, nil)
		get(t, http.MethodGet, u, nil)
		if origin.calls.Load()-before != 2 {
			t.Fatalf("%s was cached", cc)
		}
	}
}
