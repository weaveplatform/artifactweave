package sql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/internal/retention/core/port"
)

func (c cleanupStore) transaction(ctx context.Context) error {
	if !c.s.InTx(ctx) {
		return fmt.Errorf("cleanup progress requires a transaction: %w", kernel.ErrInvalid)
	}
	return nil
}

func (c cleanupStore) CreateProgress(ctx context.Context, p model.CleanupProgress) error {
	if err := c.transaction(ctx); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return fmt.Errorf("validate cleanup continuation: %w", err)
	}
	snapshot, err := dialect.ToJSON(kernel.Normalise(p))
	if err != nil {
		return err
	}
	_, err = c.s.Exec(ctx, "create cleanup progress", "INSERT INTO cleanup_progress (run_id, revision, snapshot) VALUES (?, ?, ?)", p.RunID, p.Revision, snapshot)
	return err
}

func scanCleanupProgress(row interface{ Scan(...any) error }) (model.CleanupProgress, error) {
	var p model.CleanupProgress
	var snapshot sql.NullString
	if err := row.Scan(&p.RunID, &p.Revision, &snapshot); err != nil {
		return p, fmt.Errorf("scan cleanup continuation: %w", err)
	}
	if err := dialect.FromJSON(snapshot, &p); err != nil {
		return p, err
	}
	return p, nil
}

func (c cleanupStore) Progress(ctx context.Context, id int64) (model.CleanupProgress, error) {
	p, err := scanCleanupProgress(
		c.s.Q(ctx).QueryRowContext(ctx, c.s.Dialect().Rebind("SELECT run_id, revision, snapshot FROM cleanup_progress WHERE run_id = ?"+c.s.LockClause(ctx)), id),
	)
	return p, c.s.Wrap("read cleanup progress", err)
}

func (c cleanupStore) AdvanceProgress(ctx context.Context, expected int64, p model.CleanupProgress) error {
	if err := c.transaction(ctx); err != nil {
		return err
	}
	old, err := c.Progress(ctx, p.RunID)
	if errors.Is(err, kernel.ErrNotFound) || err == nil && old.Revision != expected {
		return fmt.Errorf("stale cleanup progress: %w", kernel.ErrConflict)
	}
	if err != nil {
		return err
	}
	if err := model.ValidateCleanupProgressAdvance(old, p); err != nil {
		return fmt.Errorf("advance cleanup continuation: %w", err)
	}
	snapshot, err := dialect.ToJSON(kernel.Normalise(p))
	if err != nil {
		return err
	}
	err = c.s.ExecOne(
		ctx,
		"advance cleanup progress",
		"UPDATE cleanup_progress SET revision = ?, snapshot = ? WHERE run_id = ? AND revision = ?",
		p.Revision,
		snapshot,
		p.RunID,
		expected,
	)
	if errors.Is(err, kernel.ErrNotFound) {
		return fmt.Errorf("stale cleanup progress: %w", kernel.ErrConflict)
	}
	return err
}

const cleanupEntryCols = "run_id, kind, entity_id, version_id, revision, snapshot"

func scanCleanupEntry(row interface{ Scan(...any) error }) (model.CleanupEntry, error) {
	var e model.CleanupEntry
	var snapshot sql.NullString
	if err := row.Scan(&e.RunID, &e.Kind, &e.EntityID, &e.VersionID, &e.Revision, &snapshot); err != nil {
		return e, fmt.Errorf("scan cleanup outcome: %w", err)
	}
	if err := dialect.FromJSON(snapshot, &e); err != nil {
		return e, err
	}
	return e, nil
}

func (c cleanupStore) Entry(ctx context.Context, run int64, kind model.CleanupEntryKind, id int64) (model.CleanupEntry, error) {
	e, err := scanCleanupEntry(
		c.s.Q(ctx).
			QueryRowContext(ctx, c.s.Dialect().Rebind("SELECT "+cleanupEntryCols+" FROM cleanup_entries WHERE run_id = ? AND kind = ? AND entity_id = ?"+c.s.LockClause(ctx)), run, kind, id),
	)
	return e, c.s.Wrap("read cleanup entry", err)
}

func (c cleanupStore) CreateEntry(ctx context.Context, e model.CleanupEntry) (bool, error) {
	if err := c.transaction(ctx); err != nil {
		return false, err
	}
	e = kernel.Normalise(e)
	if err := e.Validate(); err != nil {
		return false, fmt.Errorf("validate cleanup intent: %w", err)
	}
	progress, err := c.Progress(ctx, e.RunID)
	if err != nil {
		return false, err
	}
	if progress.Phase == model.CleanupFinished {
		return false, fmt.Errorf("finished cleanup progress: %w", kernel.ErrConflict)
	}
	old, err := c.Entry(ctx, e.RunID, e.Kind, e.EntityID)
	if err == nil {
		if !model.SameCleanupIntent(old, e) {
			return false, fmt.Errorf("changed cleanup intent: %w", kernel.ErrConflict)
		}
		return false, nil
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return false, err
	}
	snapshot, err := dialect.ToJSON(e)
	if err != nil {
		return false, err
	}
	lead, rank, tie := false, int64(0), ""
	if e.Snapshot.Lead != nil && e.Snapshot.Lead.Found {
		lead, rank, tie = true, e.Snapshot.Lead.Rank, e.Snapshot.Lead.TieBreak
	}
	_, err = c.s.Exec(
		ctx,
		"create cleanup entry",
		"INSERT INTO cleanup_entries (run_id, kind, entity_id, version_id, revision, state, is_lead, lead_rank, lead_tie, snapshot) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		e.RunID,
		e.Kind,
		e.EntityID,
		e.VersionID,
		e.Revision,
		e.State,
		lead,
		rank,
		tie,
		snapshot,
	)
	return err == nil, err
}

func (c cleanupStore) AdvanceEntry(ctx context.Context, expected int64, e model.CleanupEntry) error {
	if err := c.transaction(ctx); err != nil {
		return err
	}
	progress, err := c.Progress(ctx, e.RunID)
	if err != nil {
		return err
	}
	if progress.Phase == model.CleanupFinished {
		return fmt.Errorf("finished cleanup progress: %w", kernel.ErrConflict)
	}
	old, err := c.Entry(ctx, e.RunID, e.Kind, e.EntityID)
	if errors.Is(err, kernel.ErrNotFound) || err == nil && old.Revision != expected {
		return fmt.Errorf("stale cleanup entry: %w", kernel.ErrConflict)
	}
	if err != nil {
		return err
	}
	e = kernel.Normalise(e)
	if err := model.ValidateCleanupEntryAdvance(old, e); err != nil {
		return fmt.Errorf("advance cleanup outcome: %w", err)
	}
	snapshot, err := dialect.ToJSON(e)
	if err != nil {
		return err
	}
	err = c.s.ExecOne(
		ctx,
		"advance cleanup entry",
		"UPDATE cleanup_entries SET revision = ?, state = ?, snapshot = ? WHERE run_id = ? AND kind = ? AND entity_id = ? AND revision = ?",
		e.Revision,
		e.State,
		snapshot,
		e.RunID,
		e.Kind,
		e.EntityID,
		expected,
	)
	if errors.Is(err, kernel.ErrNotFound) {
		return fmt.Errorf("stale cleanup entry: %w", kernel.ErrConflict)
	}
	return err
}

func (c cleanupStore) Entries(ctx context.Context, q port.CleanupEntryQuery, page kernel.Page) ([]model.CleanupEntry, string, error) {
	if q.RunID <= 0 {
		return nil, "", fmt.Errorf("cleanup entry run is required: %w", kernel.ErrInvalid)
	}
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("normalize cleanup outcome page: %w", err)
	}
	condition, args, err := dialect.Keyset(page, []string{"kind", "entity_id"}, map[string]bool{"entity_id": true}, false)
	if err != nil {
		return nil, "", err
	}
	where := []string{condition, "run_id = ?"}
	args = append(args, q.RunID)
	if q.VersionID != 0 {
		where = append(where, "version_id = ?")
		args = append(args, q.VersionID)
	}
	if q.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, q.Kind)
	}
	if q.State != "" {
		where = append(where, "state = ?")
		args = append(args, q.State)
	}
	var out []model.CleanupEntry
	err = c.s.Rows(
		ctx,
		"list cleanup entries",
		"SELECT "+cleanupEntryCols+" FROM cleanup_entries WHERE "+strings.Join(where, " AND ")+" ORDER BY kind, entity_id LIMIT ?",
		append(args, page.Limit+1),
		func(row *sql.Rows) error {
			e, err := scanCleanupEntry(row)
			out = append(out, e)
			return err
		},
	)
	if err != nil {
		return nil, "", err
	}
	items, next := kernel.Trim(out, page.Limit, func(e model.CleanupEntry) []string { return []string{string(e.Kind), dialect.Itoa(e.EntityID)} })
	return items, next, nil
}

func (c cleanupStore) PendingEntry(ctx context.Context, run, version int64) (model.CleanupEntry, error) {
	collation := " COLLATE BINARY"
	if c.s.Dialect().Dollar {
		collation = ` COLLATE "C"`
	}
	e, err := scanCleanupEntry(
		c.s.Q(ctx).
			QueryRowContext(ctx, c.s.Dialect().Rebind("SELECT "+cleanupEntryCols+" FROM cleanup_entries WHERE run_id = ? AND version_id = ? AND kind = ? AND state = ? ORDER BY is_lead, lead_rank, lead_tie"+collation+", entity_id LIMIT 1"), run, version, model.CleanupItemEntry, model.CleanupPending),
	)
	return e, c.s.Wrap("read pending cleanup entry", err)
}
