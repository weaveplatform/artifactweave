package descriptor

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
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
			db := exportSQLStore(t, engine)
			for _, writer := range []string{"descriptor", "repository"} {
				t.Run(writer, func(t *testing.T) { testExportSnapshot(t, db, writer) })
			}
		})
	}
}

func exportSQLStore(t *testing.T, engine string) *appstoretest.SQLStore {
	t.Helper()
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
	return db
}

type ambientExportRepositories struct {
	artifactsapi.Repositories
	reads int
}

func (r *ambientExportRepositories) List(ctx context.Context, f artifactsapi.RepoFilter) ([]repo.Config, error) {
	r.reads++
	return r.Repositories.List(ctx, f)
}

type ambientExportProjects struct {
	accessapi.Projects
	reads int
}

func (p *ambientExportProjects) Guard(ctx context.Context) error {
	p.reads++
	return p.Projects.Guard(ctx)
}

func TestAccExportRefusesAmbientSQLTransaction(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgres"} {
		t.Run(engine, func(t *testing.T) {
			db := exportSQLStore(t, engine)
			for _, outcome := range []string{"commit", "rollback"} {
				t.Run(outcome, func(t *testing.T) { testExportAmbientTransaction(t, db, outcome) })
			}
		})
	}
}

func testExportAmbientTransaction(t *testing.T, db applyStore, outcome string) {
	t.Helper()
	d := depsOf(t, db)
	repositories := &ambientExportRepositories{Repositories: d.Repositories}
	projects := &ambientExportProjects{Projects: accesstest.Over(t, db).API().Projects}
	d.Repositories, d.Projects = repositories, projects
	key := "outer-" + outcome
	patch, err := Parse(fmt.Appendf(nil, "proxies:\n  %s:\n    host: pending.invalid\n    port: 3128\n", key), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
	err = d.Tx.Tx(ctx, func(ctx context.Context) error {
		// Apply still joins the caller's transaction and follows its eventual
		// outcome; the read API must not publish that provisional state.
		if _, err := Apply(ctx, d, patch, now, false); err != nil {
			return err
		}
		repositories.reads, projects.reads = 0, 0
		body, err := NewAPI(d).Export.XML(ctx)
		if !errors.Is(err, kernel.ErrStore) || body != nil {
			t.Errorf("export returned provisional XML: %q %v", body, err)
		}
		if repositories.reads != 0 || projects.reads != 0 {
			t.Error("ambient export read metadata before refusing the call")
		}
		if outcome == "rollback" {
			return errExportFailure
		}
		return nil
	})
	if outcome == "rollback" {
		if !errors.Is(err, errExportFailure) {
			t.Fatal("outer rollback changed", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	_, body := exportXML(t, d)
	if strings.Contains(body, key) != (outcome == "commit") {
		t.Fatal("nested Apply no longer followed its outer transaction", body)
	}
}

func testExportSnapshot(t *testing.T, db applyStore, writer string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	d := depsOf(t, db)
	d.Projects = accesstest.Over(t, db).API().Projects
	plan, err := Parse(
		[]byte("localRepositories:\n  snapshot-local:\n    type: generic\nproxies:\n  snapshot-proxy:\n    host: before.invalid\n    port: 3128\n"+
			"backups:\n  snapshot-backup:\n    cronExp: 0 0 2 * * ?\n    excludedRepositories: [snapshot-local]\n"),
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
			patch, err := Parse([]byte("proxies:\n  snapshot-proxy:\n    host: after.invalid\n"), noEnv)
			if err == nil {
				writeDeps := d
				writeDeps.Tx = writeTx
				_, err = Apply(ctx, writeDeps, patch, now, false)
			}
			writeResult <- err
		} else {
			// This is the owning repository API's publication: its transaction and
			// Projects.Guard are used by HTTP repository create/update/delete too.
			// The export reads repository keys for the backup's exclusions, so a
			// deletion it must not tear is the repository writer's visible change.
			writeResult <- writeTx.Tx(ctx, func(ctx context.Context) error {
				if err := d.Projects.Guard(ctx); err != nil {
					return err
				}
				_, _, err := d.Repositories.Remove(ctx, "snapshot-local", "")
				return err
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
	if xmlText(t, node, "backups/backup/excludedRepositories/repositoryRef") != "snapshot-local" || xmlText(t, node, "proxies/proxy/host") != "before.invalid" {
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
	node, body := exportXML(t, d)
	if writer == "descriptor" && xmlText(t, node, "proxies/proxy/host") != "after.invalid" ||
		writer == "repository" && strings.Contains(body, "<repositoryRef>") {
		t.Fatal("writer never became visible", body)
	}
}
