package maven_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/maven"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var errWork = errors.New("work boom")

// workFaults fails the store methods a metadata run reads through, by name.
type workFaults struct {
	format.Store
	fail string
}

func (f workFaults) Repos() port.RepoStore { return faultyRepos{f.Store.Repos(), f.fail} }
func (f workFaults) Packages() port.PackageStore {
	return faultyRunPackages{f.Store.Packages(), f.fail}
}
func (f workFaults) Items() port.ItemStore { return faultyRunItems{f.Store.Items(), f.fail} }

type faultyRepos struct {
	port.RepoStore
	fail string
}

//nolint:wrapcheck // test double
func (r faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if r.fail == "Repos.Get" {
		return repo.Config{}, errWork
	}
	return r.RepoStore.Get(ctx, key)
}

type faultyRunPackages struct {
	port.PackageStore
	fail string
}

//nolint:wrapcheck // test double
func (p faultyRunPackages) Find(ctx context.Context, key, name string) (model.Package, error) {
	if p.fail == "Find" {
		return model.Package{}, errWork
	}
	return p.PackageStore.Find(ctx, key, name)
}

//nolint:wrapcheck // test double
func (p faultyRunPackages) Versions(ctx context.Context, id int64) ([]model.PackageVersion, error) {
	if p.fail == "Versions" {
		return nil, errWork
	}
	return p.PackageStore.Versions(ctx, id)
}

//nolint:wrapcheck // test double
func (p faultyRunPackages) Files(ctx context.Context, id int64) ([]int64, error) {
	if p.fail == "Files" {
		return nil, errWork
	}
	return p.PackageStore.Files(ctx, id)
}

type faultyRunItems struct {
	port.ItemStore
	fail string
}

//nolint:wrapcheck // test double
func (i faultyRunItems) GetByID(ctx context.Context, id int64) (model.Item, error) {
	if i.fail == "GetByID" {
		return model.Item{}, errWork
	}
	return i.ItemStore.GetByID(ctx, id)
}

//nolint:wrapcheck // test double
func (i faultyRunItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	p string,
) (model.Item, error) {
	if i.fail == "Get" {
		return model.Item{}, errWork
	}
	return i.ItemStore.Get(ctx, key, cached, p)
}

//nolint:wrapcheck // test double
func (i faultyRunItems) Search(
	ctx context.Context,
	q *aql.Query,
	o aql.Options,
) ([]aql.Hit, error) {
	if i.fail == "Search" {
		return nil, errWork
	}
	return i.ItemStore.Search(ctx, q, o)
}

//nolint:wrapcheck // test double
func (i faultyRunItems) Properties(ctx context.Context, id int64) (model.Properties, error) {
	if i.fail == "Properties" {
		return nil, errWork
	}
	return i.ItemStore.Properties(ctx, id)
}

// closedBlobs opens nothing.
type closedBlobs struct{ blob.Store }

func (closedBlobs) Open(context.Context, string) (io.ReadSeekCloser, blob.Info, error) {
	return nil, blob.Info{}, errWork
}

// Every failure a metadata run meets comes back from it, so the queue
// retries the item.
func TestUnitWorkFailures(t *testing.T) {
	plugin := model.Properties{maven.PluginProperty: {"true"}}
	const (
		pkg     = "maven-local/com.acme:app"
		noPom   = "maven-local/org.x:lib"
		plugins = "maven-local/com.acme.plugins"
	)
	for _, tc := range []struct {
		name, kind, key, fail string
		blobs, derive         bool // fail Blobs.Open, Derive
		underive              bool // fail Underive
	}{
		{name: "repository", kind: maven.KindMetadata, key: pkg, fail: "Repos.Get"},
		{name: "package", kind: maven.KindMetadata, key: pkg, fail: "Find"},
		{name: "versions", kind: maven.KindMetadata, key: pkg, fail: "Versions"},
		{name: "files", kind: maven.KindMetadata, key: pkg, fail: "Files"},
		{name: "file", kind: maven.KindMetadata, key: pkg, fail: "GetByID"},
		{name: "derive", kind: maven.KindMetadata, key: pkg, derive: true},
		{name: "stale search", kind: maven.KindMetadata, key: pkg, fail: "Search"},
		{name: "stale removal", kind: maven.KindMetadata, key: pkg, underive: true},
		{name: "stored metadata", kind: maven.KindMetadata, key: noPom, fail: "Get"},
		{name: "stored content", kind: maven.KindMetadata, key: noPom, blobs: true},
		{name: "plugin search", kind: maven.KindPlugins, key: plugins, fail: "Search"},
		{name: "plugin pom", kind: maven.KindPlugins, key: plugins, blobs: true},
		{name: "plugin stored", kind: maven.KindPlugins, key: plugins, fail: "Get"},
		{name: "plugin derive", kind: maven.KindPlugins, key: plugins, derive: true},
		{name: "package's plugins", kind: maven.KindMetadata, key: "maven-local/com.acme.plugins:p-plugin", blobs: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newMetaWorld(t, nil)
			w.deploy("com/acme/app/1.0/app-1.0.pom", nil)
			// A client's snapshot-level metadata is dropped with 202 unless
			// the repository is under deployer (#278). Stored then, it is
			// stale once the repository is back to unique, for the stale
			// removal case.
			behavior := w.c.SnapshotVersionBehavior
			w.c.SnapshotVersionBehavior = repo.Deployer
			w.deploy("com/acme/app/2.0-SNAPSHOT/maven-metadata.xml", nil)
			w.c.SnapshotVersionBehavior = behavior
			w.deploy("org/x/lib/1.0/lib-1.0.jar", nil)
			w.deploy("org/x/lib/maven-metadata.xml", nil)
			w.deploy("com/acme/plugins/p-plugin/1.0/p-plugin-1.0.pom", plugin,
				pomOf("com.acme.plugins", "p-plugin", "1.0", "maven-plugin", "P"))
			env := format.Env{
				Store: workFaults{w.db, tc.fail}, Blobs: w.blobs, Layouts: layouts{},
				Now: func() time.Time { return w.now },
				Derive: func(context.Context, format.Derived) (model.Item, error) {
					if tc.derive {
						return model.Item{}, errWork
					}
					return model.Item{}, nil
				},
				Underive: func(context.Context, repo.Config, bool, string) error {
					if tc.underive {
						return errWork
					}
					return nil
				},
			}
			if tc.blobs {
				env.Blobs = closedBlobs{w.blobs}
			}
			err := maven.Handler{}.Work(w.ctx, env, work.WorkItem{Kind: tc.kind, Key: tc.key})
			if !errors.Is(err, errWork) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// A hook whose queueing fails fails its write.
func TestUnitQueueFailures(t *testing.T) {
	w := newMetaWorld(t, nil)
	w.deploy("com/acme/app/1.0/app-1.0.pom", model.Properties{maven.PluginProperty: {"true"}})
	pom, err := w.db.Items().Get(w.ctx, w.c.Key, false, "com/acme/app/1.0/app-1.0.pom")
	if err != nil {
		t.Fatal(err)
	}
	h := maven.Handler{}
	refuse := func(context.Context, string, string, []byte) error { return errWork }
	env := format.Env{Store: w.db, Layouts: layouts{}, Enqueue: refuse}
	for name, err := range map[string]error{
		"deployed":  h.Deployed(w.ctx, env, w.c, pom),
		"deleted":   h.Deleted(w.ctx, env, w.c, []model.Item{pom}),
		"deleting":  h.Deleting(w.ctx, env, w.c, pom),
		"annotated": h.Annotated(w.ctx, env, w.c, pom, format.Annotation{Removed: []string{maven.PluginProperty}}),
		"properties": h.Deleting(w.ctx, format.Env{Store: workFaults{w.db, "Properties"}, Layouts: layouts{}},
			w.c, pom),
	} {
		if !errors.Is(err, errWork) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A POM too shallow to sit in a group queues no plugin metadata.
	top := model.Item{RepoKey: w.c.Key, Path: "app", Name: "app-1.0.pom"}
	if err := h.Annotated(w.ctx, env, w.c, top, format.Annotation{Set: model.Properties{
		maven.PluginProperty: {"true"},
	}}); err != nil {
		t.Errorf("shallow POM: %v", err)
	}
}
