package configuration

import (
	"context"
	"errors"
	"log/slog"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/migration/artifacts/api"
	configapi "github.com/weaveplatform/artifactweave/migration/configuration/api"
	"github.com/weaveplatform/artifactweave/migration/configuration/core/descriptor"
	"github.com/weaveplatform/artifactweave/migration/platform/configdoc"
	"github.com/weaveplatform/artifactweave/migration/platform/tx"
)

// Store is what the module applies descriptors through on the metadata
// store: the unit of work Apply writes in, and the configuration documents
// (the proxies, property sets, layouts and the downloadRedirectConfig
// section). Each composite store in app satisfies it. Every call joins the
// transaction ctx carries.
type Store interface {
	tx.Runner
	Configs() configdoc.ConfigStore
}

// errNoStore refuses Deps without a store, which every apply writes.
var errNoStore = errors.New("configuration: Deps.Store is required")

// Deps is what app passes New: the store and the other modules' contracts
// that a descriptor is applied through, and the module's settings.
type Deps struct {
	// Store is required.
	Store Store
	// Repositories reads, checks and writes the repositories of the four
	// repository sections; the artifacts module's API binds it over the same
	// store.
	Repositories artifactsapi.Repositories
	// Layouts checks each repoLayouts entry before it is stored.
	Layouts artifactsapi.Layouts
	// Access is the access module's API, through whose AnonymousAccess the
	// descriptor writes the security section's anonymous access switch.
	Access accessapi.API
	// Settings are the configuration options of the module's code, from
	// app/config's Configuration, which Settings returns.
	Settings configapi.Settings
	// Log receives the logs of the services the module builds itself; nil
	// means slog.Default(). New builds none, and the descriptor binding logs
	// nothing, so New does not read it.
	Log *slog.Logger
}

// Module is the configuration module as app holds it. It is safe for
// concurrent use: it holds only its bindings and settings.
type Module struct {
	api      configapi.API
	settings configapi.Settings
}

// New binds configuration/api's Descriptor and System over d with the
// descriptor core. It does no I/O, and calls nothing on the store until a
// method of the API is called; it refuses only Deps without a store.
func New(d Deps) (*Module, error) {
	if d.Store == nil {
		return nil, errNoStore
	}
	return &Module{
		api: descriptor.NewAPI(descriptor.Deps{
			Tx:              d.Store,
			Repositories:    d.Repositories,
			Layouts:         d.Layouts,
			Configs:         configs{st: d.Store},
			AnonymousAccess: d.Access.AnonymousAccess,
		}),
		settings: d.Settings,
	}, nil
}

// API returns the module's published surface: the Descriptor that parses and
// applies configuration descriptors, and the System that other modules read
// the descriptor's settings through.
func (m *Module) API() configapi.API { return m.api }

// Settings returns the Settings from Deps.
func (m *Module) Settings() configapi.Settings { return m.settings }

// configs reaches the store's configuration documents on every call rather
// than once in New, so New calls nothing on the store. The errors are the
// store's, unwrapped, for errors.Is and each caller's own wrap.
type configs struct{ st Store }

func (c configs) Put(ctx context.Context, d configdoc.ConfigDocument) error {
	//nolint:wrapcheck // the store's errors pass through
	return c.st.Configs().Put(ctx, d)
}

func (c configs) Get(
	ctx context.Context,
	kind configdoc.ConfigKind,
	key string,
) (configdoc.ConfigDocument, error) {
	//nolint:wrapcheck // the store's errors pass through
	return c.st.Configs().Get(ctx, kind, key)
}

func (c configs) List(
	ctx context.Context,
	kind configdoc.ConfigKind,
) ([]configdoc.ConfigDocument, error) {
	//nolint:wrapcheck // the store's errors pass through
	return c.st.Configs().List(ctx, kind)
}

func (c configs) Delete(ctx context.Context, kind configdoc.ConfigKind, key string) error {
	//nolint:wrapcheck // the store's errors pass through
	return c.st.Configs().Delete(ctx, kind, key)
}
