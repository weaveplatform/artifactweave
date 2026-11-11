package inmem

import (
	"context"
	"fmt"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

// Replications returns the replication store: replication definitions and
// the last run of each. Nothing here outlives the process, so passwords
// are kept as given, not sealed as the SQL store seals them.
func (s *Store) Replications() port.ReplicationStore { return replicationStore{s} }

type replicationStore struct{ s *Store }

type replicationRunKey struct {
	key     string
	started int64
}

func (rs replicationStore) replications() *memtx.Table[string, model.Replication] {
	return memtx.TableOf[string, model.Replication](rs.s.Store, "replications")
}

func (rs replicationStore) runs() *memtx.Table[replicationRunKey, model.ReplicationRun] {
	return memtx.TableOf[replicationRunKey, model.ReplicationRun](rs.s.Store, "replication_runs")
}

func (rs replicationStore) Put(ctx context.Context, r model.Replication) error {
	if r.Key == "" {
		return fmt.Errorf("replication of %s: key is required: %w", r.RepoKey, kernel.ErrInvalid)
	}
	return rs.s.Do(ctx, func() error {
		rs.replications().Put(r.Key, r)
		return nil
	})
}

func (rs replicationStore) Get(ctx context.Context, key string) (model.Replication, error) {
	var out model.Replication
	err := rs.s.Do(ctx, func() error {
		var ok bool
		if out, ok = rs.replications().Get(key); !ok {
			return memtx.NotFound("replication", key)
		}
		return nil
	})
	return out, err
}

func compareReplications(a, b model.Replication) int {
	if c := strings.Compare(a.RepoKey, b.RepoKey); c != 0 {
		return c
	}
	return strings.Compare(a.Key, b.Key)
}

func (rs replicationStore) ForRepo(
	ctx context.Context,
	repoKey string,
) ([]model.Replication, error) {
	var out []model.Replication
	err := rs.s.Do(ctx, func() error {
		out = rs.replications().
			All(func(_ string, r model.Replication) bool { return r.RepoKey == repoKey },
				compareReplications)
		return nil
	})
	return out, err
}

func (rs replicationStore) List(ctx context.Context) ([]model.Replication, error) {
	var out []model.Replication
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
			DeleteWhere(func(k replicationRunKey, _ model.ReplicationRun) bool { return k.key == key })
		return nil
	})
}

func (rs replicationStore) DeleteForRepo(ctx context.Context, repoKey string) error {
	return rs.s.Do(ctx, func() error {
		for key, r := range rs.replications().Rows {
			if r.RepoKey == repoKey {
				rs.replications().Del(key)
				rs.runs().
					DeleteWhere(func(k replicationRunKey, _ model.ReplicationRun) bool { return k.key == key })
			}
		}
		return nil
	})
}

func (rs replicationStore) PutRun(ctx context.Context, r model.ReplicationRun) error {
	return rs.s.Do(ctx, func() error {
		if !rs.replications().Has(r.ReplicationKey) {
			return memtx.NotFound("replication", r.ReplicationKey)
		}
		r = kernel.Normalise(r)
		rs.runs().Put(replicationRunKey{r.ReplicationKey, r.Started.UnixMicro()}, r)
		return nil
	})
}

func (rs replicationStore) LastRun(ctx context.Context, key string) (model.ReplicationRun, error) {
	var out model.ReplicationRun
	err := rs.s.Do(ctx, func() error {
		rows := rs.runs().
			All(func(k replicationRunKey, _ model.ReplicationRun) bool { return k.key == key },
				func(a, b model.ReplicationRun) int { return b.Started.Compare(a.Started) })
		if len(rows) == 0 {
			return memtx.NotFound("replication run of", key)
		}
		out = rows[0]
		return nil
	})
	return out, err
}
