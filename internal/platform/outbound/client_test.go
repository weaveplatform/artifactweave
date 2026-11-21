package outbound

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func testClient(t *testing.T, base, proxy *url.URL, roots *x509.CertPool, timeout time.Duration, lookup func(context.Context, string) ([]net.IPAddr, error)) *Client {
	t.Helper()
	origins := []string{Origin(base)}
	if proxy != nil {
		origins = append(origins, Origin(proxy))
	}
	policy, err := NewPolicy(origins, lookup)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{Base: base, Proxy: proxy, Policy: policy, RootCAs: roots, SocketTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func request(t *testing.T, method, raw, body string) *http.Request {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), method, raw, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.SetBasicAuth("replicator", "fixture-password")
	return r
}

func readResponse(t *testing.T) func(*http.Response, error) string {
	t.Helper()
	return func(response *http.Response, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatal("response read/close failed", readErr, closeErr)
		}
		return string(body)
	}
}

func TestAccFixedOriginClientPinsDNSAndNeverRedirectsWrites(t *testing.T) {
	var foreign atomic.Int32
	foreignRoutes := chi.NewRouter()
	foreignRoutes.HandleFunc("/*", func(w http.ResponseWriter, _ *http.Request) { foreign.Add(1); w.WriteHeader(http.StatusNoContent) })
	other := httptest.NewServer(foreignRoutes)
	t.Cleanup(other.Close)
	var puts, lookups atomic.Int32
	var authority string
	routes := chi.NewRouter()
	routes.Put("/item", func(w http.ResponseWriter, r *http.Request) {
		puts.Add(1)
		user, password, ok := r.BasicAuth()
		if !ok || user != "replicator" || password != "fixture-password" || r.Host != authority ||
			r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("logical host or authentication isolation changed")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		_, err = w.Write(body)
		if err != nil {
			t.Error(err)
		}
	})
	routes.Get("/item", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "read") })
	routes.HandleFunc("/same", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/item", http.StatusTemporaryRedirect)
	})
	routes.HandleFunc("/cross", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/leak", http.StatusTemporaryRedirect)
	})
	server := httptest.NewServer(routes)
	t.Cleanup(server.Close)
	base := parsed(t, server.URL)
	base.Host = net.JoinHostPort("replica.example", base.Port())
	authority = base.Host
	client := testClient(t, base, nil, nil, time.Second, func(context.Context, string) ([]net.IPAddr, error) {
		if lookups.Add(1) > 100 {
			return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	})
	if lookups.Load() != 0 {
		t.Fatal("client construction performed DNS")
	}
	upload := request(t, http.MethodPut, base.String()+"/item", "payload")
	upload.Host = "untrusted.example"
	upload.Header.Set("Proxy-Authorization", "proxy-secret")
	upload.Header.Set("Idempotency-Key", "automatic-replay-refused")
	if readResponse(t)(client.Do(upload)) != "payload" || upload.Host != "untrusted.example" || upload.Header.Get("Proxy-Authorization") != "proxy-secret" {
		t.Fatal("request ownership or streamed upload changed")
	}
	if readResponse(t)(client.Do(request(t, http.MethodGet, base.String()+"/same", ""))) != "read" {
		t.Fatal("same-origin read redirect failed")
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodPost} {
		for _, path := range []string{"same", "cross"} {
			response, err := client.Do(request(t, method, base.String()+"/"+path, "private-body"))
			if !errors.Is(err, ErrRedirect) || response != nil {
				t.Fatal("write redirect was not refused", method, path, err)
			}
		}
	}
	if _, err := client.Do(request(t, http.MethodGet, base.String()+"/cross", "")); !errors.Is(err, ErrRedirect) {
		t.Fatal("cross-origin read redirect accepted")
	}
	if foreign.Load() != 0 || puts.Load() != 1 {
		t.Fatal("redirect forwarded credentials/body or duplicated a write", foreign.Load(), puts.Load())
	}
	// Reject a newly forbidden DNS answer even if an earlier connection is idle.
	lookups.Store(100)
	if _, err := client.Do(request(t, http.MethodGet, base.String()+"/item", "")); !errors.Is(err, ErrSecurity) {
		t.Fatal("cached connection bypassed current destination policy", err)
	}
}

func TestAccFixedOriginClientDoesNotReplayLostWriteAcknowledgement(t *testing.T) {
	var commits atomic.Int32
	routes := chi.NewRouter()
	routes.Get("/warm", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "warm") })
	routes.Put("/commit", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
			return
		}
		commits.Add(1)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		if err := connection.Close(); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewServer(routes)
	t.Cleanup(server.Close)
	client := testClient(t, parsed(t, server.URL), nil, nil, time.Second, nil)
	readResponse(t)(client.Do(request(t, http.MethodGet, server.URL+"/warm", "")))
	upload := request(t, http.MethodPut, server.URL+"/commit?secret=signed", "body")
	upload.Header.Set("Idempotency-Key", "caller-key")
	response, err := client.Do(upload)
	if !errors.Is(err, ErrConnection) || response != nil || commits.Load() != 1 || strings.Contains(err.Error(), "secret") {
		t.Fatal("lost acknowledgement replayed or leaked endpoint data", commits.Load(), err)
	}
}

func TestAccFixedOriginClientTimeoutAndBodyErrorsRemainSafe(t *testing.T) {
	routes := chi.NewRouter()
	routes.Get("/blocked", func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	routes.Get("/truncated", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "20")
		_, _ = io.WriteString(w, "short")
	})
	server := httptest.NewServer(routes)
	t.Cleanup(server.Close)
	client := testClient(t, parsed(t, server.URL), nil, nil, 100*time.Millisecond, nil)
	if _, err := client.Do(
		request(t, http.MethodGet, server.URL+"/blocked?secret=signed", ""),
	); !errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(err.Error(), "secret") {
		t.Fatal("socket inactivity timeout leaked or lost classification", err)
	}
	response, err := client.Do(request(t, http.MethodGet, server.URL+"/truncated", ""))
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if !errors.Is(readErr, ErrConnection) || closeErr != nil {
		t.Fatal("truncated body lost its safe classification", readErr, closeErr)
	}
}

func TestAccFixedOriginClientUsesLogicalTLSIdentityAndCopiedTrust(t *testing.T) {
	var served atomic.Int32
	routes := chi.NewRouter()
	routes.Get("/item", func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		_, _ = io.WriteString(w, "authenticated TLS")
	})
	server := httptest.NewTLSServer(routes)
	t.Cleanup(server.Close)
	base := parsed(t, server.URL)
	if names := server.Certificate().DNSNames; len(names) > 0 {
		base.Host = net.JoinHostPort(names[0], base.Port())
	}
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	roots := x509.NewCertPool()
	untrusted := testClient(t, base, nil, roots, time.Second, lookup)
	// Mutating the caller's pool after construction cannot alter a live client.
	roots.AddCert(server.Certificate())
	if _, err := untrusted.Do(request(t, http.MethodGet, base.String()+"/item", "")); !errors.Is(err, ErrSecurity) {
		t.Fatal("client retained mutable trust settings", err)
	}
	trusted := testClient(t, base, nil, roots, time.Second, lookup)
	if readResponse(t)(trusted.Do(request(t, http.MethodGet, base.String()+"/item", ""))) != "authenticated TLS" {
		t.Fatal("logical TLS identity did not verify")
	}
	wrong := *base
	wrong.Host = net.JoinHostPort("wrong.invalid", base.Port())
	wrongIdentity := testClient(t, &wrong, nil, roots, time.Second, lookup)
	if _, err := wrongIdentity.Do(request(t, http.MethodGet, wrong.String()+"/item", "")); !errors.Is(err, ErrSecurity) || served.Load() != 1 {
		t.Fatal("checked IP bypassed logical TLS name verification", err, served.Load())
	}
}

func TestUnitFixedOriginClientBoundsDNSWithoutLeakingLookupContext(t *testing.T) {
	base := parsed(t, "https://slow-dns.example/item")
	client := testClient(t, base, nil, nil, 20*time.Millisecond, func(ctx context.Context, _ string) ([]net.IPAddr, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if _, err := client.Do(request(t, http.MethodGet, base.String(), "")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("configured timeout did not bound DNS", err)
	}
	if t.Context().Err() != nil {
		t.Fatal("lookup timeout cancelled the caller")
	}
}
