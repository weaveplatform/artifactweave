package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/repos"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type bindingSnapshotRepositories struct {
	artifactsapi.Repositories
	atRead  chan<- struct{}
	release <-chan struct{}
}

func (r bindingSnapshotRepositories) ProjectRepository(ctx context.Context, key string) (artifactsapi.ProjectRepository, error) {
	value, err := r.Repositories.ProjectRepository(ctx, key)
	if err != nil {
		return value, fmt.Errorf("read concurrent binding: %w", err)
	}
	close(r.atRead)
	select {
	case <-r.release:
		return value, nil
	case <-ctx.Done():
		return value, fmt.Errorf("wait for binding publication: %w", ctx.Err())
	}
}

type bindingDeleteRepositories struct {
	artifactsapi.Repositories
	listed *atomic.Int32
}

func (r bindingDeleteRepositories) List(ctx context.Context, filter artifactsapi.RepoFilter) ([]repo.Config, error) {
	values, err := r.Repositories.List(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("probe lifecycle repository snapshot: %w", err)
	}
	r.listed.Add(1)
	return values, nil
}

type bindingDeleteStore struct {
	Store
	entered *atomic.Bool
	reads   *atomic.Int32
}

func (s bindingDeleteStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	s.entered.Store(true)
	if err := s.Store.Tx(ctx, fn); err != nil {
		return fmt.Errorf("probe lifecycle transaction: %w", err)
	}
	return nil
}

func (s bindingDeleteStore) Projects() port.ProjectStore {
	return bindingDeleteProjects{ProjectStore: s.Store.Projects(), reads: s.reads}
}

type bindingDeleteProjects struct {
	port.ProjectStore
	reads *atomic.Int32
}

func (p bindingDeleteProjects) Get(ctx context.Context, key string) (model.Project, error) {
	if err := ctx.Err(); err != nil {
		return model.Project{}, fmt.Errorf("lifecycle read began after its deadline: %w", err)
	}
	p.reads.Add(1)
	value, err := p.ProjectStore.Get(ctx, key)
	if err != nil {
		return value, fmt.Errorf("probe lifecycle read: %w", err)
	}
	return value, nil
}

func testProjectRepositoryBindingLifetime(t *testing.T, db Store) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for _, key := range []string{"alpha", "beta"} {
		storekit.Must(t, db.Projects().Create(ctx, model.Project{Key: key, DisplayName: key}))
	}
	const key = "existing-arbitrary-key"
	storekit.Must(t, db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: key, Class: repo.Local, PackageType: repo.Generic})))
	repositories := repos.NewRepositories(db, nil, access.ProjectsOf(db))
	s := project.Service{Store: db, Repositories: repositories}
	storekit.Must(t, s.AttachRepository(ctx, key, "alpha", false))
	storekit.Must(t, s.ShareRepository(ctx, key, "beta", false, true))
	before := storekit.Get[model.ProjectAuthority](t)(db.Projects().Authority(ctx, "beta"))
	atRead, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	var running sync.WaitGroup
	defer func() { unblock(); cancel(); running.Wait() }()
	attacher := s
	attacher.Repositories = bindingSnapshotRepositories{Repositories: repositories, atRead: atRead, release: release}
	attached := make(chan error, 1)
	running.Go(func() { attached <- attacher.AttachRepository(ctx, key, "beta", true) })
	select {
	case <-atRead:
	case <-ctx.Done():
		t.Fatal("force attach never read the guarded repository snapshot")
	}
	var entered atomic.Bool
	var reads, listed atomic.Int32
	deletingStore := bindingDeleteStore{Store: db, entered: &entered, reads: &reads}
	deleter := project.Service{Store: deletingStore, Repositories: bindingDeleteRepositories{
		Repositories: repositories, listed: &listed,
	}}
	// Call deletion synchronously while attachment holds the snapshot. Its
	// deadline must expire before it can read canonical ownership. Recording
	// the actual transaction/read entry avoids a delayed second goroutine
	// turning this into a sequential test. Memory/SQLite wait at Tx entry;
	// PostgreSQL enters Tx, then waits in the guarded project read.
	blocked, stop := context.WithTimeout(ctx, time.Second)
	err := deleter.DeleteProject(blocked, "beta")
	ended := blocked.Err()
	stop()
	if err == nil || !errors.Is(ended, context.DeadlineExceeded) || !entered.Load() {
		t.Fatalf("concurrent deletion did not wait inside its transaction: entered=%t context=%v error=%v", entered.Load(), ended, err)
	}
	if sql, ok := db.(*appsql.Store); ok && sql.Dialect().ForUpdate != "" && reads.Load() != 1 {
		t.Fatalf("row-locking deletion never reached its actual project read: %d", reads.Load())
	}
	if listed.Load() != 0 {
		t.Fatal("deletion read canonical ownership before acquiring the current project guard")
	}
	unblock()
	select {
	case err := <-attached:
		storekit.Must(t, err)
	case <-ctx.Done():
		t.Fatal("guarded attachment did not complete")
	}
	storekit.Must(t, s.DeleteProject(ctx, "beta"))
	storekit.Must(t, s.CreateProject(ctx, model.Project{Key: "beta", DisplayName: "replacement"}))
	after := storekit.Get[model.ProjectAuthority](t)(db.Projects().Authority(ctx, "beta"))
	if after.Incarnation == before.Incarnation {
		t.Fatal("the project was not recreated with a fresh lifetime")
	}
	configuration := storekit.Get[repo.Config](t)(db.Repos().Get(ctx, key))
	if configuration.ProjectKey != "" || len(configuration.Environments) != 0 {
		t.Fatalf("recreated project inherited the obsolete canonical attachment: %+v", configuration)
	}
	status := storekit.Get[model.RepositoryAssignment](t)(s.RepositoryStatus(ctx, key))
	if status.ProjectKey != "" || len(status.Environments) != 0 || len(status.SharedWith) != 0 {
		t.Fatalf("Access status retained an obsolete owner or target share: %+v", status)
	}
}
