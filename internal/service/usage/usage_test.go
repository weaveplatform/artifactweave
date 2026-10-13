package usage_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/usage"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
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
		{Key: "fed", Class: repo.Federated},
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
		keys = append(keys, r.Key+":"+r.Type)
	}
	if got := strings.Join(
		keys,
		",",
	); got != "all:VIRTUAL,fed:FEDERATED,libs:LOCAL,up-cache:CACHE" {
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
}

var errBoom = errors.New("boom")

type failing struct {
	*inmem.Store
	op string
}

func (f failing) Blobs() store.BlobIndex { return failBlobs{f.Store.Blobs(), f.op} }
func (f failing) Items() store.ItemStore { return failItems{f.Store.Items(), f.op} }
func (f failing) Repos() store.RepoStore { return failRepos{f.Store.Repos(), f.op} }

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
	for _, op := range []string{"totals", "usage", "list"} {
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
