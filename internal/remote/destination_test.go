package remote

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestDestinationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, url, ip string
		configured    bool
		want          error
	}{
		{"public", "https://registry.example/item", "8.8.8.8", false, nil},
		{"public-v6", "https://registry.example/item", "2606:4700:4700::1111", false, nil},
		{"private", "https://registry.example/item", "10.20.30.40", false, ErrSecurity},
		{"configured-private", "https://registry.example/item", "10.20.30.40", true, nil},
		{"loopback", "http://127.0.0.1/item", "", false, ErrSecurity},
		{"configured-loopback", "http://127.0.0.1/item", "", true, nil},
		{"mapped-loopback", "http://[::ffff:127.0.0.1]/item", "", false, ErrSecurity},
		{"mapped-private", "http://[::ffff:10.0.0.1]/item", "", false, ErrSecurity},
		{"metadata", "http://169.254.169.254/latest/api/token", "", true, ErrSecurity},
		{"metadata-alibaba", "http://100.100.100.200/item", "", true, ErrSecurity},
		{"metadata-azure", "http://168.63.129.16/item", "", true, ErrSecurity},
		{"metadata-v6", "http://[fd00:ec2::254]/item", "", true, ErrSecurity},
		{"metadata-host", "http://metadata.google.internal/item", "8.8.8.8", true, ErrSecurity},
		{"multicast", "http://224.0.0.1/item", "", true, ErrSecurity},
		{"unspecified", "http://0.0.0.0/item", "", true, ErrSecurity},
		{"ula", "https://registry.example/item", "fd00::1", false, ErrSecurity},
		{"cgnat", "https://registry.example/item", "100.64.0.1", false, ErrSecurity},
		{"docs", "https://registry.example/item", "203.0.113.1", false, ErrSecurity},
		{"nat64", "https://registry.example/item", "64:ff9b::a00:1", false, ErrSecurity},
		{"six-to-four", "https://registry.example/item", "2002:a00:1::", false, ErrSecurity},
		{"user-info", "https://user:secret@registry.example/item", "8.8.8.8", false, ErrSecurity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatal(err)
			}
			p := &destinationPolicy{
				private: map[string]bool{origin(u): tc.configured},
				lookup: func(context.Context, string) ([]net.IPAddr, error) {
					return []net.IPAddr{{IP: net.ParseIP(tc.ip)}}, nil
				},
			}
			got, err := p.addresses(t.Context(), u)
			if !errors.Is(err, tc.want) {
				t.Fatalf("addresses = %v, %v; want %v", got, err, tc.want)
			}
			if err == nil && len(got) != 1 {
				t.Fatal(got)
			}
		})
	}
	for _, raw := range []string{"ftp://example.com", "https://example.com:0", "https://example.com:65536", "https://example.com:nope", "https://example.com/a/../b", "https://example.com/%2e%2e/b", "https://example.com/a%5cb", "https://example.com/a%00b", "https://example.com/#fragment", "https://[fe80::1%25en0]/item", "https://user:pass@example.com"} {
		if _, err := checkedURL(raw); !errors.Is(err, ErrSecurity) {
			t.Errorf("accepted %q: %v", raw, err)
		}
	}
}

func TestDestinationMixedDNSAndCancellation(t *testing.T) {
	u, _ := url.Parse("https://registry.example/item")
	for _, tc := range []struct {
		name      string
		ips       []net.IPAddr
		err, want error
	}{
		{name: "mixed", ips: []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("127.0.0.1")}}, want: ErrSecurity},
		{name: "invalid", ips: []net.IPAddr{{IP: []byte{1, 2}}}, want: ErrSecurity},
		{name: "zone", ips: []net.IPAddr{{IP: net.ParseIP("2606:4700::1111"), Zone: "eth0"}}, want: ErrSecurity},
		{name: "empty", want: ErrConnection},
		{name: "dns-error", err: &net.DNSError{Err: "secret", Name: "secret.example"}, want: ErrConnection},
		{name: "cancel", err: context.Canceled, want: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, want: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &destinationPolicy{
				lookup: func(context.Context, string) ([]net.IPAddr, error) { return tc.ips, tc.err },
			}
			_, err := p.addresses(t.Context(), u)
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "secret") {
				t.Fatal(err)
			}
		})
	}
}

func TestPinnedDNSRebindingAndRetry(t *testing.T) {
	upstream := fakeupstream.New(t, fakeupstream.Options{})
	upstream.Set("/item", fakeupstream.Resource{Body: []byte("pinned")})
	u, _ := url.Parse(upstream.URL)
	u.Host = net.JoinHostPort("registry.private", u.Port())
	var calls atomic.Int32
	policy := &destinationPolicy{
		private: map[string]bool{origin(u): true},
		lookup: func(context.Context, string) ([]net.IPAddr, error) {
			if calls.Add(1) > 1 {
				return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
			}
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.2")}, {IP: net.ParseIP("127.0.0.1")}}, nil
		},
	}
	security := &sourceSecurity{base: u, policy: policy}
	client := clientFor(
		t,
		repo.Config{URL: u.String()},
		Options{
			Prepare: security.prepare,
			WrapTransport: func(base *http.Transport) (http.RoundTripper, func()) {
				original := base.DialContext
				base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					if strings.HasPrefix(addr, "127.0.0.2:") {
						return nil, &net.OpError{Op: "dial", Net: network, Err: ErrConnection}
					}
					return original(ctx, network, addr)
				}
				return security.transport(base)
			},
			CheckURL: security.checkURL,
		},
	)
	if body(t, getFor(t, client, "item")) != "pinned" {
		t.Fatal("first approved DNS answers failed")
	}
	if calls.Load() != 1 {
		t.Fatal("transport re-resolved approved DNS address")
	}
	if _, err := client.Do(
		t.Context(),
		Request{Method: "GET", Path: "item"},
	); !errors.Is(
		err,
		ErrSecurity,
	) {
		t.Fatal(err)
	}
	if upstream.Count("", "") != 1 {
		t.Fatal("rebound request reached network")
	}
	if upstream.Requests()[0].Host != u.Host {
		t.Fatal("logical host was not preserved")
	}
}

func TestSecurityTransportBoundedPools(t *testing.T) {
	s := &sourceSecurity{base: &url.URL{Scheme: "https", Host: "example.com"}}
	wrapped, closeFn := s.transport(&http.Transport{})
	t.Cleanup(closeFn)
	tr := wrapped.(*secureTransport)
	for n := range 40 {
		u := &url.URL{Scheme: "https", Host: strings.Repeat("a", n+1) + ".example"}
		tr.forAuthority(u, nil)
	}
	if len(tr.clients) != 32 {
		t.Fatal(len(tr.clients))
	}
	closeFn()
	if len(tr.clients) != 0 {
		t.Fatal("pools retained after close")
	}
}
