package format

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

type hooks struct{}

func (hooks) Deployed(context.Context, Env, repo.Config, model.Item) error  { return nil }
func (hooks) Deleting(context.Context, Env, repo.Config, model.Item) error  { return nil }
func (hooks) Deleted(context.Context, Env, repo.Config, []model.Item) error { return nil }

type derived struct{ hooks }

func (derived) Classify(repo.Config, string) Class { return ClassDerived }

func TestUnitClassOf(t *testing.T) {
	if got := ClassOf(hooks{}, repo.Config{}, "a"); got != ClassUnique {
		t.Errorf("a format without a classifier: %v", got)
	}
	if got := ClassOf(derived{}, repo.Config{}, "a"); got != ClassDerived {
		t.Errorf("a classifier: %v", got)
	}
}

func TestUnitRejectf(t *testing.T) {
	var err error = Rejectf(http.StatusConflict, "the repository '%s' refuses %s", "libs", "x")
	var r *RejectionError
	if !errors.As(err, &r) || r.Status != http.StatusConflict ||
		err.Error() != "the repository 'libs' refuses x" {
		t.Fatalf("rejection %#v", err)
	}
}

// TestUnitPolicyRefusalIsNamedForTheCache (#1630): a policy's refusal names
// the repository and repo path it refuses, and Named states it for another
// key, a remote's cache, keeping its status and the path; a refusal Rejectf
// made has no key to change.
func TestUnitPolicyRefusalIsNamedForTheCache(t *testing.T) {
	refusal := PolicyRefusal(http.StatusNotFound, "'%[1]s' refuses '%[1]s:%[2]s'", "up", "a/f.bin")
	if !refusal.Policy || refusal.Status != http.StatusNotFound ||
		refusal.Message != "'up' refuses 'up:a/f.bin'" {
		t.Fatalf("refusal %#v", refusal)
	}
	named := refusal.Named("up-cache")
	if !named.Policy || named.Status != http.StatusNotFound ||
		named.Message != "'up-cache' refuses 'up-cache:a/f.bin'" {
		t.Errorf("named %#v", named)
	}
	if refusal.Message != "'up' refuses 'up:a/f.bin'" {
		t.Errorf("Named changed the refusal it was asked of: %q", refusal.Message)
	}
	other := Rejectf(http.StatusConflict, "the repository '%s' refuses %s", "up", "x")
	if got := other.Named("up-cache"); got != other {
		t.Errorf("Rejectf's refusal named %#v, want it kept", got)
	}
}

// An AcceptedError names its status, so a log of the write says what the
// client was answered.
func TestUnitAcceptedError(t *testing.T) {
	var err error = &AcceptedError{Status: http.StatusOK}
	if err.Error() != "format: accepted with status 200 and not stored" {
		t.Fatalf("%q", err.Error())
	}
}
