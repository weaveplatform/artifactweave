package sqlcommon

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Bundles implements store.Store.
func (s *Store) Bundles() store.BundleStore { return bundleStore{s} }

// Distribution implements store.Store.
func (s *Store) Distribution() store.DistributionStore { return distributionStore{s} }

// Archive implements store.Store.
func (s *Store) Archive() store.ArchiveStore { return archiveStore{s} }

// Workers implements store.Store.
func (s *Store) Workers() store.WorkerStore { return workerStore{s} }

// entRow is a *sql.Row or *sql.Rows.
type entRow interface{ Scan(dest ...any) error }

// entBytes stores an empty byte slice as NULL.
func entBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// --- Release bundles -------------------------------------------------------

type bundleStore struct{ s *Store }

var bundleCols = []string{
	"name", "version", "state", "storing_repo", "project_key", "origin", "description",
	"created_at", "created_by", "signed_at", "signing_key_alias", "signature", "notes", "spec",
}

func bundleRow(b types.ReleaseBundle) []any {
	return []any{
		b.Name, b.Version, string(b.State), b.StoringRepository, b.ProjectKey, string(b.Origin),
		b.Description, micros(b.Created), b.CreatedBy, micros(b.SignedAt), b.SigningKeyAlias,
		entBytes(b.Signature), mustJSON(b.Notes), mustJSON(b.Spec),
	}
}

func scanBundle(r entRow) (types.ReleaseBundle, error) {
	var (
		b               types.ReleaseBundle
		state, origin   string
		created, signed sql.NullInt64
		notes, spec     sql.NullString
	)
	if err := r.Scan(&b.Name, &b.Version, &state, &b.StoringRepository, &b.ProjectKey, &origin,
		&b.Description, &created, &b.CreatedBy, &signed, &b.SigningKeyAlias, &b.Signature,
		&notes, &spec); err != nil {
		return b, err //nolint:wrapcheck // wrapped by the caller
	}
	b.State, b.Origin = types.BundleState(state), types.BundleOrigin(origin)
	b.Created, b.SignedAt = fromMicros(created), fromMicros(signed)
	if err := fromJSON(notes, &b.Notes); err != nil {
		return b, err
	}
	return b, fromJSON(spec, &b.Spec)
}

// get reads one bundle version, locking it inside a transaction when lock
// is set.
func (b bundleStore) get(
	ctx context.Context,
	name, version string,
	lock bool,
) (types.ReleaseBundle, error) {
	q := "SELECT " + strings.Join(
		bundleCols,
		", ",
	) + " FROM release_bundles WHERE name = ? AND version = ?"
	if lock {
		q += b.s.lockClause(ctx)
	}
	out, err := scanBundle(b.s.q(ctx).QueryRowContext(ctx, b.s.d.Rebind(q), name, version))
	return out, b.s.wrap(fmt.Sprintf("get release bundle %s/%s", name, version), err)
}

// insertArtifacts writes a bundle's artifact list, owned by (name, version).
func (b bundleStore) insertArtifacts(
	ctx context.Context,
	name, version string,
	list []types.BundleArtifact,
) error {
	cols := []string{
		"bundle_name", "bundle_version", "target_path", "source_path",
		"sha256", "sha512", "sha1", "md5", "size", "props",
	}
	for _, a := range list {
		if _, err := b.s.exec(
			ctx,
			"add bundle artifact "+a.TargetPath,
			Insert("bundle_artifacts", cols),
			name,
			version,
			a.TargetPath,
			a.SourcePath,
			a.Checksums.SHA256,
			a.Checksums.SHA512,
			a.Checksums.SHA1,
			a.Checksums.MD5,
			a.Size,
			mustJSON(a.Props),
		); err != nil {
			return err
		}
	}
	return nil
}

func (b bundleStore) Create(
	ctx context.Context,
	rb types.ReleaseBundle,
	list []types.BundleArtifact,
) error {
	row := bundleRow(store.Normalise(rb))
	return b.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := b.s.exec(ctx, "create release bundle "+rb.Name+"/"+rb.Version,
			Insert("release_bundles", bundleCols), row...); err != nil {
			return err
		}
		return b.insertArtifacts(ctx, rb.Name, rb.Version, list)
	})
}

func (b bundleStore) Get(ctx context.Context, name, version string) (types.ReleaseBundle, error) {
	return b.get(ctx, name, version, false)
}

func (b bundleStore) Update(ctx context.Context, rb types.ReleaseBundle) error {
	row := bundleRow(store.Normalise(rb))
	sets := make([]string, 0, len(bundleCols)-2)
	for _, c := range bundleCols[2:] {
		sets = append(sets, c+" = ?")
	}
	return b.s.execOne(ctx, "update release bundle "+rb.Name+"/"+rb.Version,
		"UPDATE release_bundles SET "+strings.Join(sets, ", ")+" WHERE name = ? AND version = ?",
		append(row[2:], rb.Name, rb.Version)...)
}

func (b bundleStore) ReplaceArtifacts(
	ctx context.Context,
	name, version string,
	list []types.BundleArtifact,
) error {
	return b.s.Tx(ctx, func(ctx context.Context) error {
		cur, err := b.get(ctx, name, version, true)
		if err != nil {
			return err
		}
		if !cur.State.Editable() {
			return fmt.Errorf(
				"release bundle %s/%s is %s: %w",
				name,
				version,
				cur.State,
				store.ErrConflict,
			)
		}
		if _, err := b.s.exec(
			ctx,
			"clear bundle artifacts",
			"DELETE FROM bundle_artifacts WHERE bundle_name = ? AND bundle_version = ?",
			name,
			version,
		); err != nil {
			return err
		}
		return b.insertArtifacts(ctx, name, version, list)
	})
}

func (b bundleStore) Delete(ctx context.Context, name, version string) error {
	// Artifacts, the keep mark and the export go with the row (ON DELETE
	// CASCADE); the audit trail has no foreign key and stays.
	return b.s.execOne(ctx, fmt.Sprintf("delete release bundle %s/%s", name, version),
		"DELETE FROM release_bundles WHERE name = ? AND version = ?", name, version)
}

func (b bundleStore) List(
	ctx context.Context, f store.BundleFilter, p store.Page,
) ([]types.ReleaseBundle, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	order := []string{"name", "version"}
	cond, args, err := keyset(p, order, nil, false)
	if err != nil {
		return nil, "", err
	}
	where := []string{cond}
	for col, v := range map[string]string{
		"name": f.Name, "origin": string(f.Origin), "project_key": f.ProjectKey,
	} {
		if v != "" {
			where = append(where, col+" = ?")
			args = append(args, v)
		}
	}
	var out []types.ReleaseBundle
	err = b.s.rows(ctx, "list release bundles",
		"SELECT "+strings.Join(bundleCols, ", ")+" FROM release_bundles WHERE "+
			strings.Join(where, " AND ")+orderBy(order, false)+" LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			v, err := scanBundle(r)
			out = append(out, v)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(
		out,
		p.Limit,
		func(v types.ReleaseBundle) []string { return []string{v.Name, v.Version} },
	)
	return page, next, nil
}

// requireBundle fails with ErrNotFound when the bundle version is absent.
func (b bundleStore) requireBundle(ctx context.Context, name, version string) error {
	ok, err := b.s.exists(ctx, "find release bundle",
		"SELECT 1 FROM release_bundles WHERE name = ? AND version = ?", name, version)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("release bundle %s/%s: %w", name, version, store.ErrNotFound)
	}
	return nil
}

func (b bundleStore) Artifacts(
	ctx context.Context,
	name, version string,
) ([]types.BundleArtifact, error) {
	if err := b.requireBundle(ctx, name, version); err != nil {
		return nil, err
	}
	var out []types.BundleArtifact
	err := b.s.rows(
		ctx,
		"list bundle artifacts",
		"SELECT bundle_name, bundle_version, target_path, source_path,"+
			" sha256, sha512, sha1, md5, size, props"+
			" FROM bundle_artifacts WHERE bundle_name = ? AND bundle_version = ? ORDER BY target_path",
		[]any{name, version},
		func(r *sql.Rows) error {
			var (
				a     types.BundleArtifact
				props sql.NullString
			)
			if err := r.Scan(
				&a.BundleName,
				&a.BundleVersion,
				&a.TargetPath,
				&a.SourcePath,
				&a.Checksums.SHA256,
				&a.Checksums.SHA512,
				&a.Checksums.SHA1,
				&a.Checksums.MD5,
				&a.Size,
				&props,
			); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			if err := fromJSON(props, &a.Props); err != nil {
				return err
			}
			out = append(out, a)
			return nil
		},
	)
	return out, err
}

func (b bundleStore) SetKeep(ctx context.Context, name, version string, keep bool) error {
	return b.s.Tx(ctx, func(ctx context.Context) error {
		if err := b.requireBundle(ctx, name, version); err != nil {
			return err
		}
		q := "DELETE FROM bundle_keeps WHERE bundle_name = ? AND bundle_version = ?"
		if keep {
			q = InsertIgnore("bundle_keeps", []string{"bundle_name", "bundle_version"},
				[]string{"bundle_name", "bundle_version"})
		}
		_, err := b.s.exec(ctx, "set bundle keep", q, name, version)
		return err
	})
}

func (b bundleStore) Kept(ctx context.Context, name, version string) (bool, error) {
	if err := b.requireBundle(ctx, name, version); err != nil {
		return false, err
	}
	return b.s.exists(ctx, "find bundle keep",
		"SELECT 1 FROM bundle_keeps WHERE bundle_name = ? AND bundle_version = ?", name, version)
}

func (b bundleStore) AddAudit(ctx context.Context, a types.BundleRetentionAudit) error {
	_, err := b.s.exec(
		ctx,
		"add bundle audit",
		Insert(
			"bundle_audits",
			[]string{"bundle_name", "bundle_version", "operation", "created_at", "created_by"},
		),
		a.BundleName,
		a.BundleVersion,
		string(a.Operation),
		micros(a.Created),
		a.CreatedBy,
	)
	return err
}

func (b bundleStore) Audits(
	ctx context.Context,
	name string,
) ([]types.BundleRetentionAudit, error) {
	q := "SELECT bundle_name, bundle_version, operation, created_at, created_by FROM bundle_audits"
	var args []any
	if name != "" {
		q += " WHERE bundle_name = ?"
		args = append(args, name)
	}
	var out []types.BundleRetentionAudit
	err := b.s.rows(ctx, "list bundle audits", q+orderBy([]string{"created_at", "id"}, true), args,
		func(r *sql.Rows) error {
			var (
				a       types.BundleRetentionAudit
				op      string
				created sql.NullInt64
			)
			if err := r.Scan(
				&a.BundleName,
				&a.BundleVersion,
				&op,
				&created,
				&a.CreatedBy,
			); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			a.Operation, a.Created = types.BundleOperation(op), fromMicros(created)
			out = append(out, a)
			return nil
		})
	return out, err
}

func (b bundleStore) PutExport(ctx context.Context, e types.BundleExport) error {
	return b.s.Tx(ctx, func(ctx context.Context) error {
		if err := b.requireBundle(ctx, e.BundleName, e.BundleVersion); err != nil {
			return err
		}
		_, err := b.s.exec(ctx, "put bundle export",
			Upsert("bundle_exports",
				[]string{"bundle_name", "bundle_version", "status", "archive_sha256", "created_at"},
				[]string{"bundle_name", "bundle_version"}),
			e.BundleName, e.BundleVersion, string(e.Status), e.ArchiveSHA256, micros(e.Created))
		return err
	})
}

func (b bundleStore) Export(ctx context.Context, name, version string) (types.BundleExport, error) {
	var (
		e       types.BundleExport
		status  string
		created sql.NullInt64
	)
	err := b.s.q(ctx).QueryRowContext(ctx, b.s.d.Rebind(
		"SELECT bundle_name, bundle_version, status, archive_sha256, created_at FROM bundle_exports"+
			" WHERE bundle_name = ? AND bundle_version = ?",
	), name, version).
		Scan(&e.BundleName, &e.BundleVersion, &status, &e.ArchiveSHA256, &created)
	if err != nil {
		return e, b.s.wrap(fmt.Sprintf("get bundle export %s/%s", name, version), err)
	}
	e.Status, e.Created = types.JobStatus(status), fromMicros(created)
	return e, nil
}

func (b bundleStore) DeleteExport(ctx context.Context, name, version string) error {
	return b.s.execOne(ctx, fmt.Sprintf("delete bundle export %s/%s", name, version),
		"DELETE FROM bundle_exports WHERE bundle_name = ? AND bundle_version = ?", name, version)
}

// --- Distribution ----------------------------------------------------------

type distributionStore struct{ s *Store }

var trackerCols = []string{
	"bundle_name", "bundle_version", "kind", "dry_run", "auto_create_repos",
	"status", "created_at", "created_by", "rules",
}

func trackerRow(t types.DistributionTracker) []any {
	return []any{
		t.BundleName, t.BundleVersion, string(t.Kind), t.DryRun, t.AutoCreateMissingRepositories,
		string(t.Status), micros(t.Created), t.CreatedBy, mustJSON(t.Rules),
	}
}

func scanTracker(r entRow) (types.DistributionTracker, error) {
	var (
		t            types.DistributionTracker
		kind, status string
		created      sql.NullInt64
		rules        sql.NullString
	)
	if err := r.Scan(&t.ID, &t.BundleName, &t.BundleVersion, &kind, &t.DryRun,
		&t.AutoCreateMissingRepositories, &status, &created, &t.CreatedBy, &rules); err != nil {
		return t, err //nolint:wrapcheck // wrapped by the caller
	}
	t.Kind, t.Status, t.Created = types.TrackerKind(
		kind,
	), types.JobStatus(
		status,
	), fromMicros(
		created,
	)
	return t, fromJSON(rules, &t.Rules)
}

var jobCols = []string{
	"tracker_id",
	"peer",
	"status",
	"priority",
	"started_at",
	"finished_at",
	"error",
}

func (d distributionStore) putJob(ctx context.Context, j types.DistributionJob, upsert bool) error {
	q := Insert("distribution_jobs", jobCols)
	if upsert {
		q = Upsert("distribution_jobs", jobCols, []string{"tracker_id", "peer"})
	}
	_, err := d.s.exec(ctx, "put distribution job "+j.PeerInstanceID, q,
		j.TrackerID, j.PeerInstanceID, string(j.Status), j.Priority,
		micros(j.Started), micros(j.Finished), j.Error)
	return err
}

func (d distributionStore) CreateTracker(
	ctx context.Context, t types.DistributionTracker, jobs []types.DistributionJob,
) (types.DistributionTracker, error) {
	t = store.Normalise(t)
	err := d.s.Tx(ctx, func(ctx context.Context) error {
		id, err := d.s.insertID(ctx, "create distribution tracker",
			Insert("distribution_trackers", trackerCols), trackerRow(t)...)
		if err != nil {
			return err
		}
		t.ID = id
		for _, j := range jobs {
			j.TrackerID = t.ID
			if err := d.putJob(ctx, j, false); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return types.DistributionTracker{}, err
	}
	return t, nil
}

func (d distributionStore) Tracker(
	ctx context.Context,
	id int64,
) (types.DistributionTracker, error) {
	out, err := scanTracker(d.s.q(ctx).QueryRowContext(ctx, d.s.d.Rebind(
		"SELECT id, "+strings.Join(
			trackerCols,
			", ",
		)+" FROM distribution_trackers WHERE id = ?",
	), id))
	return out, d.s.wrap(fmt.Sprintf("get distribution tracker %d", id), err)
}

func (d distributionStore) UpdateTracker(ctx context.Context, t types.DistributionTracker) error {
	row := trackerRow(store.Normalise(t))
	sets := make([]string, 0, len(trackerCols))
	for _, c := range trackerCols {
		sets = append(sets, c+" = ?")
	}
	return d.s.execOne(
		ctx,
		fmt.Sprintf("update distribution tracker %d", t.ID),
		"UPDATE distribution_trackers SET "+strings.Join(
			sets,
			", ",
		)+" WHERE id = ?",
		append(row, t.ID)...)
}

func (d distributionStore) Trackers(
	ctx context.Context,
	name, version string,
) ([]types.DistributionTracker, error) {
	q := "SELECT id, " + strings.Join(trackerCols, ", ") + " FROM distribution_trackers WHERE 1 = 1"
	var args []any
	if name != "" {
		q += " AND bundle_name = ?"
		args = append(args, name)
	}
	if version != "" {
		q += " AND bundle_version = ?"
		args = append(args, version)
	}
	var out []types.DistributionTracker
	err := d.s.rows(
		ctx,
		"list distribution trackers",
		q+" ORDER BY id DESC",
		args,
		func(r *sql.Rows) error {
			v, err := scanTracker(r)
			out = append(out, v)
			return err
		},
	)
	return out, err
}

func (d distributionStore) PutJob(ctx context.Context, j types.DistributionJob) error {
	return d.s.Tx(ctx, func(ctx context.Context) error {
		if err := d.requireTracker(ctx, j.TrackerID); err != nil {
			return err
		}
		return d.putJob(ctx, j, true)
	})
}

// requireTracker fails with ErrNotFound when the tracker is absent.
func (d distributionStore) requireTracker(ctx context.Context, id int64) error {
	ok, err := d.s.exists(
		ctx,
		"find distribution tracker",
		"SELECT 1 FROM distribution_trackers WHERE id = ?",
		id,
	)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("distribution tracker %d: %w", id, store.ErrNotFound)
	}
	return nil
}

// jobs runs a job query.
func (d distributionStore) jobs(
	ctx context.Context,
	where string,
	args ...any,
) ([]types.DistributionJob, error) {
	var out []types.DistributionJob
	err := d.s.rows(
		ctx,
		"list distribution jobs",
		"SELECT "+strings.Join(
			jobCols,
			", ",
		)+" FROM distribution_jobs WHERE "+where+" ORDER BY tracker_id, peer",
		args,
		func(r *sql.Rows) error {
			var (
				j                 types.DistributionJob
				status            string
				started, finished sql.NullInt64
			)
			if err := r.Scan(&j.TrackerID, &j.PeerInstanceID, &status, &j.Priority,
				&started, &finished, &j.Error); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			j.Status, j.Started, j.Finished = types.JobStatus(
				status,
			), fromMicros(
				started,
			), fromMicros(
				finished,
			)
			out = append(out, j)
			return nil
		},
	)
	return out, err
}

func (d distributionStore) Jobs(
	ctx context.Context,
	trackerID int64,
) ([]types.DistributionJob, error) {
	if err := d.requireTracker(ctx, trackerID); err != nil {
		return nil, err
	}
	return d.jobs(ctx, "tracker_id = ?", trackerID)
}

func (d distributionStore) JobsForPeer(
	ctx context.Context, peer string, statuses []types.JobStatus,
) ([]types.DistributionJob, error) {
	where := "peer = ?"
	args := []any{peer}
	if len(statuses) > 0 {
		where += " AND status IN (" + Placeholders(len(statuses)) + ")"
		for _, st := range statuses {
			args = append(args, string(st))
		}
	}
	return d.jobs(ctx, where, args...)
}

// --- Archive ---------------------------------------------------------------

type archiveStore struct{ s *Store }

// ColdBlobs implements store.ArchiveStore.
func (a archiveStore) ColdBlobs() store.BlobIndex { return blobIndex{s: a.s, table: "cold_blobs"} }

// ColdReplicas implements store.ArchiveStore.
func (a archiveStore) ColdReplicas() store.ReplicaIndex {
	return replicaIndex{s: a.s, table: "cold_blob_replicas"}
}

var archivePolicyCols = []string{
	"key", "family", "description", "enabled", "cron_exp", "project_key", "duration_minutes",
	"expiration_months", "skip_trashcan", "created_at", "updated_at", "criteria",
}

func archivePolicyRow(p types.ArchivePolicy) []any {
	return []any{
		p.Key,
		string(p.Family),
		p.Description,
		p.Enabled,
		p.CronExp,
		p.ProjectKey,
		p.DurationMinutes,
		p.ExpirationMonths,
		p.SkipTrashcan,
		micros(p.Created),
		micros(p.Updated),
		rawJSON(p.Criteria),
	}
}

func scanArchivePolicy(r entRow) (types.ArchivePolicy, error) {
	var (
		p                types.ArchivePolicy
		family           string
		created, updated sql.NullInt64
		criteria         sql.NullString
	)
	if err := r.Scan(
		&p.Key,
		&family,
		&p.Description,
		&p.Enabled,
		&p.CronExp,
		&p.ProjectKey,
		&p.DurationMinutes,
		&p.ExpirationMonths,
		&p.SkipTrashcan,
		&created,
		&updated,
		&criteria,
	); err != nil {
		return p, err //nolint:wrapcheck // wrapped by the caller
	}
	p.Family, p.Created, p.Updated = types.ArchiveFamily(
		family,
	), fromMicros(
		created,
	), fromMicros(
		updated,
	)
	p.Criteria = fromRaw(criteria)
	return p, nil
}

func (a archiveStore) CreatePolicy(ctx context.Context, p types.ArchivePolicy) error {
	_, err := a.s.exec(ctx, "create archive policy "+p.Key,
		Insert("archive_policies", archivePolicyCols), archivePolicyRow(p)...)
	return err
}

func (a archiveStore) Policy(ctx context.Context, key string) (types.ArchivePolicy, error) {
	out, err := scanArchivePolicy(a.s.q(ctx).QueryRowContext(ctx, a.s.d.Rebind(
		"SELECT "+strings.Join(
			archivePolicyCols,
			", ",
		)+" FROM archive_policies WHERE key = ?",
	), key))
	return out, a.s.wrap("get archive policy "+key, err)
}

func (a archiveStore) UpdatePolicy(ctx context.Context, p types.ArchivePolicy) error {
	sets := make([]string, 0, len(archivePolicyCols)-1)
	for _, c := range archivePolicyCols[1:] {
		sets = append(sets, c+" = ?")
	}
	row := archivePolicyRow(p)
	return a.s.execOne(
		ctx,
		"update archive policy "+p.Key,
		"UPDATE archive_policies SET "+strings.Join(
			sets,
			", ",
		)+" WHERE key = ?",
		append(row[1:], p.Key)...)
}

func (a archiveStore) DeletePolicy(ctx context.Context, key string) error {
	return a.s.execOne(
		ctx,
		"delete archive policy "+key,
		"DELETE FROM archive_policies WHERE key = ?",
		key,
	)
}

func (a archiveStore) Policies(
	ctx context.Context,
	family types.ArchiveFamily,
) ([]types.ArchivePolicy, error) {
	q := "SELECT " + strings.Join(archivePolicyCols, ", ") + " FROM archive_policies"
	var args []any
	if family != "" {
		q += " WHERE family = ?"
		args = append(args, string(family))
	}
	var out []types.ArchivePolicy
	err := a.s.rows(ctx, "list archive policies", q+" ORDER BY key", args, func(r *sql.Rows) error {
		v, err := scanArchivePolicy(r)
		out = append(out, v)
		return err
	})
	return out, err
}

var executionCols = []string{
	"policy_key", "kind", "state", "started_at", "finished_at", "total", "completed", "failed",
	"total_bytes", "success_bytes", "failure_bytes", "triggered_by", "destination",
}

func executionRow(e types.ArchiveExecution) []any {
	return []any{
		e.PolicyKey,
		string(e.Kind),
		string(e.State),
		micros(e.Started),
		micros(e.Finished),
		e.Total,
		e.Completed,
		e.Failed,
		e.TotalBytes,
		e.SuccessBytes,
		e.FailureBytes,
		e.TriggeredBy,
		rawJSON(e.Destination),
	}
}

func scanExecution(r entRow) (types.ArchiveExecution, error) {
	var (
		e                 types.ArchiveExecution
		kind, state       string
		started, finished sql.NullInt64
		destination       sql.NullString
	)
	if err := r.Scan(
		&e.ID,
		&e.PolicyKey,
		&kind,
		&state,
		&started,
		&finished,
		&e.Total,
		&e.Completed,
		&e.Failed,
		&e.TotalBytes,
		&e.SuccessBytes,
		&e.FailureBytes,
		&e.TriggeredBy,
		&destination,
	); err != nil {
		return e, err //nolint:wrapcheck // wrapped by the caller
	}
	e.Kind, e.State = types.ExecutionKind(kind), types.ExecutionState(state)
	e.Started, e.Finished, e.Destination = fromMicros(
		started,
	), fromMicros(
		finished,
	), fromRaw(
		destination,
	)
	return e, nil
}

func (a archiveStore) CreateExecution(
	ctx context.Context,
	e types.ArchiveExecution,
) (types.ArchiveExecution, error) {
	e = store.Normalise(e)
	id, err := a.s.insertID(ctx, "create archive execution",
		Insert("archive_executions", executionCols), executionRow(e)...)
	if err != nil {
		return types.ArchiveExecution{}, err
	}
	e.ID = id
	return e, nil
}

func (a archiveStore) Execution(ctx context.Context, id int64) (types.ArchiveExecution, error) {
	out, err := scanExecution(a.s.q(ctx).QueryRowContext(ctx, a.s.d.Rebind(
		"SELECT id, "+strings.Join(
			executionCols,
			", ",
		)+" FROM archive_executions WHERE id = ?",
	), id))
	return out, a.s.wrap(fmt.Sprintf("get archive execution %d", id), err)
}

func (a archiveStore) UpdateExecution(ctx context.Context, e types.ArchiveExecution) error {
	sets := make([]string, 0, len(executionCols))
	for _, c := range executionCols {
		sets = append(sets, c+" = ?")
	}
	return a.s.execOne(ctx, fmt.Sprintf("update archive execution %d", e.ID),
		"UPDATE archive_executions SET "+strings.Join(sets, ", ")+" WHERE id = ?",
		append(executionRow(e), e.ID)...)
}

func (a archiveStore) Executions(
	ctx context.Context, f store.ExecutionFilter, p store.Page,
) ([]types.ArchiveExecution, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := keyset(p, []string{"id"}, map[string]bool{"id": true}, true)
	if err != nil {
		return nil, "", err
	}
	where := []string{cond}
	if f.PolicyKey != "" {
		where = append(where, "policy_key = ?")
		args = append(args, f.PolicyKey)
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, string(f.Kind))
	}
	var out []types.ArchiveExecution
	err = a.s.rows(ctx, "list archive executions",
		"SELECT id, "+strings.Join(executionCols, ", ")+" FROM archive_executions WHERE "+
			strings.Join(where, " AND ")+" ORDER BY id DESC LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			v, err := scanExecution(r)
			out = append(out, v)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(
		out,
		p.Limit,
		func(v types.ArchiveExecution) []string { return []string{itoa(v.ID)} },
	)
	return page, next, nil
}

var archivedCols = []string{
	"namespace", "repo_key", "path", "name", "depth", "sha256", "sha512", "sha1", "md5", "size",
	"created_at", "created_by", "modified_at", "modified_by", "package_type", "package_name",
	"package_version", "policy_key", "execution_id", "archived_at", "expires_at",
	"cold_blob_sha256", "item", "properties", "stats",
}

// splitArchived returns the canonical location of an item's original path.
func splitArchived(full string) (parent, name string, depth int, err error) {
	parent, name, depth, err = types.SplitItemPath(full)
	if err != nil {
		return "", "", 0, fmt.Errorf("archived item: %w: %w", store.ErrInvalid, err)
	}
	return parent, name, depth, nil
}

func archivedRow(a types.ArchivedItem) ([]any, error) {
	parent, name, depth, err := splitArchived(a.Item.FullPath())
	if err != nil {
		return nil, err
	}
	it := a.Item
	return []any{
		a.Namespace,
		it.RepoKey,
		parent,
		name,
		depth,
		it.Actual.SHA256,
		it.Actual.SHA512,
		it.Actual.SHA1,
		it.Actual.MD5,
		it.Size,
		micros(it.Created),
		it.CreatedBy,
		micros(it.Modified),
		it.ModifiedBy,
		a.PackageType,
		a.PackageName,
		a.PackageVersion,
		a.PolicyKey,
		a.ExecutionID,
		micros(a.ArchivedAt),
		micros(a.ExpiresAt),
		a.ColdBlobSHA256,
		mustJSON(a.Item),
		mustJSON(a.Properties),
		mustJSON(a.Stats),
	}, nil
}

// archivedSelect reads the columns the record is rebuilt from; the item's
// own fields come from its document.
const archivedSelect = "SELECT id, namespace, package_type, package_name, package_version, policy_key," +
	" execution_id, archived_at, expires_at, cold_blob_sha256, item, properties, stats FROM archived_items"

func scanArchived(r entRow) (types.ArchivedItem, error) {
	var (
		a                  types.ArchivedItem
		archived, expires  sql.NullInt64
		item, props, stats sql.NullString
	)
	if err := r.Scan(&a.ID, &a.Namespace, &a.PackageType, &a.PackageName, &a.PackageVersion,
		&a.PolicyKey, &a.ExecutionID, &archived, &expires, &a.ColdBlobSHA256,
		&item, &props, &stats); err != nil {
		return a, err //nolint:wrapcheck // wrapped by the caller
	}
	a.ArchivedAt, a.ExpiresAt = fromMicros(archived), fromMicros(expires)
	if err := fromJSON(item, &a.Item); err != nil {
		return a, err
	}
	if err := fromJSON(props, &a.Properties); err != nil {
		return a, err
	}
	return a, fromJSON(stats, &a.Stats)
}

func (a archiveStore) AddItem(
	ctx context.Context,
	it types.ArchivedItem,
) (types.ArchivedItem, error) {
	it = store.Normalise(it)
	row, err := archivedRow(it)
	if err != nil {
		return types.ArchivedItem{}, err
	}
	if it.ID, err = a.s.insertID(
		ctx,
		"add archived item",
		Insert("archived_items", archivedCols),
		row...); err != nil {
		return types.ArchivedItem{}, err
	}
	return it, nil
}

func (a archiveStore) Item(ctx context.Context, id int64) (types.ArchivedItem, error) {
	out, err := scanArchived(
		a.s.q(ctx).QueryRowContext(ctx, a.s.d.Rebind(archivedSelect+" WHERE id = ?"), id),
	)
	return out, a.s.wrap(fmt.Sprintf("get archived item %d", id), err)
}

// archived runs an archived-item query.
func (a archiveStore) archived(
	ctx context.Context,
	q string,
	args ...any,
) ([]types.ArchivedItem, error) {
	var out []types.ArchivedItem
	err := a.s.rows(ctx, "list archived items", q, args, func(r *sql.Rows) error {
		v, err := scanArchived(r)
		out = append(out, v)
		return err
	})
	return out, err
}

func (a archiveStore) FindItems(
	ctx context.Context,
	repoKey, fullPath string,
) ([]types.ArchivedItem, error) {
	parent, name, _, err := splitArchived(fullPath)
	if err != nil {
		return nil, err
	}
	return a.archived(
		ctx,
		archivedSelect+" WHERE repo_key = ? AND path = ? AND name = ? ORDER BY id DESC",
		repoKey,
		parent,
		name,
	)
}

func (a archiveStore) Items(
	ctx context.Context, f store.ArchivedFilter, p store.Page,
) ([]types.ArchivedItem, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := keyset(p, []string{"id"}, map[string]bool{"id": true}, false)
	if err != nil {
		return nil, "", err
	}
	where := []string{cond}
	if f.RepoKey != "" {
		where = append(where, "repo_key = ?")
		args = append(args, f.RepoKey)
	}
	if f.PolicyKey != "" {
		where = append(where, "policy_key = ?")
		args = append(args, f.PolicyKey)
	}
	if f.ExecutionID != 0 {
		where = append(where, "execution_id = ?")
		args = append(args, f.ExecutionID)
	}
	out, err := a.archived(
		ctx,
		archivedSelect+" WHERE "+strings.Join(where, " AND ")+" ORDER BY id LIMIT ?",
		append(args, p.Limit+1)...)
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(
		out,
		p.Limit,
		func(v types.ArchivedItem) []string { return []string{itoa(v.ID)} },
	)
	return page, next, nil
}

func (a archiveStore) DeleteItem(ctx context.Context, id int64) error {
	return a.s.execOne(
		ctx,
		fmt.Sprintf("delete archived item %d", id),
		"DELETE FROM archived_items WHERE id = ?",
		id,
	)
}

func (a archiveStore) ExpiredItems(
	ctx context.Context,
	before time.Time,
	limit int,
) ([]types.ArchivedItem, error) {
	return a.archived(ctx, archivedSelect+
		" WHERE expires_at IS NOT NULL AND expires_at < ? ORDER BY expires_at, id LIMIT ?",
		before.UnixMicro(), limitOr(limit))
}

// --- Workers ---------------------------------------------------------------

type workerStore struct{ s *Store }

// purposeWorkerSecret is the sealing purpose of worker secret values; the
// row id is "<worker>/<name>".
const purposeWorkerSecret = "worker_secrets.value"

var workerCols = []string{
	"key", "action", "enabled", "debug", "shared", "project_key", "description", "source",
	"program_hash", "failures", "blocked_until", "created_at", "updated_at", "filter_doc",
}

func workerRow(w types.Worker) []any {
	return []any{
		w.Key,
		string(w.Action),
		w.Enabled,
		w.Debug,
		w.Shared,
		w.ProjectKey,
		w.Description,
		w.SourceCode,
		w.ProgramHash,
		w.Failures,
		micros(w.BlockedUntil),
		micros(w.Created),
		micros(w.Updated),
		mustJSON(w.Filter),
	}
}

func scanWorker(r entRow) (types.Worker, error) {
	var (
		w                         types.Worker
		action                    string
		blocked, created, updated sql.NullInt64
		filter                    sql.NullString
	)
	if err := r.Scan(
		&w.Key,
		&action,
		&w.Enabled,
		&w.Debug,
		&w.Shared,
		&w.ProjectKey,
		&w.Description,
		&w.SourceCode,
		&w.ProgramHash,
		&w.Failures,
		&blocked,
		&created,
		&updated,
		&filter,
	); err != nil {
		return w, err //nolint:wrapcheck // wrapped by the caller
	}
	w.Action = types.WorkerAction(action)
	w.BlockedUntil, w.Created, w.Updated = fromMicros(
		blocked,
	), fromMicros(
		created,
	), fromMicros(
		updated,
	)
	return w, fromJSON(filter, &w.Filter)
}

func (w workerStore) Create(ctx context.Context, v types.Worker) error {
	_, err := w.s.exec(ctx, "create worker "+v.Key, Insert("workers", workerCols), workerRow(v)...)
	return err
}

func (w workerStore) Get(ctx context.Context, key string) (types.Worker, error) {
	out, err := scanWorker(w.s.q(ctx).QueryRowContext(ctx, w.s.d.Rebind(
		"SELECT "+strings.Join(workerCols, ", ")+" FROM workers WHERE key = ?"), key))
	return out, w.s.wrap("get worker "+key, err)
}

func (w workerStore) Update(ctx context.Context, v types.Worker) error {
	row := workerRow(v)
	sets := make([]string, 0, len(workerCols)-1)
	for _, c := range workerCols[1:] {
		sets = append(sets, c+" = ?")
	}
	return w.s.execOne(ctx, "update worker "+v.Key,
		"UPDATE workers SET "+strings.Join(sets, ", ")+" WHERE key = ?", append(row[1:], v.Key)...)
}

func (w workerStore) Delete(ctx context.Context, key string) error {
	// State, secrets and executions go with the row (ON DELETE CASCADE).
	return w.s.execOne(ctx, "delete worker "+key, "DELETE FROM workers WHERE key = ?", key)
}

func (w workerStore) List(ctx context.Context, f store.WorkerFilter) ([]types.Worker, error) {
	q := "SELECT " + strings.Join(workerCols, ", ") + " FROM workers WHERE 1 = 1"
	var args []any
	if f.Action != "" {
		q += " AND action = ?"
		args = append(args, string(f.Action))
	}
	if f.ProjectKey != "" {
		q += " AND project_key = ?"
		args = append(args, f.ProjectKey)
	}
	if f.EnabledOnly {
		q += " AND enabled = ?"
		args = append(args, true)
	}
	var out []types.Worker
	err := w.s.rows(ctx, "list workers", q+" ORDER BY key", args, func(r *sql.Rows) error {
		v, err := scanWorker(r)
		out = append(out, v)
		return err
	})
	return out, err
}

// requireWorker fails with ErrNotFound when the worker is absent.
func (w workerStore) requireWorker(ctx context.Context, key string) error {
	ok, err := w.s.exists(ctx, "find worker", "SELECT 1 FROM workers WHERE key = ?", key)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("worker %s: %w", key, store.ErrNotFound)
	}
	return nil
}

func (w workerStore) State(ctx context.Context, worker string) (map[string]string, error) {
	if err := w.requireWorker(ctx, worker); err != nil {
		return nil, err
	}
	out := map[string]string{}
	err := w.s.rows(
		ctx,
		"list worker state",
		"SELECT key, value FROM worker_state WHERE worker_key = ?",
		[]any{worker},
		func(r *sql.Rows) error {
			var k, v string
			if err := r.Scan(&k, &v); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			out[k] = v
			return nil
		},
	)
	return out, err
}

func (w workerStore) SetState(ctx context.Context, worker, key, value string) error {
	return w.s.Tx(ctx, func(ctx context.Context) error {
		if err := w.requireWorker(ctx, worker); err != nil {
			return err
		}
		_, err := w.s.exec(
			ctx,
			"set worker state",
			Upsert(
				"worker_state",
				[]string{"worker_key", "key", "value"},
				[]string{"worker_key", "key"},
			),
			worker,
			key,
			value,
		)
		return err
	})
}

func (w workerStore) DeleteState(ctx context.Context, worker, key string) error {
	_, err := w.s.exec(ctx, "delete worker state",
		"DELETE FROM worker_state WHERE worker_key = ? AND key = ?", worker, key)
	return err
}

func (w workerStore) PutSecret(ctx context.Context, worker, name, value string) error {
	sealed, err := w.s.seal(purposeWorkerSecret, worker+"/"+name, value)
	if err != nil {
		return err
	}
	return w.s.Tx(ctx, func(ctx context.Context) error {
		if err := w.requireWorker(ctx, worker); err != nil {
			return err
		}
		_, err := w.s.exec(
			ctx,
			"put worker secret",
			Upsert(
				"worker_secrets",
				[]string{"worker_key", "name", "value"},
				[]string{"worker_key", "name"},
			),
			worker,
			name,
			sealed,
		)
		return err
	})
}

func (w workerStore) Secrets(ctx context.Context, worker string) (map[string]string, error) {
	if err := w.requireWorker(ctx, worker); err != nil {
		return nil, err
	}
	out := map[string]string{}
	err := w.s.rows(
		ctx,
		"list worker secrets",
		"SELECT name, value FROM worker_secrets WHERE worker_key = ?",
		[]any{worker},
		func(r *sql.Rows) error {
			var (
				name   string
				sealed []byte
			)
			if err := r.Scan(&name, &sealed); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			v, err := w.s.unseal(purposeWorkerSecret, worker+"/"+name, sealed)
			out[name] = v
			return err
		},
	)
	return out, err
}

func (w workerStore) DeleteSecret(ctx context.Context, worker, name string) error {
	return w.s.execOne(ctx, "delete worker secret "+worker+"/"+name,
		"DELETE FROM worker_secrets WHERE worker_key = ? AND name = ?", worker, name)
}

var workerExecutionCols = []string{
	"id", "worker_key", "action", "trigger_kind", "status", "started_at", "duration_ms",
	"payload", "result", "logs", "error",
}

func scanWorkerExecution(r entRow) (types.WorkerExecution, error) {
	var (
		e                types.WorkerExecution
		action, trigger  string
		started, elapsed int64
	)
	if err := r.Scan(&e.ID, &e.WorkerKey, &action, &trigger, &e.Status, &started, &elapsed,
		&e.Payload, &e.Result, &e.Logs, &e.Error); err != nil {
		return e, err //nolint:wrapcheck // wrapped by the caller
	}
	e.Action, e.Trigger = types.WorkerAction(action), types.WorkerTrigger(trigger)
	e.Started, e.Duration = time.UnixMicro(started).UTC(), fromMillis(elapsed)
	return e, nil
}

func (w workerStore) AddExecution(ctx context.Context, e types.WorkerExecution) error {
	return w.s.Tx(ctx, func(ctx context.Context) error {
		if err := w.requireWorker(ctx, e.WorkerKey); err != nil {
			return err
		}
		// Started is NOT NULL so the newest-first keyset compares integers
		// only; a zero time stores as its own (very negative) instant.
		_, err := w.s.exec(
			ctx,
			"add worker execution "+e.ID,
			Insert("worker_executions", workerExecutionCols),
			e.ID,
			e.WorkerKey,
			string(e.Action),
			string(e.Trigger),
			e.Status,
			e.Started.UnixMicro(),
			millis(e.Duration),
			entBytes(e.Payload),
			entBytes(e.Result),
			entBytes(e.Logs),
			e.Error,
		)
		return err
	})
}

func (w workerStore) Execution(ctx context.Context, id string) (types.WorkerExecution, error) {
	out, err := scanWorkerExecution(w.s.q(ctx).QueryRowContext(ctx, w.s.d.Rebind(
		"SELECT "+strings.Join(
			workerExecutionCols,
			", ",
		)+" FROM worker_executions WHERE id = ?",
	), id))
	return out, w.s.wrap("get worker execution "+id, err)
}

func (w workerStore) Executions(
	ctx context.Context, worker string, p store.Page,
) ([]types.WorkerExecution, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	order := []string{"started_at", "id"}
	cond, args, err := keyset(p, order, map[string]bool{"started_at": true}, true)
	if err != nil {
		return nil, "", err
	}
	if worker != "" {
		cond += " AND worker_key = ?"
		args = append(args, worker)
	}
	var out []types.WorkerExecution
	err = w.s.rows(ctx, "list worker executions",
		"SELECT "+strings.Join(workerExecutionCols, ", ")+" FROM worker_executions WHERE "+cond+
			orderBy(order, true)+" LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			v, err := scanWorkerExecution(r)
			out = append(out, v)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(out, p.Limit, func(v types.WorkerExecution) []string {
		return []string{itoa(v.Started.UnixMicro()), v.ID}
	})
	return page, next, nil
}

func (w workerStore) PruneExecutions(ctx context.Context, before time.Time) (int64, error) {
	return w.s.exec(ctx, "prune worker executions",
		"DELETE FROM worker_executions WHERE started_at < ?", before.UnixMicro())
}
