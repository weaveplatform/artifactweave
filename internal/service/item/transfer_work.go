package item

import (
	"cmp"
	"context"
	"slices"
)

type (
	transferWorkKey struct{}
	queuedWorkKey   struct{ kind, key string }
	transferWork    map[queuedWorkKey][]byte
)

// withTransferWork preserves transactional enqueue semantics while locking
// work rows in one order. A move touches destination then source: opposite
// cross-repository moves otherwise retain those rows in opposite order and
// PostgreSQL detects a deadlock. Nested transfers join the outer buffer.
func (s *Service) withTransferWork(ctx context.Context, fn func(context.Context) error) error {
	if _, ok := ctx.Value(transferWorkKey{}).(transferWork); ok {
		return fn(ctx)
	}
	pending := transferWork{}
	if err := fn(context.WithValue(ctx, transferWorkKey{}, pending)); err != nil {
		return err
	}
	keys := make([]queuedWorkKey, 0, len(pending))
	for k := range pending {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b queuedWorkKey) int {
		return cmp.Or(cmp.Compare(a.kind, b.kind), cmp.Compare(a.key, b.key))
	})
	for _, k := range keys {
		if err := s.enqueue(ctx, k.kind, k.key, pending[k]); err != nil {
			return err
		}
	}
	return nil
}
