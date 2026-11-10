package itemkit

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/session"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
)

// Store is what the item kit reads and writes, and what it hands on to its
// overlays and to the handlers built on it (Content): the item service's
// store (item.Store), over which the kit builds the item service and queues
// and awaits derived work, with the upload sessions, which the Docker REST
// operations' registry service keeps (registry.Store). It names no access
// or configuration-module store: the kit reaches the access module through
// Deps.Access. Each composite store in app is a Store.
type Store interface {
	item.Store
	Uploads() session.UploadStore
}

// Deps is what the item kit reads: the stores and services an item request
// reaches, and the hooks the composition supplies. The kit never reads the
// configuration module's core: the descriptor reaches it as
// DownloadRedirectMinSize.
type Deps struct {
	// Store is the metadata store; nil answers every item route like an
	// unimplemented operation (Content).
	Store Store
	// Blobs is the blob store files are served from and written to.
	Blobs blob.Store
	// Keys derives the key for short-lived download URLs.
	Keys *crypt.Keyring
	// Redirect tunes the download redirect policy (decision 0029).
	Redirect Redirect
	// RemoteSources is the direct remote read boundary; nil keeps remote
	// repositories cache-only. RegistrySources is Docker's, OCI's and OCI
	// Helm's.
	RemoteSources, RegistrySources *source.Service
	// Work is the runtime's work queue; nil writes derived work to the
	// store's queue directly (decision 0033).
	Work *work.Queue
	// Formats returns a package type's format; nil means FormatSettings.For.
	Formats format.Registry
	// FormatSettings are the formats' server-wide options.
	FormatSettings ecosystem.Settings
	// Access is the access module's API: the authorizer behind ItemCaller
	// and AccessFor, the key pairs and the reindex authentication.
	Access accessapi.API
	// EnforceIdenticalChecksumDeploy makes a checksum deploy over a file
	// with the same content need the delete permission (MayBeIdentical).
	EnforceIdenticalChecksumDeploy bool
	// DownloadRedirectMinSize reads the descriptor's minimum redirect size
	// on each redirect: ok false leaves Redirect.MinSize, and an error is
	// logged and leaves it too. Nil means no descriptor.
	DownloadRedirectMinSize func(context.Context) (int64, bool, error)
	// Logger receives the kit's logs; nil means slog.Default.
	Logger *slog.Logger
	// Overlays are the format routes an item GET tries before the generic
	// lookup, in dispatch order; GetItem stops at the first that answers.
	// Order matters: a format route takes precedence over the generic
	// lookup, and over every overlay after it.
	Overlays []Overlay
	// Private reports a path only its format's own protocol serves, whose
	// content the generic routes never hand out: a checksum deploy may not
	// reuse such a file's content. Nil reports no path private. Trash and
	// transfer test the format predicates themselves, because each one
	// relocates and archives its paths by its own rule.
	Private func(c repo.Config, path string) bool
}

// Overlay answers an item GET that a format serves itself, such as a
// virtual repository's generated index, and reports whether it wrote the
// response; false hands the request to the next overlay and then to the
// generic lookup. acc is the caller's access, and cached whether c's key
// named a remote repository's cache.
type Overlay func(
	k *Kit,
	w http.ResponseWriter,
	r *http.Request,
	st Store,
	c repo.Config,
	cached bool,
	req ItemRequest,
	p kernel.Principal,
	acc Access,
) bool

// Kit holds the item helpers that the artifacts management handlers and the
// ecosystem root handlers share. Build one with New, which sets what the
// zero value lacks.
type Kit struct {
	Deps
	// signWarn throttles the log line for a failing download-redirect
	// signer.
	signWarn *throttle
}

// New returns a kit over d. It does no I/O.
func New(d Deps) *Kit {
	if d.Private == nil {
		d.Private = func(repo.Config, string) bool { return false }
	}
	return &Kit{Deps: d, signWarn: newThrottle(signWarnEvery)}
}

// Content returns the store. A router built without one (tests of the
// routing alone) answers like an unimplemented operation.
func (s *Kit) Content() (Store, error) {
	if s.Store == nil {
		return nil, apierror.ErrNotImplemented
	}
	return s.Store, nil
}

// InternalError logs err and returns a 500 that names nothing internal.
func (s *Kit) InternalError(ctx context.Context, op string, err error) error {
	s.Log().ErrorContext(ctx, op, "request_id", httpkit.RequestIDFrom(ctx), "error", err)
	return apierror.New(http.StatusInternalServerError, "internal error")
}

// Log is the configured logger, or slog's default for handlers built
// without one in tests.
func (s *Kit) Log() *slog.Logger {
	if s.Logger == nil {
		return slog.Default()
	}
	return s.Logger
}

// NotFoundRepo is the 404 for a repository key that names none.
func NotFoundRepo(key string) error {
	return apierror.New(http.StatusNotFound, fmt.Sprintf("Repository '%s' does not exist", key))
}

// View is the configuration a reader receives: everything but secrets for
// an admin, the public subset for anyone else.
func View(p kernel.Principal, c repo.Config) repo.Config {
	if p.Admin {
		return repo.Masked(c)
	}
	return repo.Public(c)
}
