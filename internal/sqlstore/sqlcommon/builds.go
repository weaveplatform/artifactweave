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

// Builds implements store.Store.
func (s *Store) Builds() store.BuildStore { return buildStore{s} }

type buildStore struct{ s *Store }

// buildLevel is the module_position of build-level properties.
const buildLevel = -1

var buildCols = []string{
	"id", "project_key", "name", "number", "started_at", "url",
	"created_at", "created_by", "modified_at", "modified_by", "document",
}

// buildSelect selects every run column.
var buildSelect = "SELECT " + strings.Join(buildCols, ", ") + " FROM builds"

func scanBuild(r entRow) (types.Build, error) {
	var (
		b                 types.Build
		started           int64
		created, modified sql.NullInt64
		document          sql.NullString
	)
	if err := r.Scan(&b.ID, &b.ProjectKey, &b.Name, &b.Number, &started, &b.URL,
		&created, &b.CreatedBy, &modified, &b.ModifiedBy, &document); err != nil {
		return b, err //nolint:wrapcheck // wrapped by the caller
	}
	b.Started = time.UnixMicro(started).UTC()
	b.Created, b.Modified = fromMicros(created), fromMicros(modified)
	b.Document = fromRaw(document)
	return b, nil
}

// validBuild rejects a run the store cannot key.
func validBuild(b types.Build) error {
	if b.Name == "" || b.Number == "" || b.Started.IsZero() {
		return fmt.Errorf("build %q number %q: name, number and started are required: %w",
			b.Name, b.Number, store.ErrInvalid)
	}
	return nil
}

// one reads a single run selected by where, with its content.
func (b buildStore) one(ctx context.Context, op, where string, args ...any) (types.Build, error) {
	out, err := scanBuild(
		b.s.q(ctx).QueryRowContext(ctx, b.s.d.Rebind(buildSelect+" WHERE "+where), args...),
	)
	if err != nil {
		return types.Build{}, b.s.wrap(op, err)
	}
	return out, b.loadContent(ctx, &out)
}

// loadContent reads a run's properties and modules with their files.
func (b buildStore) loadContent(ctx context.Context, run *types.Build) error {
	op := fmt.Sprintf("read build run %d", run.ID)
	if err := b.s.rows(ctx, op,
		"SELECT module_id, type FROM build_modules WHERE build_id = ? ORDER BY position",
		[]any{run.ID}, func(r *sql.Rows) error {
			var m types.BuildModule
			if err := r.Scan(&m.ID, &m.Type); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			run.Modules = append(run.Modules, m)
			return nil
		}); err != nil {
		return err
	}
	if err := b.s.rows(ctx, op,
		"SELECT module_position, key, value FROM build_properties WHERE build_id = ?"+
			" ORDER BY module_position, key, value",
		[]any{run.ID}, func(r *sql.Rows) error {
			var (
				pos  int
				k, v string
			)
			if err := r.Scan(&pos, &k, &v); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			props := &run.Properties
			if pos != buildLevel {
				props = &run.Modules[pos].Properties
			}
			if *props == nil {
				*props = types.Properties{}
			}
			(*props)[k] = append((*props)[k], v)
			return nil
		}); err != nil {
		return err
	}
	if err := b.s.rows(ctx, op,
		"SELECT module_position, name, type, path, sha1, sha256, md5 FROM build_artifacts"+
			" WHERE build_id = ? ORDER BY module_position, position",
		[]any{run.ID}, func(r *sql.Rows) error {
			var (
				pos int
				f   types.BuildFile
			)
			if err := r.Scan(&pos, &f.Name, &f.Type, &f.Path,
				&f.Checksums.SHA1, &f.Checksums.SHA256, &f.Checksums.MD5); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			run.Modules[pos].Artifacts = append(run.Modules[pos].Artifacts, f)
			return nil
		}); err != nil {
		return err
	}
	return b.s.rows(ctx, op,
		"SELECT module_position, dependency_id, name, type, path, scopes, sha1, sha256, md5"+
			" FROM build_dependencies WHERE build_id = ? ORDER BY module_position, position",
		[]any{run.ID}, func(r *sql.Rows) error {
			var (
				pos    int
				f      types.BuildFile
				scopes sql.NullString
			)
			if err := r.Scan(&pos, &f.ID, &f.Name, &f.Type, &f.Path, &scopes,
				&f.Checksums.SHA1, &f.Checksums.SHA256, &f.Checksums.MD5); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			if err := fromJSON(scopes, &f.Scopes); err != nil {
				return err
			}
			run.Modules[pos].Dependencies = append(run.Modules[pos].Dependencies, f)
			return nil
		})
}

// writeProperties stores one property set at a module position.
func (b buildStore) writeProperties(
	ctx context.Context,
	id int64,
	pos int,
	props types.Properties,
) error {
	cols := []string{"build_id", "module_position", "key", "value"}
	for k, values := range props {
		for _, v := range values {
			if _, err := b.s.exec(ctx, "add build property "+k,
				InsertIgnore("build_properties", cols, cols), id, pos, k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeContent stores a run's properties and modules with their files. The
// rows must not exist yet.
func (b buildStore) writeContent(ctx context.Context, id int64, run types.Build) error {
	if err := b.writeProperties(ctx, id, buildLevel, run.Properties); err != nil {
		return err
	}
	artCols := []string{
		"build_id",
		"module_position",
		"position",
		"name",
		"type",
		"path",
		"sha1",
		"sha256",
		"md5",
	}
	depCols := []string{
		"build_id",
		"module_position",
		"position",
		"dependency_id",
		"name",
		"type",
		"path",
		"scopes",
		"sha1",
		"sha256",
		"md5",
	}
	for pos, m := range run.Modules {
		if _, err := b.s.exec(ctx, "add build module "+m.ID,
			Insert("build_modules", []string{"build_id", "position", "module_id", "type"}),
			id, pos, m.ID, m.Type); err != nil {
			return err
		}
		if err := b.writeProperties(ctx, id, pos, m.Properties); err != nil {
			return err
		}
		for n, f := range m.Artifacts {
			if _, err := b.s.exec(
				ctx,
				"add build artifact "+f.Name,
				Insert("build_artifacts", artCols),
				id,
				pos,
				n,
				f.Name,
				f.Type,
				f.Path,
				f.Checksums.SHA1,
				f.Checksums.SHA256,
				f.Checksums.MD5,
			); err != nil {
				return err
			}
		}
		for n, f := range m.Dependencies {
			if _, err := b.s.exec(
				ctx,
				"add build dependency "+f.ID,
				Insert("build_dependencies", depCols),
				id,
				pos,
				n,
				f.ID,
				f.Name,
				f.Type,
				f.Path,
				mustJSON(f.Scopes),
				f.Checksums.SHA1,
				f.Checksums.SHA256,
				f.Checksums.MD5,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b buildStore) Put(ctx context.Context, run types.Build) (types.Build, error) {
	if err := validBuild(run); err != nil {
		return types.Build{}, err
	}
	run = store.NormaliseBuild(run)
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		id, err := b.s.insertID(
			ctx,
			fmt.Sprintf("create build run %s/%s/%s", run.ProjectKey, run.Name, run.Number),
			Insert("builds", buildCols[1:]),
			run.ProjectKey,
			run.Name,
			run.Number,
			run.Started.UnixMicro(),
			run.URL,
			micros(
				run.Created,
			),
			run.CreatedBy,
			micros(run.Modified),
			run.ModifiedBy,
			rawJSON(run.Document),
		)
		if err != nil {
			return err
		}
		run.ID = id
		return b.writeContent(ctx, id, run)
	})
	if err != nil {
		return types.Build{}, err
	}
	return run, nil
}

func (b buildStore) Replace(ctx context.Context, run types.Build) error {
	run = store.NormaliseBuild(run)
	return b.s.Tx(ctx, func(ctx context.Context) error {
		op := fmt.Sprintf("replace build run %d", run.ID)
		if err := b.s.execOne(
			ctx,
			op,
			"UPDATE builds SET url = ?, modified_at = ?, modified_by = ?, document = ? WHERE id = ?",
			run.URL,
			micros(run.Modified),
			run.ModifiedBy,
			rawJSON(run.Document),
			run.ID,
		); err != nil {
			return err
		}
		for _, table := range []string{"build_properties", "build_modules"} {
			// Deleting the modules cascades to their artifacts and
			// dependencies.
			if _, err := b.s.exec(
				ctx,
				op,
				"DELETE FROM "+table+" WHERE build_id = ?",
				run.ID,
			); err != nil {
				return err
			}
		}
		return b.writeContent(ctx, run.ID, run)
	})
}

func (b buildStore) Get(
	ctx context.Context, project, name, number string, started time.Time,
) (types.Build, error) {
	op := fmt.Sprintf("get build run %s/%s/%s", project, name, number)
	where := "project_key = ? AND name = ? AND number = ?"
	args := []any{project, name, number}
	if !started.IsZero() {
		where += " AND started_at = ?"
		args = append(args, store.Normalise(started).UnixMicro())
	}
	return b.one(ctx, op, where+" ORDER BY started_at DESC, id DESC LIMIT 1", args...)
}

func (b buildStore) ByID(ctx context.Context, id int64) (types.Build, error) {
	return b.one(ctx, fmt.Sprintf("get build run %d", id), "id = ?", id)
}

func (b buildStore) Latest(ctx context.Context, project, name string) (types.Build, error) {
	return b.one(ctx, fmt.Sprintf("get latest build %s/%s", project, name),
		"project_key = ? AND name = ? ORDER BY started_at DESC, id DESC LIMIT 1", project, name)
}

// runList reads runs selected by where in the given order.
func (b buildStore) runList(
	ctx context.Context,
	op, where, order string,
	args ...any,
) ([]types.BuildRun, error) {
	var out []types.BuildRun
	err := b.s.rows(ctx, op, buildSelect+" WHERE "+where+order, args, func(r *sql.Rows) error {
		v, err := scanBuild(r)
		out = append(out, v.Run())
		return err
	})
	return out, err
}

func (b buildStore) Runs(
	ctx context.Context, project, name string, p store.Page,
) ([]types.BuildRun, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	order := []string{"started_at", "id"}
	cond, args, err := keyset(p, order, map[string]bool{"started_at": true, "id": true}, true)
	if err != nil {
		return nil, "", err
	}
	out, err := b.runList(ctx, fmt.Sprintf("list build runs %s/%s", project, name),
		"project_key = ? AND name = ? AND "+cond, orderBy(order, true)+" LIMIT ?",
		append(append([]any{project, name}, args...), p.Limit+1)...)
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(out, p.Limit, func(r types.BuildRun) []string {
		return []string{itoa(r.Started.UnixMicro()), itoa(r.ID)}
	})
	return page, next, nil
}

func (b buildStore) Names(
	ctx context.Context,
	project string,
	p store.Page,
) ([]types.BuildName, string, error) {
	p, err := p.Normalize()
	if err != nil {
		return nil, "", err //nolint:wrapcheck // already a store sentinel
	}
	cond, args, err := keyset(p, []string{"name"}, nil, false)
	if err != nil {
		return nil, "", err
	}
	var out []types.BuildName
	err = b.s.rows(ctx, "list build names "+project,
		"SELECT name, MAX(started_at) FROM builds WHERE project_key = ? AND "+cond+
			" GROUP BY name ORDER BY name LIMIT ?",
		append(append([]any{project}, args...), p.Limit+1),
		func(r *sql.Rows) error {
			n := types.BuildName{ProjectKey: project}
			var last int64
			if err := r.Scan(&n.Name, &last); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			n.LastStarted = time.UnixMicro(last).UTC()
			out = append(out, n)
			return nil
		})
	if err != nil {
		return nil, "", err
	}
	page, next := store.Trim(
		out,
		p.Limit,
		func(n types.BuildName) []string { return []string{n.Name} },
	)
	return page, next, nil
}

func (b buildStore) DeleteNumbers(
	ctx context.Context, project, name string, numbers []string,
) ([]types.BuildRun, error) {
	var out []types.BuildRun
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		out = nil
		for _, number := range numbers {
			op := fmt.Sprintf("delete build run %s/%s/%s", project, name, number)
			runs, err := b.runList(ctx, op, "project_key = ? AND name = ? AND number = ?",
				" ORDER BY started_at, id LIMIT 1"+b.s.lockClause(ctx), project, name, number)
			if err != nil {
				return err
			}
			if len(runs) == 0 {
				continue
			}
			if _, err := b.s.exec(
				ctx,
				op,
				"DELETE FROM builds WHERE id = ?",
				runs[0].ID,
			); err != nil {
				return err
			}
			out = append(out, runs[0])
		}
		return nil
	})
	return out, err
}

func (b buildStore) DeleteAll(ctx context.Context, project, name string) ([]types.BuildRun, error) {
	var out []types.BuildRun
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		op := fmt.Sprintf("delete build %s/%s", project, name)
		var err error
		out, err = b.runList(ctx, op, "project_key = ? AND name = ?",
			" ORDER BY started_at, id"+b.s.lockClause(ctx), project, name)
		if err != nil {
			return err
		}
		for _, table := range []string{"builds", "build_retention"} {
			if _, err := b.s.exec(
				ctx,
				op,
				"DELETE FROM "+table+" WHERE project_key = ? AND name = ?",
				project,
				name,
			); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

func (b buildStore) Rename(ctx context.Context, project, from, to string) error {
	return b.s.Tx(ctx, func(ctx context.Context) error {
		op := fmt.Sprintf("rename build %s/%s to %s", project, from, to)
		has := func(name string) (bool, error) {
			return b.s.exists(ctx, op,
				"SELECT 1 FROM builds WHERE project_key = ? AND name = ? LIMIT 1", project, name)
		}
		found, err := has(from)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%s: %w", op, store.ErrNotFound)
		}
		if from == to {
			return nil
		}
		taken, err := has(to)
		if err != nil {
			return err
		}
		if taken {
			return fmt.Errorf("%s: %w", op, store.ErrConflict)
		}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{"UPDATE builds SET name = ? WHERE project_key = ? AND name = ?", []any{to, project, from}},
			{"DELETE FROM build_retention WHERE project_key = ? AND name = ?", []any{project, to}},
			{"UPDATE build_retention SET name = ? WHERE project_key = ? AND name = ?", []any{to, project, from}},
		} {
			if _, err := b.s.exec(ctx, op, q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// runExists reports whether a run exists, as ErrNotFound when it does not.
func (b buildStore) runExists(ctx context.Context, op string, id int64) error {
	found, err := b.s.exists(ctx, op, "SELECT 1 FROM builds WHERE id = ?", id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s: %w", op, store.ErrNotFound)
	}
	return nil
}

var promotionCols = []string{
	"id",
	"build_id",
	"created_at",
	"created_by",
	"status",
	"comment",
	"ci_user",
	"source_repo",
	"repo",
}

func (b buildStore) AddPromotion(
	ctx context.Context,
	p types.BuildPromotion,
) (types.BuildPromotion, error) {
	p = store.Normalise(p)
	err := b.s.Tx(ctx, func(ctx context.Context) error {
		op := fmt.Sprintf("promote build run %d", p.BuildID)
		if err := b.runExists(ctx, op, p.BuildID); err != nil {
			return err
		}
		id, err := b.s.insertID(
			ctx,
			op,
			Insert("build_promotions", promotionCols[1:]),
			p.BuildID,
			micros(p.Created),
			p.CreatedBy,
			p.Status,
			p.Comment,
			p.CIUser,
			p.SourceRepo,
			p.Repo,
		)
		p.ID = id
		return err
	})
	if err != nil {
		return types.BuildPromotion{}, err
	}
	return p, nil
}

func (b buildStore) Promotions(ctx context.Context, buildID int64) ([]types.BuildPromotion, error) {
	op := fmt.Sprintf("list promotions of build run %d", buildID)
	if err := b.runExists(ctx, op, buildID); err != nil {
		return nil, err
	}
	var out []types.BuildPromotion
	// COALESCE keeps a promotion without a time first on PostgreSQL too,
	// which sorts NULLs last.
	err := b.s.rows(ctx, op,
		"SELECT "+strings.Join(promotionCols, ", ")+" FROM build_promotions WHERE build_id = ?"+
			" ORDER BY COALESCE(created_at, 0), id",
		[]any{buildID}, func(r *sql.Rows) error {
			var (
				p       types.BuildPromotion
				created sql.NullInt64
			)
			if err := r.Scan(&p.ID, &p.BuildID, &created, &p.CreatedBy, &p.Status, &p.Comment,
				&p.CIUser, &p.SourceRepo, &p.Repo); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			p.Created = fromMicros(created)
			out = append(out, p)
			return nil
		})
	return out, err
}

// find returns the files of table whose checksums match every non-empty
// field of c, with their runs and modules.
func (b buildStore) find(
	ctx context.Context,
	table string,
	c types.Checksums,
) ([]types.BuildFileRef, error) {
	if c.SHA1 == "" && c.SHA256 == "" && c.MD5 == "" {
		return nil, fmt.Errorf("build file lookup needs a checksum: %w", store.ErrInvalid)
	}
	var (
		where []string
		args  []any
	)
	for _, m := range []struct{ col, v string }{{"f.sha1", c.SHA1}, {"f.sha256", c.SHA256}, {"f.md5", c.MD5}} {
		if m.v != "" {
			where = append(where, m.col+" = ?")
			args = append(args, m.v)
		}
	}
	dep := table == "build_dependencies"
	fileCols := "f.name, f.type, f.path, f.sha1, f.sha256, f.md5"
	if dep {
		fileCols = "f.dependency_id, f.scopes, " + fileCols
	}
	var out []types.BuildFileRef
	err := b.s.rows(
		ctx,
		"find in "+table,
		"SELECT b.id, b.project_key, b.name, b.number, b.started_at, b.created_at, m.module_id, "+fileCols+
			" FROM "+table+" f"+
			" JOIN build_modules m ON m.build_id = f.build_id AND m.position = f.module_position"+
			" JOIN builds b ON b.id = f.build_id"+
			" WHERE "+strings.Join(
			where,
			" AND ",
		)+
			" ORDER BY b.id, f.module_position, f.position",
		args,
		func(r *sql.Rows) error {
			var (
				ref     types.BuildFileRef
				started int64
				created sql.NullInt64
				scopes  sql.NullString
			)
			dest := []any{
				&ref.Run.ID,
				&ref.Run.ProjectKey,
				&ref.Run.Name,
				&ref.Run.Number,
				&started,
				&created,
				&ref.Module,
			}
			if dep {
				dest = append(dest, &ref.File.ID, &scopes)
			}
			dest = append(dest, &ref.File.Name, &ref.File.Type, &ref.File.Path,
				&ref.File.Checksums.SHA1, &ref.File.Checksums.SHA256, &ref.File.Checksums.MD5)
			if err := r.Scan(dest...); err != nil {
				return err //nolint:wrapcheck // wrapped by rows
			}
			ref.Run.Started, ref.Run.Created = time.UnixMicro(started).UTC(), fromMicros(created)
			if err := fromJSON(scopes, &ref.File.Scopes); err != nil {
				return err
			}
			out = append(out, ref)
			return nil
		},
	)
	return out, err
}

func (b buildStore) FindArtifacts(
	ctx context.Context,
	c types.Checksums,
) ([]types.BuildFileRef, error) {
	return b.find(ctx, "build_artifacts", c)
}

func (b buildStore) FindDependencies(
	ctx context.Context,
	c types.Checksums,
) ([]types.BuildFileRef, error) {
	return b.find(ctx, "build_dependencies", c)
}

var retentionCols = []string{
	"project_key", "name", "count", "minimum_build_date", "delete_artifacts", "keep_numbers",
}

func (b buildStore) PutRetention(ctx context.Context, r types.BuildRetention) error {
	r = store.Normalise(r)
	_, err := b.s.exec(
		ctx,
		fmt.Sprintf("set build retention %s/%s", r.ProjectKey, r.Name),
		Upsert("build_retention", retentionCols, retentionCols[:2]),
		r.ProjectKey,
		r.Name,
		r.Count,
		micros(r.MinimumBuildDate),
		r.DeleteArtifacts,
		mustJSON(r.KeepNumbers),
	)
	return err
}

func (b buildStore) Retention(
	ctx context.Context,
	project, name string,
) (types.BuildRetention, error) {
	var (
		r       types.BuildRetention
		minDate sql.NullInt64
		keep    sql.NullString
	)
	err := b.s.q(ctx).QueryRowContext(ctx, b.s.d.Rebind(
		"SELECT "+strings.Join(
			retentionCols,
			", ",
		)+" FROM build_retention WHERE project_key = ? AND name = ?",
	),
		project, name).Scan(&r.ProjectKey, &r.Name, &r.Count, &minDate, &r.DeleteArtifacts, &keep)
	if err != nil {
		return types.BuildRetention{}, b.s.wrap(
			fmt.Sprintf("get build retention %s/%s", project, name),
			err,
		)
	}
	r.MinimumBuildDate = fromMicros(minDate)
	return r, fromJSON(keep, &r.KeepNumbers)
}
