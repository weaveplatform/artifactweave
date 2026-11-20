package storetest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
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
	atList chan<- struct{}
}

func (r bindingDeleteRepositories) List(ctx context.Context, filter artifactsapi.RepoFilter) ([]repo.Config, error) {
	values, err := r.Repositories.List(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("probe lifecycle repository snapshot: %w", err)
	}
	close(r.atList)
	return values, nil
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
	atRead, release, atDelete, atList := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
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
	deleter := project.Service{Store: db, Repositories: bindingDeleteRepositories{
		Repositories: repositories, atList: atList,
	}}
	recreated := make(chan error, 1)
	running.Go(func() {
		close(atDelete)
		if err := deleter.DeleteProject(ctx, "beta"); err != nil {
			recreated <- err
			return
		}
		recreated <- s.CreateProject(ctx, model.Project{Key: "beta", DisplayName: "replacement"})
	})
	select {
	case <-atDelete:
	case <-ctx.Done():
		t.Fatal("concurrent project deletion never attempted its guard")
	}
	select {
	case <-atList:
		t.Error("deletion read canonical ownership while force attach held its snapshot")
	case <-time.After(20 * time.Millisecond):
	}
	// Deletion must snapshot ownership after the winning attachment commits.
	// A late guard at the final project-row delete misses the newly owned
	// repository and leaves its canonical projectKey in an obsolete lifetime.
	unblock()
	for _, result := range []<-chan error{attached, recreated} {
		select {
		case err := <-result:
			storekit.Must(t, err)
		case <-ctx.Done():
			t.Fatal("concurrent binding/lifecycle operations did not complete")
		}
	}
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
