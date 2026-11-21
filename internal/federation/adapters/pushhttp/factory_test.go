package pushhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/kernel/wire"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func putProxy(t *testing.T, store Store, key string, value wire.ProxyConfiguration, password string) {
	t.Helper()
	document, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Configs().Put(t.Context(), configdoc.ConfigDocument{
		Kind: configdoc.ConfigProxy, Key: key, Enabled: true, Document: document,
		Secrets: map[string]string{"password": password}, Updated: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestUnitTargetConnectionResolvesCurrentPolicyWithoutNetwork(t *testing.T) {
	store := platforminmem.New(memtx.New())
	var lookups atomic.Int32
	options := Options{
		RevisionKey: bytes.Repeat([]byte{9}, 32), AllowedPrivateOrigins: []string{"https://target.invalid"},
		LookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			lookups.Add(1)
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		},
	}
	factory, err := New(store, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	// Mutating caller-owned options after construction cannot change policy.
	options.RevisionKey[0] = 0
	options.AllowedPrivateOrigins[0] = "http://other.invalid"
	target := fixtureTarget("https://target.invalid")
	resolve := func() port.PushConnection {
		t.Helper()
		var connection port.PushConnection
		err := store.Tx(t.Context(), func(ctx context.Context) error {
			var err error
			connection, err = factory.Connection(ctx, target)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return connection
	}
	direct := resolve()
	if again := resolve(); again != direct || again.Revision() != direct.Revision() {
		t.Fatal("unchanged target lost stable revision")
	}
	proxy := wire.ProxyConfiguration{Host: "proxy.invalid", Port: 8080, PlatformDefault: true, Services: []string{"jfrt"}, Username: "proxy-user"}
	putProxy(t, store, "default", proxy, "proxy-secret-one")
	proxied := resolve()
	if proxied == direct || proxied.Revision() == direct.Revision() {
		t.Fatal("default proxy did not fence direct connection")
	}
	if _, err := direct.Inspect(t.Context(), "file"); !errors.Is(err, outbound.ErrClosed) {
		t.Fatal("replaced policy remained usable", err)
	}
	putProxy(t, store, "default", proxy, "proxy-secret-one")
	if again := resolve(); again != proxied || again.Revision() != proxied.Revision() {
		t.Fatal("identical proxy PUT churned effective revision")
	}
	putProxy(t, store, "default", proxy, "proxy-secret-two")
	rotated := resolve()
	if rotated.Revision() == proxied.Revision() || strings.Contains(rotated.Revision(), "secret") {
		t.Fatal("proxy password rotation not privately bound")
	}
	target.Password = "new-target-secret"
	credential := resolve()
	if credential.Revision() == rotated.Revision() {
		t.Fatal("target credential rotation not fenced")
	}
	target.DisableProxy = true
	if disabled := resolve(); disabled.Revision() == credential.Revision() {
		t.Fatal("disableProxy did not change effective policy")
	}
	if lookups.Load() != 0 {
		t.Fatal("connection construction performed DNS")
	}
	if _, err := factory.Connection(t.Context(), target); !errors.Is(err, outbound.ErrConfiguration) {
		t.Fatal("proxy configuration resolved outside metadata transaction", err)
	}
	factory.Close()
	err = store.Tx(t.Context(), func(ctx context.Context) error { _, err := factory.Connection(ctx, target); return err })
	if !errors.Is(err, outbound.ErrClosed) {
		t.Fatal("closed pool admitted connection", err)
	}
}

func TestUnitTargetProxySelectionAndUnsupportedPolicy(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*wire.ProxyConfiguration)
	}{
		{"invalid port", func(p *wire.ProxyConfiguration) { p.Port = 0 }},
		{"NTLM domain", func(p *wire.ProxyConfiguration) { p.Domain = "domain" }},
		{"NTLM host", func(p *wire.ProxyConfiguration) { p.NTHost = "host" }},
		{"forwarded credentials", func(p *wire.ProxyConfiguration) { p.RedirectedToHosts = "other.invalid" }},
		{"embedded password", func(p *wire.ProxyConfiguration) { p.Host = "http://user:secret@proxy.invalid" }},
		{"path", func(p *wire.ProxyConfiguration) { p.Host = "http://proxy.invalid/path" }},
		{"query", func(p *wire.ProxyConfiguration) { p.Host = "http://proxy.invalid?password=secret" }},
		{"port in host", func(p *wire.ProxyConfiguration) { p.Host = "proxy.invalid:8080" }},
		{"wrong protocol", func(p *wire.ProxyConfiguration) { p.Host = "ftp://proxy.invalid" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := platforminmem.New(memtx.New())
			factory, err := New(store, Options{RevisionKey: bytes.Repeat([]byte{3}, 32)})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(factory.Close)
			proxy := wire.ProxyConfiguration{Host: "proxy.invalid", Port: 8080}
			test.edit(&proxy)
			putProxy(t, store, "explicit", proxy, "secret")
			target := fixtureTarget("https://target.invalid")
			target.Proxy = "explicit"
			err = store.Tx(t.Context(), func(ctx context.Context) error { _, err := factory.Connection(ctx, target); return err })
			if !errors.Is(err, outbound.ErrConfiguration) || strings.Contains(err.Error(), "secret") {
				t.Fatal("unsupported proxy admitted or leaked", err)
			}
		})
	}
}

func TestUnitTargetProxyAmbiguityExplicitAndDisabled(t *testing.T) {
	store := platforminmem.New(memtx.New())
	factory, err := New(store, Options{RevisionKey: bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	proxy := wire.ProxyConfiguration{Host: "https://proxy.invalid", Port: 8443, PlatformDefault: true}
	putProxy(t, store, "first", proxy, "")
	putProxy(t, store, "second", proxy, "")
	target := fixtureTarget("https://target.invalid")
	err = store.Tx(t.Context(), func(ctx context.Context) error { _, err := factory.Connection(ctx, target); return err })
	if !errors.Is(err, outbound.ErrConfiguration) {
		t.Fatal("ambiguous defaults admitted", err)
	}
	target.Proxy = "first"
	err = store.Tx(t.Context(), func(ctx context.Context) error { _, err := factory.Connection(ctx, target); return err })
	if err != nil {
		t.Fatal("explicit proxy lost to defaults", err)
	}
	target.DisableProxy = true
	target.Proxy = "missing"
	err = store.Tx(t.Context(), func(ctx context.Context) error { _, err := factory.Connection(ctx, target); return err })
	if err != nil {
		t.Fatal("disableProxy did not force direct", err)
	}
	if !proxyService([]string{"other", "jfrt"}) || proxyService([]string{"other"}) || !proxyService(nil) {
		t.Fatal("proxy service selection differs from descriptor policy")
	}
}

func TestUnitTargetRejectsInvalidStartupPolicy(t *testing.T) {
	for _, test := range []struct {
		name    string
		options Options
	}{
		{"missing key", Options{}},
		{"zero key", Options{RevisionKey: make([]byte, 32)}},
		{"bad roots", Options{RevisionKey: bytes.Repeat([]byte{3}, 32), RootCAsPEM: []byte("invalid certificate")}},
		{"bad origin", Options{RevisionKey: bytes.Repeat([]byte{3}, 32), AllowedPrivateOrigins: []string{"ftp://target.invalid"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			factory, err := New(platforminmem.New(memtx.New()), test.options)
			if err == nil || factory != nil {
				t.Fatal("invalid startup policy admitted")
			}
		})
	}
}

func TestUnitTargetUploadLostAcknowledgementDoesNotReplay(t *testing.T) {
	f, _, _, connection := newFixture(t)
	body := []byte("committed before disconnect")
	var writes atomic.Int32
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != http.MethodPut {
			return false
		}
		writes.Add(1)
		value, err := io.ReadAll(r.Body)
		if err != nil {
			panic(err)
		}
		f.items["file"] = fixtureItem{body: value}
		socket, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			panic(err)
		}
		if err := socket.Close(); err != nil {
			panic(err)
		}
		return true
	}
	if err := connection.Put(t.Context(), artifactsapi.TransferItem{Path: "file", Size: int64(len(body)), Actual: sums(body)}, bytes.NewReader(body)); err == nil {
		t.Fatal("lost acknowledgement reported success")
	}
	item, err := connection.Inspect(t.Context(), "file")
	if err != nil || item.Snapshot.Actual != sums(body) || writes.Load() != 1 {
		t.Fatal("remote commit could not be independently verified or PUT replayed", item, err, writes.Load())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.items["file"].body, body) {
		t.Fatal("target lost committed bytes")
	}
}

func TestUnitTargetFactoryTrustAndBoundedConnectionOwnership(t *testing.T) {
	f := &fixture{items: map[string]fixtureItem{"file": {body: []byte("TLS content")}}}
	router := chi.NewRouter()
	router.HandleFunc("/*", f.serve)
	server := httptest.NewTLSServer(router)
	t.Cleanup(server.Close)
	store := platforminmem.New(memtx.New())
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	factory, err := New(store, Options{RevisionKey: bytes.Repeat([]byte{4}, 32), RootCAsPEM: certificate, AllowedPrivateOrigins: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	var first port.PushConnection
	err = store.Tx(t.Context(), func(ctx context.Context) error {
		for index := 0; index < 257; index++ {
			target := fixtureTarget(server.URL)
			target.Key = strconv.Itoa(index)
			connection, err := factory.Connection(ctx, target)
			if err != nil {
				return err
			}
			if index == 0 {
				first = connection
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Inspect(t.Context(), "file"); !errors.Is(err, outbound.ErrClosed) {
		t.Fatal("evicted connection retained ownership", err)
	}
	var latest port.PushConnection
	err = store.Tx(t.Context(), func(ctx context.Context) error {
		target := fixtureTarget(server.URL)
		target.Key = "256"
		var err error
		latest, err = factory.Connection(ctx, target)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := latest.Inspect(t.Context(), "file")
	if err != nil || item.Snapshot.Actual != sums([]byte("TLS content")) {
		t.Fatal("configured trust did not reach authenticated target", item, err)
	}
}

type failedConfigs struct {
	configdoc.ConfigStore
	cause error
}

func (s failedConfigs) Get(context.Context, configdoc.ConfigKind, string) (configdoc.ConfigDocument, error) {
	return configdoc.ConfigDocument{}, s.cause
}

func (s failedConfigs) List(context.Context, configdoc.ConfigKind) ([]configdoc.ConfigDocument, error) {
	return nil, s.cause
}

type failedStore struct {
	*platforminmem.Store
	cause error
}

func (s failedStore) Configs() configdoc.ConfigStore {
	return failedConfigs{ConfigStore: s.Store.Configs(), cause: s.cause}
}

func TestUnitTargetConfigurationFailuresRefuseWithoutSecretDisclosure(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, kernel.ErrStore} {
		for _, explicit := range []bool{false, true} {
			t.Run(cause.Error()+strconv.FormatBool(explicit), func(t *testing.T) {
				store := failedStore{Store: platforminmem.New(memtx.New()), cause: cause}
				factory, err := New(store, Options{RevisionKey: bytes.Repeat([]byte{4}, 32)})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(factory.Close)
				target := fixtureTarget("https://target.invalid")
				if explicit {
					target.Proxy = "proxy"
				}
				err = store.Tx(t.Context(), func(ctx context.Context) error { _, err := factory.Connection(ctx, target); return err })
				want := cause
				if errors.Is(cause, kernel.ErrStore) {
					want = outbound.ErrConfiguration
				}
				if !errors.Is(err, want) {
					t.Fatal("configuration failure changed classification", err)
				}
			})
		}
	}
	store := platforminmem.New(memtx.New())
	factory, err := New(store, Options{RevisionKey: bytes.Repeat([]byte{4}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	target := fixtureTarget("https://target.invalid")
	target.URL = "https://user:secret@target.invalid/repo"
	err = store.Tx(t.Context(), func(ctx context.Context) error { _, err := factory.Connection(ctx, target); return err })
	if !errors.Is(err, outbound.ErrConfiguration) || strings.Contains(err.Error(), "secret") {
		t.Fatal("untrusted URL admitted or exposed", err)
	}
	if err := store.Configs().Put(t.Context(), configdoc.ConfigDocument{Kind: configdoc.ConfigProxy, Key: "invalid", Enabled: true, Document: []byte("invalid JSON")}); err != nil {
		t.Fatal(err)
	}
	target = fixtureTarget("https://target.invalid")
	err = store.Tx(t.Context(), func(ctx context.Context) error { _, err := factory.Connection(ctx, target); return err })
	if !errors.Is(err, outbound.ErrConfiguration) {
		t.Fatal("malformed proxy document admitted", err)
	}
}
