package configuration

import (
	"errors"
	"testing"

	configapi "github.com/weaveplatform/artifactweave/migration/configuration/api"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/configdoc"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

// New refuses Deps without a store and otherwise binds the API without
// calling the store.
func TestUnitNew(t *testing.T) {
	if m, err := New(Deps{}); m != nil || err == nil ||
		err.Error() != "configuration: Deps.Store is required" {
		t.Errorf("New without a store = %v, %v; want the required-store error", m, err)
	}

	// Any call on this store panics, so New succeeding shows it does no I/O.
	settings := configapi.Settings{DockerSubdomainMatch: true}
	m, err := New(Deps{Store: struct{ Store }{}, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	if api := m.API(); api.Descriptor == nil || api.System == nil {
		t.Errorf("API = %+v, want Descriptor and System bound", api)
	}
	if got := m.Settings(); got != settings {
		t.Errorf("Settings = %+v, want %+v", got, settings)
	}
}

// The descriptor binding's configuration documents are the store's, with
// its answers and errors as it gave them.
func TestUnitConfigsReachTheStore(t *testing.T) {
	ctx := t.Context()
	st := inmem.New()
	c := configs{st: st}
	const kind, key = configdoc.ConfigProxy, "corporate"
	doc := configdoc.ConfigDocument{Kind: kind, Key: key, Enabled: true, Document: []byte(`{}`)}
	if err := c.Put(ctx, doc); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, kind, key)
	if err != nil || got.Key != key || !got.Enabled {
		t.Errorf("Get = %+v, %v; want the document put", got, err)
	}
	stored, err := st.Configs().Get(ctx, kind, key)
	if err != nil || stored.Key != got.Key {
		t.Errorf("store's Get = %+v, %v; want the document put through configs", stored, err)
	}
	list, err := c.List(ctx, kind)
	if err != nil || len(list) != 1 || list[0].Key != key {
		t.Errorf("List = %+v, %v; want the one document", list, err)
	}
	if err := c.Delete(ctx, kind, key); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, kind, key); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
	if err := c.Delete(ctx, kind, key); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("Delete twice = %v, want ErrNotFound", err)
	}
}
