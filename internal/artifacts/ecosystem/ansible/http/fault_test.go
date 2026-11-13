package ansiblehttp_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

var errStore = errors.New("injected store failure")

type failingStore struct {
	pkgroute.Store
	stage string
}

func (s failingStore) Packages() port.PackageStore {
	return failingPackages{s.Store.Packages(), s.stage}
}
func (s failingStore) Items() port.ItemStore { return failingItems{s.Store.Items(), s.stage} }
func (s failingStore) Repos() port.RepoStore { return failingRepos{s.Store.Repos(), s.stage} }

type failingRepos struct {
	port.RepoStore
	stage string
}

func (s failingRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if s.stage == "member" && key == "ansible-local" {
		return repo.Config{}, errStore
	}
	return s.RepoStore.Get(ctx, key)
}

type failingPackages struct {
	port.PackageStore
	stage string
}

func (s failingPackages) Find(ctx context.Context, key, name string) (model.Package, error) {
	if s.stage == "find" {
		return model.Package{}, errStore
	}
	return s.PackageStore.Find(ctx, key, name)
}

func (s failingPackages) Versions(ctx context.Context, id int64) ([]model.PackageVersion, error) {
	if s.stage == "versions" {
		return nil, errStore
	}
	v, err := s.PackageStore.Versions(ctx, id)
	if s.stage == "metadata" && len(v) > 0 {
		v[0].Metadata = []byte("{")
	}
	return v, err
}

// VersionNames and Version fail as Versions does: a version list reads the
// names, and then its page's versions one at a time.
func (s failingPackages) VersionNames(ctx context.Context, id int64) ([]string, error) {
	if s.stage == "versions" {
		return nil, errStore
	}
	return s.PackageStore.VersionNames(ctx, id)
}

func (s failingPackages) Version(
	ctx context.Context,
	id int64,
	version string,
) (model.PackageVersion, error) {
	if s.stage == "version" {
		return model.PackageVersion{}, errStore
	}
	v, err := s.PackageStore.Version(ctx, id, version)
	if s.stage == "metadata" {
		v.Metadata = []byte("{")
	}
	return v, err
}

type failingItems struct {
	port.ItemStore
	stage string
}

func (s failingItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (model.Item, error) {
	if strings.HasSuffix(path, ".tar.gz") {
		switch s.stage {
		case "get":
			return model.Item{}, errStore
		case "missing":
			return model.Item{}, kernel.ErrNotFound
		}
	}
	it, err := s.ItemStore.Get(ctx, key, cached, path)
	if s.stage == "checksum" {
		it.Actual.SHA256 = "wrong"
	}
	return it, err
}

func (s failingItems) FindByChecksum(
	ctx context.Context,
	c kernel.Checksums,
	page kernel.Page,
) ([]model.Item, string, error) {
	if s.stage == "task" {
		return nil, "", errStore
	}
	return s.ItemStore.FindByChecksum(ctx, c, page)
}

func TestAccGalaxyStoreFailuresNeverExposeSuccessfulMetadata(t *testing.T) {
	for _, stage := range []string{"find", "versions", "metadata", "get", "checksum", "task", "member", "missing"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture(t)
			requireStatus(t, f.publish(f.data, ""), 201)
			f.store = failingStore{f.db, stage}
			f.mount(nil)
			path := prefix + "/api/v3/collections/weave/sample/"
			want := 500
			if stage == "task" {
				path = prefix + "/api/v3/imports/collections/" + strings.Repeat("a", 64) + "/"
			}
			if stage == "member" {
				v := *repo.Defaults(&repo.Config{Key: "v", Class: repo.Virtual, PackageType: repo.Ansible, Repositories: []string{f.repo.Key}})
				if err := f.db.Repos().Create(t.Context(), v); err != nil {
					t.Fatal(err)
				}
				path = "/artifactory/api/ansible/v/api/v3/collections/weave/sample/"
			}
			if stage == "missing" {
				want = 404
			}
			requireStatus(t, f.do("GET", path, nil, "", ""), want)
		})
	}
}

func TestAccGalaxyRemoteArchiveIntegrityAndWriteDenial(t *testing.T) {
	f, up, c, base := remoteFixture(t)
	requireStatus(t, f.do("POST", base+"/api/v3/artifacts/collections/", nil, "", ""), 405)
	up.Set("/files/weave-sample-1.2.3.tar.gz", fakeupstream.Resource{Body: []byte("corrupt")})
	endpoint := base + "/api/v3/plugin/ansible/content/published/collections/artifacts/weave-sample-1.2.3.tar.gz"
	requireStatus(t, f.do("GET", endpoint, nil, "", ""), 502)
	if _, err := f.db.Items().
		Get(t.Context(), c.Key, true, "collections/weave/sample/1.2.3/weave-sample-1.2.3.tar.gz"); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatalf("corrupt archive published: %v", err)
	}
	up.Set(
		"/files/weave-sample-1.2.3.tar.gz",
		fakeupstream.Resource{Status: http.StatusServiceUnavailable},
	)
	requireStatus(t, f.do("GET", endpoint, nil, "", ""), 503)
}
