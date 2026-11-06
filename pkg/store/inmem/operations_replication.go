package inmem

import (
	"context"
	"fmt"
	"strings"

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

func (rs replicationStore) replications() *table[string, types.Replication] {
	return tableOf[string, types.Replication](rs.s, "replications")
}

func (rs replicationStore) runs() *table[replicationRunKey, types.ReplicationRun] {
	return tableOf[replicationRunKey, types.ReplicationRun](rs.s, "replication_runs")
}

func (rs replicationStore) Put(ctx context.Context, r types.Replication) error {
	if r.Key == "" {
		return fmt.Errorf("replication of %s: key is required: %w", r.RepoKey, store.ErrInvalid)
	}
	return rs.s.do(ctx, func() error {
		rs.replications().put(r.Key, r)
		return nil
	})
}

func (rs replicationStore) Get(ctx context.Context, key string) (types.Replication, error) {
	var out types.Replication
	err := rs.s.do(ctx, func() error {
		var ok bool
		if out, ok = rs.replications().get(key); !ok {
			return notFound("replication", key)
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
	err := rs.s.do(ctx, func() error {
		out = rs.replications().
			all(func(_ string, r types.Replication) bool { return r.RepoKey == repoKey },
				compareReplications)
		return nil
	})
	return out, err
}

func (rs replicationStore) List(ctx context.Context) ([]types.Replication, error) {
	var out []types.Replication
	err := rs.s.do(ctx, func() error {
		out = rs.replications().all(nil, compareReplications)
		return nil
	})
	return out, err
}

func (rs replicationStore) Delete(ctx context.Context, key string) error {
	return rs.s.do(ctx, func() error {
		if !rs.replications().del(key) {
			return notFound("replication", key)
		}
		rs.runs().
			deleteWhere(func(k replicationRunKey, _ types.ReplicationRun) bool { return k.key == key })
		return nil
	})
}

func (rs replicationStore) DeleteForRepo(ctx context.Context, repoKey string) error {
	return rs.s.do(ctx, func() error {
		for key, r := range rs.replications().rows {
			if r.RepoKey == repoKey {
				rs.replications().del(key)
				rs.runs().
					deleteWhere(func(k replicationRunKey, _ types.ReplicationRun) bool { return k.key == key })
			}
		}
		return nil
	})
}

func (rs replicationStore) PutRun(ctx context.Context, r types.ReplicationRun) error {
	return rs.s.do(ctx, func() error {
		if !rs.replications().has(r.ReplicationKey) {
			return notFound("replication", r.ReplicationKey)
		}
		r = store.Normalise(r)
		rs.runs().put(replicationRunKey{r.ReplicationKey, r.Started.UnixMicro()}, r)
		return nil
	})
}

func (rs replicationStore) LastRun(ctx context.Context, key string) (types.ReplicationRun, error) {
	var out types.ReplicationRun
	err := rs.s.do(ctx, func() error {
		rows := rs.runs().
			all(func(k replicationRunKey, _ types.ReplicationRun) bool { return k.key == key },
				func(a, b types.ReplicationRun) int { return b.Started.Compare(a.Started) })
		if len(rows) == 0 {
			return notFound("replication run of", key)
		}
		out = rows[0]
		return nil
	})
	return out, err
}
