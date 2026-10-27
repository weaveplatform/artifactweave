package remote

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func ptr[T any](v T) *T { return &v }

func clientFor(t *testing.T, c repo.Config, opts Options) *Client {
	t.Helper()
	c.Key = "remote"
	client, err := NewClient(c, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func body(t *testing.T, res *http.Response) string {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAccFetchAndValidators(t *testing.T) {
	s := fakeupstream.New(t, fakeupstream.Options{})
	s.Set("/root/item", fakeupstream.Resource{Body: []byte("package"), ETag: `"v1"`})
	c := clientFor(t, repo.Config{URL: s.URL + "/root"}, Options{})
	res, err := c.Fetch(t.Context(), Request{Path: "item"})
	if err != nil {
		t.Fatal(err)
	}
	if got := body(t, res); got != "package" {
		t.Fatal(got)
	}
	if s.Count("HEAD", "") != 1 || s.Count("GET", "") != 1 {
		t.Fatal(s.Requests())
	}
	res, err = c.Fetch(
		t.Context(),
		Request{Path: "item", Header: http.Header{"If-None-Match": {`"v1"`}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 304 || body(t, res) != "" || s.Count("GET", "") != 1 {
		t.Fatal("conditional HEAD must not fetch a body")
	}
	res, err = c.Fetch(t.Context(), Request{Path: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 404 {
		t.Fatal(res.StatusCode)
	}
	res.Body.Close()
	if s.Count("GET", "/root/missing") != 0 {
		t.Fatal("HEAD miss fetched")
	}
}

func TestAccBypassCallerHEADAndFallback(t *testing.T) {
	for _, bypass := range []bool{false, true} {
		for _, method := range []string{"GET", "HEAD"} {
			t.Run(
				method+"/"+map[bool]string{true: "bypass", false: "probe"}[bypass],
				func(t *testing.T) {
					s := fakeupstream.New(t, fakeupstream.Options{})
					s.Set("/item", fakeupstream.Resource{Body: []byte("ok")})
					c := clientFor(
						t,
						repo.Config{URL: s.URL, BypassHeadRequests: ptr(bypass)},
						Options{},
					)
					res, err := c.Fetch(t.Context(), Request{Method: method, Path: "item"})
					if err != nil {
						t.Fatal(err)
					}
					body(t, res)
					wantHead, wantGet := 1, 1
					if bypass {
						wantHead = 0
					}
					if method == "HEAD" {
						wantHead = 1
						wantGet = 0
					}
					if s.Count("HEAD", "") != wantHead || s.Count("GET", "") != wantGet {
						t.Fatal(s.Requests())
					}
				},
			)
		}
	}
	for _, status := range []int{405, 501, 403} {
		s := fakeupstream.New(t, fakeupstream.Options{})
		s.Set("/item", fakeupstream.Resource{Status: status})
		c := clientFor(t, repo.Config{URL: s.URL}, Options{})
		res, err := c.Fetch(t.Context(), Request{Path: "item"})
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		want := 1
		if status == 403 {
			want = 0
		}
		if s.Count("GET", "") != want {
			t.Fatal(status, s.Requests())
		}
	}
}

func TestUnitURLPreservation(t *testing.T) {
	base, _ := baseURL("https://example.test/root?sig=a%2Fb+z&x=1")
	for _, tc := range []struct {
		path, query, expected string
		raw, propagate        bool
	}{
		{"@scope%2fpkg/a%20b", "token=X%2fY+Z", "https://example.test/root/@scope%2fpkg/a%20b?sig=a%2Fb+z&x=1&extra=%2B&token=X%2fY+Z", false, true},
		{"a//b", "drop=1", "https://example.test/root/a/b?sig=a%2Fb+z&x=1&extra=%2B", false, false},
		{"a//b", "", "https://example.test/root/a//b?sig=a%2Fb+z&x=1&extra=%2B", true, false},
	} {
		u, err := requestURL(
			repo.Config{
				QueryParams:             "extra=%2B",
				DisableURLNormalization: ptr(tc.raw),
				PropagateQueryParams:    ptr(tc.propagate),
			},
			base,
			tc.path,
			tc.query,
		)
		if err != nil || u.String() != tc.expected {
			t.Fatalf("URL mismatch: %v %v", u, err)
		}
	}
	for _, path := range []string{"../x", "%2e%2e/x", "a%2f..%2fx", "/x", "//host", "a?x", "a#x", "a\\x", "%00", "%xx"} {
		if _, err := requestURL(repo.Config{}, base, path, ""); !errors.Is(err, ErrURL) {
			t.Fatal(path, err)
		}
	}
	for _, raw := range []string{"file:///tmp/x", "http://", "https://u:p@example.test", "https://example.test/#f", "://"} {
		if _, err := baseURL(raw); !errors.Is(err, ErrURL) {
			t.Fatal(raw, err)
		}
	}
}

func TestUnitP2MetadataFileBaseURLNormalizesOnlyP2(t *testing.T) {
	for _, tc := range []struct {
		format   repo.PackageType
		base     string
		expected string
	}{
		{repo.P2, "https://eclipse.example/site/content.jar", "https://eclipse.example/site/p2.index"},
		{repo.P2, "https://eclipse.example/site/p2.index", "https://eclipse.example/site/p2.index"},
		{repo.P2, "https://eclipse.example/site/sub/content.xml", "https://eclipse.example/site/sub/p2.index"},
		{repo.Generic, "https://eclipse.example/site/content.jar", "https://eclipse.example/site/content.jar/p2.index"},
	} {
		base, err := baseURL(tc.base)
		if err != nil {
			t.Fatal(err)
		}
		u, err := requestURL(repo.Config{PackageType: tc.format}, base, "p2.index", "")
		if err != nil || u.String() != tc.expected {
			t.Errorf("%s %s: URL=%v, error=%v, want %s", tc.format, tc.base, u, err, tc.expected)
		}
	}
	base, err := baseURL("https://eclipse.example/site/content.jar")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := requestURL(
		repo.Config{PackageType: repo.P2, QueryParams: "token=ok#fragment"},
		base,
		"p2.index",
		"",
	); !errors.Is(
		err,
		ErrURL,
	) {
		t.Errorf("P2 base URL accepted a query fragment: %v", err)
	}
	signed, err := baseURL("https://eclipse.example/site/content.jar?signature=a%2Fb")
	if err != nil {
		t.Fatal(err)
	}
	u, err := requestURL(repo.Config{PackageType: repo.P2}, signed, "p2.index", "")
	if err != nil || u.String() != "https://eclipse.example/site/p2.index?signature=a%2Fb" {
		t.Errorf("P2 metadata URL lost its signed query: %v, %v", u, err)
	}
}

func TestAccProxyTLSAndBind(t *testing.T) {
	for _, secure := range []bool{false, true} {
		opts := fakeupstream.Options{}
		if secure {
			opts.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		s := fakeupstream.New(t, opts)
		s.Set("/item", fakeupstream.Resource{Body: []byte("through proxy")})
		proxy := fakeupstream.NewProxy(t, fakeupstream.Auth{}, s)
		proxyURL, _ := url.Parse(proxy.URL)
		var tc *tls.Config
		if secure {
			tc = s.Client().Transport.(*http.Transport).TLSClientConfig
		}
		for _, disable := range []bool{false, true} {
			c := clientFor(
				t,
				repo.Config{URL: s.URL, DisableProxy: ptr(disable), LocalAddress: "127.0.0.1"},
				Options{ProxyURL: proxyURL, TLSConfig: tc},
			)
			res, err := c.Do(t.Context(), Request{Method: "GET", Path: "item"})
			if err != nil {
				t.Fatal(err)
			}
			if body(t, res) != "through proxy" {
				t.Fatal("body")
			}
		}
		if proxy.Count("", "") != 1 {
			t.Fatal(proxy.Requests())
		}
	}
}

func TestAccRedirectIsolationAndLoops(t *testing.T) {
	dest := fakeupstream.New(t, fakeupstream.Options{})
	dest.Set("/item", fakeupstream.Resource{Body: []byte("ok")})
	s := fakeupstream.New(t, fakeupstream.Options{})
	s.Set("/redirect", fakeupstream.Resource{Redirect: dest.URL + "/item"})
	s.Set("/loop", fakeupstream.Resource{Redirect: s.URL + "/loop"})
	c := clientFor(t, repo.Config{URL: s.URL}, Options{})
	res, err := c.Do(
		t.Context(),
		Request{
			Method: "GET",
			Path:   "redirect",
			Header: http.Header{
				"Authorization": {"Bearer secret"},
				"Cookie":        {"session=secret"},
				"X-Secret":      {"secret"},
				"Range":         {"bytes=0-1"},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	body(t, res)
	r := dest.Requests()[0]
	if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" ||
		r.Header.Get("X-Secret") != "" ||
		r.Header.Get("Range") == "" {
		t.Fatal(r)
	}
	_, err = c.Do(t.Context(), Request{Method: "GET", Path: "loop"})
	if !errors.Is(err, ErrRedirect) || s.Count("GET", "/loop") != 10 {
		t.Fatal(err, s.Count("GET", "/loop"))
	}
	c = clientFor(
		t,
		repo.Config{URL: s.URL},
		Options{CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirect }},
	)
	_, err = c.Do(t.Context(), Request{Method: "GET", Path: "redirect"})
	if !errors.Is(err, ErrRedirect) {
		t.Fatal(err)
	}
}

func TestAccCancellationTimeoutAndShortBody(t *testing.T) {
	s := fakeupstream.New(t, fakeupstream.Options{})
	barrier := new(fakeupstream.Barrier)
	s.Set(
		"/slow",
		fakeupstream.Resource{
			Body:  []byte("slow"),
			Fault: fakeupstream.Fault{BeforeHeaders: barrier},
		},
	)
	s.Set(
		"/short",
		fakeupstream.Resource{Body: []byte("payload"), Fault: fakeupstream.Fault{LengthDelta: 10}},
	)
	c := clientFor(t, repo.Config{URL: s.URL, SocketTimeoutMillis: ptr(40)}, Options{})
	_, err := c.Do(t.Context(), Request{Method: "GET", Path: "slow"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = c.Do(ctx, Request{Method: "GET", Path: "slow"})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Truncation is an independent failure mode. The deliberate 40 ms timeout
	// above can expire before this response arrives on a busy race-test runner.
	c = clientFor(t, repo.Config{URL: s.URL}, Options{})
	res, err := c.Do(t.Context(), Request{Method: "GET", Path: "short"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(res.Body)
	res.Body.Close()
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	barrier.Release()
}

func TestAccMetadataDeadlineAndBodyClose(t *testing.T) {
	s := fakeupstream.New(t, fakeupstream.Options{})
	barrier := new(fakeupstream.Barrier)
	s.Set(
		"/item",
		fakeupstream.Resource{Body: []byte("ok"), Fault: fakeupstream.Fault{BeforeBody: barrier}},
	)
	c := clientFor(t, repo.Config{URL: s.URL, MetadataRetrievalTimeoutSecs: ptr(1)}, Options{})
	res, err := c.Fetch(t.Context(), Request{Method: "GET", Path: "item", Metadata: true})
	if err != nil {
		t.Fatal(err)
	}
	if deadline, ok := res.Request.Context().Deadline(); !ok || time.Until(deadline) > time.Second {
		t.Fatal("metadata deadline missing")
	}
	res.Body.Close()
	if res.Request.Context().Err() != context.Canceled {
		t.Fatal("body close did not release metadata context")
	}
	barrier.Release()
}

func TestAccPoolChangesAndClose(t *testing.T) {
	p := new(Pool)
	c := repo.Config{Key: "r", URL: "https://example.test"}
	a, err := p.Client(c, Options{Revision: "1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.Client(c, Options{Revision: "1"})
	if err != nil || a != b {
		t.Fatal("not reused", err)
	}
	b, err = p.Client(c, Options{Revision: "2"})
	if err != nil || a == b {
		t.Fatal("revision not applied", err)
	}
	c.URL = "https://other.test"
	d, err := p.Client(c, Options{Revision: "2"})
	if err != nil || d == b {
		t.Fatal("URL not applied", err)
	}
	p.Remove("r")
	b, err = p.Client(c, Options{})
	if err != nil || d == b {
		t.Fatal("deletion not applied", err)
	}
	p.Close()
	p.Close()
	p.Remove("r")
	if _, err = p.Client(c, Options{}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestUnitInvalidSettingsAndSafeErrors(t *testing.T) {
	for _, c := range []repo.Config{{URL: "bad"}, {URL: "https://example.test", LocalAddress: "bad"}, {URL: "https://example.test", SocketTimeoutMillis: ptr(-1)}, {URL: "https://example.test", MetadataRetrievalTimeoutSecs: ptr(-1)}} {
		if _, err := NewClient(c, Options{}); err == nil {
			t.Fatal("accepted invalid config")
		}
	}
	proxy, _ := url.Parse("socks5://example.test")
	if _, err := NewClient(
		repo.Config{URL: "https://example.test"},
		Options{ProxyURL: proxy},
	); err == nil {
		t.Fatal("bad proxy")
	}
	c := clientFor(
		t,
		repo.Config{URL: "https://example.test"},
		Options{Prepare: func(*http.Request) error { return errors.New("signed-secret") }},
	)
	for _, r := range []Request{{Method: "POST"}, {Method: "GET", Path: "%"}, {Method: "GET", Path: "item"}} {
		_, err := c.Do(t.Context(), r)
		if err == nil || strings.Contains(err.Error(), "signed-secret") {
			t.Fatal(err)
		}
	}
	if !errors.Is(safeError(context.DeadlineExceeded), context.DeadlineExceeded) {
		t.Fatal("deadline classification")
	}
}

func TestAccUntrustedTLSAndHeaderBudget(t *testing.T) {
	s := fakeupstream.New(t, fakeupstream.Options{TLS: &tls.Config{MinVersion: tls.VersionTLS12}})
	s.Set("/item", fakeupstream.Resource{Body: []byte("ok")})
	c := clientFor(t, repo.Config{URL: s.URL}, Options{})
	_, err := c.Do(t.Context(), Request{Method: "GET", Path: "item"})
	if !errors.Is(err, ErrSecurity) {
		t.Fatalf("untrusted TLS must be classified as security, got %v", err)
	}
	s = fakeupstream.New(t, fakeupstream.Options{})
	s.Set(
		"/item",
		fakeupstream.Resource{
			Body:   []byte("ok"),
			Header: http.Header{"X-Large": {strings.Repeat("x", 4096)}},
		},
	)
	c = clientFor(t, repo.Config{URL: s.URL}, Options{MaxResponseHeaderBytes: 256})
	_, err = c.Fetch(t.Context(), Request{Path: "item", Metadata: true})
	if !errors.Is(err, ErrRequest) {
		t.Fatalf("oversized headers: %v", err)
	}
	p := new(Pool)
	defer p.Close()
	if _, err = p.Client(repo.Config{Key: "bad", URL: "file:///tmp"}, Options{}); err == nil {
		t.Fatal("invalid pooled client")
	}
}

func TestAccPrepareRedirectAndSettingsSnapshot(t *testing.T) {
	s := fakeupstream.New(t, fakeupstream.Options{})
	s.Set("/from", fakeupstream.Resource{Redirect: s.URL + "/to"})
	s.Set("/to", fakeupstream.Resource{Body: []byte("ok")})
	bypass := true
	c := clientFor(
		t,
		repo.Config{URL: s.URL, BypassHeadRequests: &bypass},
		Options{
			Prepare: func(r *http.Request) error { r.Header.Set("X-Configured", "value"); return nil },
		},
	)
	bypass = false
	res, err := c.Fetch(t.Context(), Request{Path: "from", Metadata: true})
	if err != nil {
		t.Fatal(err)
	}
	body(t, res)
	if s.Count("HEAD", "") != 0 || !s.Requests()[1].HeaderMatches("X-Configured", "value") {
		t.Fatal("settings changed or redirect preparation skipped")
	}
	for _, raw := range []string{"http://example.test", "ftp://example.test", "https://u:p@example.test"} {
		u, _ := url.Parse(raw)
		from, _ := http.NewRequestWithContext(t.Context(), "GET", "https://example.test", nil)
		to := &http.Request{URL: u, Header: make(http.Header)}
		if err := redirectPolicy(
			Options{},
		)(
			to,
			[]*http.Request{from},
		); !errors.Is(
			err,
			ErrRedirect,
		) {
			t.Fatal(raw, err)
		}
	}
}

func TestAccDroppedConnectionClassification(t *testing.T) {
	upstream := fakeupstream.New(t, fakeupstream.Options{})
	upstream.Set("/drop", fakeupstream.Resource{Fault: fakeupstream.Fault{DropConnection: true}})
	client := clientFor(t, repo.Config{URL: upstream.URL}, Options{})
	if _, err := client.Do(
		t.Context(),
		Request{Method: http.MethodGet, Path: "drop"},
	); !errors.Is(
		err,
		ErrConnection,
	) {
		t.Fatalf("dropped headers = %v", err)
	}
	if upstream.Count("GET", "/drop") == 0 {
		t.Fatal("drop fixture was not reached")
	}
	for _, cause := range []error{io.EOF, io.ErrUnexpectedEOF} {
		if !errors.Is(
			safeError(
				&url.Error{Op: "Get", URL: "https://upstream.invalid/?secret=token", Err: cause},
			),
			ErrConnection,
		) {
			t.Fatal("EOF lost connection classification")
		}
	}
}

func TestAccWrappedSocketTimeoutClassification(t *testing.T) {
	cause := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}
	wrapped := &url.Error{
		Op:  "Get",
		URL: "https://secret.invalid/?token=secret",
		Err: fmt.Errorf("read remote: %w", cause),
	}
	err := safeError(wrapped)
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}

func TestUnitAIEditorGalleryPOSTTransportIsBoundedAndCannotRedirect(t *testing.T) {
	type seen struct{ method, path, query, media, accept, body string }
	requests := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		requests <- seen{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Content-Type"), r.Header.Get("Accept"), string(b)}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()
	c := clientFor(
		t,
		repo.Config{URL: srv.URL + "/_apis/public/gallery", PackageType: repo.AIEditorExtensions},
		Options{},
	)
	valid := func() Request {
		return Request{
			Method: http.MethodPost, Path: "extensionquery",
			Body: []byte(`{"filters":[]}`), Metadata: true,
			Header: http.Header{"Accept": {"application/json;api-version=3.0-preview.1"}},
		}
	}
	response, err := c.Fetch(t.Context(), valid())
	if err != nil || body(t, response) != `{"results":[]}` {
		t.Fatalf("valid gallery POST: %v", err)
	}
	got := <-requests
	if got != (seen{
		http.MethodPost, "/_apis/public/gallery/extensionquery", "", "application/json",
		"application/json;api-version=3.0-preview.1", `{"filters":[]}`,
	}) {
		t.Fatalf("gallery request changed: %#v", got)
	}
	configured := clientFor(
		t,
		repo.Config{URL: srv.URL + "/_apis/public/gallery", PackageType: repo.AIEditorExtensions},
		Options{
			Prepare: func(r *http.Request) error {
				r.Header.Set("Accept", "text/plain")
				r.Header.Set("Content-Type", "text/plain")
				r.Header.Set("Authorization", "Bearer configured-token")
				return nil
			},
		},
	)
	response, err = configured.Fetch(t.Context(), valid())
	if err != nil {
		t.Fatal(err)
	}
	body(t, response)
	got = <-requests
	if got.accept != "application/json;api-version=3.0-preview.1" ||
		got.media != "application/json" {
		t.Fatalf("configured headers changed the gallery protocol: %#v", got)
	}
	for _, tc := range []struct {
		name   string
		change func(*Request)
	}{
		{"empty body", func(r *Request) { r.Body = nil }},
		{"large body", func(r *Request) { r.Body = []byte(strings.Repeat("x", 64<<10+1)) }},
		{"other path", func(r *Request) { r.Path = "admin/config" }},
		{"query", func(r *Request) { r.Query = "token=secret" }},
		{"external URL", func(r *Request) { r.ExternalURL = "https://example.org/" }},
		{"caller header", func(r *Request) { r.Header.Set("Authorization", "Bearer secret") }},
		{"wrong accept", func(r *Request) { r.Header.Set("Accept", "*/*") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := valid()
			tc.change(&r)
			if _, err := c.Fetch(t.Context(), r); !errors.Is(err, ErrRequest) {
				t.Fatalf("invalid gallery transport request admitted: %v", err)
			}
		})
	}
	other := clientFor(t, repo.Config{URL: srv.URL, PackageType: repo.NPM}, Options{})
	if _, err := other.Fetch(t.Context(), valid()); !errors.Is(err, ErrRequest) {
		t.Fatalf("other package type admitted POST: %v", err)
	}
	for _, base := range []string{srv.URL + "/_apis/public/gallery?secret=1"} {
		c := clientFor(t, repo.Config{URL: base, PackageType: repo.AIEditorExtensions}, Options{})
		if _, err := c.Fetch(t.Context(), valid()); !errors.Is(err, ErrRequest) {
			t.Fatalf("gallery base with query or fragment was admitted: %s: %v", base, err)
		}
	}
	select {
	case unexpected := <-requests:
		t.Fatalf("invalid request reached upstream: %#v", unexpected)
	default:
	}

	redirected := false
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/other" {
			redirected = true
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	redirectClient := clientFor(
		t,
		repo.Config{
			URL:         redirect.URL + "/_apis/public/gallery",
			PackageType: repo.AIEditorExtensions,
		},
		Options{},
	)
	if _, err := redirectClient.Fetch(t.Context(), valid()); err == nil || redirected {
		t.Fatalf("gallery POST followed redirect: err=%v redirected=%t", err, redirected)
	}
}
