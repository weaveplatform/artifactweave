package token

import (
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitProjectTokenLifetimeIncludesRefreshReferenceAndRegistry(t *testing.T) {
	for _, scope := range []string{
		"applied-permissions/user",
		"applied-permissions/roles:alpha:Viewer",
		"project:alpha/roles:r",
	} {
		t.Run(scope, func(t *testing.T) {
			s, db, _ := world(t)
			ctx := t.Context()
			if err := db.Projects().Create(ctx, model.Project{Key: "alpha"}); err != nil {
				t.Fatal(err)
			}
			sc, err := kernel.ParseScope(scope)
			if err != nil {
				t.Fatal(err)
			}
			request := Request{Scope: sc, Refreshable: true, IncludeReference: true}
			if scope == "applied-permissions/user" {
				request.ProjectKey = "alpha"
			}
			out := issue(t, s, request)
			if out.Record.ProjectIncarnations["alpha"] == "" {
				t.Fatal("project token has no persisted lifetime")
			}
			refreshed, err := s.Refresh(ctx, out.RefreshToken)
			if err != nil || !maps.Equal(refreshed.Record.ProjectIncarnations, out.Record.ProjectIncarnations) {
				t.Fatalf("refresh changed project lifetime: %+v, %v", refreshed.Record.ProjectIncarnations, err)
			}
			child, err := s.IssueRegistry(ctx, RegistryRequest{
				Username: "alice", ExpiresIn: time.Minute, Scope: sc, Parent: refreshed.Record.ID,
			})
			if err != nil {
				t.Fatal(err)
			}
			verified, err := s.Verify(ctx, child.AccessToken)
			if err != nil || !maps.Equal(verified.ProjectIncarnations, out.Record.ProjectIncarnations) || verified.ProjectKey != request.ProjectKey {
				t.Fatalf("registry lost parent's project boundary: %+v, %v", verified, err)
			}
			if err := db.Projects().Delete(ctx, "alpha"); err != nil {
				t.Fatal(err)
			}
			if err := db.Projects().Create(ctx, model.Project{Key: "alpha"}); err != nil {
				t.Fatal(err)
			}
			for _, raw := range []string{refreshed.AccessToken, refreshed.ReferenceToken, child.AccessToken} {
				if _, err := s.Verify(ctx, raw); !errors.Is(err, authn.ErrInvalidToken) {
					t.Fatalf("recreated project revived credential: %v", err)
				}
			}
			if _, err := s.VerifyExpired(ctx, refreshed.AccessToken); !errors.Is(err, authn.ErrInvalidToken) {
				t.Fatalf("expired verification lost lifetime fence: %v", err)
			}
			if _, err := s.Refresh(ctx, refreshed.RefreshToken); !errors.Is(err, authn.ErrInvalidToken) {
				t.Fatalf("refresh rebound stale scope to recreated project: %v", err)
			}
		})
	}
}

func TestUnitProjectTokenUnknownProjectAndUnboundUpgradeAreDenied(t *testing.T) {
	s, db, _ := world(t)
	ctx := t.Context()
	if _, err := s.Issue(ctx, Request{Username: "alice", ProjectKey: "missing"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("mint for missing project: %v", err)
	}
	if err := db.Projects().Create(ctx, model.Project{Key: "alpha"}); err != nil {
		t.Fatal(err)
	}
	sc, err := kernel.ParseScope("applied-permissions/roles:alpha:Viewer")
	if err != nil {
		t.Fatal(err)
	}
	out := issue(t, s, Request{Scope: sc, IncludeReference: true})
	legacy := out.Record
	legacy.ProjectIncarnations = nil
	if err := db.Tokens().Delete(ctx, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Tokens().Create(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{out.AccessToken, out.ReferenceToken} {
		if _, err := s.Verify(ctx, raw); !errors.Is(err, authn.ErrInvalidToken) {
			t.Fatalf("upgrade assigned a lifetime to old scope: %v", err)
		}
	}
	// Role changes revoke through live policy, rather than invalidating the
	// credential or forcing a new JWT whenever a definition is edited.
	fresh := issue(t, s, Request{Scope: sc})
	if err := db.Projects().PutRole(ctx, model.Role{ProjectKey: "alpha", Name: "Viewer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Verify(ctx, fresh.AccessToken); err != nil {
		t.Fatalf("role edit invalidated an otherwise current credential: %v", err)
	}
}
