package storetest

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/apilimit"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/session"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/outbox"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// Store is every store the platform owns. All of them join the caller's
// unit of work.
type Store interface {
	tx.Runner
	Settings() settings.SettingsStore
	Leases() work.LeaseStore
	WorkQueue() work.WorkQueue
	Configs() configdoc.ConfigStore
	Instance() instance.InstanceStore
	Outbox() outbox.OutboxStore
	Uploads() session.UploadStore
	Replays() idempotency.ReplayStore
	RequestBudgets() apilimit.RequestBudgetStore
}

// Factory returns a fresh, empty store whose config store notifies
// watches, and registers the store's cleanup with t.
type Factory func(t *testing.T, watches ...configdoc.Watch) Store

// Run runs the platform contract on stores from newStore, one subtest per
// concern, each on a store of its own, the watch cases included.
func Run(t *testing.T, newStore Factory) {
	t.Helper()
	RunWithoutWatches(t, func(t *testing.T) Store { return newStore(t) })
	runConfigWatches(t, newStore)
}

// RunWithoutWatches runs every case of Run except the watch cases, on
// stores from newStore. A composite store, which registers watches of its
// own, calls it: the watch cases need a store whose only watches are
// theirs.
func RunWithoutWatches(t *testing.T, newStore func(t *testing.T) Store) {
	t.Helper()
	runPlatformOperations(t, newStore)
	runWork(t, newStore)
	runInstance(t, newStore)
	runOutbox(t, newStore)
	runUploads(t, newStore)
	runReplays(t, newStore)
	runRequestBudgets(t, newStore)
}
