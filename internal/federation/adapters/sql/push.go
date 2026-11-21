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

// PushProgress keeps logical execution evidence independently of target rows.
func (s *Store) PushProgress() port.PushProgressStore { return pushProgress{s} }

type pushProgress struct{ s *Store }

const pushRunCols = "run_id, incarnation, target_key, binding_json, version, phase, source_cursor, target_cursor, " +
	"source_done, target_done, started_at, updated_at, completed_at"

func scanPushRun(row interface{ Scan(...any) error }) (model.PushRun, error) {
	var (
		r                     model.PushRun
		target, binding       string
		started, updated, end sql.NullInt64
	)
	if err := row.Scan(&r.Reference.ID, &r.Reference.Incarnation, &target, &binding, &r.Version, &r.Phase,
		&r.SourceCursor, &r.TargetCursor, &r.SourceDone, &r.TargetDone, &started, &updated, &end); err != nil {
		return model.PushRun{}, err //nolint:wrapcheck // the owning store wraps scan/driver errors
	}
	if err := dialect.FromJSON(sql.NullString{String: binding, Valid: true}, &r.Binding); err != nil {
		return model.PushRun{}, fmt.Errorf("decode stored push binding: %w: %w", kernel.ErrStore, err)
	}
	r.Started, r.Updated, r.Completed = dialect.FromMicros(started), dialect.FromMicros(updated), dialect.FromMicros(end)
	if target != r.Binding.TargetKey {
		return model.PushRun{}, fmt.Errorf("stored push target binding disagrees: %w", kernel.ErrStore)
	}
	if err := r.Validate(); err != nil {
		return model.PushRun{}, fmt.Errorf("stored push run is invalid: %w: %w", kernel.ErrStore, err)
	}
	return r, nil
}

func (p pushProgress) read(ctx context.Context, id string) (model.PushRun, error) {
	r, err := scanPushRun(p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT "+pushRunCols+" FROM replication_push_runs WHERE run_id = ?"+p.s.LockClause(ctx)), id))
	return r, p.s.Wrap("read push run", err)
}

func (p pushProgress) Create(ctx context.Context, id string, binding model.PushBinding, at time.Time) (model.PushRun, error) {
	candidate, err := model.NewPushRun(id, binding, at)
	if err != nil {
		return model.PushRun{}, fmt.Errorf("validate push run: %w", err)
	}
	document, err := dialect.ToJSON(candidate.Binding)
	if err != nil {
		return model.PushRun{}, fmt.Errorf("encode push binding: %w", err)
	}
	var out model.PushRun
	err = p.s.Tx(ctx, func(ctx context.Context) error {
		_, err := p.s.Exec(ctx, "claim push run", dialect.InsertIgnore("replication_push_runs", dialect.OpsCols(pushRunCols), []string{"run_id"}),
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
		return model.PushRun{}, fmt.Errorf("create push run: %w", err)
	}
	return out, nil
}

func (p pushProgress) Run(ctx context.Context, ref model.PushReference) (model.PushRun, error) {
	if err := ref.Validate(); err != nil {
		return model.PushRun{}, fmt.Errorf("validate push reference: %w", err)
	}
	r, err := p.read(ctx, ref.ID)
	if err != nil {
		return model.PushRun{}, err
	}
	if r.Reference != ref {
		return model.PushRun{}, kernel.ErrNotFound
	}
	return r, nil
}

func (p pushProgress) Advance(ctx context.Context, expected int64, next model.PushRun) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("push run advance requires a transaction: %w", kernel.ErrInvalid)
	}
	next = kernel.Normalise(next)
	current, err := p.Run(ctx, next.Reference)
	if errors.Is(err, kernel.ErrNotFound) || err == nil && current.Version != expected {
		return kernel.ErrConflict
	}
	if err != nil {
		return err
	}
	if err := model.ValidatePushAdvance(current, next); err != nil {
		return fmt.Errorf("validate push advance: %w", err)
	}
	if next.Phase != current.Phase && next.Phase != model.PushFailed {
		pending, err := p.s.Exists(ctx, "check unfinished push intents", "SELECT 1 FROM replication_push_items WHERE run_id=? AND run_incarnation=? AND phase<>? LIMIT 1",
			next.Reference.ID, next.Reference.Incarnation, model.PushItemComplete)
		if err != nil {
			return err
		}
		if pending {
			return fmt.Errorf("push traversal has unfinished items: %w", kernel.ErrConflict)
		}
	}
	n, err := p.s.Exec(
		ctx,
		"advance push run",
		"UPDATE replication_push_runs SET version=?, phase=?, source_cursor=?, target_cursor=?, source_done=?, target_done=?, updated_at=?, completed_at=? "+
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

const pushItemCols = "run_id, run_incarnation, operation_key, intent_fingerprint, action, path, version, phase, document"

func scanPushItem(row interface{ Scan(...any) error }, run model.PushRun) (model.PushItem, error) {
	var (
		i                                       model.PushItem
		id, incarnation, key, fingerprint, path string
		action                                  model.PushAction
		version                                 int64
		phase                                   model.PushItemPhase
		document                                string
	)
	if err := row.Scan(&id, &incarnation, &key, &fingerprint, &action, &path, &version, &phase, &document); err != nil {
		return model.PushItem{}, err //nolint:wrapcheck // the owning store wraps scan/driver errors
	}
	if err := dialect.FromJSON(sql.NullString{String: document, Valid: true}, &i); err != nil {
		return model.PushItem{}, fmt.Errorf("decode stored push intent: %w: %w", kernel.ErrStore, err)
	}
	actualKey, err := model.PushItemKey(i.Action, i.Path)
	if err != nil {
		return model.PushItem{}, fmt.Errorf("stored push operation key is invalid: %w: %w", kernel.ErrStore, err)
	}
	actualFingerprint, err := model.PushIntentFingerprint(i)
	if err != nil {
		return model.PushItem{}, fmt.Errorf("stored push fingerprint is invalid: %w: %w", kernel.ErrStore, err)
	}
	if i.Run.ID != id || i.Run.Incarnation != incarnation || i.Action != action || i.Path != path || i.Version != version || i.Phase != phase ||
		key != actualKey || fingerprint != actualFingerprint {
		return model.PushItem{}, fmt.Errorf("stored push intent disagrees with its identity: %w", kernel.ErrStore)
	}
	if err := i.Validate(run); err != nil {
		return model.PushItem{}, fmt.Errorf("stored push intent is invalid: %w: %w", kernel.ErrStore, err)
	}
	return i, nil
}

func (p pushProgress) item(ctx context.Context, run model.PushRun, action model.PushAction, path string) (model.PushItem, error) {
	key, err := model.PushItemKey(action, path)
	if err != nil {
		return model.PushItem{}, fmt.Errorf("derive push operation key: %w", err)
	}
	i, err := scanPushItem(p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind("SELECT "+pushItemCols+
		" FROM replication_push_items WHERE run_id=? AND run_incarnation=? AND operation_key=?"+p.s.LockClause(ctx)),
		run.Reference.ID, run.Reference.Incarnation, key), run)
	if err != nil {
		return model.PushItem{}, p.s.Wrap("read push intent", err)
	}
	if i.Action != action || i.Path != path {
		return model.PushItem{}, kernel.ErrConflict
	}
	return i, nil
}

func (p pushProgress) CreateItem(ctx context.Context, in model.PushItem) (model.PushItem, error) {
	in = kernel.Normalise(in)
	key, err := model.PushItemKey(in.Action, in.Path)
	if err != nil {
		return model.PushItem{}, fmt.Errorf("derive push operation key: %w", err)
	}
	fingerprint, err := model.PushIntentFingerprint(in)
	if err != nil {
		return model.PushItem{}, fmt.Errorf("fingerprint push intent: %w", err)
	}
	var out model.PushItem
	err = p.s.Tx(ctx, func(ctx context.Context) error {
		run, err := p.Run(ctx, in.Run)
		if err != nil {
			return err
		}
		if run.Phase.Terminal() {
			return kernel.ErrConflict
		}
		if err := in.Validate(run); err != nil {
			return fmt.Errorf("validate push intent: %w", err)
		}
		current, err := p.item(ctx, run, in.Action, in.Path)
		if err == nil {
			stored, err := model.PushIntentFingerprint(current)
			if err != nil {
				return fmt.Errorf("fingerprint push intent: %w", err)
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
		if err := model.ValidatePushItemCreation(run, in); err != nil {
			return fmt.Errorf("validate new push intent: %w", err)
		}
		document, err := dialect.ToJSON(in)
		if err != nil {
			return fmt.Errorf("encode push intent: %w", err)
		}
		if _, err := p.s.Exec(ctx, "create push intent", dialect.Insert("replication_push_items", dialect.OpsCols(pushItemCols)),
			in.Run.ID, in.Run.Incarnation, key, fingerprint, in.Action, in.Path, in.Version, in.Phase, document); err != nil {
			return err
		}
		out = in
		return nil
	})
	if err != nil {
		return model.PushItem{}, fmt.Errorf("create push intent: %w", err)
	}
	return out, nil
}

func (p pushProgress) Item(ctx context.Context, ref model.PushReference, action model.PushAction, path string) (model.PushItem, error) {
	var out model.PushItem
	err := p.s.Tx(ctx, func(ctx context.Context) error {
		run, err := p.Run(ctx, ref)
		if err != nil {
			return err
		}
		out, err = p.item(ctx, run, action, path)
		return err
	})
	if err != nil {
		return model.PushItem{}, fmt.Errorf("read push intent: %w", err)
	}
	return out, nil
}

func (p pushProgress) AdvanceItem(ctx context.Context, expected int64, next model.PushItem) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("push item advance requires a transaction: %w", kernel.ErrInvalid)
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
	if err := model.ValidatePushItemAdvance(run, current, next); err != nil {
		return fmt.Errorf("validate push item advance: %w", err)
	}
	key, err := model.PushItemKey(next.Action, next.Path)
	if err != nil {
		return fmt.Errorf("derive push operation key: %w", err)
	}
	document, err := dialect.ToJSON(next)
	if err != nil {
		return fmt.Errorf("encode push item evidence: %w", err)
	}
	n, err := p.s.Exec(
		ctx,
		"advance push intent",
		"UPDATE replication_push_items SET version=?, phase=?, document=? WHERE run_id=? AND run_incarnation=? AND operation_key=? AND version=?",
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

func (p pushProgress) Items(ctx context.Context, ref model.PushReference, pending bool, page kernel.Page) ([]model.PushItem, string, error) {
	if err := ref.Validate(); err != nil {
		return nil, "", fmt.Errorf("validate push reference: %w", err)
	}
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("push item page: %w", err)
	}
	after, err := page.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("push item continuation: %w", err)
	}
	var out []model.PushItem
	var next string
	err = p.s.Tx(ctx, func(ctx context.Context) error {
		run, err := p.Run(ctx, ref)
		if err != nil {
			return err
		}
		query := "SELECT " + pushItemCols + " FROM replication_push_items WHERE run_id=? AND run_incarnation=?"
		args := []any{ref.ID, ref.Incarnation}
		if pending {
			query += " AND phase<>?"
			args = append(args, model.PushItemComplete)
		}
		if len(after) > 0 {
			query += " AND operation_key>?"
			args = append(args, after[0])
		}
		query += " ORDER BY operation_key LIMIT ?"
		args = append(args, page.Limit+1)
		if err := p.s.Rows(ctx, "page push intents", query, args, func(row *sql.Rows) error {
			i, err := scanPushItem(row, run)
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
			key, err := model.PushItemKey(out[len(out)-1].Action, out[len(out)-1].Path)
			if err != nil {
				return fmt.Errorf("derive push operation key: %w", err)
			}
			next = kernel.Cursor(key)
		}
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("page push intents: %w", err)
	}
	return out, next, nil
}

var _ port.PushProgressStore = pushProgress{}
