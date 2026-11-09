package inmem

import (
	"context"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/platform/tx/memtx"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Replications implements store.Store.
func (s *Store) Replications() store.ReplicationStore { return replicationStore{s} }

type replicationStore struct{ s *Store }

type replicationRunKey struct {
	key     string
	started int64
}

func (rs replicationStore) replications() *memtx.Table[string, types.Replication] {
	return memtx.TableOf[string, types.Replication](rs.s.Store, "replications")
}

func (rs replicationStore) runs() *memtx.Table[replicationRunKey, types.ReplicationRun] {
	return memtx.TableOf[replicationRunKey, types.ReplicationRun](rs.s.Store, "replication_runs")
}

func (rs replicationStore) Put(ctx context.Context, r types.Replication) error {
	if r.Key == "" {
		return fmt.Errorf("replication of %s: key is required: %w", r.RepoKey, store.ErrInvalid)
	}
	return rs.s.Do(ctx, func() error {
		rs.replications().Put(r.Key, r)
		return nil
	})
}

func (rs replicationStore) Get(ctx context.Context, key string) (types.Replication, error) {
	var out types.Replication
	err := rs.s.Do(ctx, func() error {
		var ok bool
		if out, ok = rs.replications().Get(key); !ok {
			return memtx.NotFound("replication", key)
		}
		return nil
	})
	return out, err
}

func compareReplications(a, b types.Replication) int {
	if c := strings.Compare(a.RepoKey, b.RepoKey); c != 0 {
		return c
	}
	return strings.Compare(a.Key, b.Key)
}

func (rs replicationStore) ForRepo(
	ctx context.Context,
	repoKey string,
) ([]types.Replication, error) {
	var out []types.Replication
	err := rs.s.Do(ctx, func() error {
		out = rs.replications().
			All(func(_ string, r types.Replication) bool { return r.RepoKey == repoKey },
				compareReplications)
		return nil
	})
	return out, err
}

func (rs replicationStore) List(ctx context.Context) ([]types.Replication, error) {
	var out []types.Replication
	err := rs.s.Do(ctx, func() error {
		out = rs.replications().All(nil, compareReplications)
		return nil
	})
	return out, err
}

func (rs replicationStore) Delete(ctx context.Context, key string) error {
	return rs.s.Do(ctx, func() error {
		if !rs.replications().Del(key) {
			return memtx.NotFound("replication", key)
		}
		rs.runs().
			DeleteWhere(func(k replicationRunKey, _ types.ReplicationRun) bool { return k.key == key })
		return nil
	})
}

func (rs replicationStore) DeleteForRepo(ctx context.Context, repoKey string) error {
	return rs.s.Do(ctx, func() error {
		for key, r := range rs.replications().Rows {
			if r.RepoKey == repoKey {
				rs.replications().Del(key)
				rs.runs().
					DeleteWhere(func(k replicationRunKey, _ types.ReplicationRun) bool { return k.key == key })
			}
		}
		return nil
	})
}

func (rs replicationStore) PutRun(ctx context.Context, r types.ReplicationRun) error {
	return rs.s.Do(ctx, func() error {
		if !rs.replications().Has(r.ReplicationKey) {
			return memtx.NotFound("replication", r.ReplicationKey)
		}
		r = store.Normalise(r)
		rs.runs().Put(replicationRunKey{r.ReplicationKey, r.Started.UnixMicro()}, r)
		return nil
	})
}

func (rs replicationStore) LastRun(ctx context.Context, key string) (types.ReplicationRun, error) {
	var out types.ReplicationRun
	err := rs.s.Do(ctx, func() error {
		rows := rs.runs().
			All(func(k replicationRunKey, _ types.ReplicationRun) bool { return k.key == key },
				func(a, b types.ReplicationRun) int { return b.Started.Compare(a.Started) })
		if len(rows) == 0 {
			return memtx.NotFound("replication run of", key)
		}
		out = rows[0]
		return nil
	})
	return out, err
}
