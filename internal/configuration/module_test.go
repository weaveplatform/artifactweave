package configuration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/logging"
)

// New refuses Deps without a store and otherwise binds the API without
// calling the store.
func TestUnitNew(t *testing.T) {
	if m, err := New(Deps{}); m != nil || err == nil ||
		err.Error() != "configuration: Deps.Store is required" {
		t.Errorf("New without a store = %v, %v; want the required-store error", m, err)
	}

	// Any call on this store panics, so New succeeding shows it does no I/O.
	settings := configapi.Settings{DockerSubdomainMatch: true, BaseURL: "https://reg.example", CDNHosts: []string{"cdn.example"}}
	m, err := New(Deps{Store: struct{ Store }{}, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	if api := m.API(); api.Descriptor == nil || api.System == nil || api.WebServer == nil {
		t.Errorf("API = %+v, want Descriptor and System bound", api)
	}
	if got := m.Settings(); !reflect.DeepEqual(got, settings) {
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

func TestUnitExportRechecksModuleAuthorityWithLoggers(t *testing.T) {
	for _, stage := range []string{"credential revoked", "administrator demoted"} {
		t.Run(stage, func(t *testing.T) {
			st := inmem.New()
			var order []string
			record := func(ctx context.Context, label string) {
				t.Helper()
				if !st.InTx(ctx) {
					t.Fatal("export authority checked outside transaction")
				}
				order = append(order, label)
			}
			access := accessapi.API{
				Projects: &accessapitest.Projects{GuardFunc: func(ctx context.Context) error {
					record(ctx, "guard")
					return st.Projects().Guard(ctx)
				}},
				Authenticator: &accessapitest.Authenticator{CurrentFunc: func(ctx context.Context, p kernel.Principal) (kernel.Principal, error) {
					record(ctx, "credential")
					if stage == "credential revoked" {
						return kernel.Principal{}, kernel.ErrBadCredentials
					}
					return p, nil
				}},
				Authorizer: &accessapitest.Authorizer{DecideFunc: func(ctx context.Context, _ accessapi.Subject, _ bool) (accessapi.Decider, error) {
					record(ctx, "permissions")
					return &accessapitest.Decider{Admin: false}, nil
				}},
			}
			control := logging.New(slog.New(slog.NewJSONHandler(io.Discard, nil)))
			module, err := New(Deps{Store: st, Access: access, Logging: control})
			if err != nil {
				t.Fatal(err)
			}
			defer module.Close()
			ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
			body, err := module.API().Export.XML(ctx)
			wantErr := kernel.ErrForbidden
			wantOrder := []string{"guard", "credential", "permissions"}
			if stage == "credential revoked" {
				wantErr = kernel.ErrBadCredentials
				wantOrder = wantOrder[:2]
			}
			if body != nil || !errors.Is(err, wantErr) || !slices.Equal(order, wantOrder) {
				t.Fatalf("export after authority change = %q, %v, %v; want no XML, %v, %v", body, err, order, wantErr, wantOrder)
			}
			if module.API().Loggers == nil {
				t.Fatal("export binding displaced the runtime logger binding")
			}
		})
	}
}
