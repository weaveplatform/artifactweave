package inmem

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
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

// Bundles implements store.Store.
func (s *Store) Bundles() store.BundleStore { return bundleStore{s} }

// Distribution implements store.Store.
func (s *Store) Distribution() store.DistributionStore { return distributionStore{s} }

// --- Release bundles -------------------------------------------------------

type bundleStore struct{ s *Store }

func (b bundleStore) bundles() *memtx.Table[bundleKey, types.ReleaseBundle] {
	return memtx.TableOf[bundleKey, types.ReleaseBundle](b.s.Store, "release_bundles")
}

func (b bundleStore) artifacts() *memtx.Table[bundleKey, []types.BundleArtifact] {
	return memtx.TableOf[bundleKey, []types.BundleArtifact](b.s.Store, "bundle_artifacts")
}

func (b bundleStore) keeps() *memtx.Table[bundleKey, bool] {
	return memtx.TableOf[bundleKey, bool](b.s.Store, "bundle_keeps")
}

func (b bundleStore) audits() *memtx.Table[int64, bundleAudit] {
	return memtx.TableOf[int64, bundleAudit](b.s.Store, "bundle_audits")
}

func (b bundleStore) exports() *memtx.Table[bundleKey, types.BundleExport] {
	return memtx.TableOf[bundleKey, types.BundleExport](b.s.Store, "bundle_exports")
}

// putArtifacts stores a bundle's artifact list, owned by key and sorted by
// target path; ErrConflict when two share a target path.
func (b bundleStore) putArtifacts(key bundleKey, list []types.BundleArtifact) error {
	list = slices.Clone(list)
	for i := range list {
		list[i].BundleName, list[i].BundleVersion = key.Name, key.Version
	}
	slices.SortFunc(
		list,
		func(x, y types.BundleArtifact) int { return strings.Compare(x.TargetPath, y.TargetPath) },
	)
	for i := 1; i < len(list); i++ {
		if list[i].TargetPath == list[i-1].TargetPath {
			return memtx.Conflict("bundle artifact "+key.String(), list[i].TargetPath)
		}
	}
	b.artifacts().Put(key, list)
	return nil
}

func (b bundleStore) Create(
	ctx context.Context,
	rb types.ReleaseBundle,
	list []types.BundleArtifact,
) error {
	return b.s.Do(ctx, func() error {
		key := bundleKey{rb.Name, rb.Version}
		if b.bundles().Has(key) {
			return memtx.Conflict("release bundle", key)
		}
		if err := b.putArtifacts(key, list); err != nil {
			return err
		}
		b.bundles().Put(key, rb)
		return nil
	})
}

func (b bundleStore) Get(ctx context.Context, name, version string) (types.ReleaseBundle, error) {
	var out types.ReleaseBundle
	err := b.s.Do(ctx, func() error {
		var ok bool
		if out, ok = b.bundles().Get(bundleKey{name, version}); !ok {
			return memtx.NotFound("release bundle", bundleKey{name, version})
		}
		return nil
	})
	return out, err
}

func (b bundleStore) Update(ctx context.Context, rb types.ReleaseBundle) error {
	return b.s.Do(ctx, func() error {
		key := bundleKey{rb.Name, rb.Version}
		if !b.bundles().Has(key) {
			return memtx.NotFound("release bundle", key)
		}
		b.bundles().Put(key, rb)
		return nil
	})
}

func (b bundleStore) ReplaceArtifacts(
	ctx context.Context,
	name, version string,
	list []types.BundleArtifact,
) error {
	return b.s.Do(ctx, func() error {
		key := bundleKey{name, version}
		cur, ok := b.bundles().Get(key)
		if !ok {
			return memtx.NotFound("release bundle", key)
		}
		if !cur.State.Editable() {
			return memtx.Conflict("release bundle "+string(cur.State), key)
		}
		return b.putArtifacts(key, list)
	})
}

func (b bundleStore) Delete(ctx context.Context, name, version string) error {
	return b.s.Do(ctx, func() error {
		key := bundleKey{name, version}
		if !b.bundles().Del(key) {
			return memtx.NotFound("release bundle", key)
		}
		b.artifacts().Del(key)
		b.keeps().Del(key)
		b.exports().Del(key)
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
	err := b.s.Do(ctx, func() error {
		rows := b.bundles().All(func(_ bundleKey, v types.ReleaseBundle) bool {
			return (f.Name == "" || v.Name == f.Name) &&
				(f.Origin == "" || v.Origin == f.Origin) &&
				(f.ProjectKey == "" || v.ProjectKey == f.ProjectKey)
		}, func(x, y types.ReleaseBundle) int {
			return cmp.Or(strings.Compare(x.Name, y.Name), strings.Compare(x.Version, y.Version))
		})
		var err error
		out, next, err = memtx.Paginate(rows, p, false, func(v types.ReleaseBundle) []any {
			return []any{v.Name, v.Version}
		})
		return err
	})
	return out, next, err
}

func (b bundleStore) Artifacts(
	ctx context.Context,
	name, version string,
) ([]types.BundleArtifact, error) {
	var out []types.BundleArtifact
	err := b.s.Do(ctx, func() error {
		key := bundleKey{name, version}
		if !b.bundles().Has(key) {
			return memtx.NotFound("release bundle", key)
		}
		out, _ = b.artifacts().Get(key)
		return nil
	})
	return out, err
}

func (b bundleStore) SetKeep(ctx context.Context, name, version string, keep bool) error {
	return b.s.Do(ctx, func() error {
		key := bundleKey{name, version}
		if !b.bundles().Has(key) {
			return memtx.NotFound("release bundle", key)
		}
		if keep {
			b.keeps().Put(key, true)
		} else {
			b.keeps().Del(key)
		}
		return nil
	})
}

func (b bundleStore) Kept(ctx context.Context, name, version string) (bool, error) {
	var kept bool
	err := b.s.Do(ctx, func() error {
		key := bundleKey{name, version}
		if !b.bundles().Has(key) {
			return memtx.NotFound("release bundle", key)
		}
		kept = b.keeps().Has(key)
		return nil
	})
	return kept, err
}

func (b bundleStore) AddAudit(ctx context.Context, a types.BundleRetentionAudit) error {
	return b.s.Do(ctx, func() error {
		id := b.s.NextID("bundle_audits")
		b.audits().Put(id, bundleAudit{ID: id, Audit: a})
		return nil
	})
}

func (b bundleStore) Audits(
	ctx context.Context,
	name string,
) ([]types.BundleRetentionAudit, error) {
	var out []types.BundleRetentionAudit
	err := b.s.Do(ctx, func() error {
		rows := b.audits().All(func(_ int64, v bundleAudit) bool {
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
	return b.s.Do(ctx, func() error {
		key := bundleKey{e.BundleName, e.BundleVersion}
		if !b.bundles().Has(key) {
			return memtx.NotFound("release bundle", key)
		}
		b.exports().Put(key, e)
		return nil
	})
}

func (b bundleStore) Export(ctx context.Context, name, version string) (types.BundleExport, error) {
	var out types.BundleExport
	err := b.s.Do(ctx, func() error {
		var ok bool
		if out, ok = b.exports().Get(bundleKey{name, version}); !ok {
			return memtx.NotFound("bundle export", bundleKey{name, version})
		}
		return nil
	})
	return out, err
}

func (b bundleStore) DeleteExport(ctx context.Context, name, version string) error {
	return b.s.Do(ctx, func() error {
		if !b.exports().Del(bundleKey{name, version}) {
			return memtx.NotFound("bundle export", bundleKey{name, version})
		}
		return nil
	})
}

// --- Distribution ----------------------------------------------------------

type distributionStore struct{ s *Store }

func (d distributionStore) trackers() *memtx.Table[int64, types.DistributionTracker] {
	return memtx.TableOf[int64, types.DistributionTracker](d.s.Store, "distribution_trackers")
}

func (d distributionStore) jobs() *memtx.Table[jobKey, types.DistributionJob] {
	return memtx.TableOf[jobKey, types.DistributionJob](d.s.Store, "distribution_jobs")
}

func (d distributionStore) CreateTracker(
	ctx context.Context, t types.DistributionTracker, jobs []types.DistributionJob,
) (types.DistributionTracker, error) {
	var out types.DistributionTracker
	err := d.s.Do(ctx, func() error {
		seen := map[string]bool{}
		for _, j := range jobs {
			if seen[j.PeerInstanceID] {
				return memtx.Conflict("distribution job peer", j.PeerInstanceID)
			}
			seen[j.PeerInstanceID] = true
		}
		t.ID = d.s.NextID("distribution_trackers")
		d.trackers().Put(t.ID, t)
		for _, j := range jobs {
			j.TrackerID = t.ID
			d.jobs().Put(jobKey{t.ID, j.PeerInstanceID}, j)
		}
		out, _ = d.trackers().Get(t.ID)
		return nil
	})
	return out, err
}

func (d distributionStore) Tracker(
	ctx context.Context,
	id int64,
) (types.DistributionTracker, error) {
	var out types.DistributionTracker
	err := d.s.Do(ctx, func() error {
		var ok bool
		if out, ok = d.trackers().Get(id); !ok {
			return memtx.NotFound("distribution tracker", id)
		}
		return nil
	})
	return out, err
}

func (d distributionStore) UpdateTracker(ctx context.Context, t types.DistributionTracker) error {
	return d.s.Do(ctx, func() error {
		if !d.trackers().Has(t.ID) {
			return memtx.NotFound("distribution tracker", t.ID)
		}
		d.trackers().Put(t.ID, t)
		return nil
	})
}

func (d distributionStore) Trackers(
	ctx context.Context,
	name, version string,
) ([]types.DistributionTracker, error) {
	var out []types.DistributionTracker
	err := d.s.Do(ctx, func() error {
		out = d.trackers().All(func(_ int64, v types.DistributionTracker) bool {
			return (name == "" || v.BundleName == name) &&
				(version == "" || v.BundleVersion == version)
		}, func(x, y types.DistributionTracker) int { return cmp.Compare(y.ID, x.ID) })
		return nil
	})
	return out, err
}

func (d distributionStore) PutJob(ctx context.Context, j types.DistributionJob) error {
	return d.s.Do(ctx, func() error {
		if !d.trackers().Has(j.TrackerID) {
			return memtx.NotFound("distribution tracker", j.TrackerID)
		}
		d.jobs().Put(jobKey{j.TrackerID, j.PeerInstanceID}, j)
		return nil
	})
}

func compareJobs(x, y types.DistributionJob) int {
	return cmp.Or(
		cmp.Compare(x.TrackerID, y.TrackerID),
		strings.Compare(x.PeerInstanceID, y.PeerInstanceID),
	)
}

func (d distributionStore) Jobs(
	ctx context.Context,
	trackerID int64,
) ([]types.DistributionJob, error) {
	var out []types.DistributionJob
	err := d.s.Do(ctx, func() error {
		if !d.trackers().Has(trackerID) {
			return memtx.NotFound("distribution tracker", trackerID)
		}
		out = d.jobs().
			All(func(k jobKey, _ types.DistributionJob) bool { return k.Tracker == trackerID }, compareJobs)
		return nil
	})
	return out, err
}

func (d distributionStore) JobsForPeer(
	ctx context.Context, peer string, statuses []types.JobStatus,
) ([]types.DistributionJob, error) {
	var out []types.DistributionJob
	err := d.s.Do(ctx, func() error {
		out = d.jobs().All(func(k jobKey, v types.DistributionJob) bool {
			return k.Peer == peer && (len(statuses) == 0 || slices.Contains(statuses, v.Status))
		}, compareJobs)
		return nil
	})
	return out, err
}
