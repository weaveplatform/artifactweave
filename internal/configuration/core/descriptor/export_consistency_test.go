package descriptor

import (
	"context"
	"encoding/xml"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
)

type pausedExportRepositories struct {
	artifactsapi.Repositories
	read    chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *pausedExportRepositories) List(ctx context.Context, f artifactsapi.RepoFilter) ([]repo.Config, error) {
	repos, err := r.Repositories.List(ctx, f)
	r.once.Do(func() {
		close(r.read)
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	})
	return repos, err
}

type observedExportTx struct {
	tx.Runner
	attempted chan struct{}
	once      sync.Once
}

func (p *observedExportTx) Tx(ctx context.Context, fn func(context.Context) error) error {
	p.once.Do(func() { close(p.attempted) })
	return p.Runner.Tx(ctx, fn)
}

func TestAccExportSnapshotSerializesDescriptorAndRepositoryWriters(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			var db *appstoretest.SQLStore
			var err error
			if engine == "sqlite" {
				db, err = appstoretest.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "export.db"), appstoretest.SQLiteOptions{})
			} else {
				dsn := sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
				db, err = appstoretest.OpenPostgres(t.Context(), dsn, appstoretest.PostgresOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			for _, writer := range []string{"descriptor", "repository"} {
				t.Run(writer, func(t *testing.T) { testExportSnapshot(t, db, writer) })
			}
		})
	}
}

func testExportSnapshot(t *testing.T, db applyStore, writer string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	d := depsOf(t, db)
	d.Projects = accesstest.Over(t, db).API().Projects
	plan, err := Parse(
		[]byte("localRepositories:\n  snapshot-local:\n    type: generic\n    description: before\nproxies:\n  snapshot-proxy:\n    host: before.invalid\n    port: 3128\n"),
		noEnv,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(ctx, d, plan, now, false); err != nil {
		t.Fatal(err)
	}
	paused := &pausedExportRepositories{Repositories: d.Repositories, read: make(chan struct{}), release: make(chan struct{})}
	readDeps := d
	readDeps.Repositories = paused
	type result struct {
		body []byte
		err  error
	}
	readResult := make(chan result, 1)
	go func() {
		body, err := NewAPI(readDeps).Export.XML(kernel.WithPrincipal(ctx, kernel.Principal{Name: "admin", Admin: true}))
		readResult <- result{body, err}
	}()
	select {
	case <-paused.read:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// SQLite serializes writers at BeginTx, PostgreSQL at Projects.Guard. Observe
	// the transaction attempt so the same proof covers both locking boundaries.
	writeTx := &observedExportTx{Runner: d.Tx, attempted: make(chan struct{})}
	writeResult := make(chan error, 1)
	go func() {
		if writer == "descriptor" {
			patch, err := Parse([]byte("localRepositories:\n  snapshot-local:\n    description: after\nproxies:\n  snapshot-proxy:\n    host: after.invalid\n"), noEnv)
			if err == nil {
				writeDeps := d
				writeDeps.Tx = writeTx
				_, err = Apply(ctx, writeDeps, patch, now, false)
			}
			writeResult <- err
		} else {
			// This is the owning repository API's publication: its transaction and
			// Projects.Guard are used by HTTP repository create/update/delete too.
			writeResult <- writeTx.Tx(ctx, func(ctx context.Context) error {
				if err := d.Projects.Guard(ctx); err != nil {
					return err
				}
				c, err := d.Repositories.Get(ctx, "snapshot-local")
				if err != nil {
					return err
				}
				c.Description = "after"
				return d.Repositories.Update(ctx, c)
			})
		}
	}()
	select {
	case <-writeTx.attempted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-writeResult:
		close(paused.release)
		t.Fatalf("writer passed the live snapshot guard: %v", err)
	case <-time.After(50 * time.Millisecond):
	case <-ctx.Done():
		close(paused.release)
		t.Fatal(ctx.Err())
	}
	close(paused.release)
	var got result
	select {
	case got = <-readResult:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	var node exportNode
	if err := xml.Unmarshal(got.body, &node); err != nil {
		t.Fatal(err)
	}
	if xmlText(t, node, "localRepositories/localRepository/description") != "before" || xmlText(t, node, "proxies/proxy/host") != "before.invalid" {
		t.Fatal("torn export", string(got.body))
	}
	select {
	case err := <-writeResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	node, _ = exportXML(t, d)
	if xmlText(t, node, "localRepositories/localRepository/description") != "after" {
		t.Fatal("writer never became visible")
	}
}
