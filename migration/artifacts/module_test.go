package artifacts

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/upstream"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/gc"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
)

// newModule builds the module over a fresh in-memory store and the real
// access module, then opens its remote sources, as OpenRuntime does.
func newModule(t *testing.T) *Module {
	t.Helper()
	st := inmem.New()
	m, err := New(Deps{
		Store: st, Access: accesstest.Over(t, st).API(),
		Log: slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	if err := m.OpenRemoteSources(t.Context()); err != nil {
		t.Fatal(err)
	}
	return m
}

// New refuses Deps without a store and otherwise binds everything without
// calling the store; Close before OpenRemoteSources, or on nil, does nothing.
func TestUnitNew(t *testing.T) {
	if m, err := New(Deps{}); m != nil || err == nil ||
		err.Error() != "artifacts: Deps.Store is required" {
		t.Errorf("New without a store = %v, %v; want the required-store error", m, err)
	}

	// Any call on this store panics, so New succeeding shows it does no I/O.
	keys, err := crypt.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Deps{Store: struct{ Store }{}, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	if api := m.API(); api.Repositories == nil || api.Layouts == nil {
		t.Errorf("API = %+v, want Repositories and Layouts bound", api)
	}
	if got := m.FormatSettings().TerraformBackend.Keys; got != keys {
		t.Errorf("Terraform backend keys = %p, want the keyring %p", got, keys)
	}
	if m.items.Work != nil {
		t.Error("without a queue, the item service's Work is a non-nil interface")
	}
	if m.log != slog.Default() {
		t.Error("a nil Log is not slog.Default()")
	}
	m.Close()
	(*Module)(nil).Close()
}

func TestAccLogGC(t *testing.T) {
	var buf bytes.Buffer
	f := logGC(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	f(gc.Report{}, errors.New("boom"))
	f(gc.Report{Skipped: true}, nil)
	f(gc.Report{}, nil)
	f(gc.Report{BlobsDeleted: 2, BytesFreed: 10}, nil)
	for _, want := range []string{
		`level=WARN msg="garbage collection failed"`,
		"another instance holds the lease",
		"nothing to do",
		`level=INFO msg="garbage collection" trash_purged=0 uploads_reaped=0 blobs_deleted=2`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in:\n%s", want, buf.String())
		}
	}
}

func TestAccRemotePolicyKeyInitialization(t *testing.T) {
	m := &Module{store: inmem.New()}
	const count = 8
	keys := make(chan []byte, count)
	errs := make(chan error, count)
	for range count {
		go func() { key, err := m.remotePolicyKey(t.Context()); keys <- key; errs <- err }()
	}
	var first []byte
	for range count {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		key := <-keys
		if first == nil {
			first = key
		}
		if len(key) != 32 || !bytes.Equal(first, key) {
			t.Fatal("initialization disagreed")
		}
	}
	if err := m.store.Settings().
		Put(t.Context(), remotePolicyKeySetting, []byte("short")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.remotePolicyKey(t.Context()); err == nil {
		t.Fatal("corrupt stored key accepted")
	}
}

// The format slices' tests install the hooks on their own fixtures, so only
// this composition shows a hook the application stopped installing. A policy
// without it still serves every request, only differently: a NuGet v2 204
// would no longer be an empty feed, and without the registry path parser
// Smart Remote would stop synchronising registry properties.
func TestAccRemoteWiringInstallsFormatHooks(t *testing.T) {
	m := newModule(t)
	content := m.registrySources.HTTP.(*docker.RegistryContent)
	policies := map[string]*remote.Policy{
		"remote":   m.remoteSources.HTTP.(*maven.MavenBackend).Policy,
		"registry": content.Policy,
	}
	for name, p := range policies {
		res := &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     http.Header{},
			Body:       http.NoBody,
		}
		q := source.Request{Resource: source.Resource{NuGetEmptyFeed: true}}
		if p.ProjectResponse == nil || p.ProjectResponse(q, res) != nil ||
			res.StatusCode != http.StatusOK {
			t.Fatalf("%s policy does not project the NuGet empty feed", name)
		}
	}
	clients, ok := content.Registries.(*upstream.RegistryClients)
	if !ok || clients.Pool != &m.registryHTTP || clients.Options == nil {
		t.Fatal("registry content does not open the runtime's registry clients")
	}
	if m.registryPath == nil {
		t.Fatal("no registry path parser for Smart Remote")
	}
	image, operation, reference, err := m.registryPath("library/alpine/manifests/latest")
	if err != nil || image != "library/alpine" || operation != "manifests" ||
		reference != "latest" {
		t.Fatalf("registry path: %q %q %q %v", image, operation, reference, err)
	}
}
