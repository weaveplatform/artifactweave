package notifications

import (
	"context"
	"errors"
	"fmt"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/webhook"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// Store is the notifications catalogue and its shared metadata transaction.
type Store interface{ port.Store }

var errNoStore = errors.New("notifications: Deps.Store is required")

// Deps binds administration over the same unit of work as access and artifacts.
type Deps struct {
	Store        Store
	Access       accessapi.API
	Repositories artifactsapi.Repositories
	// ServiceURL is the configured platform URL used by qualified system scopes.
	// It never comes from the receiver URL, Host or forwarded request headers.
	ServiceURL string
}

// Module owns subscription configuration. It opens no network connections and
// starts no delivery work: observed delivery belongs to P19a.
type Module struct{ subscriptions webhook.Administration }

// New constructs administration without performing I/O. Missing runtime access
// contracts fail closed when used; a missing store refuses construction.
func New(d Deps) (*Module, error) {
	if d.Store == nil {
		return nil, errNoStore
	}
	return &Module{subscriptions: webhook.Administration{
		Service:   webhook.Service{Store: d.Store},
		Validator: webhook.Validator{Repositories: d.Repositories},
		Authorize: d.currentPolicy,
	}}, nil
}

// Subscriptions supplies this module's administration to its HTTP adapter at
// the composition root. Delivery consumers use their private service contract.
func (m *Module) Subscriptions() webhook.Administration { return m.subscriptions }

type webhookPolicy struct {
	decider    accessapi.Decider
	serviceURL string
	action     kernel.ScopeActions
}

func (p webhookPolicy) Any() bool { return p.decider.HasWebhookAccess(p.serviceURL, p.action) }
func (p webhookPolicy) Allows(projectKey, domain string) bool {
	return p.decider.ManagesWebhook(projectKey, domain, p.serviceURL, p.action)
}

func (d Deps) currentPolicy(ctx context.Context, action kernel.ScopeActions) (webhook.Policy, error) {
	p, err := kernel.RequireAuthenticated(ctx)
	if err != nil {
		return nil, fmt.Errorf("webhook caller: %w", err)
	}
	if d.Access.Projects == nil || d.Access.Authenticator == nil || d.Access.Authorizer == nil {
		return nil, apierror.ErrNotImplemented
	}
	// Administration enters the store transaction before calling this hook.
	// This guard precedes identity and subscription/secret locks and remains
	// held through the owner's mutation and commit.
	if err := d.Access.Projects.Guard(ctx); err != nil {
		return nil, fmt.Errorf("guard webhook administration: %w", err)
	}
	p, err = d.Access.Authenticator.Current(ctx, p)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil, kernel.ErrBadCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("current webhook credential: %w", err)
	}
	decider, err := d.Access.Authorizer.For(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("current webhook permissions: %w", err)
	}
	if decider == nil {
		return nil, kernel.ErrForbidden
	}
	return webhookPolicy{decider: decider, serviceURL: d.ServiceURL, action: action}, nil
}
