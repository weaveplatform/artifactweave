package pushhttp

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel/wire"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

const (
	ntlmProxyUser     = "proxy-user"
	ntlmProxySecret   = "proxy-private-ntlm"
	ntlmProxyDomain   = "CORP"
	ntlmProxyHostname = "REPLICATOR"
)

// ntlmWorld is an owned chi push target behind the independent NTLMv2 verifier.
// The verifier, not the production dependency, decides whether a proof is valid.
type ntlmWorld struct {
	target  *fixture
	origin  *httptest.Server
	proxy   *fakeupstream.NTLMProxy
	store   *platforminmem.Store
	factory *Factory
}

func newNTLMWorld(t *testing.T, secureOrigin, secureProxy bool, options fakeupstream.NTLMOptions) *ntlmWorld {
	t.Helper()
	f := &fixture{items: make(map[string]fixtureItem)}
	router := chi.NewRouter()
	router.HandleFunc("/*", f.serve)
	origin := httptest.NewUnstartedServer(router)
	if secureOrigin {
		origin.StartTLS()
	} else {
		origin.Start()
	}
	t.Cleanup(origin.Close)
	f.url = origin.URL
	options.Username, options.Password = ntlmProxyUser, ntlmProxySecret
	options.Domain, options.Workstation = ntlmProxyDomain, ntlmProxyHostname
	if options.Origins == nil {
		options.Origins = []string{origin.URL}
	}
	if secureProxy {
		options.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	proxy := fakeupstream.NewNTLMProxy(t, options)
	store := platforminmem.New(memtx.New())
	// httptest and the fixture proxy share Go's loopback test certificate.
	var roots []byte
	switch {
	case secureProxy:
		roots = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxy.Certificate().Raw})
	case secureOrigin:
		roots = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: origin.Certificate().Raw})
	}
	factory, err := New(store, Options{
		RevisionKey: bytes.Repeat([]byte{5}, 32), RootCAsPEM: roots, AllowedPrivateOrigins: []string{origin.URL, proxy.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	return &ntlmWorld{target: f, origin: origin, proxy: proxy, store: store, factory: factory}
}

// configure stores the proxy as the descriptor would: host and port separately,
// with the password only in the sealed secret map.
func (w *ntlmWorld) configure(t *testing.T, edit func(*wire.ProxyConfiguration), password string) {
	t.Helper()
	address, err := url.Parse(w.proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(address.Port())
	if err != nil {
		t.Fatal(err)
	}
	proxy := wire.ProxyConfiguration{
		Host: address.Scheme + "://" + address.Hostname(), Port: port, Username: ntlmProxyUser,
		Domain: ntlmProxyDomain, NTHost: ntlmProxyHostname, PlatformDefault: true,
	}
	if edit != nil {
		edit(&proxy)
	}
	putProxy(t, w.store, "ntlm", proxy, password)
}

func (w *ntlmWorld) connection(t *testing.T, target model.Replication) port.PushConnection {
	t.Helper()
	var connection port.PushConnection
	if err := w.store.Tx(t.Context(), func(ctx context.Context) error {
		var err error
		connection, err = w.factory.Connection(ctx, target)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return connection
}

func (w *ntlmWorld) targetItem(path string) (fixtureItem, bool) {
	w.target.mu.Lock()
	defer w.target.mu.Unlock()
	item, ok := w.target.items[path]
	return item, ok
}

func (w *ntlmWorld) targetRequests() []string {
	w.target.mu.Lock()
	defer w.target.mu.Unlock()
	return slices.Clone(w.target.requests)
}

// verified requires that every authenticated tunnel proved the configured
// identity and that neither credential crossed into the other's exchange.
func (w *ntlmWorld) verified(t *testing.T) {
	t.Helper()
	authenticated := 0
	for _, record := range w.proxy.Negotiations() {
		if record.OriginAuthorization {
			t.Fatal("target credentials reached the NTLM proxy")
		}
		if record.Message == 1 && (record.Domain != ntlmProxyDomain || record.Workstation != ntlmProxyHostname) {
			t.Fatal("NEGOTIATE carried the wrong domain or workstation", record)
		}
		if record.Message == 3 {
			if !record.Authorized || record.Domain != ntlmProxyDomain || record.Workstation != ntlmProxyHostname {
				t.Fatal("NTLMv2 proof, domain or workstation was not verified", record)
			}
			authenticated++
		}
	}
	if authenticated == 0 {
		t.Fatal("push bypassed its NTLM proxy")
	}
	for _, request := range w.proxy.Requests() {
		if request.Header.Get("Authorization") != "" {
			t.Fatal("target Authorization was sent outside the tunnel")
		}
	}
}

func TestAccTargetNTLMProxyPushEffects(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		secureOrigin, secureProxy bool
		explicit                  bool
	}{
		{"http-origin-http-proxy-default", false, false, false},
		{"http-origin-http-proxy-explicit", false, false, true},
		{"https-origin-http-proxy-default", true, false, false},
		{"http-origin-https-proxy-explicit", false, true, true},
		{"https-origin-https-proxy-default", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newNTLMWorld(t, tc.secureOrigin, tc.secureProxy, fakeupstream.NTLMOptions{})
			w.configure(t, func(p *wire.ProxyConfiguration) { p.PlatformDefault = !tc.explicit }, ntlmProxySecret)
			target := fixtureTarget(w.origin.URL)
			if tc.explicit {
				target.Proxy = "ntlm"
			}
			connection := w.connection(t, target)
			for _, body := range [][]byte{[]byte("first NTLM replicated bytes"), []byte("updated NTLM replicated bytes")} {
				item := artifactsapi.TransferItem{Path: "dir/file.bin", Size: int64(len(body)), Actual: sums(body)}
				if err := connection.Put(t.Context(), item, bytes.NewReader(body), allowMutation); err != nil {
					t.Fatal("push through NTLM proxy failed", err)
				}
				stored, ok := w.targetItem("dir/file.bin")
				if !ok || !bytes.Equal(stored.body, body) {
					t.Fatal("target did not store the pushed bytes")
				}
			}
			observed, err := connection.Inspect(t.Context(), "dir/file.bin")
			if err != nil || observed.Snapshot.Actual != sums([]byte("updated NTLM replicated bytes")) {
				t.Fatal("target metadata disagreed with the pushed bytes", err)
			}
			if err := connection.Delete(t.Context(), "dir/file.bin", observed.Snapshot, allowMutation); err != nil {
				t.Fatal("delete through NTLM proxy failed", err)
			}
			if _, ok := w.targetItem("dir/file.bin"); ok {
				t.Fatal("target retained the deleted file")
			}
			if puts := slices.DeleteFunc(w.targetRequests(), func(r string) bool { return !strings.HasPrefix(r, http.MethodPut) }); len(puts) != 2 {
				t.Fatal("an upload was replayed or lost", puts)
			}
			w.verified(t)
		})
	}
}

func TestAccTargetNTLMProxyFailuresHaveNoEffect(t *testing.T) {
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(ntlmProxyUser+":"+ntlmProxySecret))
	for _, tc := range []struct {
		name     string
		options  fakeupstream.NTLMOptions
		edit     func(*wire.ProxyConfiguration)
		password string
		want     error
	}{
		{name: "wrong password", password: "wrong-proxy-password", want: outbound.ErrSecurity},
		{name: "wrong domain", edit: func(p *wire.ProxyConfiguration) { p.Domain = "OTHER" }, want: outbound.ErrSecurity},
		{name: "wrong workstation", edit: func(p *wire.ProxyConfiguration) { p.NTHost = "OTHER" }, want: outbound.ErrSecurity},
		{name: "malformed challenge", options: fakeupstream.NTLMOptions{Challenge: []byte("not an NTLM challenge")}, want: outbound.ErrSecurity},
		{name: "interrupted negotiation", options: fakeupstream.NTLMOptions{DropAfterNegotiate: true}},
		{name: "closed after challenge", options: fakeupstream.NTLMOptions{CloseAfterChallenge: true}, want: outbound.ErrSecurity},
		{name: "basic downgrade", options: fakeupstream.NTLMOptions{InitialAuthenticate: `Basic realm="proxy"`}, want: outbound.ErrSecurity},
		{name: "denied destination", options: fakeupstream.NTLMOptions{Origins: []string{}}, want: outbound.ErrSecurity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newNTLMWorld(t, false, false, tc.options)
			password := tc.password
			if password == "" {
				password = ntlmProxySecret
			}
			w.configure(t, tc.edit, password)
			connection := w.connection(t, fixtureTarget(w.origin.URL))
			body := []byte("must not be published")
			err := connection.Put(t.Context(), artifactsapi.TransferItem{Path: "file", Size: int64(len(body)), Actual: sums(body)}, bytes.NewReader(body), allowMutation)
			if err == nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatal("failed negotiation changed classification", err)
			}
			for _, secret := range []string{ntlmProxySecret, "wrong-proxy-password", "fixture-secret", w.proxy.URL, w.origin.URL} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("negotiation error disclosed configuration")
				}
			}
			if len(w.targetRequests()) != 0 {
				t.Fatal("target observed an exchange before proxy authentication")
			}
			for _, request := range w.proxy.Requests() {
				if request.HeaderMatches("Proxy-Authorization", basic) {
					t.Fatal("NTLM identity fell back to Basic")
				}
			}
		})
	}
}

func TestAccTargetNTLMProxyCancellationAndTimeout(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(strconv.FormatBool(timeout), func(t *testing.T) {
			barrier := &fakeupstream.Barrier{}
			t.Cleanup(barrier.Release)
			w := newNTLMWorld(t, false, false, fakeupstream.NTLMOptions{BeforeChallenge: barrier})
			w.configure(t, nil, ntlmProxySecret)
			target := fixtureTarget(w.origin.URL)
			if timeout {
				// The socket inactivity bound, not a caller deadline, ends a
				// proxy that stops answering mid-negotiation.
				target.SocketTimeout = 200 * time.Millisecond
			}
			connection := w.connection(t, target)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if !timeout {
				go func() {
					select {
					case <-barrier.Reached():
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			body := []byte("cancelled publication")
			err := connection.Put(ctx, artifactsapi.TransferItem{Path: "file", Size: int64(len(body)), Actual: sums(body)}, bytes.NewReader(body), allowMutation)
			want := context.Canceled
			if timeout {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatal("interrupted negotiation lost its context classification", err)
			}
			if len(w.targetRequests()) != 0 {
				t.Fatal("interrupted negotiation reached the target")
			}
		})
	}
}

func TestAccTargetNTLMProxyReconfigurationRetiresPools(t *testing.T) {
	w := newNTLMWorld(t, false, false, fakeupstream.NTLMOptions{})
	target := fixtureTarget(w.origin.URL)
	push := func(connection port.PushConnection, body string) error {
		data := []byte(body)
		return connection.Put(t.Context(), artifactsapi.TransferItem{Path: "file", Size: int64(len(data)), Actual: sums(data)}, bytes.NewReader(data), allowMutation)
	}
	w.configure(t, nil, ntlmProxySecret)
	current := w.connection(t, target)
	if err := push(current, "authenticated identity"); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		name     string
		edit     func(*wire.ProxyConfiguration)
		password string
		success  bool
	}{
		{name: "domain changed", edit: func(p *wire.ProxyConfiguration) { p.Domain = "OTHER" }, password: ntlmProxySecret},
		{name: "domain restored", password: ntlmProxySecret, success: true},
		{name: "workstation changed", edit: func(p *wire.ProxyConfiguration) { p.NTHost = "OTHER" }, password: ntlmProxySecret},
		{name: "password rotated", password: "rotated-proxy-password"},
		{name: "identity restored", password: ntlmProxySecret, success: true},
		// Removing both NTLM names selects Basic, which this proxy refuses: the
		// authenticated NTLM tunnels must not survive the selection change.
		{name: "NTLM removed", edit: func(p *wire.ProxyConfiguration) { p.Domain, p.NTHost = "", "" }, password: ntlmProxySecret},
		// ntHost alone selects NTLM; without the verifier's domain it fails
		// in negotiation rather than falling back to Basic.
		{name: "workstation only", edit: func(p *wire.ProxyConfiguration) { p.Domain = "" }, password: ntlmProxySecret},
		{name: "NTLM restored", password: ntlmProxySecret, success: true},
	} {
		t.Run(step.name, func(t *testing.T) {
			w.configure(t, step.edit, step.password)
			next := w.connection(t, target)
			if next == current || next.Revision() == current.Revision() {
				t.Fatal("proxy identity change did not rotate the connection revision")
			}
			if _, err := current.Inspect(t.Context(), "file"); !errors.Is(err, outbound.ErrClosed) {
				t.Fatal("stale authenticated pool remained usable", err)
			}
			if strings.Contains(next.Revision(), ntlmProxyDomain) || strings.Contains(next.Revision(), ntlmProxySecret) {
				t.Fatal("revision disclosed identity material")
			}
			err := push(next, step.name)
			if step.success != (err == nil) {
				t.Fatal("current proxy identity was not the one negotiated", err)
			}
			current = next
		})
	}
	t.Run("proxy removed", func(t *testing.T) {
		before := len(w.proxy.Negotiations())
		target.DisableProxy = true
		direct := w.connection(t, target)
		if err := push(direct, "direct after proxy"); err != nil || len(w.proxy.Negotiations()) != before {
			t.Fatal("disableProxy did not select the direct connection", err)
		}
	})
}

func TestUnitTargetNTLMRevisionInputs(t *testing.T) {
	store := platforminmem.New(memtx.New())
	factory, err := New(store, Options{RevisionKey: bytes.Repeat([]byte{6}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	target := fixtureTarget("https://target.invalid")
	target.Proxy = "explicit"
	revision := func(edit func(*wire.ProxyConfiguration)) string {
		t.Helper()
		proxy := wire.ProxyConfiguration{Host: "proxy.invalid", Port: 8080, Username: ntlmProxyUser, Domain: ntlmProxyDomain, NTHost: ntlmProxyHostname}
		if edit != nil {
			edit(&proxy)
		}
		putProxy(t, store, "explicit", proxy, ntlmProxySecret)
		var connection port.PushConnection
		if err := store.Tx(t.Context(), func(ctx context.Context) error {
			var err error
			connection, err = factory.Connection(ctx, target)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return connection.Revision()
	}
	base := revision(nil)
	if revision(func(p *wire.ProxyConfiguration) { p.RedirectedToHosts = "other.invalid, 127.0.0.1" }) != base {
		t.Fatal("redirectedToHosts, which push cannot use, churned the transport revision")
	}
	seen := map[string]string{"base": base}
	for name, edit := range map[string]func(*wire.ProxyConfiguration){
		"domain":      func(p *wire.ProxyConfiguration) { p.Domain = "OTHER" },
		"workstation": func(p *wire.ProxyConfiguration) { p.NTHost = "OTHER" },
		"username":    func(p *wire.ProxyConfiguration) { p.Username = "other-user" },
		"basic":       func(p *wire.ProxyConfiguration) { p.Domain, p.NTHost = "", "" },
		// Swapped names must not collide with the original identity.
		"swapped": func(p *wire.ProxyConfiguration) { p.Domain, p.NTHost = ntlmProxyHostname, ntlmProxyDomain },
	} {
		current := revision(edit)
		for other, previous := range seen {
			if current == previous {
				t.Fatalf("%s and %s share a transport revision", name, other)
			}
		}
		seen[name] = current
	}
}
