package sql

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

// CleanupIntents persists immutable cleanup bindings and irreversible outcomes.
func (s *Store) CleanupIntents() port.CleanupIntentStore { return cleanupIntents{s} }

type cleanupIntents struct{ s *Store }

const cleanupIntentDocumentLimit = 4 << 20

func (p cleanupIntents) Read(ctx context.Context, id string) (model.CleanupIntent, error) {
	var revision int64
	var document string
	err := p.s.Q(ctx).QueryRowContext(ctx, p.s.Dialect().Rebind(
		"SELECT revision, CASE WHEN LENGTH(document) <= ? THEN document ELSE '' END FROM federated_cleanup_intents WHERE id = ?"+p.s.LockClause(ctx)),
		cleanupIntentDocumentLimit, id).Scan(&revision, &document)
	if err != nil {
		return model.CleanupIntent{}, p.s.Wrap("read cleanup coordinator outcome", err)
	}
	var out model.CleanupIntent
	if document == "" || json.Unmarshal([]byte(document), &out) != nil || out.ID != id || out.Revision != revision || out.Validate() != nil {
		return model.CleanupIntent{}, fmt.Errorf("stored cleanup coordinator outcome is invalid: %w", kernel.ErrStore)
	}
	return out, nil
}

func (p cleanupIntents) Create(ctx context.Context, value model.CleanupIntent) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("cleanup operation admission requires a transaction: %w", kernel.ErrInvalid)
	}
	value = kernel.Normalise(value)
	if err := value.Validate(); err != nil {
		return fmt.Errorf("validate cleanup operation admission: %w", err)
	}
	if value.State != model.CleanupPreparing || value.Revision != 1 {
		return fmt.Errorf("cleanup operation must begin preparing: %w", kernel.ErrInvalid)
	}
	document, err := json.Marshal(value)
	if err != nil || len(document) > cleanupIntentDocumentLimit {
		return fmt.Errorf("cleanup operation exceeds its storage bound: %w", kernel.ErrInvalid)
	}
	_, err = p.s.Exec(ctx, "create cleanup coordinator operation",
		dialect.InsertIgnore("federated_cleanup_intents", []string{"id", "revision", "document"}, []string{"id"}), value.ID, value.Revision, string(document))
	if err != nil {
		return fmt.Errorf("persist cleanup operation: %w", err)
	}
	current, err := p.Read(ctx, value.ID)
	if err != nil {
		return fmt.Errorf("persist cleanup operation: %w", err)
	}
	if current.RunID != value.RunID || current.FileID != value.FileID || !reflect.DeepEqual(current.Selected, value.Selected) {
		return fmt.Errorf("cleanup operation identity was already bound: %w", kernel.ErrConflict)
	}
	return nil
}

func (p cleanupIntents) Advance(ctx context.Context, expected int64, next model.CleanupIntent) error {
	if !p.s.InTx(ctx) {
		return fmt.Errorf("cleanup operation publication requires a transaction: %w", kernel.ErrInvalid)
	}
	next = kernel.Normalise(next)
	current, err := p.Read(ctx, next.ID)
	if err != nil {
		return fmt.Errorf("persist cleanup operation: %w", err)
	}
	if current.Revision != expected {
		return fmt.Errorf("cleanup operation owner changed: %w", kernel.ErrConflict)
	}
	if err := model.ValidateCleanupIntentAdvance(current, next); err != nil {
		return fmt.Errorf("persist cleanup operation: %w", err)
	}
	document, err := json.Marshal(next)
	if err != nil || len(document) > cleanupIntentDocumentLimit {
		return fmt.Errorf("cleanup operation exceeds its storage bound: %w", kernel.ErrInvalid)
	}
	n, err := p.s.Exec(ctx, "publish cleanup coordinator outcome",
		"UPDATE federated_cleanup_intents SET revision = ?, document = ? WHERE id = ? AND revision = ?",
		next.Revision, string(document), next.ID, expected)
	if err != nil {
		return fmt.Errorf("persist cleanup operation: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("cleanup operation publication changed: %w", kernel.ErrConflict)
	}
	return nil
}
