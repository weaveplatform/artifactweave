package federation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// Membership returns the federated repository membership service over the
// module's store (decision 0022). kernel.ErrStore when the store has no peer
// records, membership table or work queue.
func (m *Module) Membership() (federated.Membership, error) {
	return m.deps.membership()
}

func (d Deps) membership() (federated.Membership, error) {
	st, ok := d.Store.(federated.Store)
	if !ok {
		return federated.Membership{}, fmt.Errorf("federated repository membership store unavailable: %w", kernel.ErrStore)
	}
	ms := federated.Membership{Store: st, BaseURL: d.BaseURL}
	if e := d.streams.get(); e != nil {
		ms.Changed = func(ctx context.Context, key string) error {
			return e.Wake(ctx, model.RepositoryStream(key)) //nolint:wrapcheck // the engine names the stream
		}
		ms.Dropped = func(ctx context.Context, key, peer string) error {
			return e.Forget(ctx, peer, model.RepositoryStream(key)) //nolint:wrapcheck // the engine names the stream
		}
	}
	return ms, nil
}

// Operations returns the federation API's configSync, migrate,
// convertToLocal, member URL rewrite, full sync, recovery, member removal,
// prioritization and propagateCreatedTime, for administrators only.
func (m *Module) Operations() (federated.Operations, error) {
	ms, err := m.deps.membership()
	if err != nil {
		return federated.Operations{}, err
	}
	return federated.Operations{
		Membership: ms, Repositories: m.deps.Repositories, Authorize: m.deps.pairingAdministrator,
		Streams: m.Streams,
	}, nil
}

// Receiver returns the peer API's configuration receiver (PUT
// /repositories/{repoKey}).
func (m *Module) Receiver() (federated.Receiver, error) {
	ms, err := m.deps.membership()
	if err != nil {
		return federated.Receiver{}, err
	}
	return federated.Receiver{Membership: ms, Repositories: m.deps.Repositories}, nil
}

// OpenConfigSync gives the module the transport configSync sends members
// their configuration through, the signed peer client; RegisterWork then
// registers the send. Without it no configSync work runs, and queued items
// wait for a runtime that has one.
func (m *Module) OpenConfigSync(remote federated.Remote, log *slog.Logger) {
	m.pushMu.Lock()
	defer m.pushMu.Unlock()
	m.configRemote, m.configLog = remote, log
}

// registerConfigSyncWork registers ConfigSyncKind once OpenConfigSync has
// given the module its transport.
func (m *Module) registerConfigSyncWork(queue *work.Queue) error {
	m.pushMu.Lock()
	remote, log := m.configRemote, m.configLog
	m.pushMu.Unlock()
	if remote == nil {
		return nil
	}
	ms, err := m.deps.membership()
	if err != nil {
		return err
	}
	sender := federated.Sender{Membership: ms, Repositories: m.deps.Repositories, Remote: remote, Log: log}
	if err := queue.Handle(federated.ConfigSyncKind, sender.Send); err != nil {
		return fmt.Errorf("register federated configSync: %w", err)
	}
	resyncer := federated.Resyncer{Membership: ms, Remote: remote, Log: log}
	if err := queue.Handle(federated.FullSyncRequestKind, resyncer.Send); err != nil {
		return fmt.Errorf("register federated full-sync requests: %w", err)
	}
	return nil
}

// AdmitRepository checks a federated repository's members before it is
// stored and rewrites them to their stored form (federated.Membership.Admit).
// A refusal is the 400 the repositories API answers in its envelope.
func (l repositoryLifecycle) AdmitRepository(ctx context.Context, c *repo.Config) error {
	if c.Class != repo.Federated {
		return nil
	}
	ms, err := l.deps.membership()
	if err != nil {
		return err
	}
	return answer(ms.Admit(ctx, c))
}

// CreatedRepository stores a new federated repository's members and queues
// its configuration to each of them.
func (l repositoryLifecycle) CreatedRepository(ctx context.Context, key string) error {
	return l.reconcile(ctx, key)
}

func (l repositoryLifecycle) reconcile(ctx context.Context, key string) error {
	ms, err := l.deps.membership()
	if err != nil || l.deps.Repositories == nil {
		// A store without membership holds no federated members to keep.
		return nil //nolint:nilerr // see above
	}
	c, err := l.deps.Repositories.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read repository %s for its members: %w", key, err)
	}
	return answer(ms.Reconcile(ctx, c, true))
}

// answer turns a membership refusal into the repositories API's 400.
func answer(err error) error {
	var refusal *federated.RefusalError
	if errors.As(err, &refusal) {
		return apierror.New(http.StatusBadRequest, refusal.Message)
	}
	return err
}

// MembershipLifecycle is the membership part of the module's repository
// lifecycle over st alone, for config apply and the bootstrap descriptor,
// which run without the modules: their federated repositories get the
// member checks and the membership table the repositories API gives, with
// configSync queued for the runtime to send. get reads a stored
// repository in the caller's transaction.
func MembershipLifecycle(
	st federated.Store,
	baseURL string,
	get func(context.Context, string) (repo.Config, error),
) artifactsapi.RepositoryLifecycle {
	return membershipLifecycle{ms: federated.Membership{Store: st, BaseURL: baseURL}, get: get}
}

type membershipLifecycle struct {
	ms  federated.Membership
	get func(context.Context, string) (repo.Config, error)
}

func (l membershipLifecycle) ResetRepository(ctx context.Context, key string) error {
	return l.ms.Clear(ctx, key) //nolint:wrapcheck // Clear names what failed
}

func (l membershipLifecycle) RefreshRepository(ctx context.Context, key string) error {
	return l.reconcile(ctx, key)
}

func (l membershipLifecycle) AdmitRepository(ctx context.Context, c *repo.Config) error {
	if c.Class != repo.Federated {
		return nil
	}
	return answer(l.ms.Admit(ctx, c))
}

func (l membershipLifecycle) CreatedRepository(ctx context.Context, key string) error {
	return l.reconcile(ctx, key)
}

func (l membershipLifecycle) reconcile(ctx context.Context, key string) error {
	c, err := l.get(ctx, key)
	if err != nil {
		return fmt.Errorf("read repository %s for its members: %w", key, err)
	}
	return answer(l.ms.Reconcile(ctx, c, true))
}
