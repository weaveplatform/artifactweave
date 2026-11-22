package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/weaveplatform/artifactweave/internal/access"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/gc"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/repos"
	nimmodelhttp "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nimmodel/http"
	"github.com/weaveplatform/artifactweave/internal/configuration"
	"github.com/weaveplatform/artifactweave/internal/federation"
	"github.com/weaveplatform/artifactweave/internal/notifications"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/events"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/internal/retention"
)

// modules are the modules app builds, which the runtime holds for serve and
// close.
type modules struct {
	access        *access.Module
	artifacts     *artifacts.Module
	configuration *configuration.Module
	retention     *retention.Module
	federation    *federation.Module
	events        *events.Bus
	notifications *notifications.Module
}

// moduleStore is the metadata store the modules are built over: the store
// type each module's constructor declares.
type moduleStore interface {
	access.Store
	artifacts.Store
	configuration.Store
	notifications.Store
	retention.Store
	federation.Store
}

// moduleDeps is what OpenRuntime opens before the modules and buildModules
// builds them over.
type moduleDeps struct {
	store moduleStore
	// keys is the master key's keyring, nil without a master key.
	keys *crypt.Keyring
	// auth and tokens are the authenticator and the token service that the
	// access module binds. Opening them reads the store and crypto/rand, so
	// OpenRuntime opens them at their places in the start-up order, before
	// the blob tiers.
	auth   *authn.Authenticator
	tokens *token.Service
	// blobs is the hot tier's blob store.
	blobs blob.Store
	work  *work.Queue
	// events is the shared local bus. Consumers register during module
	// assembly; OpenRuntime registers durable cursors before starting Work.
	events *events.Bus
	// gc is the garbage collector over app's multipart and registry upload
	// services, whose loop the artifacts module runs.
	gc  *gc.Collector
	log *slog.Logger
}

// buildModules builds the access, artifacts, configuration, notifications and retention modules over
// d with cfg's settings, in that order, each over the APIs of the ones
// before it. The constructors do no I/O, so building them once the work
// queue is open moves none of OpenRuntime's I/O; a constructor's refusal is
// returned wrapped with its module's name.
func buildModules(cfg config.Config, d moduleDeps) (modules, error) {
	acc, err := access.New(access.Deps{
		Store: d.store, Authenticator: d.auth, Tokens: d.tokens,
		ProjectRepositories: repos.NewProjectRepositoryReader(d.store),
		Settings:            cfg.Access(),
		Log:                 d.log,
	})
	if err != nil {
		return modules{}, fmt.Errorf("access: %w", err)
	}
	accessAPI := acc.API()
	fed, err := federation.New(federation.Deps{
		UserQueryLimit: cfg.Search.UserQueryLimit,
		Lifetimes:      repos.NewRepositoryLifetimes(d.store),
		Store:          d.store, Access: accessAPI, Ownership: repos.NewProjectRepositoryLookup(d.store),
		Repositories: repos.NewRepositories(d.store, accessAPI.Keys, accessAPI.Projects),
	})
	if err != nil {
		return modules{}, fmt.Errorf("federation: %w", err)
	}
	art, err := artifacts.New(artifacts.Deps{
		Store: d.store, Blobs: d.blobs, Keys: d.keys, Access: accessAPI,
		RepositoryLifecycle: fed.RepositoryLifecycle(),
		Work:                d.work, GC: d.gc, Settings: cfg.Artifacts(), Log: d.log,
		// The artifacts module root may not import an ecosystem's http
		// package, where NIM's upload preparation lives; app may.
		PrepareNIMModelUpload: nimmodelhttp.PrepareUpload,
	})
	if err != nil {
		return modules{}, fmt.Errorf("artifacts: %w", err)
	}
	artifactsAPI := art.API()
	conf, err := configuration.New(configuration.Deps{
		Store:        d.store,
		Repositories: artifactsAPI.Repositories,
		Layouts:      artifactsAPI.Layouts,
		Access:       accessAPI,
		Settings:     cfg.Configuration(),
		Log:          d.log,
	})
	if err != nil {
		return modules{}, fmt.Errorf("configuration: %w", err)
	}
	notify, err := notifications.New(notifications.Deps{
		Store: d.store, Access: accessAPI, Repositories: artifactsAPI.Repositories, ServiceURL: cfg.BaseURL,
	})
	if err != nil {
		return modules{}, fmt.Errorf("notifications: %w", err)
	}
	ret, err := retention.New(
		retention.Deps{
			Store:        d.store,
			Access:       accessAPI,
			Repositories: repos.NewProjectRepositoryLookup(d.store),
			PackageIndex: artifactsAPI.PackageIndex,
			ItemRemoval:  artifactsAPI.ItemRemoval,
		},
	)
	if err != nil {
		return modules{}, fmt.Errorf("retention: %w", err)
	}
	return modules{access: acc, artifacts: art, configuration: conf, retention: ret, federation: fed, events: d.events, notifications: notify}, nil
}

// tokenKeyBits is the access-token RSA key size OpenRuntime uses when its
// options set none; zero means the production size. Tests set a small one
// so every runtime they open stays cheap.
var tokenKeyBits int

// openTokens creates the instance identity when missing and the token
// service over it, which creates the first signing key of keyBits bits.
func openTokens(ctx context.Context, st token.Store, keyBits int) (*token.Service, error) {
	if err := instance.EnsureInstance(ctx, st); err != nil {
		return nil, err //nolint:wrapcheck // EnsureInstance's errors start "instance identity:"
	}
	tokens, err := token.New(ctx, st, token.Options{KeyBits: keyBits})
	if err != nil {
		return nil, fmt.Errorf("access tokens: %w", err)
	}
	return tokens, nil
}
