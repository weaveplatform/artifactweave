package inmem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/mirror"
	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
)

// Replicas returns the replica index over the blob_replicas table: which
// mirror members hold which blobs (decision 0028).
func (s *Store) Replicas() port.ReplicaIndex { return replicaIndex{s: s, table: "blob_replicas"} }

// replicaIndex is a ReplicaIndex over one table, shared by the hot tier
// (blob_replicas) and the archive tier (cold_blob_replicas).
type replicaIndex struct {
	s     *Store
	table string
}

type replicaKey struct{ sha256, member string }

func (r replicaIndex) t() *memtx.Table[replicaKey, mirror.BlobReplica] {
	return memtx.TableOf[replicaKey, mirror.BlobReplica](r.s.Store, r.table)
}

func (r replicaIndex) Put(ctx context.Context, v mirror.BlobReplica) error {
	return r.s.Do(ctx, func() error {
		if v.SHA256 == "" || v.Member == "" {
			return fmt.Errorf("%s: sha256 and member are required: %w", r.table, kernel.ErrInvalid)
		}
		r.t().Put(replicaKey{v.SHA256, v.Member}, v)
		return nil
	})
}

func (r replicaIndex) Get(ctx context.Context, sha256, member string) (mirror.BlobReplica, error) {
	var out mirror.BlobReplica
	err := r.s.Do(ctx, func() error {
		var ok bool
		if out, ok = r.t().Get(replicaKey{sha256, member}); !ok {
			return memtx.NotFound(r.table, sha256+"@"+member)
		}
		return nil
	})
	return out, err
}

func (r replicaIndex) ForBlob(ctx context.Context, sha256 string) ([]mirror.BlobReplica, error) {
	var out []mirror.BlobReplica
	err := r.s.Do(ctx, func() error {
		out = r.t().All(func(k replicaKey, _ mirror.BlobReplica) bool { return k.sha256 == sha256 },
			func(a, b mirror.BlobReplica) int { return strings.Compare(a.Member, b.Member) })
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
func compareReplicas(a, b mirror.BlobReplica) int {
	return cmp.Or(
		cmp.Compare(replicaMicros(a.Updated), replicaMicros(b.Updated)),
		strings.Compare(a.SHA256, b.SHA256),
		strings.Compare(a.Member, b.Member),
	)
}

func replicaSortKey(v mirror.BlobReplica) []any {
	return []any{replicaMicros(v.Updated), v.SHA256, v.Member}
}

func (r replicaIndex) List(
	ctx context.Context, f mirror.ReplicaFilter, p kernel.Page,
) ([]mirror.BlobReplica, string, error) {
	var (
		out  []mirror.BlobReplica
		next string
	)
	err := r.s.Do(ctx, func() error {
		rows := r.t().All(func(_ replicaKey, v mirror.BlobReplica) bool {
			return (f.Member == "" || v.Member == f.Member) &&
				(len(f.States) == 0 || slices.Contains(f.States, v.State))
		}, compareReplicas)
		var err error
		out, next, err = memtx.Paginate(rows, p, false, replicaSortKey)
		return err //nolint:wrapcheck // Paginate's error wraps kernel.ErrInvalidCursor
	})
	return out, next, err
}

func (r replicaIndex) Delete(ctx context.Context, sha256, member string) error {
	return r.s.Do(ctx, func() error {
		if !r.t().Del(replicaKey{sha256, member}) {
			return memtx.NotFound(r.table, sha256+"@"+member)
		}
		return nil
	})
}

func (r replicaIndex) DeleteBlob(ctx context.Context, sha256 string) (int64, error) {
	var n int64
	err := r.s.Do(ctx, func() error {
		n = r.t().
			DeleteWhere(func(k replicaKey, _ mirror.BlobReplica) bool { return k.sha256 == sha256 })
		return nil
	})
	return n, err
}

func (r replicaIndex) UnderReplicated(ctx context.Context, redundancy int) (int64, error) {
	var n int64
	err := r.s.Do(ctx, func() error {
		seen, present, deleting := map[string]bool{}, map[string]int{}, map[string]bool{}
		for _, v := range r.t().Rows {
			seen[v.SHA256] = true
			switch v.State {
			case mirror.ReplicaPresent:
				present[v.SHA256]++
			case mirror.ReplicaDeleting:
				deleting[v.SHA256] = true
			}
		}
		for sha := range seen {
			if !deleting[sha] && present[sha] < redundancy {
				n++
			}
		}
		return nil
	})
	return n, err
}

func (r replicaIndex) SoleCopies(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	err := r.s.Do(ctx, func() error {
		holders, deleting := map[string][]string{}, map[string]bool{}
		for _, v := range r.t().Rows {
			switch v.State {
			case mirror.ReplicaPresent:
				holders[v.SHA256] = append(holders[v.SHA256], v.Member)
			case mirror.ReplicaDeleting:
				deleting[v.SHA256] = true
			}
		}
		for sha, members := range holders {
			if len(members) == 1 && !deleting[sha] {
				out[members[0]]++
			}
		}
		return nil
	})
	return out, err
}

func (r replicaIndex) Counts(ctx context.Context) ([]mirror.ReplicaCount, error) {
	var out []mirror.ReplicaCount
	err := r.s.Do(ctx, func() error {
		agg := map[[2]string]*mirror.ReplicaCount{}
		for _, v := range r.t().Rows {
			k := [2]string{v.Member, string(v.State)}
			c, ok := agg[k]
			if !ok {
				c = &mirror.ReplicaCount{Member: v.Member, State: v.State}
				agg[k] = c
			}
			c.Count++
			c.Bytes += v.Size
		}
		for _, c := range agg {
			out = append(out, *c)
		}
		slices.SortFunc(out, func(a, b mirror.ReplicaCount) int {
			return cmp.Or(
				strings.Compare(a.Member, b.Member),
				strings.Compare(string(a.State), string(b.State)),
			)
		})
		return nil
	})
	return out, err
}
