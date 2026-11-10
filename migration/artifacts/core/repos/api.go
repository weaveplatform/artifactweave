package repos

import (
	"context"
	"encoding/json"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/migration/artifacts/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/trash"
	"github.com/weaveplatform/artifactweave/migration/platform/work"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

var _ artifactsapi.Repositories = repositories{}

// Store is what the repositories read and write: the repository
// configurations, the work queue a reindex request goes to, and what Remove
// deletes with a repository, its items and package index, and the trash can
// its content moves to (trash.Store). Every call joins the transaction ctx
// carries. Each composite store in app is a Store.
type Store interface {
	trash.Store
	Repos() port.RepoStore
	Packages() port.PackageStore
	WorkQueue() work.WorkQueue
}

// NewRepositories returns the artifacts module's Repositories over st, which
// reads key pairs through keys and environments through projects for
// CheckKeypairs and CheckEnvironments. Each method calls st's repository
// store or work queue, or this package's function of the same name, once,
// and returns its results as they are, so a caller sees exactly what a
// direct call returns. Every call uses the context it is given, so it takes
// part in a transaction of st that the context carries; keys and projects
// must take part in that transaction too, as the access module's bindings
// over st do. Nothing is read or checked until a method is called.
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

func (r repositories) Get(ctx context.Context, key string) (repo.Config, error) {
	//nolint:wrapcheck // preserves upstream error identity
	return r.st.Repos().Get(ctx, key)
}

func (r repositories) List(ctx context.Context, f artifactsapi.RepoFilter) ([]repo.Config, error) {
	//nolint:wrapcheck // preserves upstream error identity
	return r.st.Repos().List(ctx, store.RepoFilter(f))
}

func (r repositories) UsingKeypair(ctx context.Context, name string) ([]string, error) {
	//nolint:wrapcheck // preserves upstream error identity
	return r.st.Repos().UsingKeypair(ctx, name)
}

func (r repositories) Create(ctx context.Context, c repo.Config) error {
	//nolint:wrapcheck // preserves upstream error identity
	return r.st.Repos().Create(ctx, c)
}

func (r repositories) Update(ctx context.Context, c repo.Config) error {
	//nolint:wrapcheck // preserves upstream error identity
	return r.st.Repos().Update(ctx, c)
}

func (r repositories) Remove(ctx context.Context, key, by string) (int64, bool, error) {
	return Remove(ctx, r.st, key, by)
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

func (repositories) CheckMembers(c repo.Config, known []repo.Config) error {
	return CheckMembers(c, known)
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
