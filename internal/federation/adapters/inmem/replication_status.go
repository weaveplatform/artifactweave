package inmem

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (rs replicationStore) StatusTargets(ctx context.Context, repoKey string, page kernel.Page) ([]model.ReplicationTargetState, string, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("replication status page: %w", err)
	}
	key, err := page.Key(1)
	if err != nil {
		return nil, "", fmt.Errorf("replication status cursor: %w", err)
	}
	var states []model.ReplicationTargetState
	err = rs.s.Do(ctx, func() error {
		targets := rs.replications().First(func(_ string, target model.Replication) bool {
			return target.RepoKey == repoKey && (len(key) == 0 || target.Key > key[0])
		}, compareReplications, page.Limit+1)
		for _, target := range targets {
			state := model.ReplicationTargetState{Key: target.Key, URL: target.URL}
			// Only the latest run is returned, so only it is copied.
			runs := rs.runs().Sorted(func(key replicationRunKey, _ model.ReplicationRun) bool { return key.key == target.Key },
				func(a, b model.ReplicationRun) int { return b.Started.Compare(a.Started) })
			if len(runs) > 0 {
				state.Latest = kernel.Clone(runs[0])
			}
			for _, run := range runs {
				if run.Completed.After(state.LastCompleted) {
					state.LastCompleted = run.Completed
				}
			}
			states = append(states, state)
		}
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	states, next := kernel.Trim(states, page.Limit, func(state model.ReplicationTargetState) []string { return []string{state.Key} })
	return states, next, nil
}
