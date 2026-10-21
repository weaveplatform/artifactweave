package acceptance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
)

func (w *world) registerCache(sc *godog.ScenarioContext) {
	w.registerCacheMaintenance(sc)
	sc.Step(`^the remote "([^"]*)" caches the file "([^"]*)" at "([^"]*)"$`, w.cacheFile)
}

// cacheFile stores a scenario file in a remote repository's cache, without a
// network fetch, for the AQL cache-alias fixture (decision 0007). No
// API writes a cache, so the step deploys through the item service into the
// running server's default SQLite database and fs blob store under its data
// directory; the database's WAL and busy timeout let both processes write.
func (w *world) cacheFile(remote, file, target string) error {
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(w.dataDir, "artifactweave.db"),
		sqlite.Options{Options: sqlcommon.Options{SkipMigrate: true}})
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	blobs, err := fs.New(filepath.Join(w.dataDir, "blobs"))
	if err != nil {
		return err
	}
	c, err := st.Repos().Get(ctx, remote)
	if err != nil {
		return fmt.Errorf("repository %s: %w", remote, err)
	}
	f, err := os.Open(filepath.Join(w.dir, file))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = (&item.Service{Store: st, Blobs: blobs}).Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Cached: true, Path: target}, Body: f, By: "admin",
	})
	return err
}
