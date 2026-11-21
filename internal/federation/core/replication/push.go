package replication

import (
	"context"
	"errors"
	"fmt"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ErrPushStatistics keeps an unsupported configured capability visible. The
// documented flag alone does not establish a destination statistics merge rule.
var ErrPushStatistics = errors.New("replication: statistics synchronization is unavailable")

// PushFence is the owner's final local publication fence. A worker checks its
// lease generation and a fresh expiry clock here, after domain/progress locks.
// Nothing may acquire another domain lock after it. It performs no network I/O.
type PushFence func(context.Context) error

// PushRequest admits one immutable logical run under an exact current target.
// ID is caller-stable; a different binding can never reuse it after recreation.
type PushRequest struct{ ID, TargetKey, TargetRevision, Root string }

// Push executes bounded resumable single-target steps. The module supplies
// Restore, which refreshes the original verified credential under the project
// guard and returns it on the input context, without leaking a transaction.
// The dispatch owner supplies exclusive work ownership and the final lease
// fence. A step never promises exactly-once remote effects across HTTP.
type Push struct {
	Store          port.PushStore
	Configurations Service
	Source         artifactsapi.TransferSource
	Connections    port.PushConnections
	Restore        func(context.Context, kernel.CredentialReference) (context.Context, error)
	Now            func() time.Time
}

func (p Push) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC().Truncate(time.Microsecond)
	}
	return time.Now().UTC().Truncate(time.Microsecond)
}

func (p Push) available() error {
	if p.Store == nil || p.Source == nil || p.Connections == nil || p.Restore == nil {
		return fmt.Errorf("single-push contracts unavailable: %w", kernel.ErrStore)
	}
	return nil
}

// Start records the run and optional owner admission (for example queue enqueue)
// in one transaction. The owner separately authorizes manual/scheduled execution.
// No remote request occurs here, including while resolving transport revisions.
func (p Push) Start(ctx context.Context, request PushRequest, admit func(context.Context, model.PushRun) error) (model.PushRun, error) {
	if err := p.available(); err != nil {
		return model.PushRun{}, err
	}
	caller, err := kernel.RequireAuthenticated(ctx)
	if err != nil {
		return model.PushRun{}, fmt.Errorf("push admission caller: %w", err)
	}
	var out model.PushRun
	err = p.Configurations.WithTarget(ctx, request.TargetKey, request.TargetRevision, func(ctx context.Context, target model.Replication) error {
		if target.SyncStatistics {
			return ErrPushStatistics
		}
		scope, err := newPushScope(request.Root, target.IncludePathPrefix, target.ExcludePathPrefix)
		if err != nil {
			return err
		}
		repository, err := p.Source.Repository(ctx, target.RepoKey)
		if err != nil {
			return fmt.Errorf("push source admission: %w", err)
		}
		connection, err := p.Connections.Connection(ctx, target)
		if err != nil {
			return fmt.Errorf("push connection admission: %w", err)
		}
		if connection == nil || connection.Revision() == "" {
			return fmt.Errorf("push connection has no policy revision: %w", kernel.ErrStore)
		}
		binding := model.PushBinding{
			TargetKey: target.Key, TargetRevision: target.Revision, TransportRevision: connection.Revision(), Source: repository,
			Credential: caller.CredentialReference(), Root: scope.root, Include: target.IncludePathPrefix, Exclude: target.ExcludePathPrefix,
			SyncDeletes: target.SyncDeletes, SyncProperties: target.SyncProperties,
		}
		out, err = p.Store.PushProgress().Create(ctx, request.ID, binding, p.now())
		if err != nil {
			return fmt.Errorf("create logical push: %w", err)
		}
		if admit != nil {
			if err := admit(ctx, out); err != nil {
				return fmt.Errorf("publish push admission: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return model.PushRun{}, fmt.Errorf("start single push: %w", err)
	}
	return out, nil
}

// Step processes one pending item or advances one bounded enumeration position.
// Pending intents always precede new traversal. The returned run is retained
// progress, never a public OK result: failed/skipped outcomes remain reportable.
func (p Push) Step(ctx context.Context, ref model.PushReference, fence PushFence) (model.PushRun, error) {
	if err := p.available(); err != nil {
		return model.PushRun{}, err
	}
	if p.Store.InTx(ctx) || fence == nil {
		return model.PushRun{}, fmt.Errorf("push step needs an external context and ownership fence: %w", kernel.ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return model.PushRun{}, fmt.Errorf("push step cancelled: %w", err)
	}
	run, err := p.Store.PushProgress().Run(ctx, ref)
	if err != nil {
		return model.PushRun{}, fmt.Errorf("read logical push: %w", err)
	}
	if run.Phase.Terminal() {
		return run, nil
	}
	ctx, err = p.Restore(ctx, run.Binding.Credential)
	if err != nil {
		return run, fmt.Errorf("restore push caller: %w", err)
	}
	if ctx == nil || p.Store.InTx(ctx) {
		return run, fmt.Errorf("push caller restoration leaked a transaction: %w", kernel.ErrStore)
	}
	pending, _, err := p.Store.PushProgress().Items(ctx, ref, true, kernel.Page{Limit: 1})
	if err != nil {
		return run, fmt.Errorf("read pending push intent: %w", err)
	}
	switch {
	case len(pending) > 0:
		err = p.item(ctx, run, pending[0], fence)
	case run.Phase == model.PushCopy:
		err = p.copyPage(ctx, run, fence)
	default:
		err = p.reconcilePage(ctx, run, fence)
	}
	if err != nil {
		return run, fmt.Errorf("single push step: %w", err)
	}
	current, err := p.Store.PushProgress().Run(ctx, ref)
	if err != nil {
		return run, fmt.Errorf("read advanced push: %w", err)
	}
	return current, nil
}

// pushGate validates source facts before taking private progress locks.
type pushGate func(context.Context, func(context.Context) error) error

func (p Push) withRun(ctx context.Context, expected model.PushRun, gate pushGate, fence PushFence, effect func(context.Context, model.PushRun, port.PushConnection) error) error {
	return p.Configurations.WithTarget(ctx, expected.Binding.TargetKey, expected.Binding.TargetRevision, func(ctx context.Context, target model.Replication) error {
		repository, err := p.Source.Repository(ctx, target.RepoKey)
		if err != nil {
			return fmt.Errorf("current push repository: %w", err)
		}
		if repository != expected.Binding.Source {
			return fmt.Errorf("push source lifetime changed: %w", kernel.ErrConflict)
		}
		local := func(ctx context.Context) error {
			connection, err := p.Connections.Connection(ctx, target)
			if err != nil {
				return fmt.Errorf("current push connection: %w", err)
			}
			if connection == nil || connection.Revision() != expected.Binding.TransportRevision {
				return fmt.Errorf("push transport policy changed: %w", kernel.ErrConflict)
			}
			current, err := p.Store.PushProgress().Run(ctx, expected.Reference)
			if err != nil {
				return fmt.Errorf("lock current push: %w", err)
			}
			if current.Version != expected.Version || current.Phase.Terminal() {
				return kernel.ErrConflict
			}
			if effect != nil {
				if err := effect(ctx, current, connection); err != nil {
					return err
				}
			}
			if err := fence(ctx); err != nil {
				return fmt.Errorf("push ownership fence: %w", err)
			}
			return nil
		}
		if gate != nil {
			return gate(ctx, local)
		}
		return local(ctx)
	})
}

func (p Push) sourceGate(source artifactsapi.TransferItem) pushGate {
	return func(ctx context.Context, effect func(context.Context) error) error {
		return p.Source.WithCurrent(ctx, source, effect)
	}
}

func (p Push) absenceGate(run model.PushRun, path string) pushGate {
	return func(ctx context.Context, effect func(context.Context) error) error {
		return p.Source.WithAbsent(ctx, run.Binding.Source, path, effect)
	}
}

func (p Push) connection(ctx context.Context, run model.PushRun, gate pushGate, fence PushFence) (port.PushConnection, error) {
	var out port.PushConnection
	err := p.withRun(ctx, run, gate, fence, func(_ context.Context, _ model.PushRun, connection port.PushConnection) error {
		out = connection
		return nil
	})
	return out, err
}

func (p Push) advance(ctx context.Context, run model.PushRun, gate pushGate, fence PushFence, edit func(*model.PushRun)) error {
	return p.withRun(ctx, run, gate, fence, func(ctx context.Context, current model.PushRun, _ port.PushConnection) error {
		next := current
		next.Version++
		next.Updated = p.now()
		edit(&next)
		if err := p.Store.PushProgress().Advance(ctx, current.Version, next); err != nil {
			return fmt.Errorf("advance push traversal: %w", err)
		}
		return nil
	})
}

func (p Push) recordItem(ctx context.Context, run model.PushRun, item model.PushItem, gate pushGate, fence PushFence, edit func(*model.PushItem)) error {
	return p.withRun(ctx, run, gate, fence, func(ctx context.Context, _ model.PushRun, _ port.PushConnection) error {
		next := kernel.Clone(item)
		next.Version++
		next.Updated = p.now()
		edit(&next)
		if err := p.Store.PushProgress().AdvanceItem(ctx, item.Version, next); err != nil {
			return fmt.Errorf("advance push item: %w", err)
		}
		return nil
	})
}

func (p Push) outcome(ctx context.Context, run model.PushRun, item model.PushItem, fence PushFence, outcome model.PushOutcome, reason string) error {
	return p.recordItem(ctx, run, item, nil, fence, func(next *model.PushItem) {
		next.Phase, next.Outcome, next.Reason = model.PushItemComplete, outcome, reason
	})
}

func (p Push) targetError(ctx context.Context, run model.PushRun, item model.PushItem, fence PushFence, err error) error {
	var refusal port.PushTargetFailure
	if errors.As(err, &refusal) {
		return p.outcome(ctx, run, item, fence, model.PushItemFailed, refusal.Reason())
	}
	return err
}
