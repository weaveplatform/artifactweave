package webhook

import (
	"context"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
)

// Policy is current administration authority for one operation. Any authorizes
// entering the subscription collection; Allows still checks each object's
// immutable project and its event domain before it can be returned or changed.
type Policy interface {
	Any() bool
	Allows(projectKey, domain string) bool
}

// Administration applies current authorization and configuration validation to
// the secret-safe Service. Authorize must acquire the shared project guard and
// refresh the credential and policy through the supplied transaction context,
// before any subscription parent or secret row is locked. It performs no remote
// I/O. A missing authorizer or a policy with no authority refuses every operation.
type Administration struct {
	Service   Service
	Validator Validator
	Authorize func(context.Context, kernel.ScopeActions) (Policy, error)
}

func (a Administration) run(ctx context.Context, action kernel.ScopeActions, fn func(context.Context, Policy) error) error {
	if a.Authorize == nil {
		return kernel.ErrForbidden
	}
	if a.Service.Store == nil {
		return fmt.Errorf("webhook administration store is unavailable: %w", kernel.ErrStore)
	}
	err := a.Service.Store.Tx(ctx, func(ctx context.Context) error {
		policy, err := a.Authorize(ctx, action)
		if err != nil {
			return fmt.Errorf("current webhook authority: %w", err)
		}
		if policy == nil || !policy.Any() {
			return kernel.ErrForbidden
		}
		return fn(ctx, policy)
	})
	if err != nil {
		return fmt.Errorf("webhook administration transaction: %w", err)
	}
	return nil
}

// Create authorizes the requested project/domain before inspecting criteria
// repositories and publishing the complete subscription, secrets included.
func (a Administration) Create(ctx context.Context, in Input) (View, error) {
	var out View
	err := a.run(ctx, kernel.ScopeWrite, func(ctx context.Context, policy Policy) error {
		project := ""
		if in.ProjectKey != nil {
			project = *in.ProjectKey
		}
		if !policy.Allows(project, string(in.Domain)) {
			return kernel.ErrForbidden
		}
		if err := a.Validator.Validate(ctx, in); err != nil {
			return err
		}
		var err error
		out, err = a.Service.Create(ctx, in)
		return err
	})
	if err != nil {
		return View{}, err
	}
	return out, nil
}

// Update checks both the stored and replacement domain. An omitted project
// keeps the stored immutable owner; changing it is invalid even for an admin.
func (a Administration) Update(ctx context.Context, key string, in Input) error {
	return a.run(ctx, kernel.ScopeWrite, func(ctx context.Context, policy Policy) error {
		old, err := a.Service.Get(ctx, key)
		if err != nil {
			return err
		}
		if !policy.Allows(old.ProjectKey, string(old.Domain)) || !policy.Allows(old.ProjectKey, string(in.Domain)) {
			return kernel.ErrForbidden
		}
		if in.ProjectKey != nil && *in.ProjectKey != old.ProjectKey || in.Key != "" && in.Key != key {
			return fmt.Errorf("subscription key and project are immutable: %w", kernel.ErrInvalid)
		}
		in.Key = key
		if err := a.Validator.Validate(ctx, in); err != nil {
			return err
		}
		_, err = a.Service.Update(ctx, key, in)
		return err
	})
}

// Get returns only an authorized, redacted configuration after commit succeeds.
func (a Administration) Get(ctx context.Context, key string) (View, error) {
	var out View
	err := a.run(ctx, kernel.ScopeRead, func(ctx context.Context, policy Policy) error {
		var err error
		out, err = a.Service.Get(ctx, key)
		if err != nil {
			return err
		}
		if !policy.Allows(out.ProjectKey, string(out.Domain)) {
			return kernel.ErrForbidden
		}
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return out, nil
}

// List authorizes before applying the caller's filter and narrows the result
// to current project/domain authority. Empty authorized results are an array.
func (a Administration) List(ctx context.Context, filter port.WebhookFilter) ([]View, error) {
	out := []View{}
	err := a.run(ctx, kernel.ScopeRead, func(ctx context.Context, policy Policy) error {
		all, err := a.Service.List(ctx, filter)
		if err != nil {
			return err
		}
		for _, view := range all {
			if policy.Allows(view.ProjectKey, string(view.Domain)) {
				out = append(out, view)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes an authorized subscription and its owned delivery rows in the
// same transaction. It does not deliver, test, or troubleshoot a webhook.
func (a Administration) Delete(ctx context.Context, key string) error {
	return a.run(ctx, kernel.ScopeDelete, func(ctx context.Context, policy Policy) error {
		old, err := a.Service.Get(ctx, key)
		if err != nil {
			return err
		}
		if !policy.Allows(old.ProjectKey, string(old.Domain)) {
			return kernel.ErrForbidden
		}
		if err := a.Service.Store.Webhooks().Delete(ctx, key); err != nil {
			return fmt.Errorf("delete webhook subscription: %w", err)
		}
		return nil
	})
}
