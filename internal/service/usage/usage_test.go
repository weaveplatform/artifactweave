package usage_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/usage"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestSummarize(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	blobs, err := fs.New(root)
	if err != nil {
		t.Fatal(err)
	}
	db := inmem.New()
	svc := &item.Service{Store: db, Blobs: blobs}
	configs := map[string]repo.Config{}
	for _, c := range []repo.Config{
		{Key: "libs", Class: repo.Local, PackageType: repo.Generic},
		{Key: "up", Class: repo.Remote, URL: "https://up.example"},
		{Key: "all", Class: repo.Virtual, Repositories: []string{"libs"}},
		{Key: "fed", Class: repo.Federated, ProjectKey: "proj"},
	} {
		if err := db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		configs[c.Key], _ = db.Repos().Get(ctx, c.Key)
	}
	for _, d := range []struct {
		repo   string
		cached bool
		path   string
		body   string
	}{
		{"libs", false, "a/one.txt", "same content"},
		{"libs", false, "two.txt", "same content"},
		{"up", true, "c.txt", "cached"},
	} {
		if _, err := svc.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: configs[d.repo], Cached: d.cached, Path: d.path},
			Body:   strings.NewReader(d.body),
		}); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := usage.Summarize(
		ctx,
		db,
		usage.Location{Type: "file-system", Directory: root, Root: root},
	)
	if err != nil {
		t.Fatal(err)
	}
	if sum.BinariesCount != 2 || sum.ArtifactsCount != 3 || sum.ItemsCount != 4 ||
		sum.ArtifactsSize != int64(2*len("same content")+len("cached")) {
		t.Fatalf("totals %+v", sum)
	}
	keys := make([]string, 0, len(sum.Rows))
	for _, r := range sum.Rows {
		keys = append(keys, r.Key+":"+r.Type+":"+string(r.PackageType)+":"+r.ProjectKey)
	}
	// The trash can has its row while it is empty, as 7.x lists it on an
	// empty instance.
	if got := strings.Join(keys, ","); got != "all:VIRTUAL:generic:default,"+
		"auto-trashcan:NA::default,fed:FEDERATED:generic:proj,libs:LOCAL:generic:default,"+
		"up-cache:CACHE:generic:default" {
		t.Fatalf("rows %s", got)
	}
	if sum.Space == nil || sum.Space.Total <= 0 {
		t.Fatalf("a local store reports its volume: %+v", sum.Space)
	}
	if sum, err = usage.Summarize(
		ctx,
		db,
		usage.Location{Type: "s3"},
	); err != nil ||
		sum.Space != nil {
		t.Fatalf("object storage capacity is unknown: %+v, %v", sum.Space, err)
	}
	if sum, err = usage.Summarize(
		ctx,
		db,
		usage.Location{Root: root + "/absent"},
	); err != nil ||
		sum.Space != nil {
		t.Fatalf("an unreadable volume is unknown: %+v, %v", sum.Space, err)
	}

	// The trash can is counted as the repository 7.x keeps it in: a folder
	// deleted with its content, and one path deleted twice, which is one
	// file there of the size last deleted.
	for _, d := range []struct{ path, body string }{
		{"gone/deep/x.txt", "xx"},
		{"b.txt", "first"},
		{"b.txt", "second!"},
	} {
		target := item.Target{Repo: configs["libs"], Path: d.path}
		if _, err := svc.Deploy(ctx, item.Deploy{
			Target: target, Body: strings.NewReader(d.body),
		}); err != nil {
			t.Fatal(err)
		}
		if d.path == "gone/deep/x.txt" {
			target.Path = "gone"
		}
		if _, err := svc.Delete(ctx, target, "admin"); err != nil {
			t.Fatal(err)
		}
	}
	if sum, err = usage.Summarize(ctx, db, usage.Location{}); err != nil {
		t.Fatal(err)
	}
	var bin usage.Row
	for _, r := range sum.Rows {
		if r.Key == "auto-trashcan" {
			bin = r
		}
	}
	// Files gone/deep/x.txt and b.txt; folders libs, libs/gone and
	// libs/gone/deep.
	if bin.Files != 2 || bin.Folders != 3 || bin.Bytes != int64(len("xx")+len("second!")) {
		t.Fatalf("trash row %+v", bin)
	}
	if sum.ArtifactsCount != 3+2 || sum.ItemsCount != 4+5 ||
		sum.ArtifactsSize != int64(2*len("same content")+len("cached"))+bin.Bytes {
		t.Fatalf("totals include the trash can: %+v", sum)
	}
}

func TestSummarizeReadsTheWholeTrash(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	// More entries than one page of the trash listing holds.
	n := store.MaxLimit + 1
	for i := range n {
		if _, err := db.Trash().Add(ctx, types.TrashEntry{Record: types.TrashRecord{
			Item: types.Item{RepoKey: "bulk", Path: ".", Name: fmt.Sprintf("f%04d", i), Size: 1},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	sum, err := usage.Summarize(ctx, db, usage.Location{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Rows) != 1 || sum.Rows[0].Files != int64(n) || sum.Rows[0].Bytes != int64(n) ||
		sum.Rows[0].Folders != 1 {
		t.Fatalf("rows %+v", sum.Rows)
	}
}

var errBoom = errors.New("boom")

type failing struct {
	*inmem.Store
	op string
}

func (f failing) Blobs() store.BlobIndex  { return failBlobs{f.Store.Blobs(), f.op} }
func (f failing) Items() store.ItemStore  { return failItems{f.Store.Items(), f.op} }
func (f failing) Repos() store.RepoStore  { return failRepos{f.Store.Repos(), f.op} }
func (f failing) Trash() store.TrashStore { return failTrash{f.Store.Trash(), f.op} }

type failTrash struct {
	store.TrashStore
	op string
}

func (f failTrash) List(
	ctx context.Context, repoKey, prefix string, p store.Page,
) ([]types.TrashEntry, string, error) {
	if f.op == "trash" {
		return nil, "", errBoom
	}
	return f.TrashStore.List(ctx, repoKey, prefix, p) //nolint:wrapcheck // test double
}

type failBlobs struct {
	store.BlobIndex
	op string
}

func (f failBlobs) Totals(ctx context.Context) (int64, int64, error) {
	if f.op == "totals" {
		return 0, 0, errBoom
	}
	return f.BlobIndex.Totals(ctx) //nolint:wrapcheck // test double
}

type failItems struct {
	store.ItemStore
	op string
}

func (f failItems) Usage(ctx context.Context) ([]store.RepoUsage, error) {
	if f.op == "usage" {
		return nil, errBoom
	}
	return f.ItemStore.Usage(ctx) //nolint:wrapcheck // test double
}

type failRepos struct {
	store.RepoStore
	op string
}

func (f failRepos) List(ctx context.Context, filter store.RepoFilter) ([]repo.Config, error) {
	if f.op == "list" {
		return nil, errBoom
	}
	return f.RepoStore.List(ctx, filter) //nolint:wrapcheck // test double
}

func TestSummarizeFailures(t *testing.T) {
	for _, op := range []string{"totals", "usage", "list", "trash"} {
		if _, err := usage.Summarize(
			context.Background(),
			failing{inmem.New(), op},
			usage.Location{},
		); !errors.Is(
			err,
			errBoom,
		) {
			t.Errorf("%s: %v", op, err)
		}
	}
}
