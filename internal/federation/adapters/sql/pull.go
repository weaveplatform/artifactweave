package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// PullProgress keeps logical execution evidence independently of target rows.
func (s *Store) PullProgress() port.PullProgressStore { return pullProgress{s} }

type pullProgress struct{ s *Store }

const pullRunCols = "run_id, incarnation, target_key, binding_json, version, phase, source_cursor, target_cursor, " +
	"source_done, target_done, started_at, updated_at, completed_at"

func scanPullRun(row interface{ Scan(...any) error }) (model.PullRun, error) {
	var (
		r                     model.PullRun
		target, binding       string
		started, updated, end sql.NullInt64
	)
	if err := row.Scan(&r.Reference.ID, &r.Reference.Incarnation, &target, &binding, &r.Version, &r.Phase,
		&r.SourceCursor, &r.TargetCursor, &r.SourceDone, &r.TargetDone, &started, &updated, &end); err != nil {
		return model.PullRun{}, err //nolint:wrapcheck // the owning store wraps scan/driver errors
	}
	if err := dialect.FromJSON(sql.NullString{String: binding, Valid: true}, &r.Binding); err != nil {
		return model.PullRun{}, fmt.Errorf("decode stored pull binding: %w: %w", kernel.ErrStore, err)
	}
	r.Started, r.Updated, r.Completed = dialect.FromMicros(started), dialect.FromMicros(updated), dialect.FromMicros(end)
	if target != r.Binding.TargetKey {
		return model.PullRun{}, fmt.Errorf("stored pull target binding disagrees: %w", kernel.ErrStore)
	}
	if err := r.Validate(); err != nil {
		return model.PullRun{}, fmt.Errorf("stored pull run is invalid: %w: %w", kernel.ErrStore, err)
	}
	return r, nil
}

func (p pullProgress) read(ctx context.Context, id string) (model.PullRun, error) {
	r, err := scanPullRun(p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT "+pullRunCols+" FROM replication_pull_runs WHERE run_id = ?"+p.s.LockClause(ctx)), id))
	return r, p.s.Wrap("read pull run", err)
}

func (p pullProgress) Create(ctx context.Context, id string, binding model.PullBinding, at time.Time) (model.PullRun, error) {
	candidate, err := model.NewPullRun(id, binding, at)
	if err != nil {
		return model.PullRun{}, fmt.Errorf("validate pull run: %w", err)
	}
	document, err := dialect.ToJSON(candidate.Binding)
	if err != nil {
		return model.PullRun{}, fmt.Errorf("encode pull binding: %w", err)
	}
	var out model.PullRun
	err = p.s.Tx(ctx, func(ctx context.Context) error {
		_, err := p.s.Exec(ctx, "claim pull run", dialect.InsertIgnore("replication_pull_runs", dialect.OpsCols(pullRunCols), []string{"run_id"}),
			id, candidate.Reference.Incarnation, candidate.Binding.TargetKey, document, candidate.Version, candidate.Phase,
			candidate.SourceCursor, candidate.TargetCursor, candidate.SourceDone, candidate.TargetDone,
			dialect.Micros(candidate.Started), dialect.Micros(candidate.Updated), dialect.Micros(candidate.Completed))
		if err != nil {
			return err
		}
		current, err := p.read(ctx, id)
		if err != nil {
			return err
		}
		if current.Binding != candidate.Binding {
			return kernel.ErrConflict
		}
		out = current
		return nil
	})
	if err != nil {
		return model.PullRun{}, fmt.Errorf("create pull run: %w", err)
	}
	return out, nil
}

func (p pullProgress) Run(ctx context.Context, ref model.PullReference) (model.PullRun, error) {
	if err := ref.Validate(); err != nil {
		return model.PullRun{}, fmt.Errorf("validate pull reference: %w", err)
	}
	r, err := p.read(ctx, ref.ID)
	if err != nil {
		return model.PullRun{}, err
	}
	if r.Reference != ref {
		return model.PullRun{}, kernel.ErrNotFound
	}
	return r, nil
}

func (p pullProgress) Advance(ctx context.Context, expected int64, next model.PullRun) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("pull run advance requires a transaction: %w", kernel.ErrInvalid)
	}
	next = kernel.Normalise(next)
	current, err := p.Run(ctx, next.Reference)
	if errors.Is(err, kernel.ErrNotFound) || err == nil && current.Version != expected {
		return kernel.ErrConflict
	}
	if err != nil {
		return err
	}
	if err := model.ValidatePullAdvance(current, next); err != nil {
		return fmt.Errorf("validate pull advance: %w", err)
	}
	if next.Phase != current.Phase && next.Phase != model.PullFailed {
		pending, err := p.s.Exists(ctx, "check unfinished pull intents", "SELECT 1 FROM replication_pull_items WHERE run_id=? AND run_incarnation=? AND phase<>? LIMIT 1",
			next.Reference.ID, next.Reference.Incarnation, model.PullItemComplete)
		if err != nil {
			return err
		}
		if pending {
			return fmt.Errorf("pull traversal has unfinished items: %w", kernel.ErrConflict)
		}
	}
	n, err := p.s.Exec(
		ctx,
		"advance pull run",
		"UPDATE replication_pull_runs SET version=?, phase=?, source_cursor=?, target_cursor=?, source_done=?, target_done=?, updated_at=?, completed_at=? "+
			"WHERE run_id=? AND incarnation=? AND version=?",
		next.Version,
		next.Phase,
		next.SourceCursor,
		next.TargetCursor,
		next.SourceDone,
		next.TargetDone,
		dialect.Micros(next.Updated),
		dialect.Micros(next.Completed),
		next.Reference.ID,
		next.Reference.Incarnation,
		expected,
	)
	if err != nil {
		return err
	}
	if n != 1 {
		return kernel.ErrConflict
	}
	return nil
}

const pullItemCols = "run_id, run_incarnation, operation_key, intent_fingerprint, action, path, version, phase, document"

func scanPullItem(row interface{ Scan(...any) error }, run model.PullRun) (model.PullItem, error) {
	var (
		i                                       model.PullItem
		id, incarnation, key, fingerprint, path string
		action                                  model.PullAction
		version                                 int64
		phase                                   model.PullItemPhase
		document                                string
	)
	if err := row.Scan(&id, &incarnation, &key, &fingerprint, &action, &path, &version, &phase, &document); err != nil {
		return model.PullItem{}, err //nolint:wrapcheck // the owning store wraps scan/driver errors
	}
	if err := dialect.FromJSON(sql.NullString{String: document, Valid: true}, &i); err != nil {
		return model.PullItem{}, fmt.Errorf("decode stored pull intent: %w: %w", kernel.ErrStore, err)
	}
	actualKey, err := model.PullItemKey(i.Action, i.Path)
	if err != nil {
		return model.PullItem{}, fmt.Errorf("stored pull operation key is invalid: %w: %w", kernel.ErrStore, err)
	}
	actualFingerprint, err := model.PullIntentFingerprint(i)
	if err != nil {
		return model.PullItem{}, fmt.Errorf("stored pull fingerprint is invalid: %w: %w", kernel.ErrStore, err)
	}
	if i.Run.ID != id || i.Run.Incarnation != incarnation || i.Action != action || i.Path != path || i.Version != version || i.Phase != phase ||
		key != actualKey || fingerprint != actualFingerprint {
		return model.PullItem{}, fmt.Errorf("stored pull intent disagrees with its identity: %w", kernel.ErrStore)
	}
	if err := i.Validate(run); err != nil {
		return model.PullItem{}, fmt.Errorf("stored pull intent is invalid: %w: %w", kernel.ErrStore, err)
	}
	return i, nil
}

func (p pullProgress) item(ctx context.Context, run model.PullRun, action model.PullAction, path string) (model.PullItem, error) {
	key, err := model.PullItemKey(action, path)
	if err != nil {
		return model.PullItem{}, fmt.Errorf("derive pull operation key: %w", err)
	}
	i, err := scanPullItem(p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind("SELECT "+pullItemCols+
		" FROM replication_pull_items WHERE run_id=? AND run_incarnation=? AND operation_key=?"+p.s.LockClause(ctx)),
		run.Reference.ID, run.Reference.Incarnation, key), run)
	if err != nil {
		return model.PullItem{}, p.s.Wrap("read pull intent", err)
	}
	if i.Action != action || i.Path != path {
		return model.PullItem{}, kernel.ErrConflict
	}
	return i, nil
}

func (p pullProgress) CreateItem(ctx context.Context, in model.PullItem) (model.PullItem, error) {
	in = kernel.Normalise(in)
	key, err := model.PullItemKey(in.Action, in.Path)
	if err != nil {
		return model.PullItem{}, fmt.Errorf("derive pull operation key: %w", err)
	}
	fingerprint, err := model.PullIntentFingerprint(in)
	if err != nil {
		return model.PullItem{}, fmt.Errorf("fingerprint pull intent: %w", err)
	}
	var out model.PullItem
	err = p.s.Tx(ctx, func(ctx context.Context) error {
		run, err := p.Run(ctx, in.Run)
		if err != nil {
			return err
		}
		if run.Phase.Terminal() {
			return kernel.ErrConflict
		}
		if err := in.Validate(run); err != nil {
			return fmt.Errorf("validate pull intent: %w", err)
		}
		current, err := p.item(ctx, run, in.Action, in.Path)
		if err == nil {
			stored, err := model.PullIntentFingerprint(current)
			if err != nil {
				return fmt.Errorf("fingerprint pull intent: %w", err)
			}
			if stored != fingerprint {
				return kernel.ErrConflict
			}
			out = current
			return nil
		}
		if !errors.Is(err, kernel.ErrNotFound) {
			return err
		}
		if err := model.ValidatePullItemCreation(run, in); err != nil {
			return fmt.Errorf("validate new pull intent: %w", err)
		}
		document, err := dialect.ToJSON(in)
		if err != nil {
			return fmt.Errorf("encode pull intent: %w", err)
		}
		var ordinal int64
		if err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
			"SELECT COALESCE(MAX(ordinal),0)+1 FROM replication_pull_items WHERE run_id=? AND run_incarnation=?"),
			in.Run.ID, in.Run.Incarnation).Scan(&ordinal); err != nil {
			return p.s.Wrap("order pull intent", err)
		}
		if _, err := p.s.Exec(ctx, "create pull intent", dialect.Insert("replication_pull_items", append(dialect.OpsCols(pullItemCols), "ordinal")),
			in.Run.ID, in.Run.Incarnation, key, fingerprint, in.Action, in.Path, in.Version, in.Phase, document, ordinal); err != nil {
			return err
		}
		out = in
		return nil
	})
	if err != nil {
		return model.PullItem{}, fmt.Errorf("create pull intent: %w", err)
	}
	return out, nil
}

func (p pullProgress) Item(ctx context.Context, ref model.PullReference, action model.PullAction, path string) (model.PullItem, error) {
	var out model.PullItem
	err := p.s.Tx(ctx, func(ctx context.Context) error {
		run, err := p.Run(ctx, ref)
		if err != nil {
			return err
		}
		out, err = p.item(ctx, run, action, path)
		return err
	})
	if err != nil {
		return model.PullItem{}, fmt.Errorf("read pull intent: %w", err)
	}
	return out, nil
}

func (p pullProgress) AdvanceItem(ctx context.Context, expected int64, next model.PullItem) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("pull item advance requires a transaction: %w", kernel.ErrInvalid)
	}
	next = kernel.Normalise(next)
	run, err := p.Run(ctx, next.Run)
	if errors.Is(err, kernel.ErrNotFound) {
		return kernel.ErrConflict
	}
	if err != nil {
		return err
	}
	current, err := p.item(ctx, run, next.Action, next.Path)
	if errors.Is(err, kernel.ErrNotFound) || err == nil && current.Version != expected {
		return kernel.ErrConflict
	}
	if err != nil {
		return err
	}
	if err := model.ValidatePullItemAdvance(run, current, next); err != nil {
		return fmt.Errorf("validate pull item advance: %w", err)
	}
	key, err := model.PullItemKey(next.Action, next.Path)
	if err != nil {
		return fmt.Errorf("derive pull operation key: %w", err)
	}
	document, err := dialect.ToJSON(next)
	if err != nil {
		return fmt.Errorf("encode pull item evidence: %w", err)
	}
	n, err := p.s.Exec(
		ctx,
		"advance pull intent",
		"UPDATE replication_pull_items SET version=?, phase=?, document=? WHERE run_id=? AND run_incarnation=? AND operation_key=? AND version=?",
		next.Version,
		next.Phase,
		document,
		next.Run.ID,
		next.Run.Incarnation,
		key,
		expected,
	)
	if err != nil {
		return err
	}
	if n != 1 {
		return kernel.ErrConflict
	}
	return nil
}

func (p pullProgress) NextPending(ctx context.Context, ref model.PullReference) (model.PullItem, bool, error) {
	if err := ref.Validate(); err != nil {
		return model.PullItem{}, false, fmt.Errorf("validate pull reference: %w", err)
	}
	var out model.PullItem
	found := false
	err := p.s.Tx(ctx, func(ctx context.Context) error {
		run, err := p.Run(ctx, ref)
		if err != nil {
			return err
		}
		return p.s.Rows(ctx, "read next pull intent",
			"SELECT "+pullItemCols+" FROM replication_pull_items WHERE run_id=? AND run_incarnation=? AND phase<>? ORDER BY ordinal, operation_key LIMIT 1",
			[]any{ref.ID, ref.Incarnation, model.PullItemComplete}, func(row *sql.Rows) error {
				out, err = scanPullItem(row, run)
				found = err == nil
				return err
			})
	})
	if err != nil {
		return model.PullItem{}, false, fmt.Errorf("read next pull intent: %w", err)
	}
	return out, found, nil
}

func (p pullProgress) Items(ctx context.Context, ref model.PullReference, pending bool, page kernel.Page) ([]model.PullItem, string, error) {
	if err := ref.Validate(); err != nil {
		return nil, "", fmt.Errorf("validate pull reference: %w", err)
	}
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("pull item page: %w", err)
	}
	after, err := page.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("pull item continuation: %w", err)
	}
	var out []model.PullItem
	var next string
	err = p.s.Tx(ctx, func(ctx context.Context) error {
		run, err := p.Run(ctx, ref)
		if err != nil {
			return err
		}
		query := "SELECT " + pullItemCols + " FROM replication_pull_items WHERE run_id=? AND run_incarnation=?"
		args := []any{ref.ID, ref.Incarnation}
		if pending {
			query += " AND phase<>?"
			args = append(args, model.PullItemComplete)
		}
		if len(after) > 0 {
			query += " AND operation_key>?"
			args = append(args, after[0])
		}
		query += " ORDER BY operation_key LIMIT ?"
		args = append(args, page.Limit+1)
		if err := p.s.Rows(ctx, "page pull intents", query, args, func(row *sql.Rows) error {
			i, err := scanPullItem(row, run)
			if err != nil {
				return err
			}
			out = append(out, i)
			return nil
		}); err != nil {
			return err
		}
		if len(out) > page.Limit {
			out = out[:page.Limit]
			key, err := model.PullItemKey(out[len(out)-1].Action, out[len(out)-1].Path)
			if err != nil {
				return fmt.Errorf("derive pull operation key: %w", err)
			}
			next = kernel.Cursor(key)
		}
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("page pull intents: %w", err)
	}
	return out, next, nil
}

var _ port.PullProgressStore = pullProgress{}
