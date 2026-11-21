package outbound

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func TestAccFixedOriginProxyPinsDestinationAndSeparatesAuthentication(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP proxy", true: "HTTPS proxy"}[secure], func(t *testing.T) {
			var logicalHost string
			targetRoutes := chi.NewRouter()
			targetRoutes.Put("/item", func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				if !ok || user != "replicator" || password != "fixture-password" || r.Host != logicalHost || r.Header.Get("Proxy-Authorization") != "" {
					t.Error("proxy changed origin host/authentication")
				}
				_, err := io.Copy(w, r.Body)
				if err != nil {
					t.Error(err)
				}
			})
			target := httptest.NewServer(targetRoutes)
			t.Cleanup(target.Close)
			actualTarget := parsed(t, target.URL)
			base := *actualTarget
			base.Host = net.JoinHostPort("destination.example", base.Port())
			logicalHost = base.Host
			var connects atomic.Int32
			var pipes sync.WaitGroup
			proxyRoutes := chi.NewRouter()
			proxyRoutes.Method(http.MethodConnect, "/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connects.Add(1)
				want := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-password"))
				if r.Host != actualTarget.Host || r.Header.Get("Proxy-Authorization") != want || r.Header.Get("Authorization") != "" {
					t.Error("proxy CONNECT did not use checked IP/separate credentials")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				remote, err := (&net.Dialer{Timeout: time.Second}).DialContext(r.Context(), "tcp", actualTarget.Host)
				if err != nil {
					t.Error(err)
					return
				}
				incoming, buffer, err := w.(http.Hijacker).Hijack()
				if err != nil {
					_ = remote.Close()
					t.Error(err)
					return
				}
				if _, err := buffer.WriteString("HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
					_ = remote.Close()
					_ = incoming.Close()
					t.Error(err)
					return
				}
				if err := buffer.Flush(); err != nil {
					_ = remote.Close()
					_ = incoming.Close()
					t.Error(err)
					return
				}
				pipes.Add(2)
				for _, pair := range [][2]net.Conn{{incoming, remote}, {remote, incoming}} {
					go func() {
						defer pipes.Done()
						_, _ = io.Copy(pair[0], pair[1])
						_ = incoming.Close()
						_ = remote.Close()
					}()
				}
			}))
			var proxy *httptest.Server
			if secure {
				proxy = httptest.NewTLSServer(proxyRoutes)
			} else {
				proxy = httptest.NewServer(proxyRoutes)
			}
			t.Cleanup(proxy.Close)
			proxyURL := parsed(t, proxy.URL)
			roots := x509.NewCertPool()
			if secure {
				roots.AddCert(proxy.Certificate())
				if names := proxy.Certificate().DNSNames; len(names) > 0 {
					proxyURL.Host = net.JoinHostPort(names[0], proxyURL.Port())
				}
			} else {
				proxyURL.Host = net.JoinHostPort("proxy.example", proxyURL.Port())
			}
			proxyURL.User = url.UserPassword("proxy-user", "proxy-password")
			client := testClient(t, &base, proxyURL, roots, time.Second, func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
			})
			t.Cleanup(func() { client.Close(); pipes.Wait() })
			if readResponse(t)(client.Do(request(t, http.MethodPut, base.String()+"/item", "through proxy"))) != "through proxy" || connects.Load() != 1 {
				t.Fatal("authenticated pinned proxy upload failed", connects.Load())
			}
		})
	}
}
