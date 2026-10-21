package fakeupstream_test

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
)

func proxyClient(t *testing.T, target, proxy *fakeupstream.Server, auth string) *http.Client {
	t.Helper()
	address, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		address.User = url.UserPassword("proxy-user", auth)
	}
	transport := target.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(address)
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

func TestProxy(t *testing.T) {
	t.Parallel()
	for _, tlsEnabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP", true: "CONNECT"}[tlsEnabled], func(t *testing.T) {
			opts := fakeupstream.Options{}
			if tlsEnabled {
				opts.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
			}
			target := fakeupstream.New(t, opts)
			target.Set("/artifact", fakeupstream.Resource{Body: []byte("through proxy")})
			proxy := fakeupstream.NewProxy(
				t,
				fakeupstream.Auth{Username: "proxy-user", Password: "proxy-secret"},
				target,
			)
			client := proxyClient(t, target, proxy, "proxy-secret")
			r := request(t, client, "GET", target.URL+"/artifact", nil)
			if body(t, r) != "through proxy" {
				t.Fatal("proxy bytes")
			}
			if proxy.Count("", "") != 1 || target.Count("GET", "/artifact") != 1 {
				t.Fatal(proxy.Requests(), target.Requests())
			}
			if target.Requests()[0].Header.Get("Proxy-Authorization") != "" {
				t.Fatal("proxy credential reached origin")
			}
			record := proxy.Requests()[0]
			if !record.Authorized ||
				record.Header.Get("Proxy-Authorization") != fakeupstream.Redacted {
				t.Fatal(record)
			}
			if tlsEnabled && record.Method != "CONNECT" {
				t.Fatal(record.Method)
			}
			unauthorized := proxyClient(t, target, proxy, "")
			req, _ := http.NewRequestWithContext(t.Context(), "GET", target.URL+"/artifact", nil)
			response, err := unauthorized.Do(req)
			if response != nil {
				response.Body.Close()
				if response.StatusCode != 407 || response.Header.Get("Proxy-Authenticate") == "" {
					t.Fatal(response.StatusCode)
				}
			} else if err == nil || !tlsEnabled {
				t.Fatal("missing proxy auth did not fail")
			}
			if proxy.Count("", "") != 2 || target.Count("", "") != 1 {
				t.Fatal("denied proxy reached origin")
			}
			proxy.Close()
			proxy.Close()
		})
	}
}

func TestProxyRefusesUnlistedTargetsAndFailures(t *testing.T) {
	t.Parallel()
	allowed := fakeupstream.New(t, fakeupstream.Options{})
	unlisted := fakeupstream.New(t, fakeupstream.Options{})
	proxy := fakeupstream.NewProxy(t, fakeupstream.Auth{}, allowed)
	client := proxyClient(t, allowed, proxy, "")
	for _, address := range []string{unlisted.URL + "/artifact", "http://unreachable.invalid/artifact"} {
		r := request(t, client, "GET", address, nil)
		if r.StatusCode != 403 {
			t.Fatal(r.StatusCode)
		}
	}
	if unlisted.Count("", "") != 0 {
		t.Fatal("proxy escaped allow list")
	}
	// Sending CONNECT as an ordinary request exercises the refusal without opening
	// a TLS session or looking up the named host.
	r := request(t, proxy.Client(), "CONNECT", proxy.URL, nil)
	if r.StatusCode != 403 {
		t.Fatal(r.StatusCode)
	}
	allowed.Close()
	r = request(t, client, "GET", allowed.URL+"/artifact", nil)
	if r.StatusCode != 502 {
		t.Fatal(r.StatusCode)
	}
	secure := fakeupstream.New(
		t,
		fakeupstream.Options{TLS: &tls.Config{MinVersion: tls.VersionTLS12}},
	)
	connect := fakeupstream.NewProxy(t, fakeupstream.Auth{}, secure)
	secure.Close()
	connectClient := proxyClient(t, secure, connect, "")
	req, _ := http.NewRequestWithContext(t.Context(), "GET", secure.URL+"/artifact", nil)
	response, err := connectClient.Do(req)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "Bad Gateway") {
		t.Fatalf("closed target: %v", err)
	}
	if connect.Count("CONNECT", "") != 1 {
		t.Fatal(connect.Requests())
	}
}

func TestProxyClosesLiveTunnel(t *testing.T) {
	t.Parallel()
	gate := new(fakeupstream.Barrier)
	target := fakeupstream.New(
		t,
		fakeupstream.Options{TLS: &tls.Config{MinVersion: tls.VersionTLS12}},
	)
	target.Set(
		"/blocked",
		fakeupstream.Resource{
			Body:  []byte("unreleased"),
			Fault: fakeupstream.Fault{BeforeBody: gate},
		},
	)
	proxy := fakeupstream.NewProxy(t, fakeupstream.Auth{}, target)
	client := proxyClient(t, target, proxy, "")
	response := request(t, client, "GET", target.URL+"/blocked", nil)
	<-gate.Reached()
	proxy.Close()
	if _, err := io.ReadAll(response.Body); err == nil {
		t.Fatal("live tunnel remained open")
	}
	gate.Release()
}
