package upstream

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

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

// closeOnHandOver returns ctx with a trace that runs closePool once, when
// net/http has taken the request's connection back but not yet handed over
// its bodyless answer, and then holds net/http there so the round trip sees
// the closed connection first. Its PutIdleConn hook runs in that window.
func closeOnHandOver(ctx context.Context, closePool func()) context.Context {
	var once sync.Once
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{PutIdleConn: func(error) {
		once.Do(func() {
			closePool()
			time.Sleep(100 * time.Millisecond)
		})
	}})
}

// requireConnectionLost sends request through client five times, each
// closing client's pools while net/http hands over the first bodyless
// answer. Each failure must be remote.ErrConnection, and at least one must
// occur; an answer that still wins on a starved scheduler is not a failure.
func requireConnectionLost(t *testing.T, client *Client, request remote.Request) {
	t.Helper()
	lost := 0
	for range 5 {
		response, err := client.Do(closeOnHandOver(t.Context(), client.Close), request)
		if err == nil {
			_ = body(t, response)
			continue
		}
		if !errors.Is(err, remote.ErrConnection) {
			t.Fatal("request interrupted by a pool close classified as", err)
		}
		lost++
	}
	if lost == 0 {
		t.Fatal("no request saw its connection close")
	}
}

// A Close that releases the connection of a bodyless answer, here a HEAD's,
// fails the request as a connection failure. Before #1878 it answered
// remote.ErrRequest, which Smart Remote maps to a protocol failure.
func TestAccClientCloseUnderABodylessAnswerIsAConnectionFailure(t *testing.T) {
	origin := fakeupstream.New(t, fakeupstream.Options{})
	origin.Set("/item", fakeupstream.Resource{Body: []byte("bytes")})
	client := clientFor(t, repo.Config{Class: repo.Remote, URL: origin.URL}, remote.Options{})
	requireConnectionLost(t, client, remote.Request{Method: http.MethodHead, Path: "item"})
}

// The NTLM forward exchange takes its connection back before each 407
// challenge, which has no body. A Close in that window interrupts the
// challenge as a connection failure (#1878).
func TestAccProxyNTLMForwardCloseUnderAChallengeIsAConnectionFailure(t *testing.T) {
	origin := fakeupstream.New(t, fakeupstream.Options{})
	origin.Set("/item", fakeupstream.Resource{Body: []byte("bytes")})
	options := ntlmFixtureOptions()
	options.Forward, options.ForwardOnly = true, true
	proxy := fakeupstream.NewNTLMProxy(t, options, origin)
	st := inmem.New()
	p := proxyConfig(t, proxy.URL)
	p.Domain, p.NTHost = "CORP", "ARTIFACTWEAVE"
	putProxy(t, st, "ntlm", p, "proxy-secret")
	c := repo.Config{Key: "ntlm", Class: repo.Remote, URL: origin.URL, Proxy: "ntlm"}
	saveRepo(t, st, c)
	client := clientFor(t, c, resolveFor(t, resolverFor(t, st, ResolverOptions{}), c))
	requireConnectionLost(t, client, remote.Request{Method: http.MethodGet, Path: "item"})
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
