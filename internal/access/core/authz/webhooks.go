package authz

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func (d *Decider) managementFlags(ctx context.Context, src Sources) error {
	if !d.hasAppliedPermissions || d.isAdmin || src.Users == nil {
		return nil
	}
	if d.user != "" {
		u, err := src.Users.Get(ctx, d.user)
		if err != nil && !errors.Is(err, kernel.ErrNotFound) {
			return fmt.Errorf("authz: platform roles of %s: %w", d.user, err)
		}
		d.webhookManager = err == nil && u.Roles.ManageWebhook
	}
	for _, name := range d.groups {
		g, err := src.Groups.Get(ctx, name)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("authz: platform roles of group %s: %w", name, err)
		}
		d.webhookManager = d.webhookManager || g.Roles.ManageWebhook
	}
	return nil
}

func webhookActions(want kernel.ScopeActions) bool {
	return want != 0 && (kernel.ScopeRead | kernel.ScopeWrite | kernel.ScopeDelete | kernel.ScopeExecute).Covers(want)
}

func (d *Decider) webhookProject(key string) bool {
	return d.tokenProject == "" || d.tokenProject == key
}

func (d *Decider) managementCeiling() bool {
	return d.scope != nil && slices.ContainsFunc(d.scope.Entries, func(e kernel.ScopeEntry) bool {
		return e.Kind == kernel.ScopeArtifact || e.Kind == kernel.ScopeBuild || e.Kind == kernel.ScopeRepo || e.Kind == kernel.ScopeProject
	})
}

// webhookScopes keeps the explicit capability separate from user/group roles.
// A qualifier names this platform, not a delivery receiver. Unqualified system
// scopes retain the native scope grammar's service-wide capability semantics.
func (d *Decider) webhookScopes(serviceURL string, want kernel.ScopeActions, allows func(string) bool) bool {
	if d.scope == nil {
		return false
	}
	for _, e := range d.scope.Entries {
		if e.Kind != kernel.ScopeSystem || e.Target != "webhooks" || !e.Actions.Covers(want) {
			continue
		}
		if e.Qualifier == "" {
			if allows(d.tokenProject) {
				return true
			}
			continue
		}
		key, platformURL, ok := strings.Cut(e.Qualifier, "/")
		if !ok || key == "" || serviceURL == "" || platformURL != serviceURL || !d.webhookProject(key) {
			continue
		}
		if allows(key) {
			return true
		}
	}
	return false
}

// ManagesWebhook is the documented webhook capability: current platform role,
// current own-project administrator for four domains, or an explicit action
// scope. Artifact permissions and repository management never substitute for it.
func (d *Decider) ManagesWebhook(projectKey, domain, serviceURL string, want kernel.ScopeActions) bool {
	if d.anonymous || !webhookActions(want) || !d.webhookProject(projectKey) {
		return false
	}
	if d.webhookScopes(serviceURL, want, func(key string) bool { return key == "" || key == projectKey }) {
		return true
	}
	if d.managementCeiling() {
		return false
	}
	if d.isAdmin || d.webhookManager {
		return true
	}
	return projectKey != "" && d.projectAdmins[projectKey] && slices.Contains([]string{"artifact", "artifact_property", "docker", "build"}, domain)
}

// HasWebhookAccess authorizes a list before any subscription is loaded.
func (d *Decider) HasWebhookAccess(serviceURL string, want kernel.ScopeActions) bool {
	if d.anonymous || !webhookActions(want) {
		return false
	}
	if d.webhookScopes(serviceURL, want, func(string) bool { return true }) {
		return true
	}
	if d.managementCeiling() {
		return false
	}
	if d.isAdmin || d.webhookManager {
		return true
	}
	for key := range d.projectAdmins {
		if d.webhookProject(key) {
			return true
		}
	}
	return false
}
