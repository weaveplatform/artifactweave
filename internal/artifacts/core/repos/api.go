package repos

import (
	"context"
	"encoding/json"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artifactevents "github.com/weaveplatform/artifactweave/internal/artifacts/core/events"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/trash"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var _ artifactsapi.Repositories = repositories{}

// Store is what the repositories read and write: the repository
// configurations, the work queue a reindex request goes to, and what Remove
// deletes with a repository, its items and package index, and the trash can
// its content moves to (trash.Store). Every call joins the transaction ctx
// carries. Each composite store in app is a Store.
type Store interface {
	artifactevents.CacheStore
	trash.Store
	Repos() port.RepoStore
	Packages() port.PackageStore
	WorkQueue() work.WorkQueue
}

// NewRepositories returns the artifacts module's Repositories over st, which
// reads key pairs through keys and environments through projects for
// CheckKeypairs and CheckEnvironments. Create, Update and Remove coordinate
// canonical configuration with access-owned bindings and shares when projects
// is supplied; their writes and failures commit or roll back together. Reads
// and validation preserve the underlying operation's error identity. Every
// call joins the transaction ctx carries; keys and projects must use that same
// unit of work. Nothing is read or checked until a method is called.
func NewRepositories(
	st Store,
	keys accessapi.Keys,
	projects accessapi.Projects,
) artifactsapi.Repositories {
	return repositories{st: st, keys: keys, projects: projects}
}

type repositories struct {
	st       Store
	keys     accessapi.Keys
	projects accessapi.Projects
}

func (r repositories) ProjectRepository(ctx context.Context, key string) (artifactsapi.ProjectRepository, error) {
	return r.st.Repos().ProjectRepository(ctx, key) //nolint:wrapcheck // preserves upstream error identity
}

func (r repositories) Get(ctx context.Context, key string) (repo.Config, error) {
	//nolint:wrapcheck // preserves upstream error identity
	return r.st.Repos().Get(ctx, key)
}

func (r repositories) List(ctx context.Context, f artifactsapi.RepoFilter) ([]repo.Config, error) {
	//nolint:wrapcheck // preserves upstream error identity
	return r.st.Repos().List(ctx, port.RepoFilter(f))
}

func (r repositories) UsingKeypair(ctx context.Context, name string) ([]string, error) {
	//nolint:wrapcheck // preserves upstream error identity
	return r.st.Repos().UsingKeypair(ctx, name)
}

func (r repositories) Create(ctx context.Context, c repo.Config) error {
	return r.tx(ctx, func(ctx context.Context) error {
		if err := checkProjectPrefix(c); err != nil {
			return err
		}
		if err := r.st.Repos().Create(ctx, c); err != nil {
			return err //nolint:wrapcheck // preserves upstream error identity
		}
		if r.projects != nil {
			return r.projects.BindRepository(ctx, c.Key, c.ProjectKey, c.Environments, true) //nolint:wrapcheck // access owns binding context
		}
		return nil
	})
}

func (r repositories) Update(ctx context.Context, c repo.Config) error {
	return r.tx(ctx, func(ctx context.Context) error {
		if err := r.st.Repos().Update(ctx, c); err != nil {
			return err //nolint:wrapcheck // preserves upstream error identity
		}
		if r.projects != nil {
			return r.projects.BindRepository(ctx, c.Key, c.ProjectKey, c.Environments, false) //nolint:wrapcheck // access owns binding context
		}
		return nil
	})
}

func (r repositories) Remove(ctx context.Context, key, by string) (int64, bool, error) {
	var removed int64
	var trashed bool
	err := r.tx(ctx, func(ctx context.Context) error {
		var err error
		removed, trashed, err = Remove(ctx, r.st, key, by)
		if err == nil && r.projects != nil {
			err = r.projects.RemoveRepository(ctx, key)
		}
		return err //nolint:wrapcheck // preserves removal/binding error identity for the caller
	})
	return removed, trashed, err
}

func (r repositories) tx(ctx context.Context, fn func(context.Context) error) error {
	//nolint:wrapcheck // preserves upstream error identity
	return r.st.Tx(ctx, func(ctx context.Context) error {
		if r.projects != nil {
			if err := r.projects.Guard(ctx); err != nil {
				return err //nolint:wrapcheck // preserves authority-store error identity
			}
		}
		return fn(ctx)
	})
}

func (r repositories) RequestReindex(ctx context.Context, key, by string, at time.Time) error {
	// The payload is the one the configuration descriptor has always queued.
	// A WorkRequest that sets only By always encodes, so the error is nil.
	payload, _ := json.Marshal(format.WorkRequest{By: by})
	//nolint:wrapcheck // preserves upstream error identity
	return r.st.WorkQueue().Enqueue(ctx, format.KindReindex, key, payload, at)
}

func (repositories) Protected(key string) bool { return Protected(key) }

func (repositories) PrepareCreate(c repo.Config, existing []repo.Config) (repo.Config, error) {
	return PrepareCreate(c, existing)
}

func (repositories) CheckMembers(c, stored *repo.Config, known []repo.Config) error {
	return CheckMembers(c, stored, known)
}

func (r repositories) CheckKeypairs(ctx context.Context, c repo.Config) error {
	return CheckKeypairs(ctx, r.keys, c)
}

func (r repositories) CheckEnvironments(ctx context.Context, c *repo.Config) error {
	return CheckEnvironments(ctx, r.projects, c)
}

func (repositories) Merge(
	stored repo.Config,
	patch map[string]json.RawMessage,
) (repo.Config, error) {
	return Merge(stored, patch)
}

func (repositories) OwesReindex(stored, updated repo.Config) bool {
	return OwesReindex(stored, updated)
}
