package sql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
)

func (rs replicationStore) StatusTargets(ctx context.Context, repoKey string, page kernel.Page) ([]model.ReplicationTargetState, string, error) {
	page, err := page.Normalize()
	if err != nil {
		return nil, "", fmt.Errorf("replication status page: %w", err)
	}
	condition, args, err := dialect.Keyset(page, []string{"target.replication_key"}, nil, false)
	if err != nil {
		return nil, "", fmt.Errorf("replication status cursor: %w", err)
	}
	// The current target is the lifetime anchor. No replicationCols/scan call
	// can enter this path: those helpers unseal private target credentials.
	query := `SELECT target.replication_key, target.url,
		recent.started_at, recent.completed_at, recent.status,
		(SELECT MAX(completed_at) FROM replication_runs completed WHERE completed.replication_key = target.replication_key)
		FROM replications target LEFT JOIN replication_runs recent
		ON recent.replication_key = target.replication_key AND recent.started_at =
		(SELECT MAX(started_at) FROM replication_runs latest WHERE latest.replication_key = target.replication_key)
		WHERE target.repo_key = ? AND ` + condition + ` ORDER BY target.replication_key LIMIT ?`
	var states []model.ReplicationTargetState
	err = rs.s.Rows(ctx, "replication status targets", query, append(append([]any{repoKey}, args...), page.Limit+1), func(rows *sql.Rows) error {
		var state model.ReplicationTargetState
		var started, completed, lastCompleted sql.NullInt64
		var status sql.NullString
		if err := rows.Scan(&state.Key, &state.URL, &started, &completed, &status, &lastCompleted); err != nil {
			return fmt.Errorf("scan replication status target: %w", err)
		}
		if started.Valid {
			state.Latest = model.ReplicationRun{
				ReplicationKey: state.Key, Started: dialect.FromMicros(started),
				Completed: dialect.FromMicros(completed), Status: model.ReplicationStatus(status.String),
			}
		}
		state.LastCompleted = dialect.FromMicros(lastCompleted)
		states = append(states, state)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	states, next := kernel.Trim(states, page.Limit, func(state model.ReplicationTargetState) []string { return []string{state.Key} })
	return states, next, nil
}
