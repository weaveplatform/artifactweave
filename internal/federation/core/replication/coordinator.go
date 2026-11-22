package replication

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/schedule"
)

// ExecutionWorkKind carries one immutable logical execution reference. It is
// distinct from target reservations and the scheduler's occurrence queue rows.
const ExecutionWorkKind = "replication:run"

// CoordinatorStore joins dispatch, configuration and work in one unit of work.
type CoordinatorStore interface {
	OwnershipStore
	port.ConfigurationStore
	port.ExecutionStore
}

// TransferRequest selects a direction driver's immutable logical transfer.
// Start must make no network request and must join the supplied transaction.
type TransferRequest struct {
	ID, TargetKey, TargetRevision, Root string
	Options                             *model.ExecutionOptions
}

// TransferAdmission identifies actual retained direction-specific progress.
// It carries no copied credentials, permission decision or target connection.
type TransferAdmission struct {
	ID, Incarnation string
	Repository      artifactsapi.TransferRepository
}

// TransferStart is the no-network admission seam implemented by push and pull.
type TransferStart func(context.Context, TransferRequest) (TransferAdmission, error)

// Coordinator admits manual and Quartz work using the same immutable execution
// type. Driver progress, execution linkage and queue enqueue commit atomically.
// Workers still own target exclusion, fresh authority and publication fencing.
type Coordinator struct {
	Store          CoordinatorStore
	Configurations Service
	Drivers        map[model.ReplicationDirection]TransferStart
	Restore        func(context.Context, kernel.CredentialReference) (context.Context, error)
	Now            func() time.Time
}

func (c Coordinator) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

// Execute validates a canonical repository/path and records one manual run.
// Repeated user requests are distinct logical runs, even at the same instant.
func (c Coordinator) Execute(ctx context.Context, repository, root string) (model.ExecutionRun, error) {
	return c.admit(ctx, repository, root, rand.Text(), "", "", "", nil, nil)
}

// ExecuteWithOptions records resolved one-shot values in the same transaction
// as direction progress, logical dispatch and queue admission.
func (c Coordinator) ExecuteWithOptions(ctx context.Context, repository, root string, input ExecutionInput) (model.ExecutionRun, error) {
	return c.admit(ctx, repository, root, rand.Text(), "", "", "", nil, &input)
}

func (c Coordinator) admit(ctx context.Context, repository, root, id, occurrence, expectedTarget, expectedRevision string,
	finalFence func(context.Context) error,
	input *ExecutionInput,
) (model.ExecutionRun, error) {
	if c.Store == nil || c.Configurations.Guard == nil {
		return model.ExecutionRun{}, fmt.Errorf("replication dispatch contracts unavailable: %w", kernel.ErrStore)
	}
	if _, err := newPushScope(root, "", ""); err != nil {
		return model.ExecutionRun{}, err
	}
	var out model.ExecutionRun
	err := c.Configurations.run(ctx, func(ctx context.Context, policy Policy) error {
		owner, err := c.Configurations.repository(ctx, repository, policy)
		if err != nil {
			return err
		}
		direction, supported := model.DirectionFor(owner.Class)
		start := c.Drivers[direction]
		if !supported || start == nil {
			return fmt.Errorf("replication direction is unavailable: %w", kernel.ErrInvalid)
		}
		target, err := c.Configurations.target(ctx, repository)
		if err != nil {
			return err
		}
		if expectedTarget != "" && (target.Key != expectedTarget || target.Revision != expectedRevision) {
			return kernel.ErrConflict
		}
		return c.Configurations.WithTarget(ctx, target.Key, target.Revision, func(ctx context.Context, target model.Replication) error {
			var options *model.ExecutionOptions
			if input != nil {
				resolved, err := c.Configurations.resolveExecutionOptions(ctx, owner, target, *input)
				if err != nil {
					return err
				}
				options = &resolved
			}
			caller, err := kernel.RequireAuthenticated(ctx)
			if err != nil {
				return fmt.Errorf("replication execution author: %w", err)
			}
			transfer, err := start(ctx, TransferRequest{ID: id, TargetKey: target.Key, TargetRevision: target.Revision, Root: root, Options: options})
			if err != nil {
				return fmt.Errorf("admit replication transfer: %w", err)
			}
			if transfer.Repository.Key != repository {
				return fmt.Errorf("replication transfer escaped repository: %w", kernel.ErrStore)
			}
			binding := model.ExecutionBinding{
				TargetKey: target.Key, TargetRevision: target.Revision,
				Repository: transfer.Repository, Direction: direction, Root: root, Credential: caller.CredentialReference(),
				Occurrence: occurrence, TransferID: transfer.ID, TransferIncarnation: transfer.Incarnation,
				OneShot: options != nil,
			}
			out, err = c.Store.Executions().Create(ctx, id, binding, c.now())
			if err != nil {
				return fmt.Errorf("admit logical replication execution: %w", err)
			}
			if options != nil {
				if err := c.Store.Executions().PutOptions(ctx, out.Reference, *options); err != nil {
					return fmt.Errorf("admit immutable execution options: %w", err)
				}
			}
			if err := c.enqueue(ctx, out); err != nil {
				return err
			}
			if finalFence != nil {
				return finalFence(ctx) //nolint:wrapcheck // scheduler checks its queue generation last
			}
			return nil
		})
	})
	if err != nil {
		return model.ExecutionRun{}, fmt.Errorf("admit single replication: %w", err)
	}
	return out, nil
}

func (c Coordinator) enqueue(ctx context.Context, run model.ExecutionRun) error {
	if run.Phase == model.ExecutionComplete || run.Phase == model.ExecutionAborted {
		return nil
	}
	key := ownershipHash(run.Reference)
	existing, err := c.Store.WorkQueue().Get(ctx, ExecutionWorkKind, key)
	if err == nil {
		var ref model.ExecutionReference
		if json.Unmarshal(existing.Payload, &ref) != nil || ref != run.Reference {
			return fmt.Errorf("replication execution queue identity disagrees: %w", kernel.ErrStore)
		}
		// An occurrence replay must not dirty/restart an already leased run.
		return nil
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return fmt.Errorf("read replication execution queue: %w", err)
	}
	payload, err := json.Marshal(run.Reference)
	if err != nil {
		return fmt.Errorf("encode replication execution reference: %w", err)
	}
	if err := c.Store.WorkQueue().Enqueue(ctx, ExecutionWorkKind, key, payload, c.now()); err != nil {
		return fmt.Errorf("enqueue replication execution: %w", err)
	}
	return nil
}

// Scheduled restores the original verified schedule author outside a metadata
// transaction, then uses the manual admission path and stable Quartz identity.
func (c Coordinator) Scheduled(ctx context.Context, trigger schedule.Trigger) (model.ExecutionRun, error) {
	if c.Store == nil || c.Restore == nil || c.Configurations.Guard == nil || c.Store.InTx(ctx) ||
		trigger.Reference.Kind != ScheduleKind || trigger.Reference.Key == "" || trigger.Revision == "" || trigger.ID == "" ||
		trigger.Fire.IsZero() || trigger.Fence == nil {
		return model.ExecutionRun{}, fmt.Errorf("invalid replication occurrence dispatch: %w", kernel.ErrInvalid)
	}
	var target model.Replication
	err := c.Store.Tx(ctx, func(ctx context.Context) error {
		if err := c.Configurations.Guard(ctx); err != nil {
			return fmt.Errorf("guard replication occurrence author: %w", err)
		}
		var err error
		target, err = c.Store.Replications().Get(ctx, trigger.Reference.Key)
		if err != nil {
			return fmt.Errorf("read replication occurrence target: %w", err)
		}
		if !target.Enabled || target.Revision != trigger.Revision || target.CronExp == "" || !validScheduleAuthor(target.ScheduleAuthor) {
			return kernel.ErrConflict
		}
		return nil
	})
	if err != nil {
		return model.ExecutionRun{}, fmt.Errorf("read replication occurrence author: %w", err)
	}
	ctx, err = c.Restore(ctx, target.ScheduleAuthor)
	if err != nil {
		return model.ExecutionRun{}, fmt.Errorf("restore replication occurrence author: %w", err)
	}
	if ctx == nil || c.Store.InTx(ctx) {
		return model.ExecutionRun{}, fmt.Errorf("replication author restoration leaked a transaction: %w", kernel.ErrStore)
	}
	return c.admit(ctx, target.RepoKey, "", trigger.ID, trigger.ID, target.Key, trigger.Revision, trigger.Fence, nil)
}
