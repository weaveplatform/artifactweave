package mirror_test

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/mirror"
	platinmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// testDB stands in for the metadata store a mirror runs beside. Upload
// sessions and leases are the platform's in-memory stores. The blob and
// replica indexes are this file's: the real ones are the artifacts module's,
// and platform code, tests included, never imports a module. Every row
// lives in one memtx.Store, as one server's database would hold them.
type testDB struct{ *platinmem.Store }

func newTestDB() *testDB { return &testDB{platinmem.New(memtx.New())} }

// Replicas returns the replica index over the blob_replicas table.
func (d *testDB) Replicas() memReplicas { return memReplicas{d.Store.Store} }

// Blobs returns the blob index over the blobs table.
func (d *testDB) Blobs() memBlobs { return memBlobs{d.Store.Store} }

// replicaIndex is the mirror's replica index with Get, which the tests read
// single records with.
type replicaIndex interface {
	mirror.ReplicaIndex
	Get(ctx context.Context, sha256, member string) (mirror.BlobReplica, error)
}

// memReplicas answers every call as the artifacts module's in-memory replica
// index does, so a mirror sees the same records, order, pages and errors:
// rows keyed by (SHA256, Member) in a memtx table, listed by (Updated,
// SHA256, Member) and paged with memtx.Paginate.
type memReplicas struct{ db *memtx.Store }

const replicaTable = "blob_replicas"

type replicaKey struct{ sha256, member string }

func (r memReplicas) t() *memtx.Table[replicaKey, mirror.BlobReplica] {
	return memtx.TableOf[replicaKey, mirror.BlobReplica](r.db, replicaTable)
}

func (r memReplicas) Put(ctx context.Context, v mirror.BlobReplica) error {
	return r.db.Do(ctx, func() error {
		if v.SHA256 == "" || v.Member == "" {
			return fmt.Errorf(
				"%s: sha256 and member are required: %w",
				replicaTable,
				kernel.ErrInvalid,
			)
		}
		r.t().Put(replicaKey{v.SHA256, v.Member}, v)
		return nil
	})
}

func (r memReplicas) Get(ctx context.Context, sha256, member string) (mirror.BlobReplica, error) {
	var out mirror.BlobReplica
	err := r.db.Do(ctx, func() error {
		var ok bool
		if out, ok = r.t().Get(replicaKey{sha256, member}); !ok {
			return memtx.NotFound(replicaTable, sha256+"@"+member)
		}
		return nil
	})
	return out, err
}

func (r memReplicas) ForBlob(ctx context.Context, sha256 string) ([]mirror.BlobReplica, error) {
	var out []mirror.BlobReplica
	err := r.db.Do(ctx, func() error {
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

func (r memReplicas) List(
	ctx context.Context, f mirror.ReplicaFilter, p kernel.Page,
) ([]mirror.BlobReplica, string, error) {
	var (
		out  []mirror.BlobReplica
		next string
	)
	err := r.db.Do(ctx, func() error {
		rows := r.t().All(func(_ replicaKey, v mirror.BlobReplica) bool {
			return (f.Member == "" || v.Member == f.Member) &&
				(len(f.States) == 0 || slices.Contains(f.States, v.State))
		}, func(a, b mirror.BlobReplica) int {
			return cmp.Or(
				cmp.Compare(replicaMicros(a.Updated), replicaMicros(b.Updated)),
				strings.Compare(a.SHA256, b.SHA256),
				strings.Compare(a.Member, b.Member),
			)
		})
		var err error
		out, next, err = memtx.Paginate(rows, p, false, func(v mirror.BlobReplica) []any {
			return []any{replicaMicros(v.Updated), v.SHA256, v.Member}
		})
		return err
	})
	return out, next, err
}

func (r memReplicas) Delete(ctx context.Context, sha256, member string) error {
	return r.db.Do(ctx, func() error {
		if !r.t().Del(replicaKey{sha256, member}) {
			return memtx.NotFound(replicaTable, sha256+"@"+member)
		}
		return nil
	})
}

func (r memReplicas) DeleteBlob(ctx context.Context, sha256 string) (int64, error) {
	var n int64
	err := r.db.Do(ctx, func() error {
		n = r.t().
			DeleteWhere(func(k replicaKey, _ mirror.BlobReplica) bool { return k.sha256 == sha256 })
		return nil
	})
	return n, err
}

func (r memReplicas) UnderReplicated(ctx context.Context, redundancy int) (int64, error) {
	var n int64
	err := r.db.Do(ctx, func() error {
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

func (r memReplicas) SoleCopies(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	err := r.db.Do(ctx, func() error {
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

func (r memReplicas) Counts(ctx context.Context) ([]mirror.ReplicaCount, error) {
	var out []mirror.ReplicaCount
	err := r.db.Do(ctx, func() error {
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

// memBlobs is the blob index. Get reads a row as the artifacts module's
// in-memory blob index does. Ref only records references to live content:
// the tests reference content that has no row or a live one, never content
// garbage collection is deleting.
type memBlobs struct{ db *memtx.Store }

const blobTable = "blobs"

func (b memBlobs) t() *memtx.Table[string, blob.Blob] {
	return memtx.TableOf[string, blob.Blob](b.db, blobTable)
}

func (b memBlobs) Get(ctx context.Context, sha256 string) (blob.Blob, error) {
	var out blob.Blob
	err := b.db.Do(ctx, func() error {
		var ok bool
		if out, ok = b.t().Get(sha256); !ok {
			return memtx.NotFound(blobTable, sha256)
		}
		return nil
	})
	return out, err
}

func (b memBlobs) Ref(ctx context.Context, row blob.Blob, n int64) (blob.Blob, error) {
	var out blob.Blob
	err := b.db.Do(ctx, func() error {
		cur, ok := b.t().Get(row.Checksums.SHA256)
		if !ok {
			cur = row
			cur.Refs, cur.State, cur.Deleted = 0, blob.BlobLive, time.Time{}
		}
		cur.Refs += n
		b.t().Put(cur.Checksums.SHA256, cur)
		out, _ = b.t().Get(cur.Checksums.SHA256)
		return nil
	})
	return out, err
}
