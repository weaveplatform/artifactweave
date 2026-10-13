package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Replicas implements store.Store.
func (s *Store) Replicas() store.ReplicaIndex { return replicaIndex{s: s, table: "blob_replicas"} }

// replicaIndex is a ReplicaIndex over one table, shared by the hot tier
// (blob_replicas) and the archive tier (cold_blob_replicas).
type replicaIndex struct {
	s     *Store
	table string
}

type replicaKey struct{ sha256, member string }

func (r replicaIndex) t() *table[replicaKey, types.BlobReplica] {
	return tableOf[replicaKey, types.BlobReplica](r.s, r.table)
}

func (r replicaIndex) Put(ctx context.Context, v types.BlobReplica) error {
	return r.s.do(ctx, func() error {
		if v.SHA256 == "" || v.Member == "" {
			return fmt.Errorf("%s: sha256 and member are required: %w", r.table, store.ErrInvalid)
		}
		r.t().put(replicaKey{v.SHA256, v.Member}, v)
		return nil
	})
}

func (r replicaIndex) Get(ctx context.Context, sha256, member string) (types.BlobReplica, error) {
	var out types.BlobReplica
	err := r.s.do(ctx, func() error {
		var ok bool
		if out, ok = r.t().get(replicaKey{sha256, member}); !ok {
			return notFound(r.table, sha256+"@"+member)
		}
		return nil
	})
	return out, err
}

func (r replicaIndex) ForBlob(ctx context.Context, sha256 string) ([]types.BlobReplica, error) {
	var out []types.BlobReplica
	err := r.s.do(ctx, func() error {
		out = r.t().all(func(k replicaKey, _ types.BlobReplica) bool { return k.sha256 == sha256 },
			func(a, b types.BlobReplica) int { return strings.Compare(a.Member, b.Member) })
		return nil
	})
	return out, err
}

// replicaMicros is the sort value of an update time; an unset time sorts as
// 0, as the SQL column stores it.
func replicaMicros(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

// compareReplicas orders by (Updated, SHA256, Member).
func compareReplicas(a, b types.BlobReplica) int {
	return cmp.Or(
		cmp.Compare(replicaMicros(a.Updated), replicaMicros(b.Updated)),
		strings.Compare(a.SHA256, b.SHA256),
		strings.Compare(a.Member, b.Member),
	)
}

func replicaSortKey(v types.BlobReplica) []any {
	return []any{replicaMicros(v.Updated), v.SHA256, v.Member}
}

func (r replicaIndex) List(
	ctx context.Context, f store.ReplicaFilter, p store.Page,
) ([]types.BlobReplica, string, error) {
	var (
		out  []types.BlobReplica
		next string
	)
	err := r.s.do(ctx, func() error {
		rows := r.t().all(func(_ replicaKey, v types.BlobReplica) bool {
			return (f.Member == "" || v.Member == f.Member) &&
				(len(f.States) == 0 || slices.Contains(f.States, v.State))
		}, compareReplicas)
		var err error
		out, next, err = paginate(rows, p, false, replicaSortKey)
		return err
	})
	return out, next, err
}

func (r replicaIndex) Delete(ctx context.Context, sha256, member string) error {
	return r.s.do(ctx, func() error {
		if !r.t().del(replicaKey{sha256, member}) {
			return notFound(r.table, sha256+"@"+member)
		}
		return nil
	})
}

func (r replicaIndex) DeleteBlob(ctx context.Context, sha256 string) (int64, error) {
	var n int64
	err := r.s.do(ctx, func() error {
		n = r.t().
			deleteWhere(func(k replicaKey, _ types.BlobReplica) bool { return k.sha256 == sha256 })
		return nil
	})
	return n, err
}

func (r replicaIndex) Counts(ctx context.Context) ([]store.ReplicaCount, error) {
	var out []store.ReplicaCount
	err := r.s.do(ctx, func() error {
		agg := map[[2]string]*store.ReplicaCount{}
		for _, v := range r.t().rows {
			k := [2]string{v.Member, string(v.State)}
			c, ok := agg[k]
			if !ok {
				c = &store.ReplicaCount{Member: v.Member, State: v.State}
				agg[k] = c
			}
			c.Count++
			c.Bytes += v.Size
		}
		for _, c := range agg {
			out = append(out, *c)
		}
		slices.SortFunc(out, func(a, b store.ReplicaCount) int {
			return cmp.Or(
				strings.Compare(a.Member, b.Member),
				strings.Compare(string(a.State), string(b.State)),
			)
		})
		return nil
	})
	return out, err
}
