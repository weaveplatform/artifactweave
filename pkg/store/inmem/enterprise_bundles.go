package inmem

import (
	"cmp"
	"context"
	"slices"
	"strings"

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
	slices.SortFunc(
		list,
		func(x, y types.BundleArtifact) int { return strings.Compare(x.TargetPath, y.TargetPath) },
	)
	for i := 1; i < len(list); i++ {
		if list[i].TargetPath == list[i-1].TargetPath {
			return conflict("bundle artifact "+key.String(), list[i].TargetPath)
		}
	}
	b.artifacts().put(key, list)
	return nil
}

func (b bundleStore) Create(
	ctx context.Context,
	rb types.ReleaseBundle,
	list []types.BundleArtifact,
) error {
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

func (b bundleStore) ReplaceArtifacts(
	ctx context.Context,
	name, version string,
	list []types.BundleArtifact,
) error {
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

func (b bundleStore) Artifacts(
	ctx context.Context,
	name, version string,
) ([]types.BundleArtifact, error) {
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

func (b bundleStore) Audits(
	ctx context.Context,
	name string,
) ([]types.BundleRetentionAudit, error) {
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

func (d distributionStore) Tracker(
	ctx context.Context,
	id int64,
) (types.DistributionTracker, error) {
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

func (d distributionStore) Trackers(
	ctx context.Context,
	name, version string,
) ([]types.DistributionTracker, error) {
	var out []types.DistributionTracker
	err := d.s.do(ctx, func() error {
		out = d.trackers().all(func(_ int64, v types.DistributionTracker) bool {
			return (name == "" || v.BundleName == name) &&
				(version == "" || v.BundleVersion == version)
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
	err := d.s.do(ctx, func() error {
		if !d.trackers().has(trackerID) {
			return notFound("distribution tracker", trackerID)
		}
		out = d.jobs().
			all(func(k jobKey, _ types.DistributionJob) bool { return k.Tracker == trackerID }, compareJobs)
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
