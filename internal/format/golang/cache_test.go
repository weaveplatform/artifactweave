package golang_test

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Only a remote repository's cache is indexed (pkgindex.Stored): a file in
// the remote's own items, which no download stores there, joins no version,
// just as a reindex would not read it. The cache's file does.
func TestRemoteIndexesOnlyItsCache(t *testing.T) {
	w := newWorld(t)
	c := repo.Config{
		Key: "go-remote", Class: repo.Remote, URL: "https://proxy.golang.org", PackageType: repo.Go,
	}
	repo.Defaults(&c)
	w.must(w.st.Repos().Create(w.ctx, c))
	for _, cached := range []bool{false, true} {
		_, err := w.items.Deploy(w.ctx, item.Deploy{
			Target: item.Target{Repo: c, Cached: cached, Path: dir + ".mod"},
			Body:   strings.NewReader("module example.com/m\n"), By: "test",
		})
		w.must(err)
		if _, _, ok := w.version(c, "example.com/m", "v1.0.0"); ok != cached {
			t.Fatalf("cached %v: indexed %v", cached, ok)
		}
	}
}
