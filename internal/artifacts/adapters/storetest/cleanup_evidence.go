package storetest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

// RunCleanupDownloads checks the bounded cleanup observation contract.
func RunCleanupDownloads(t *testing.T, newStore Factory) {
	t.Helper()
	testCleanupDownloads(t, newStore(t))
}

var errCleanupDownloadRollback = errors.New("rollback download evidence")

func testCleanupDownloads(t *testing.T, s port.Store) {
	t.Helper()
	ctx := t.Context()
	c := *repo.Defaults(&repo.Config{
		Key: "cleanup-evidence", Class: repo.Federated, PackageType: repo.NPM,
		Members: []repo.Member{{URL: repo.SelfMemberURL("cleanup-evidence"), Enabled: repo.Flex(true)}},
		Notes:   "private-unrelated-fixture-data",
	})
	storekit.Must(t, s.Repos().Create(ctx, c))
	p := storekit.Get[model.Package](t)(s.Packages().Ensure(ctx, model.Package{RepoKey: c.Key, Type: repo.NPM, Name: "widget"}))
	requests := make([]api.DownloadVersion, 0, api.MaxDownloadVersions)
	var firstVersion, firstItem int64
	for n := range api.MaxDownloadVersions {
		version := fmt.Sprintf("1.0.%d", n)
		v := storekit.Get[model.PackageVersion](t)(s.Packages().PutVersion(ctx, model.PackageVersion{PackageID: p.ID, Version: version, Created: storekit.At(1)}))
		it := storekit.Get[model.Item](t)(s.Items().Put(ctx, model.Item{
			RepoKey: c.Key, Path: "widget", Name: version + ".tgz", Depth: 2,
			Created: storekit.At(1), Size: 1, Actual: kernel.Checksums{SHA256: "fixture-content"},
		}))
		storekit.Must(t, s.Packages().LinkFile(ctx, model.VersionFile{VersionID: v.ID, ItemID: it.ID}))
		requests = append(requests, api.DownloadVersion{Package: p.Name, Version: version})
		if n == 0 {
			firstVersion, firstItem = v.ID, it.ID
		}
	}
	reader := pkgindex.NewCleanupMetadata(s)
	repository := storekit.Get[api.CleanupRepository](t)(reader.Repository(ctx, c.Key))
	if repository.Incarnation == "" || repository.Generation == "" || len(repository.Members) != 1 {
		t.Fatal("cleanup metadata omitted its current lifetime or membership")
	}
	if strings.Contains(fmt.Sprintf("%+v", repository), c.Notes) {
		t.Fatal("cleanup context opened unrelated repository data")
	}
	rows := storekit.Get[[]api.VersionDownload](t)(reader.Downloads(ctx, repository, requests))
	if len(rows) != api.MaxDownloadVersions {
		t.Fatal("maximum supported download batch was not returned", len(rows))
	}
	one := []api.DownloadVersion{requests[0]}
	read := func() api.VersionDownload {
		t.Helper()
		rows := storekit.Get[[]api.VersionDownload](t)(reader.Downloads(ctx, repository, one))
		if len(rows) != 1 {
			t.Fatal("existing indexed version has no coherent download observation")
		}
		return rows[0]
	}
	initial := read()
	storekit.Must(t, s.Items().SetProperties(ctx, firstItem, model.Properties{"changed": {"yes"}}, storekit.At(2)))
	storekit.Same(t, "non-download changes do not invent new download evidence", initial.Revision, read().Revision)
	storekit.Must(t, s.Items().RecordDownload(ctx, firstItem, port.Download{At: storekit.At(3), By: "reader"}))
	downloaded := read()
	if downloaded.Revision == initial.Revision || !downloaded.LastDownloaded.Equal(storekit.Want(storekit.At(3))) {
		t.Fatalf("item-only download did not protect its version before the aggregate write: initial=%+v downloaded=%+v", initial, downloaded)
	}
	storekit.Must(t, s.Packages().RecordVersionDownload(ctx, firstVersion, storekit.At(3)))
	if read().Revision == downloaded.Revision {
		t.Fatal("a same-time download reused old evidence authority")
	}
	v := storekit.Get[model.PackageVersion](t)(s.Packages().Version(ctx, p.ID, requests[0].Version))
	unchanged := read()
	v.Metadata = []byte(`{"changed":"metadata only"}`)
	_, err := s.Packages().PutVersion(ctx, v)
	storekit.Must(t, err)
	storekit.Same(t, "metadata edits preserve download authority", unchanged.Revision, read().Revision)
	v.LastDownloaded = storekit.At(2)
	_, err = s.Packages().PutVersion(ctx, v)
	storekit.Must(t, err)
	if read().Revision == unchanged.Revision {
		t.Fatal("aggregate statistics replacement reused old download evidence")
	}
	storekit.Must(t, s.Items().PutStats(ctx, firstItem, model.ItemStats{RemoteLastDownloaded: storekit.At(4)}))
	latest := read()
	if !latest.LastDownloaded.Equal(storekit.Want(storekit.At(4))) {
		t.Fatal("known remote use was discarded from the conservative download bound")
	}
	rollback := errCleanupDownloadRollback
	storekit.WantErr(t, s.Tx(ctx, func(ctx context.Context) error {
		if err := s.Items().RecordDownload(ctx, firstItem, port.Download{At: storekit.At(9)}); err != nil {
			return fmt.Errorf("record rollback download: %w", err)
		}
		return rollback
	}), rollback)
	storekit.Same(t, "rollback restores data and evidence generation", latest, read())
	absent := storekit.Get[[]api.VersionDownload](t)(reader.Downloads(ctx, repository, []api.DownloadVersion{{Package: "foreign", Version: "1"}}))
	if len(absent) != 0 {
		t.Fatal("absent versions became invented never-downloaded observations")
	}
	storekit.Must(t, s.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "foreign-evidence", Class: repo.Local, PackageType: repo.NPM})))
	for _, kind := range []string{"foreign", "cached", "folder"} {
		version := storekit.Get[model.PackageVersion](t)(s.Packages().PutVersion(ctx, model.PackageVersion{PackageID: p.ID, Version: "only-" + kind, Created: storekit.At(1)}))
		item := model.Item{RepoKey: c.Key, Name: "only-" + kind + ".tgz", Created: storekit.At(1)}
		switch kind {
		case "foreign":
			item.RepoKey = "foreign-evidence"
		case "cached":
			item.Cached = true
		case "folder":
			item.Folder = true
		}
		linked := storekit.Get[model.Item](t)(s.Items().Put(ctx, item))
		storekit.Must(t, s.Packages().LinkFile(ctx, model.VersionFile{VersionID: version.ID, ItemID: linked.ID}))
		observed := storekit.Get[[]api.VersionDownload](t)(reader.Downloads(ctx, repository, []api.DownloadVersion{{Package: p.Name, Version: version.Version}}))
		if len(observed) != 0 {
			t.Fatal("non-hosted or foreign index links invented version evidence", kind)
		}
	}
	for _, invalid := range [][]api.DownloadVersion{nil, append(append([]api.DownloadVersion{}, requests...), requests[0]), {requests[0], requests[0]}} {
		_, err := reader.Downloads(ctx, repository, invalid)
		storekit.WantErr(t, err, kernel.ErrInvalid)
	}
	// Returning to identical member values cannot revive an earlier scope.
	c.Members[0].Enabled = repo.Flex(false)
	storekit.Must(t, s.Repos().Update(ctx, c))
	c.Members[0].Enabled = repo.Flex(true)
	storekit.Must(t, s.Repos().Update(ctx, c))
	_, err = reader.Downloads(ctx, repository, one)
	storekit.WantErr(t, err, kernel.ErrConflict)
	fresh := storekit.Get[api.CleanupRepository](t)(reader.Repository(ctx, c.Key))
	if fresh.Incarnation != repository.Incarnation || fresh.Generation == repository.Generation {
		t.Fatal("membership edits did not fence the existing repository lifetime")
	}
	storekit.Must(t, s.Repos().Delete(ctx, c.Key))
	storekit.Must(t, s.Repos().Create(ctx, c))
	recreated := storekit.Get[api.CleanupRepository](t)(reader.Repository(ctx, c.Key))
	if recreated.Incarnation == repository.Incarnation {
		t.Fatal("recreated repository reused old download authority")
	}
	_, err = reader.Downloads(ctx, fresh, one)
	storekit.WantErr(t, err, kernel.ErrConflict)
}
