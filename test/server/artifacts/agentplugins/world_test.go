package agentplugins_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	agentpluginsfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/agentplugins"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/agentcommon"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// queuedWorld is agentcommon's world, the users deployer and user and the
// repository libs-local included, with the runtime's work queue, which
// the marketplace files are stored from. No worker runs it: settle runs
// the marketplace work as a server's workers would once an upload has
// answered, and a download of a marketplace file runs the work it owes
// itself. Items is the item service over the same store and queue, for a
// reindex.
type queuedWorld struct {
	*agentcommon.World
	Queue *work.Queue
	Items *item.Service
}

func newQueuedWorld(t *testing.T) *queuedWorld {
	t.Helper()
	db := inmem.New()
	support.Seed(t, db)
	for _, name := range []string{"deployer", "user"} {
		if err := db.Users().Create(t.Context(), accesstest.User{
			Name: name, PasswordHash: support.CheapHash,
		}); err != nil {
			t.Fatal(err)
		}
	}
	c := repo.Config{Key: "libs-local", Class: repo.Local}
	if err := db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(
		db.Users(),
		db.Settings(),
		authn.Options{Hash: support.TestHash},
	)
	if err != nil {
		t.Fatal(err)
	}
	q, err := work.New(db, work.Options{})
	if err != nil {
		t.Fatal(err)
	}
	svc := &item.Service{Store: db, Blobs: blobs, Work: q, Formats: ecosystem.For}
	if err := svc.RegisterWork(q); err != nil {
		t.Fatal(err)
	}
	h := harness.Router(harness.Config{
		BaseURL: "https://reg.example", CompatVersion: "7.155.0",
		Store: db, Blobs: blobs, Auth: a, Work: q,
	})
	return &queuedWorld{
		World: &agentcommon.World{T: t, Handler: h, DB: db, Blobs: blobs},
		Queue: q, Items: svc,
	}
}

// settle runs the marketplace work the repository key owes.
func (w *queuedWorld) settle(key string) {
	w.T.Helper()
	if err := w.Queue.EnsureFresh(w.T.Context(), agentpluginsfmt.KindMarketplace, key); err != nil {
		w.T.Fatal(err)
	}
}
