package replication

import (
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// StatusStore reads execution evidence and current target identities under one
// metadata snapshot. Neither query opens sealed credentials or private bindings.
type StatusStore interface {
	port.ConfigurationStore
	port.ExecutionStore
}

// TargetStatus contains only current target identity and scoped effect history.
type TargetStatus struct {
	URL           string
	Status        model.ReplicationStatus
	LastCompleted time.Time
}

// StatusView describes one repository/path, never a request spanning repositories.
type StatusView struct {
	Class   repo.Class
	Targets []TargetStatus
}

// StatusReader refreshes read authority before selecting any retained history.
// Authorize returns the current repository class/incarnation under the shared
// project guard. It must cover the entire requested subtree, not merely one
// descendant, since aggregates would otherwise disclose unauthorized effects.
type StatusReader struct {
	Store     StatusStore
	Authorize func(context.Context, string, string) (repo.Class, string, error)
}

// Read selects actual effects under the current target and repository lifetime.
func (s StatusReader) Read(ctx context.Context, repository, root string) (StatusView, error) {
	if s.Store == nil || s.Authorize == nil {
		return StatusView{}, fmt.Errorf("replication status unavailable: %w", kernel.ErrStore)
	}
	if _, err := newPushScope(root, "", ""); err != nil {
		return StatusView{}, err
	}
	var out StatusView
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		class, incarnation, err := s.Authorize(ctx, repository, root)
		if err != nil {
			return err
		}
		if _, ok := model.DirectionFor(class); !ok {
			return fmt.Errorf("unsupported replication repository: %w", kernel.ErrInvalid)
		}
		if incarnation == "" {
			return fmt.Errorf("missing repository lifetime: %w", kernel.ErrStore)
		}
		out.Class = class
		page := kernel.Page{Limit: 100}
		for {
			targets, next, err := s.Store.Replications().StatusTargets(ctx, repository, page)
			if err != nil {
				return fmt.Errorf("current replication targets: %w", err)
			}
			for _, target := range targets {
				if validateURL(target.URL) != nil {
					return fmt.Errorf("invalid stored replication status target: %w", kernel.ErrStore)
				}
				state, err := s.Store.Executions().PathStatus(ctx, target.Key, incarnation, root)
				if err != nil {
					return fmt.Errorf("scoped replication history: %w", err)
				}
				status := state.Latest.Status
				if state.Latest.Started.IsZero() {
					status = model.ReplicationNeverRun
				}
				out.Targets = append(out.Targets, TargetStatus{URL: target.URL, Status: status, LastCompleted: state.LastCompleted})
			}
			if next == "" {
				break
			}
			page.After = next
		}
		if len(out.Targets) > 1 {
			return ErrMultipleTargets
		}
		return nil
	})
	if err != nil {
		return StatusView{}, fmt.Errorf("read replication status: %w", err)
	}
	return out, nil
}
