package configuration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/descriptor"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/logging"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
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
	// means slog.Default(). The descriptor and logger policy emit no records.
	Log *slog.Logger
	// Logging is the process logging mechanism shared with app and the other modules.
	Logging *logging.Control
}

// Module is the configuration module as app holds it. It is safe for
// concurrent use: its bindings serialize temporary logger session changes.
type Module struct {
	api      configapi.API
	settings configapi.Settings
	loggers  *system.Loggers
}

// New binds configuration/api's Descriptor, System and Mail over d with the
// descriptor core. It does no I/O, and calls nothing on the store until a
// method of the API is called; it refuses only Deps without a store.
func New(d Deps) (*Module, error) {
	if d.Store == nil {
		return nil, errNoStore
	}
	api := descriptor.NewAPI(descriptor.Deps{
		Tx:              d.Store,
		Repositories:    d.Repositories,
		Layouts:         d.Layouts,
		Configs:         configs{st: d.Store},
		AnonymousAccess: d.Access.AnonymousAccess,
		Projects:        d.Access.Projects,
	})
	var loggers *system.Loggers
	if d.Logging != nil {
		loggers = system.NewLoggers(system.LoggerDeps{Control: d.Logging, Authorize: d.loggerAdministrator})
		api.Loggers = loggers
	}
	return &Module{api: api, settings: d.Settings, loggers: loggers}, nil
}

// API returns the descriptor and the current system/mail settings readers.
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

// Close restores logger levels and joins the temporary session's timer. A nil
// Module is safe during cleanup of a partially constructed runtime.
func (m *Module) Close() {
	if m != nil && m.loggers != nil {
		m.loggers.Close()
	}
}

func (d Deps) loggerAdministrator(ctx context.Context) error {
	if _, err := kernel.RequireAdmin(ctx); err != nil {
		return fmt.Errorf("logger administrator: %w", err)
	}
	if d.Access.Projects == nil || d.Access.Authenticator == nil || d.Access.Authorizer == nil {
		return fmt.Errorf("logger authority unavailable: %w", kernel.ErrStore)
	}
	err := d.Store.Tx(ctx, func(ctx context.Context) error {
		if err := d.Access.Projects.Guard(ctx); err != nil {
			return fmt.Errorf("guard logger administration: %w", err)
		}
		p, _ := kernel.FromContext(ctx)
		p, err := d.Access.Authenticator.Current(ctx, p)
		if errors.Is(err, kernel.ErrNotFound) {
			return kernel.ErrBadCredentials
		}
		if err != nil {
			return fmt.Errorf("current logger credential: %w", err)
		}
		decider, err := d.Access.Authorizer.For(ctx, p)
		if err != nil {
			return fmt.Errorf("current logger permissions: %w", err)
		}
		if decider == nil || !decider.IsAdmin() {
			return kernel.ErrForbidden
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("authorize logger administration: %w", err)
	}
	return nil
}
