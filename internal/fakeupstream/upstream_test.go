package fakeupstream_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func request(
	t *testing.T,
	client *http.Client,
	method, address string,
	header http.Header,
) *http.Response {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), method, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header = header.Clone()
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func body(t *testing.T, response *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return string(data)
}

func TestAccResources(t *testing.T) {
	t.Parallel()
	clock := fakeupstream.NewClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	s := fakeupstream.New(t, fakeupstream.Options{Now: clock.Now})
	resource := fakeupstream.Resource{
		Body: []byte("0123456789"),
		Header: http.Header{
			"Content-Type":  {"application/octet-stream"},
			"Cache-Control": {"max-age=60"},
		},
	}
	s.Set("/artifact.bin", resource)
	resource.Body[0] = 'x'
	resource.Header.Set("Content-Type", "mutated")
	first := request(t, s.Client(), "GET", s.URL+"/artifact.bin", nil)
	if first.StatusCode != 200 || body(t, first) != "0123456789" ||
		first.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("first response: %+v", first)
	}
	etag := first.Header.Get("ETag")
	for _, tc := range []struct {
		name, method string
		header       http.Header
		status       int
		content      string
	}{
		{"head", "HEAD", nil, 200, ""},
		{"range", "GET", http.Header{"Range": {"bytes=2-5"}}, 206, "2345"},
		{"suffix", "GET", http.Header{"Range": {"bytes=-3"}}, 206, "789"},
		{"etag", "GET", http.Header{"If-None-Match": {etag}}, 304, ""},
		{"modified", "GET", http.Header{"If-Modified-Since": {clock.Now().Format(http.TimeFormat)}}, 304, ""},
		{"ifrange", "GET", http.Header{"Range": {"bytes=2-5"}, "If-Range": {etag}}, 206, "2345"},
		{"stale-ifrange", "GET", http.Header{"Range": {"bytes=2-5"}, "If-Range": {`"old"`}}, 200, "0123456789"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := request(t, s.Client(), tc.method, s.URL+"/artifact.bin", tc.header)
			if got := body(t, response); response.StatusCode != tc.status || got != tc.content {
				t.Fatalf("got %d %q, want %d %q", response.StatusCode, got, tc.status, tc.content)
			}
		})
	}
	if s.Count("HEAD", "/artifact.bin") != 1 || s.Count("", "/artifact.bin") != 8 {
		t.Fatalf("counts: %+v", s.Requests())
	}
	clock.Advance(61 * time.Second)
	s.Set("/artifact.bin", fakeupstream.Resource{Body: []byte("replacement")})
	next := request(
		t,
		s.Client(),
		"GET",
		s.URL+"/artifact.bin",
		http.Header{"If-None-Match": {etag}},
	)
	if next.StatusCode != 200 || body(t, next) != "replacement" ||
		next.Header.Get("ETag") == etag ||
		next.Header.Get("Last-Modified") != clock.Now().Format(http.TimeFormat) {
		t.Fatalf("replacement: %+v", next)
	}
	if next.Header.Get("Date") != clock.Now().Format(http.TimeFormat) {
		t.Fatal("server ignored clock")
	}
	s.Delete("/artifact.bin")
	if response := request(
		t,
		s.Client(),
		"GET",
		s.URL+"/artifact.bin",
		nil,
	); response.StatusCode != 404 {
		t.Fatal(response.StatusCode)
	}
	if response := request(
		t,
		s.Client(),
		"PUT",
		s.URL+"/artifact.bin",
		nil,
	); response.StatusCode != 405 ||
		response.Header.Get("Allow") != "GET, HEAD" {
		t.Fatal(response.StatusCode)
	}
	if s.Count("", "") != 11 {
		t.Fatal(s.Count("", ""))
	}
}

func TestAccFaults(t *testing.T) {
	t.Parallel()
	for _, code := range []int{401, 403, 404, 429, 500, 502, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			s := fakeupstream.New(t, fakeupstream.Options{})
			s.Set(
				"/metadata.json",
				fakeupstream.Resource{
					Status: code,
					Body:   []byte(`{"error":"fixture"}`),
					Header: http.Header{"Retry-After": {"3"}},
				},
			)
			r := request(t, s.Client(), "GET", s.URL+"/metadata.json", nil)
			if r.StatusCode != code || body(t, r) != `{"error":"fixture"}` ||
				r.Header.Get("Retry-After") != "3" {
				t.Fatalf("response %+v", r)
			}
			r = request(t, s.Client(), "HEAD", s.URL+"/metadata.json", nil)
			if r.StatusCode != code || body(t, r) != "" || r.ContentLength != 19 {
				t.Fatalf("head %+v", r)
			}
			if s.Count("", "/metadata.json") != 2 {
				t.Fatal(s.Requests())
			}
		})
	}
	for _, tc := range []struct {
		name  string
		fault fakeupstream.Fault
		want  string
	}{
		{"truncated", fakeupstream.Fault{TruncateAfter: 3}, "012"},
		{"long-length", fakeupstream.Fault{LengthDelta: 1}, "0123456789"},
		{"short-length", fakeupstream.Fault{LengthDelta: -1}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fakeupstream.New(t, fakeupstream.Options{})
			s.Set("/artifact", fakeupstream.Resource{Body: []byte("0123456789"), Fault: tc.fault})
			r := request(t, s.Client(), "GET", s.URL+"/artifact", nil)
			got, err := io.ReadAll(r.Body)
			if !errors.Is(err, io.ErrUnexpectedEOF) || string(got) != tc.want {
				t.Fatalf("got %q %v", got, err)
			}
			if s.Count("GET", "/artifact") != 1 {
				t.Fatal(s.Requests())
			}
		})
	}
	t.Run("dropped", func(t *testing.T) {
		s := fakeupstream.New(t, fakeupstream.Options{})
		s.Set("/artifact", fakeupstream.Resource{Fault: fakeupstream.Fault{DropConnection: true}})
		r, _ := http.NewRequestWithContext(t.Context(), "GET", s.URL+"/artifact", nil)
		response, err := s.Client().Do(r)
		if response != nil {
			response.Body.Close()
		}
		if err == nil || s.Count("GET", "/artifact") != 1 {
			t.Fatalf("drop error %v, count %d", err, s.Count("GET", "/artifact"))
		}
	})
	t.Run("bad-content", func(t *testing.T) {
		s := fakeupstream.New(t, fakeupstream.Options{})
		s.Set(
			"/metadata.json",
			fakeupstream.Resource{
				Body: []byte("{broken"),
				Header: http.Header{
					"Content-Type":      {"text/html"},
					"X-Checksum-Sha256": {"incorrect"},
				},
			},
		)
		r := request(t, s.Client(), "GET", s.URL+"/metadata.json", nil)
		got := body(t, r)
		if json.Valid([]byte(got)) || r.Header.Get("Content-Type") != "text/html" ||
			r.Header.Get("X-Checksum-Sha256") == fmt.Sprintf("%x", sha256.Sum256([]byte(got))) {
			t.Fatal("fault fixture became valid")
		}
		if s.Count("GET", "/metadata.json") != 1 {
			t.Fatal(s.Requests())
		}
	})
}

func TestAccAuthenticationRecordingAndCookies(t *testing.T) {
	t.Parallel()
	for _, policy := range []fakeupstream.Auth{{Username: "reader", Password: "basic-secret"}, {Token: "bearer-secret"}} {
		t.Run(fmt.Sprint(policy.Token != ""), func(t *testing.T) {
			s := fakeupstream.New(
				t,
				fakeupstream.Options{
					Auth:    policy,
					Secrets: []string{"path-secret", "query-key-secret"},
				},
			)
			s.Set(
				"/path-secret",
				fakeupstream.Resource{
					Body:   []byte("ok"),
					Header: http.Header{"Set-Cookie": {"session=cookie-secret; Path=/"}},
				},
			)
			denied := request(t, s.Client(), "GET", s.URL+"/path-secret", nil)
			if denied.StatusCode != 401 || denied.Header.Get("WWW-Authenticate") == "" {
				t.Fatal(denied.StatusCode)
			}
			r, _ := http.NewRequestWithContext(
				t.Context(),
				"GET",
				s.URL+"/path-secret?token=query-secret",
				nil,
			)
			if policy.Token != "" {
				r.Header.Set("Authorization", "Bearer "+policy.Token)
			} else {
				r.SetBasicAuth(policy.Username, policy.Password)
			}
			r.Header.Set("X-Api-Key", "header-secret")
			r.Header.Set("Range", "bytes=0-1")
			client := *s.Client()
			client.Jar, _ = cookiejar.New(nil)
			response, err := client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			if body(t, response) != "ok" {
				t.Fatal(response.StatusCode)
			}
			response, err = client.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			body(t, response)
			records := s.Requests()
			last := records[2]
			if !last.Authorized || records[0].Authorized ||
				!last.HeaderMatches("authorization", r.Header.Get("Authorization")) ||
				!last.HeaderMatches("Cookie", "session=cookie-secret") ||
				last.HeaderMatches("Authorization", "wrong") ||
				last.HeaderMatches("Absent", "") {
				t.Fatalf("record %+v", last)
			}
			if last.Header.Get("Range") != "bytes=0-1" ||
				last.Header.Get("Cookie") != fakeupstream.Redacted ||
				last.Query.Get("token") != fakeupstream.Redacted {
				t.Fatalf("record %+v", last)
			}
			printed := fmt.Sprintf("%+v", records)
			for _, secret := range []string{"basic-secret", "bearer-secret", "path-secret", "query-secret", "header-secret", "cookie-secret"} {
				if strings.Contains(printed, secret) {
					t.Fatalf("record leaked %s", secret)
				}
			}
			last.Header.Set("Range", "modified")
			last.Query.Set("token", "modified")
			if s.Requests()[2].Header.Get("Range") != "bytes=0-1" ||
				s.Requests()[2].Query.Get("token") != fakeupstream.Redacted ||
				s.Count("GET", "/path-secret") != 3 {
				t.Fatal("snapshot changed recorder")
			}
		})
	}
	if (fakeupstream.Request{}).HeaderMatches("x", "y") {
		t.Fatal("empty request matches")
	}
}

func TestAccRedirectAndTLS(t *testing.T) {
	t.Parallel()
	destination := fakeupstream.New(
		t,
		fakeupstream.Options{TLS: &tls.Config{MinVersion: tls.VersionTLS12}},
	)
	destination.Set("/artifact", fakeupstream.Resource{Body: []byte("tls bytes")})
	source := fakeupstream.New(t, fakeupstream.Options{})
	source.Set("/redirect", fakeupstream.Resource{Redirect: destination.URL + "/artifact"})
	if source.Certificate() != nil || destination.Certificate() == nil {
		t.Fatal("certificate state")
	}
	r := request(t, destination.Client(), "GET", source.URL+"/redirect", nil)
	if body(t, r) != "tls bytes" || source.Count("GET", "/redirect") != 1 ||
		destination.Count("GET", "/artifact") != 1 {
		t.Fatal("redirect counts")
	}
	source.Set(
		"/redirect",
		fakeupstream.Resource{Status: 307, Redirect: destination.URL + "/artifact"},
	)
	client := *source.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	r = request(t, &client, "HEAD", source.URL+"/redirect", nil)
	if r.StatusCode != 307 || r.Header.Get("Location") != destination.URL+"/artifact" {
		t.Fatal(r.StatusCode)
	}
	identity, pool := fakeupstream.ClientIdentity(t)
	mtls := fakeupstream.New(
		t,
		fakeupstream.Options{
			TLS: &tls.Config{
				MinVersion: tls.VersionTLS12,
				ClientAuth: tls.RequireAndVerifyClientCert,
				ClientCAs:  pool,
			},
		},
	)
	mtls.Set("/secure", fakeupstream.Resource{Body: []byte("client verified")})
	tr := mtls.Client().Transport.(*http.Transport).Clone()
	tr.TLSClientConfig.Certificates = []tls.Certificate{identity}
	t.Cleanup(tr.CloseIdleConnections)
	r = request(t, &http.Client{Transport: tr}, "GET", mtls.URL+"/secure", nil)
	if body(t, r) != "client verified" || !mtls.Requests()[0].VerifiedClientCertificate {
		t.Fatal("client not verified")
	}
	for _, certs := range [][]tls.Certificate{nil, {}} {
		bad := mtls.Client().Transport.(*http.Transport).Clone()
		if certs != nil {
			untrusted, _ := fakeupstream.ClientIdentity(t)
			certs = []tls.Certificate{untrusted}
		}
		bad.TLSClientConfig.Certificates = certs
		t.Cleanup(bad.CloseIdleConnections)
		req, _ := http.NewRequestWithContext(t.Context(), "GET", mtls.URL+"/secure", nil)
		response, err := (&http.Client{Transport: bad}).Do(req)
		if response != nil {
			response.Body.Close()
		}
		if err == nil {
			t.Fatal("mTLS accepted missing/untrusted client")
		}
	}
	if mtls.Count("", "") != 1 {
		t.Fatal("handshake rejection reached HTTP")
	}
}

func TestAccBarriersAndConcurrentMutation(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"headers", "body"} {
		t.Run(stage, func(t *testing.T) {
			s := fakeupstream.New(t, fakeupstream.Options{})
			gate := new(fakeupstream.Barrier)
			fault := fakeupstream.Fault{BeforeHeaders: gate}
			if stage == "body" {
				fault = fakeupstream.Fault{BeforeBody: gate}
			}
			s.Set(
				"/metadata.json",
				fakeupstream.Resource{Body: []byte(`{"version":1}`), Fault: fault},
			)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				req, _ := http.NewRequestWithContext(ctx, "GET", s.URL+"/metadata.json", nil)
				response, err := s.Client().Do(req)
				if err == nil {
					_, err = io.Copy(io.Discard, response.Body)
					response.Body.Close()
				}
				done <- err
			}()
			<-gate.Reached()
			if s.Count("GET", "/metadata.json") != 1 {
				t.Fatal(s.Requests())
			}
			cancel()
			if err := <-done; err == nil {
				t.Fatal("cancelled request succeeded")
			}
			s.Close()
			s.Close()
		})
	}
	clock := fakeupstream.NewClock(time.Now())
	s := fakeupstream.New(t, fakeupstream.Options{Now: clock.Now})
	gate := new(fakeupstream.Barrier)
	s.Set(
		"/artifact",
		fakeupstream.Resource{Body: []byte("old"), Fault: fakeupstream.Fault{BeforeHeaders: gate}},
	)
	const n = 16
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			r, err := http.NewRequestWithContext(t.Context(), "GET", s.URL+"/artifact", nil)
			if err != nil {
				t.Error(err)
				return
			}
			response, err := s.Client().Do(r)
			if err != nil {
				t.Error(err)
				return
			}
			defer response.Body.Close()
			got, err := io.ReadAll(response.Body)
			if err != nil || (string(got) != "old" && string(got) != "new") {
				t.Errorf("snapshot %q %v", got, err)
			}
		})
	}
	<-gate.Reached()
	clock.Advance(time.Hour)
	s.Set("/artifact", fakeupstream.Resource{Body: []byte("new")})
	gate.Release()
	gate.Release()
	wg.Wait()
	if s.Count("GET", "/artifact") != n {
		t.Fatal(s.Count("", ""))
	}
	if err := gate.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := new(fakeupstream.Barrier).Wait(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAccMutableArtifactAndMetadata(t *testing.T) {
	t.Parallel()
	for _, fixture := range []struct{ path, contentType, old, next string }{
		{"/artifacts/file.bin", "application/octet-stream", "first bytes", "updated bytes"},
		{"/registry/index.json", "application/json", `{"versions":["1"]}`, `{"versions":["1","2"]}`},
	} {
		t.Run(fixture.path, func(t *testing.T) {
			clock := fakeupstream.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			upstream := fakeupstream.New(t, fakeupstream.Options{Now: clock.Now})
			resource := fakeupstream.Resource{Body: []byte(fixture.old), Header: http.Header{
				"Content-Type": {
					fixture.contentType,
				},
				"Cache-Control": {"max-age=60"},
				"Expires":       {clock.Now().Add(time.Minute).Format(http.TimeFormat)},
			}}
			upstream.Set(fixture.path, resource)
			first := request(t, upstream.Client(), "GET", upstream.URL+fixture.path, nil)
			if body(t, first) != fixture.old {
				t.Fatal("initial fixture bytes")
			}
			clock.Advance(61 * time.Second)
			expires, err := http.ParseTime(first.Header.Get("Expires"))
			if err != nil || !clock.Now().After(expires) {
				t.Fatal("fixture did not expire")
			}
			resource.Body = []byte(fixture.next)
			gate := new(fakeupstream.Barrier)
			resource.Fault = fakeupstream.Fault{BeforeBody: gate, TruncateAfter: 3}
			upstream.Set(fixture.path, resource)
			interrupted := request(
				t,
				upstream.Client(),
				"GET",
				upstream.URL+fixture.path,
				http.Header{"If-None-Match": {first.Header.Get("ETag")}},
			)
			<-gate.Reached()
			gate.Release()
			data, err := io.ReadAll(interrupted.Body)
			interrupted.Body.Close()
			if !errors.Is(err, io.ErrUnexpectedEOF) || string(data) != fixture.next[:3] {
				t.Fatalf("interruption %q %v", data, err)
			}
			resource.Fault = fakeupstream.Fault{}
			upstream.Set(fixture.path, resource)
			retried := request(t, upstream.Client(), "GET", upstream.URL+fixture.path, nil)
			if body(t, retried) != fixture.next ||
				retried.Header.Get("Content-Type") != fixture.contentType {
				t.Fatal("retry did not read new fixture")
			}
			if upstream.Count("GET", fixture.path) != 3 {
				t.Fatal(upstream.Requests())
			}
		})
	}
}

// This checks fixture wiring through one application endpoint. The real remote
// adapter acceptance must use artifactweave here; this is not its implementation.
func TestAccApplicationEndpointOnly(t *testing.T) {
	t.Parallel()
	upstream := fakeupstream.New(t, fakeupstream.Options{})
	upstream.Set("/file.bin", fakeupstream.Resource{Body: []byte("artifact bytes")})
	upstream.Set(
		"/index.json",
		fakeupstream.Resource{
			Body:   []byte(`{"versions":["1.0.0"]}`),
			Header: http.Header{"Content-Type": {"application/json"}},
		},
	)
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Handle("/*", httputil.NewSingleHostReverseProxy(target))
	application := httptest.NewServer(router)
	t.Cleanup(application.Close)
	for _, path := range []string{"/file.bin", "/index.json"} {
		response := request(t, application.Client(), "GET", application.URL+path, nil)
		if body(t, response) == "" ||
			response.Request.URL.Host != strings.TrimPrefix(application.URL, "http://") {
			t.Fatal("client escaped application endpoint")
		}
		if upstream.Count("GET", path) != 1 {
			t.Fatal("upstream request hidden by fallback or client cache")
		}
	}
	if upstream.Count("", "") != 2 {
		t.Fatal(upstream.Requests())
	}
}

func TestAccRedactedPathsKeepIndependentCounts(t *testing.T) {
	t.Parallel()
	s := fakeupstream.New(t, fakeupstream.Options{Secrets: []string{"secret-one", "secret-two"}})
	for _, path := range []string{"/secret-one", "/secret-two"} {
		s.Set(path, fakeupstream.Resource{})
		body(t, request(t, s.Client(), "GET", s.URL+path, nil))
		if s.Count("GET", path) != 1 {
			t.Fatal("redaction merged distinct request counters")
		}
	}
	records := s.Requests()
	if records[0].Path != records[1].Path || s.Count("GET", "/secret-one") != 1 {
		t.Fatal("redacted counters not independent")
	}
}
