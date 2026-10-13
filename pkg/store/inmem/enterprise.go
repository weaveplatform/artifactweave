package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// bundleKey identifies one release bundle version.
type bundleKey struct{ Name, Version string }

func (k bundleKey) String() string { return k.Name + "/" + k.Version }

// bundleAudit is an audit record with the sequence number that orders
// records created in the same microsecond.
type bundleAudit struct {
	ID    int64
	Audit types.BundleRetentionAudit
}

// jobKey identifies one distribution job.
type jobKey struct {
	Tracker int64
	Peer    string
}

// workerEntry identifies one state entry or secret of a worker.
type workerEntry struct{ Worker, Name string }

// Bundles implements store.Store.
func (s *Store) Bundles() store.BundleStore { return bundleStore{s} }

// Distribution implements store.Store.
func (s *Store) Distribution() store.DistributionStore { return distributionStore{s} }

// Archive implements store.Store.
func (s *Store) Archive() store.ArchiveStore { return archiveStore{s} }

// Workers implements store.Store.
func (s *Store) Workers() store.WorkerStore { return workerStore{s} }

// --- Release bundles -------------------------------------------------------

type bundleStore struct{ s *Store }

func (b bundleStore) bundles() *table[bundleKey, types.ReleaseBundle] {
	return tableOf[bundleKey, types.ReleaseBundle](b.s, "release_bundles")
}

func (b bundleStore) artifacts() *table[bundleKey, []types.BundleArtifact] {
	return tableOf[bundleKey, []types.BundleArtifact](b.s, "bundle_artifacts")
}

func (b bundleStore) keeps() *table[bundleKey, bool] {
	return tableOf[bundleKey, bool](b.s, "bundle_keeps")
}

func (b bundleStore) audits() *table[int64, bundleAudit] {
	return tableOf[int64, bundleAudit](b.s, "bundle_audits")
}

func (b bundleStore) exports() *table[bundleKey, types.BundleExport] {
	return tableOf[bundleKey, types.BundleExport](b.s, "bundle_exports")
}

// putArtifacts stores a bundle's artifact list, owned by key and sorted by
// target path; ErrConflict when two share a target path.
func (b bundleStore) putArtifacts(key bundleKey, list []types.BundleArtifact) error {
	list = slices.Clone(list)
	for i := range list {
		list[i].BundleName, list[i].BundleVersion = key.Name, key.Version
	}
	slices.SortFunc(list, func(x, y types.BundleArtifact) int { return strings.Compare(x.TargetPath, y.TargetPath) })
	for i := 1; i < len(list); i++ {
		if list[i].TargetPath == list[i-1].TargetPath {
			return conflict("bundle artifact "+key.String(), list[i].TargetPath)
		}
	}
	b.artifacts().put(key, list)
	return nil
}

func (b bundleStore) Create(ctx context.Context, rb types.ReleaseBundle, list []types.BundleArtifact) error {
	return b.s.do(ctx, func() error {
		key := bundleKey{rb.Name, rb.Version}
		if b.bundles().has(key) {
			return conflict("release bundle", key)
		}
		if err := b.putArtifacts(key, list); err != nil {
			return err
		}
		b.bundles().put(key, rb)
		return nil
	})
}

func (b bundleStore) Get(ctx context.Context, name, version string) (types.ReleaseBundle, error) {
	var out types.ReleaseBundle
	err := b.s.do(ctx, func() error {
		var ok bool
		if out, ok = b.bundles().get(bundleKey{name, version}); !ok {
			return notFound("release bundle", bundleKey{name, version})
		}
		return nil
	})
	return out, err
}

func (b bundleStore) Update(ctx context.Context, rb types.ReleaseBundle) error {
	return b.s.do(ctx, func() error {
		key := bundleKey{rb.Name, rb.Version}
		if !b.bundles().has(key) {
			return notFound("release bundle", key)
		}
		b.bundles().put(key, rb)
		return nil
	})
}

func (b bundleStore) ReplaceArtifacts(ctx context.Context, name, version string, list []types.BundleArtifact) error {
	return b.s.do(ctx, func() error {
		key := bundleKey{name, version}
		cur, ok := b.bundles().get(key)
		if !ok {
			return notFound("release bundle", key)
		}
		if !cur.State.Editable() {
			return conflict("release bundle "+string(cur.State), key)
		}
		return b.putArtifacts(key, list)
	})
}

func (b bundleStore) Delete(ctx context.Context, name, version string) error {
	return b.s.do(ctx, func() error {
		key := bundleKey{name, version}
		if !b.bundles().del(key) {
			return notFound("release bundle", key)
		}
		b.artifacts().del(key)
		b.keeps().del(key)
		b.exports().del(key)
		return nil
	})
}

func (b bundleStore) List(
	ctx context.Context, f store.BundleFilter, p store.Page,
) ([]types.ReleaseBundle, string, error) {
	var (
		out  []types.ReleaseBundle
		next string
	)
	err := b.s.do(ctx, func() error {
		rows := b.bundles().all(func(_ bundleKey, v types.ReleaseBundle) bool {
			return (f.Name == "" || v.Name == f.Name) &&
				(f.Origin == "" || v.Origin == f.Origin) &&
				(f.ProjectKey == "" || v.ProjectKey == f.ProjectKey)
		}, func(x, y types.ReleaseBundle) int {
			return cmp.Or(strings.Compare(x.Name, y.Name), strings.Compare(x.Version, y.Version))
		})
		var err error
		out, next, err = paginate(rows, p, false, func(v types.ReleaseBundle) []any {
			return []any{v.Name, v.Version}
		})
		return err
	})
	return out, next, err
}

func (b bundleStore) Artifacts(ctx context.Context, name, version string) ([]types.BundleArtifact, error) {
	var out []types.BundleArtifact
	err := b.s.do(ctx, func() error {
		key := bundleKey{name, version}
		if !b.bundles().has(key) {
			return notFound("release bundle", key)
		}
		out, _ = b.artifacts().get(key)
		return nil
	})
	return out, err
}

func (b bundleStore) SetKeep(ctx context.Context, name, version string, keep bool) error {
	return b.s.do(ctx, func() error {
		key := bundleKey{name, version}
		if !b.bundles().has(key) {
			return notFound("release bundle", key)
		}
		if keep {
			b.keeps().put(key, true)
		} else {
			b.keeps().del(key)
		}
		return nil
	})
}

func (b bundleStore) Kept(ctx context.Context, name, version string) (bool, error) {
	var kept bool
	err := b.s.do(ctx, func() error {
		key := bundleKey{name, version}
		if !b.bundles().has(key) {
			return notFound("release bundle", key)
		}
		kept = b.keeps().has(key)
		return nil
	})
	return kept, err
}

func (b bundleStore) AddAudit(ctx context.Context, a types.BundleRetentionAudit) error {
	return b.s.do(ctx, func() error {
		id := b.s.nextID("bundle_audits")
		b.audits().put(id, bundleAudit{ID: id, Audit: a})
		return nil
	})
}

func (b bundleStore) Audits(ctx context.Context, name string) ([]types.BundleRetentionAudit, error) {
	var out []types.BundleRetentionAudit
	err := b.s.do(ctx, func() error {
		rows := b.audits().all(func(_ int64, v bundleAudit) bool {
			return name == "" || v.Audit.BundleName == name
		}, func(x, y bundleAudit) int {
			return cmp.Or(y.Audit.Created.Compare(x.Audit.Created), cmp.Compare(y.ID, x.ID))
		})
		for _, r := range rows {
			out = append(out, r.Audit)
		}
		return nil
	})
	return out, err
}

func (b bundleStore) PutExport(ctx context.Context, e types.BundleExport) error {
	return b.s.do(ctx, func() error {
		key := bundleKey{e.BundleName, e.BundleVersion}
		if !b.bundles().has(key) {
			return notFound("release bundle", key)
		}
		b.exports().put(key, e)
		return nil
	})
}

func (b bundleStore) Export(ctx context.Context, name, version string) (types.BundleExport, error) {
	var out types.BundleExport
	err := b.s.do(ctx, func() error {
		var ok bool
		if out, ok = b.exports().get(bundleKey{name, version}); !ok {
			return notFound("bundle export", bundleKey{name, version})
		}
		return nil
	})
	return out, err
}

func (b bundleStore) DeleteExport(ctx context.Context, name, version string) error {
	return b.s.do(ctx, func() error {
		if !b.exports().del(bundleKey{name, version}) {
			return notFound("bundle export", bundleKey{name, version})
		}
		return nil
	})
}

// --- Distribution ----------------------------------------------------------

type distributionStore struct{ s *Store }

func (d distributionStore) trackers() *table[int64, types.DistributionTracker] {
	return tableOf[int64, types.DistributionTracker](d.s, "distribution_trackers")
}

func (d distributionStore) jobs() *table[jobKey, types.DistributionJob] {
	return tableOf[jobKey, types.DistributionJob](d.s, "distribution_jobs")
}

func (d distributionStore) CreateTracker(
	ctx context.Context, t types.DistributionTracker, jobs []types.DistributionJob,
) (types.DistributionTracker, error) {
	var out types.DistributionTracker
	err := d.s.do(ctx, func() error {
		seen := map[string]bool{}
		for _, j := range jobs {
			if seen[j.PeerInstanceID] {
				return conflict("distribution job peer", j.PeerInstanceID)
			}
			seen[j.PeerInstanceID] = true
		}
		t.ID = d.s.nextID("distribution_trackers")
		d.trackers().put(t.ID, t)
		for _, j := range jobs {
			j.TrackerID = t.ID
			d.jobs().put(jobKey{t.ID, j.PeerInstanceID}, j)
		}
		out, _ = d.trackers().get(t.ID)
		return nil
	})
	return out, err
}

func (d distributionStore) Tracker(ctx context.Context, id int64) (types.DistributionTracker, error) {
	var out types.DistributionTracker
	err := d.s.do(ctx, func() error {
		var ok bool
		if out, ok = d.trackers().get(id); !ok {
			return notFound("distribution tracker", id)
		}
		return nil
	})
	return out, err
}

func (d distributionStore) UpdateTracker(ctx context.Context, t types.DistributionTracker) error {
	return d.s.do(ctx, func() error {
		if !d.trackers().has(t.ID) {
			return notFound("distribution tracker", t.ID)
		}
		d.trackers().put(t.ID, t)
		return nil
	})
}

func (d distributionStore) Trackers(ctx context.Context, name, version string) ([]types.DistributionTracker, error) {
	var out []types.DistributionTracker
	err := d.s.do(ctx, func() error {
		out = d.trackers().all(func(_ int64, v types.DistributionTracker) bool {
			return (name == "" || v.BundleName == name) && (version == "" || v.BundleVersion == version)
		}, func(x, y types.DistributionTracker) int { return cmp.Compare(y.ID, x.ID) })
		return nil
	})
	return out, err
}

func (d distributionStore) PutJob(ctx context.Context, j types.DistributionJob) error {
	return d.s.do(ctx, func() error {
		if !d.trackers().has(j.TrackerID) {
			return notFound("distribution tracker", j.TrackerID)
		}
		d.jobs().put(jobKey{j.TrackerID, j.PeerInstanceID}, j)
		return nil
	})
}

func compareJobs(x, y types.DistributionJob) int {
	return cmp.Or(cmp.Compare(x.TrackerID, y.TrackerID), strings.Compare(x.PeerInstanceID, y.PeerInstanceID))
}

func (d distributionStore) Jobs(ctx context.Context, trackerID int64) ([]types.DistributionJob, error) {
	var out []types.DistributionJob
	err := d.s.do(ctx, func() error {
		if !d.trackers().has(trackerID) {
			return notFound("distribution tracker", trackerID)
		}
		out = d.jobs().all(func(k jobKey, _ types.DistributionJob) bool { return k.Tracker == trackerID }, compareJobs)
		return nil
	})
	return out, err
}

func (d distributionStore) JobsForPeer(
	ctx context.Context, peer string, statuses []types.JobStatus,
) ([]types.DistributionJob, error) {
	var out []types.DistributionJob
	err := d.s.do(ctx, func() error {
		out = d.jobs().all(func(k jobKey, v types.DistributionJob) bool {
			return k.Peer == peer && (len(statuses) == 0 || slices.Contains(statuses, v.Status))
		}, compareJobs)
		return nil
	})
	return out, err
}

// --- Archive ---------------------------------------------------------------

type archiveStore struct{ s *Store }

func (a archiveStore) policies() *table[string, types.ArchivePolicy] {
	return tableOf[string, types.ArchivePolicy](a.s, "archive_policies")
}

func (a archiveStore) executions() *table[int64, types.ArchiveExecution] {
	return tableOf[int64, types.ArchiveExecution](a.s, "archive_executions")
}

func (a archiveStore) items() *table[int64, types.ArchivedItem] {
	return tableOf[int64, types.ArchivedItem](a.s, "archived_items")
}

// ColdBlobs implements store.ArchiveStore.
func (a archiveStore) ColdBlobs() store.BlobIndex { return blobIndex{s: a.s, table: "cold_blobs"} }

func (a archiveStore) CreatePolicy(ctx context.Context, p types.ArchivePolicy) error {
	return a.s.do(ctx, func() error {
		if a.policies().has(p.Key) {
			return conflict("archive policy", p.Key)
		}
		a.policies().put(p.Key, p)
		return nil
	})
}

func (a archiveStore) Policy(ctx context.Context, key string) (types.ArchivePolicy, error) {
	var out types.ArchivePolicy
	err := a.s.do(ctx, func() error {
		var ok bool
		if out, ok = a.policies().get(key); !ok {
			return notFound("archive policy", key)
		}
		return nil
	})
	return out, err
}

func (a archiveStore) UpdatePolicy(ctx context.Context, p types.ArchivePolicy) error {
	return a.s.do(ctx, func() error {
		if !a.policies().has(p.Key) {
			return notFound("archive policy", p.Key)
		}
		a.policies().put(p.Key, p)
		return nil
	})
}

func (a archiveStore) DeletePolicy(ctx context.Context, key string) error {
	return a.s.do(ctx, func() error {
		if !a.policies().del(key) {
			return notFound("archive policy", key)
		}
		return nil
	})
}

func (a archiveStore) Policies(ctx context.Context, family types.ArchiveFamily) ([]types.ArchivePolicy, error) {
	var out []types.ArchivePolicy
	err := a.s.do(ctx, func() error {
		out = a.policies().all(func(_ string, v types.ArchivePolicy) bool {
			return family == "" || v.Family == family
		}, func(x, y types.ArchivePolicy) int { return strings.Compare(x.Key, y.Key) })
		return nil
	})
	return out, err
}

func (a archiveStore) CreateExecution(ctx context.Context, e types.ArchiveExecution) (types.ArchiveExecution, error) {
	var out types.ArchiveExecution
	err := a.s.do(ctx, func() error {
		e.ID = a.s.nextID("archive_executions")
		a.executions().put(e.ID, e)
		out, _ = a.executions().get(e.ID)
		return nil
	})
	return out, err
}

func (a archiveStore) Execution(ctx context.Context, id int64) (types.ArchiveExecution, error) {
	var out types.ArchiveExecution
	err := a.s.do(ctx, func() error {
		var ok bool
		if out, ok = a.executions().get(id); !ok {
			return notFound("archive execution", id)
		}
		return nil
	})
	return out, err
}

func (a archiveStore) UpdateExecution(ctx context.Context, e types.ArchiveExecution) error {
	return a.s.do(ctx, func() error {
		if !a.executions().has(e.ID) {
			return notFound("archive execution", e.ID)
		}
		a.executions().put(e.ID, e)
		return nil
	})
}

func (a archiveStore) Executions(
	ctx context.Context, f store.ExecutionFilter, p store.Page,
) ([]types.ArchiveExecution, string, error) {
	var (
		out  []types.ArchiveExecution
		next string
	)
	err := a.s.do(ctx, func() error {
		rows := a.executions().all(func(_ int64, v types.ArchiveExecution) bool {
			return (f.PolicyKey == "" || v.PolicyKey == f.PolicyKey) && (f.Kind == "" || v.Kind == f.Kind)
		}, func(x, y types.ArchiveExecution) int { return cmp.Compare(y.ID, x.ID) })
		var err error
		out, next, err = paginate(rows, p, true, func(v types.ArchiveExecution) []any { return []any{v.ID} })
		return err
	})
	return out, next, err
}

// archivedLocation is the canonical (parent, name) of an archived item's
// original path.
func archivedLocation(it types.Item) (parent, name string, err error) {
	parent, name, _, err = types.SplitItemPath(it.FullPath())
	if err != nil {
		return "", "", fmt.Errorf("archived item: %w: %w", store.ErrInvalid, err)
	}
	return parent, name, nil
}

func (a archiveStore) AddItem(ctx context.Context, it types.ArchivedItem) (types.ArchivedItem, error) {
	var out types.ArchivedItem
	err := a.s.do(ctx, func() error {
		if _, _, err := archivedLocation(it.Item); err != nil {
			return err
		}
		it.ID = a.s.nextID("archived_items")
		a.items().put(it.ID, it)
		out, _ = a.items().get(it.ID)
		return nil
	})
	return out, err
}

func (a archiveStore) Item(ctx context.Context, id int64) (types.ArchivedItem, error) {
	var out types.ArchivedItem
	err := a.s.do(ctx, func() error {
		var ok bool
		if out, ok = a.items().get(id); !ok {
			return notFound("archived item", id)
		}
		return nil
	})
	return out, err
}

func (a archiveStore) FindItems(ctx context.Context, repoKey, fullPath string) ([]types.ArchivedItem, error) {
	parent, name, _, err := types.SplitItemPath(fullPath)
	if err != nil {
		return nil, fmt.Errorf("find archived items: %w: %w", store.ErrInvalid, err)
	}
	var out []types.ArchivedItem
	err = a.s.do(ctx, func() error {
		out = a.items().all(func(_ int64, v types.ArchivedItem) bool {
			p, n, err := archivedLocation(v.Item)
			return err == nil && v.Item.RepoKey == repoKey && p == parent && n == name
		}, func(x, y types.ArchivedItem) int { return cmp.Compare(y.ID, x.ID) })
		return nil
	})
	return out, err
}

func (a archiveStore) Items(
	ctx context.Context, f store.ArchivedFilter, p store.Page,
) ([]types.ArchivedItem, string, error) {
	var (
		out  []types.ArchivedItem
		next string
	)
	err := a.s.do(ctx, func() error {
		rows := a.items().all(func(_ int64, v types.ArchivedItem) bool {
			return (f.RepoKey == "" || v.Item.RepoKey == f.RepoKey) &&
				(f.PolicyKey == "" || v.PolicyKey == f.PolicyKey) &&
				(f.ExecutionID == 0 || v.ExecutionID == f.ExecutionID)
		}, func(x, y types.ArchivedItem) int { return cmp.Compare(x.ID, y.ID) })
		var err error
		out, next, err = paginate(rows, p, false, func(v types.ArchivedItem) []any { return []any{v.ID} })
		return err
	})
	return out, next, err
}

func (a archiveStore) DeleteItem(ctx context.Context, id int64) error {
	return a.s.do(ctx, func() error {
		if !a.items().del(id) {
			return notFound("archived item", id)
		}
		return nil
	})
}

func (a archiveStore) ExpiredItems(ctx context.Context, before time.Time, limit int) ([]types.ArchivedItem, error) {
	// Compared at the stored precision, as the SQL column is.
	before = before.Truncate(time.Microsecond)
	var out []types.ArchivedItem
	err := a.s.do(ctx, func() error {
		out = a.items().all(func(_ int64, v types.ArchivedItem) bool {
			return !v.ExpiresAt.IsZero() && v.ExpiresAt.Before(before)
		}, func(x, y types.ArchivedItem) int {
			return cmp.Or(x.ExpiresAt.Compare(y.ExpiresAt), cmp.Compare(x.ID, y.ID))
		})
		out = out[:min(len(out), expiredLimit(limit))]
		return nil
	})
	return out, err
}

// expiredLimit applies the default and maximum limit for "at most limit"
// lists.
func expiredLimit(limit int) int {
	if limit <= 0 {
		return store.DefaultLimit
	}
	return min(limit, store.MaxLimit)
}

// --- Workers ---------------------------------------------------------------

type workerStore struct{ s *Store }

func (w workerStore) workers() *table[string, types.Worker] {
	return tableOf[string, types.Worker](w.s, "workers")
}

func (w workerStore) state() *table[workerEntry, string] {
	return tableOf[workerEntry, string](w.s, "worker_state")
}

func (w workerStore) secrets() *table[workerEntry, string] {
	return tableOf[workerEntry, string](w.s, "worker_secrets")
}

func (w workerStore) executions() *table[string, types.WorkerExecution] {
	return tableOf[string, types.WorkerExecution](w.s, "worker_executions")
}

// exists fails with ErrNotFound when the worker does not exist.
func (w workerStore) exists(key string) error {
	if !w.workers().has(key) {
		return notFound("worker", key)
	}
	return nil
}

func (w workerStore) Create(ctx context.Context, v types.Worker) error {
	return w.s.do(ctx, func() error {
		if w.workers().has(v.Key) {
			return conflict("worker", v.Key)
		}
		w.workers().put(v.Key, v)
		return nil
	})
}

func (w workerStore) Get(ctx context.Context, key string) (types.Worker, error) {
	var out types.Worker
	err := w.s.do(ctx, func() error {
		var ok bool
		if out, ok = w.workers().get(key); !ok {
			return notFound("worker", key)
		}
		return nil
	})
	return out, err
}

func (w workerStore) Update(ctx context.Context, v types.Worker) error {
	return w.s.do(ctx, func() error {
		if err := w.exists(v.Key); err != nil {
			return err
		}
		w.workers().put(v.Key, v)
		return nil
	})
}

func (w workerStore) Delete(ctx context.Context, key string) error {
	return w.s.do(ctx, func() error {
		if !w.workers().del(key) {
			return notFound("worker", key)
		}
		owned := func(k workerEntry, _ string) bool { return k.Worker == key }
		w.state().deleteWhere(owned)
		w.secrets().deleteWhere(owned)
		w.executions().deleteWhere(func(_ string, v types.WorkerExecution) bool { return v.WorkerKey == key })
		return nil
	})
}

func (w workerStore) List(ctx context.Context, f store.WorkerFilter) ([]types.Worker, error) {
	var out []types.Worker
	err := w.s.do(ctx, func() error {
		out = w.workers().all(func(_ string, v types.Worker) bool {
			return (f.Action == "" || v.Action == f.Action) &&
				(f.ProjectKey == "" || v.ProjectKey == f.ProjectKey) &&
				(!f.EnabledOnly || v.Enabled)
		}, func(x, y types.Worker) int { return strings.Compare(x.Key, y.Key) })
		return nil
	})
	return out, err
}

// entries collects a worker's rows of an entry table into a map.
func (w workerStore) entries(t *table[workerEntry, string], worker string) map[string]string {
	out := map[string]string{}
	for k, v := range t.rows {
		if k.Worker == worker {
			out[k.Name] = v
		}
	}
	return out
}

func (w workerStore) State(ctx context.Context, worker string) (map[string]string, error) {
	var out map[string]string
	err := w.s.do(ctx, func() error {
		if err := w.exists(worker); err != nil {
			return err
		}
		out = w.entries(w.state(), worker)
		return nil
	})
	return out, err
}

func (w workerStore) SetState(ctx context.Context, worker, key, value string) error {
	return w.s.do(ctx, func() error {
		if err := w.exists(worker); err != nil {
			return err
		}
		w.state().put(workerEntry{worker, key}, value)
		return nil
	})
}

func (w workerStore) DeleteState(ctx context.Context, worker, key string) error {
	return w.s.do(ctx, func() error {
		w.state().del(workerEntry{worker, key})
		return nil
	})
}

func (w workerStore) PutSecret(ctx context.Context, worker, name, value string) error {
	return w.s.do(ctx, func() error {
		if err := w.exists(worker); err != nil {
			return err
		}
		w.secrets().put(workerEntry{worker, name}, value)
		return nil
	})
}

func (w workerStore) Secrets(ctx context.Context, worker string) (map[string]string, error) {
	var out map[string]string
	err := w.s.do(ctx, func() error {
		if err := w.exists(worker); err != nil {
			return err
		}
		out = w.entries(w.secrets(), worker)
		return nil
	})
	return out, err
}

func (w workerStore) DeleteSecret(ctx context.Context, worker, name string) error {
	return w.s.do(ctx, func() error {
		if !w.secrets().del(workerEntry{worker, name}) {
			return notFound("worker secret", workerEntry{worker, name})
		}
		return nil
	})
}

func (w workerStore) AddExecution(ctx context.Context, e types.WorkerExecution) error {
	return w.s.do(ctx, func() error {
		if w.executions().has(e.ID) {
			return conflict("worker execution", e.ID)
		}
		if err := w.exists(e.WorkerKey); err != nil {
			return err
		}
		w.executions().put(e.ID, e)
		return nil
	})
}

func (w workerStore) Execution(ctx context.Context, id string) (types.WorkerExecution, error) {
	var out types.WorkerExecution
	err := w.s.do(ctx, func() error {
		var ok bool
		if out, ok = w.executions().get(id); !ok {
			return notFound("worker execution", id)
		}
		return nil
	})
	return out, err
}

func (w workerStore) Executions(
	ctx context.Context, worker string, p store.Page,
) ([]types.WorkerExecution, string, error) {
	var (
		out  []types.WorkerExecution
		next string
	)
	err := w.s.do(ctx, func() error {
		rows := w.executions().all(func(_ string, v types.WorkerExecution) bool {
			return worker == "" || v.WorkerKey == worker
		}, func(x, y types.WorkerExecution) int {
			return cmp.Or(y.Started.Compare(x.Started), strings.Compare(y.ID, x.ID))
		})
		var err error
		out, next, err = paginate(rows, p, true, func(v types.WorkerExecution) []any {
			return []any{v.Started.UnixMicro(), v.ID}
		})
		return err
	})
	return out, next, err
}

func (w workerStore) PruneExecutions(ctx context.Context, before time.Time) (int64, error) {
	before = before.Truncate(time.Microsecond)
	var n int64
	err := w.s.do(ctx, func() error {
		n = w.executions().deleteWhere(func(_ string, v types.WorkerExecution) bool { return v.Started.Before(before) })
		return nil
	})
	return n, err
}
