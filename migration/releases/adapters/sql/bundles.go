package sql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/migration/releases/core/model"
	"github.com/weaveplatform/artifactweave/migration/releases/core/port"
)

// Bundles returns the release bundle store: bundles with their
// artifacts, keeps, retention audits and exports (decision 0023).
func (s *Store) Bundles() port.BundleStore { return bundleStore{s} }

// Distribution returns the distribution store: trackers and their
// per-peer jobs (decision 0023).
func (s *Store) Distribution() port.DistributionStore { return distributionStore{s} }

// --- Release bundles -------------------------------------------------------

type bundleStore struct{ s *Store }

var bundleCols = []string{
	"name", "version", "state", "storing_repo", "project_key", "origin", "description",
	"created_at", "created_by", "signed_at", "signing_key_alias", "signature", "notes", "spec",
}

func bundleRow(b model.ReleaseBundle) []any {
	return []any{
		b.Name, b.Version, string(b.State), b.StoringRepository, b.ProjectKey, string(b.Origin),
		b.Description, dialect.Micros(b.Created), b.CreatedBy, dialect.Micros(b.SignedAt),
		b.SigningKeyAlias,
		dialect.EntBytes(b.Signature), dialect.MustJSON(b.Notes), dialect.MustJSON(b.Spec),
	}
}

func scanBundle(r dialect.EntRow) (model.ReleaseBundle, error) {
	var (
		b               model.ReleaseBundle
		state, origin   string
		created, signed sql.NullInt64
		notes, spec     sql.NullString
	)
	if err := r.Scan(&b.Name, &b.Version, &state, &b.StoringRepository, &b.ProjectKey, &origin,
		&b.Description, &created, &b.CreatedBy, &signed, &b.SigningKeyAlias, &b.Signature,
		&notes, &spec); err != nil {
		return b, err //nolint:wrapcheck // wrapped by the caller
	}
	b.State, b.Origin = model.BundleState(state), model.BundleOrigin(origin)
	b.Created, b.SignedAt = dialect.FromMicros(created), dialect.FromMicros(signed)
	if err := dialect.FromJSON(notes, &b.Notes); err != nil {
		return b, err
	}
	return b, dialect.FromJSON(spec, &b.Spec)
}

// get reads one bundle version, locking it inside a transaction when lock
// is set.
func (b bundleStore) get(
	ctx context.Context,
	name, version string,
	lock bool,
) (model.ReleaseBundle, error) {
	q := "SELECT " + strings.Join(
		bundleCols,
		", ",
	) + " FROM release_bundles WHERE name = ? AND version = ?"
	if lock {
		q += b.s.LockClause(ctx)
	}
	out, err := scanBundle(b.s.Q(ctx).QueryRowContext(ctx, b.s.Dialect().Rebind(q), name, version))
	return out, b.s.Wrap(fmt.Sprintf("get release bundle %s/%s", name, version), err)
}

// insertArtifacts writes a bundle's artifact list, owned by (name, version).
func (b bundleStore) insertArtifacts(
	ctx context.Context,
	name, version string,
	list []model.BundleArtifact,
) error {
	cols := []string{
		"bundle_name", "bundle_version", "target_path", "source_path",
		"sha256", "sha512", "sha1", "md5", "size", "props",
	}
	for _, a := range list {
		if _, err := b.s.Exec(
			ctx,
			"add bundle artifact "+a.TargetPath,
			dialect.Insert("bundle_artifacts", cols),
			name,
			version,
			a.TargetPath,
			a.SourcePath,
			a.Checksums.SHA256,
			a.Checksums.SHA512,
			a.Checksums.SHA1,
			a.Checksums.MD5,
			a.Size,
			dialect.MustJSON(a.Props),
		); err != nil {
			return err
		}
	}
	return nil
}

func (b bundleStore) Create(
	ctx context.Context,
	rb model.ReleaseBundle,
	list []model.BundleArtifact,
) error {
	row := bundleRow(kernel.Normalise(rb))
	return b.s.Tx(ctx, func(ctx context.Context) error {
		if _, err := b.s.Exec(ctx, "create release bundle "+rb.Name+"/"+rb.Version,
			dialect.Insert("release_bundles", bundleCols), row...); err != nil {
			return err
		}
		return b.insertArtifacts(ctx, rb.Name, rb.Version, list)
	})
}

func (b bundleStore) Get(ctx context.Context, name, version string) (model.ReleaseBundle, error) {
	return b.get(ctx, name, version, false)
}

func (b bundleStore) Update(ctx context.Context, rb model.ReleaseBundle) error {
	row := bundleRow(kernel.Normalise(rb))
	sets := make([]string, 0, len(bundleCols)-2)
	for _, c := range bundleCols[2:] {
		sets = append(sets, c+" = ?")
	}
	return b.s.ExecOne(ctx, "update release bundle "+rb.Name+"/"+rb.Version,
		"UPDATE release_bundles SET "+strings.Join(sets, ", ")+" WHERE name = ? AND version = ?",
		append(row[2:], rb.Name, rb.Version)...)
}

func (b bundleStore) ReplaceArtifacts(
	ctx context.Context,
	name, version string,
	list []model.BundleArtifact,
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
				kernel.ErrConflict,
			)
		}
		if _, err := b.s.Exec(
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
	return b.s.ExecOne(ctx, fmt.Sprintf("delete release bundle %s/%s", name, version),
		"DELETE FROM release_bundles WHERE name = ? AND version = ?", name, version)
}

func (b bundleStore) List(
	ctx context.Context, f port.BundleFilter, p kernel.Page,
) ([]model.ReleaseBundle, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	order := []string{"name", "version"}
	cond, args, err := dialect.Keyset(p, order, nil, false)
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
	var out []model.ReleaseBundle
	err = b.s.Rows(ctx, "list release bundles",
		"SELECT "+strings.Join(bundleCols, ", ")+" FROM release_bundles WHERE "+
			strings.Join(where, " AND ")+dialect.OrderBy(order, false)+" LIMIT ?",
		append(args, p.Limit+1),
		func(r *sql.Rows) error {
			v, err := scanBundle(r)
			out = append(out, v)
			return err
		})
	if err != nil {
		return nil, "", err
	}
	page, next := kernel.Trim(
		out,
		p.Limit,
		func(v model.ReleaseBundle) []string { return []string{v.Name, v.Version} },
	)
	return page, next, nil
}

// requireBundle fails with ErrNotFound when the bundle version is absent.
func (b bundleStore) requireBundle(ctx context.Context, name, version string) error {
	ok, err := b.s.Exists(ctx, "find release bundle",
		"SELECT 1 FROM release_bundles WHERE name = ? AND version = ?", name, version)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("release bundle %s/%s: %w", name, version, kernel.ErrNotFound)
	}
	return nil
}

func (b bundleStore) Artifacts(
	ctx context.Context,
	name, version string,
) ([]model.BundleArtifact, error) {
	if err := b.requireBundle(ctx, name, version); err != nil {
		return nil, err
	}
	var out []model.BundleArtifact
	err := b.s.Rows(
		ctx,
		"list bundle artifacts",
		"SELECT bundle_name, bundle_version, target_path, source_path,"+
			" sha256, sha512, sha1, md5, size, props"+
			" FROM bundle_artifacts WHERE bundle_name = ? AND bundle_version = ? ORDER BY target_path",
		[]any{name, version},
		func(r *sql.Rows) error {
			var (
				a     model.BundleArtifact
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
				return err //nolint:wrapcheck // wrapped by Rows
			}
			if err := dialect.FromJSON(props, &a.Props); err != nil {
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
			q = dialect.InsertIgnore("bundle_keeps", []string{"bundle_name", "bundle_version"},
				[]string{"bundle_name", "bundle_version"})
		}
		_, err := b.s.Exec(ctx, "set bundle keep", q, name, version)
		return err
	})
}

func (b bundleStore) Kept(ctx context.Context, name, version string) (bool, error) {
	if err := b.requireBundle(ctx, name, version); err != nil {
		return false, err
	}
	return b.s.Exists(ctx, "find bundle keep",
		"SELECT 1 FROM bundle_keeps WHERE bundle_name = ? AND bundle_version = ?", name, version)
}

func (b bundleStore) AddAudit(ctx context.Context, a model.BundleRetentionAudit) error {
	_, err := b.s.Exec(
		ctx,
		"add bundle audit",
		dialect.Insert(
			"bundle_audits",
			[]string{"bundle_name", "bundle_version", "operation", "created_at", "created_by"},
		),
		a.BundleName,
		a.BundleVersion,
		string(a.Operation),
		dialect.Micros(a.Created),
		a.CreatedBy,
	)
	return err
}

func (b bundleStore) Audits(
	ctx context.Context,
	name string,
) ([]model.BundleRetentionAudit, error) {
	q := "SELECT bundle_name, bundle_version, operation, created_at, created_by FROM bundle_audits"
	var args []any
	if name != "" {
		q += " WHERE bundle_name = ?"
		args = append(args, name)
	}
	var out []model.BundleRetentionAudit
	err := b.s.Rows(
		ctx,
		"list bundle audits",
		q+dialect.OrderBy([]string{"created_at", "id"}, true),
		args,
		func(r *sql.Rows) error {
			var (
				a       model.BundleRetentionAudit
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
				return err //nolint:wrapcheck // wrapped by Rows
			}
			a.Operation, a.Created = model.BundleOperation(op), dialect.FromMicros(created)
			out = append(out, a)
			return nil
		},
	)
	return out, err
}

func (b bundleStore) PutExport(ctx context.Context, e model.BundleExport) error {
	return b.s.Tx(ctx, func(ctx context.Context) error {
		if err := b.requireBundle(ctx, e.BundleName, e.BundleVersion); err != nil {
			return err
		}
		_, err := b.s.Exec(ctx, "put bundle export",
			dialect.Upsert("bundle_exports",
				[]string{"bundle_name", "bundle_version", "status", "archive_sha256", "created_at"},
				[]string{"bundle_name", "bundle_version"}),
			e.BundleName, e.BundleVersion, string(e.Status), e.ArchiveSHA256,
			dialect.Micros(e.Created))
		return err
	})
}

func (b bundleStore) Export(ctx context.Context, name, version string) (model.BundleExport, error) {
	var (
		e       model.BundleExport
		status  string
		created sql.NullInt64
	)
	err := b.s.Q(ctx).QueryRowContext(ctx, b.s.Dialect().Rebind(
		"SELECT bundle_name, bundle_version, status, archive_sha256, created_at FROM bundle_exports"+
			" WHERE bundle_name = ? AND bundle_version = ?",
	),
		name, version).
		Scan(&e.BundleName, &e.BundleVersion, &status, &e.ArchiveSHA256, &created)
	if err != nil {
		return e, b.s.Wrap(fmt.Sprintf("get bundle export %s/%s", name, version), err)
	}
	e.Status, e.Created = model.JobStatus(status), dialect.FromMicros(created)
	return e, nil
}

func (b bundleStore) DeleteExport(ctx context.Context, name, version string) error {
	return b.s.ExecOne(ctx, fmt.Sprintf("delete bundle export %s/%s", name, version),
		"DELETE FROM bundle_exports WHERE bundle_name = ? AND bundle_version = ?", name, version)
}

// --- Distribution ----------------------------------------------------------

type distributionStore struct{ s *Store }

var trackerCols = []string{
	"bundle_name", "bundle_version", "kind", "dry_run", "auto_create_repos",
	"status", "created_at", "created_by", "rules",
}

func trackerRow(t model.DistributionTracker) []any {
	return []any{
		t.BundleName, t.BundleVersion, string(t.Kind), t.DryRun, t.AutoCreateMissingRepositories,
		string(t.Status), dialect.Micros(t.Created), t.CreatedBy, dialect.MustJSON(t.Rules),
	}
}

func scanTracker(r dialect.EntRow) (model.DistributionTracker, error) {
	var (
		t            model.DistributionTracker
		kind, status string
		created      sql.NullInt64
		rules        sql.NullString
	)
	if err := r.Scan(&t.ID, &t.BundleName, &t.BundleVersion, &kind, &t.DryRun,
		&t.AutoCreateMissingRepositories, &status, &created, &t.CreatedBy, &rules); err != nil {
		return t, err //nolint:wrapcheck // wrapped by the caller
	}
	t.Kind, t.Status, t.Created = model.TrackerKind(
		kind,
	), model.JobStatus(
		status,
	), dialect.FromMicros(
		created,
	)
	return t, dialect.FromJSON(rules, &t.Rules)
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

func (d distributionStore) putJob(ctx context.Context, j model.DistributionJob, upsert bool) error {
	q := dialect.Insert("distribution_jobs", jobCols)
	if upsert {
		q = dialect.Upsert("distribution_jobs", jobCols, []string{"tracker_id", "peer"})
	}
	_, err := d.s.Exec(ctx, "put distribution job "+j.PeerInstanceID, q,
		j.TrackerID, j.PeerInstanceID, string(j.Status), j.Priority,
		dialect.Micros(j.Started), dialect.Micros(j.Finished), j.Error)
	return err
}

func (d distributionStore) CreateTracker(
	ctx context.Context, t model.DistributionTracker, jobs []model.DistributionJob,
) (model.DistributionTracker, error) {
	t = kernel.Normalise(t)
	err := d.s.Tx(ctx, func(ctx context.Context) error {
		id, err := d.s.InsertID(ctx, "create distribution tracker",
			dialect.Insert("distribution_trackers", trackerCols), trackerRow(t)...)
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
		return model.DistributionTracker{}, err
	}
	return t, nil
}

func (d distributionStore) Tracker(
	ctx context.Context,
	id int64,
) (model.DistributionTracker, error) {
	out, err := scanTracker(d.s.Q(ctx).QueryRowContext(ctx, d.s.Dialect().Rebind(
		"SELECT id, "+strings.Join(
			trackerCols,
			", ",
		)+" FROM distribution_trackers WHERE id = ?",
	), id))
	return out, d.s.Wrap(fmt.Sprintf("get distribution tracker %d", id), err)
}

func (d distributionStore) UpdateTracker(ctx context.Context, t model.DistributionTracker) error {
	row := trackerRow(kernel.Normalise(t))
	sets := make([]string, 0, len(trackerCols))
	for _, c := range trackerCols {
		sets = append(sets, c+" = ?")
	}
	return d.s.ExecOne(
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
) ([]model.DistributionTracker, error) {
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
	var out []model.DistributionTracker
	err := d.s.Rows(
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

func (d distributionStore) PutJob(ctx context.Context, j model.DistributionJob) error {
	return d.s.Tx(ctx, func(ctx context.Context) error {
		if err := d.requireTracker(ctx, j.TrackerID); err != nil {
			return err
		}
		return d.putJob(ctx, j, true)
	})
}

// requireTracker fails with ErrNotFound when the tracker is absent.
func (d distributionStore) requireTracker(ctx context.Context, id int64) error {
	ok, err := d.s.Exists(
		ctx,
		"find distribution tracker",
		"SELECT 1 FROM distribution_trackers WHERE id = ?",
		id,
	)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("distribution tracker %d: %w", id, kernel.ErrNotFound)
	}
	return nil
}

// jobs runs a job query.
func (d distributionStore) jobs(
	ctx context.Context,
	where string,
	args ...any,
) ([]model.DistributionJob, error) {
	var out []model.DistributionJob
	err := d.s.Rows(
		ctx,
		"list distribution jobs",
		"SELECT "+strings.Join(
			jobCols,
			", ",
		)+" FROM distribution_jobs WHERE "+where+" ORDER BY tracker_id, peer",
		args,
		func(r *sql.Rows) error {
			var (
				j                 model.DistributionJob
				status            string
				started, finished sql.NullInt64
			)
			if err := r.Scan(&j.TrackerID, &j.PeerInstanceID, &status, &j.Priority,
				&started, &finished, &j.Error); err != nil {
				return err //nolint:wrapcheck // wrapped by Rows
			}
			j.Status, j.Started, j.Finished = model.JobStatus(
				status,
			), dialect.FromMicros(
				started,
			), dialect.FromMicros(
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
) ([]model.DistributionJob, error) {
	if err := d.requireTracker(ctx, trackerID); err != nil {
		return nil, err
	}
	return d.jobs(ctx, "tracker_id = ?", trackerID)
}

func (d distributionStore) JobsForPeer(
	ctx context.Context, peer string, statuses []model.JobStatus,
) ([]model.DistributionJob, error) {
	where := "peer = ?"
	args := []any{peer}
	if len(statuses) > 0 {
		where += " AND status IN (" + dialect.Placeholders(len(statuses)) + ")"
		for _, st := range statuses {
			args = append(args, string(st))
		}
	}
	return d.jobs(ctx, where, args...)
}
