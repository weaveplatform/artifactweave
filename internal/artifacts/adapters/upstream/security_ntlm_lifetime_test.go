package upstream

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
)

func TestAccProxyNTLMForwardPoolLifetime(t *testing.T) {
	for _, mode := range []string{"close", "evict"} {
		t.Run(mode, func(t *testing.T) { testNTLMForwardPoolLifetime(t, mode) })
	}
}

func testNTLMForwardPoolLifetime(t *testing.T, mode string) {
	t.Helper()
	origin := fakeupstream.New(t, fakeupstream.Options{
		Auth: fakeupstream.Auth{Username: "origin-user", Password: "origin-secret"},
	})
	origin.Set("/item", fakeupstream.Resource{Body: []byte("authenticated lifetime bytes")})
	options := ntlmFixtureOptions()
	options.Forward, options.ForwardOnly = true, true
	proxy := fakeupstream.NewNTLMProxy(t, options, origin)
	st := inmem.New()
	p := proxyConfig(t, proxy.URL)
	p.Domain, p.NTHost = "CORP", "ARTIFACTWEAVE"
	putProxy(t, st, "ntlm", p, "proxy-secret")
	c := repo.Config{
		Key: "ntlm", Class: repo.Remote, URL: origin.URL, Proxy: "ntlm",
		Username: "origin-user", Password: "origin-secret",
	}
	saveRepo(t, st, c)
	client := clientFor(t, c, resolveFor(t, resolverFor(t, st, ResolverOptions{}), c))
	if body(t, getFor(t, client, "item")) != "authenticated lifetime bytes" {
		t.Fatal("uninterrupted NTLM forwarding failed")
	}
	secure := client.http.Transport.(*secureTransport)
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		n := 0
		for {
			select {
			case <-stop:
				return
			default:
				if mode == "close" {
					client.Close()
				} else {
					logical := *secure.source.base
					logical.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(20000+n%64))
					secure.forAuthority(&logical, secure.source.proxy, false)
					n++
				}
				runtime.Gosched()
			}
		}
	}()
	defer func() { close(stop); <-stopped }()
	var workers sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		workers.Go(func() {
			for range 128 {
				response, err := client.Do(t.Context(), remote.Request{Method: http.MethodGet, Path: "item"})
				if err != nil {
					// Closing idle pools can interrupt a bounded challenge, but
					// cannot expose the proxy's challenge as an origin response.
					if errors.Is(err, remote.ErrSecurity) || errors.Is(err, remote.ErrConnection) {
						continue
					}
					results <- fmt.Errorf("concurrent close request: %w", err)
					return
				}
				data, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if response.StatusCode != http.StatusOK || readErr != nil || string(data) != "authenticated lifetime bytes" {
					results <- fmt.Errorf("selected NTLM transport lost authentication: status %d, read error %v", response.StatusCode, readErr)
					return
				}
			}
			results <- nil
		})
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Error(err)
		}
	}
	for _, record := range origin.Requests() {
		if !record.Authorized || record.Header.Get("Proxy-Authorization") != "" {
			t.Fatal("origin and proxy credentials crossed")
		}
	}
}
