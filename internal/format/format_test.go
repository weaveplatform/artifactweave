package format

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// hooks has the required hooks alone.
type hooks struct{}

func (hooks) Deployed(context.Context, Env, repo.Config, types.Item) error  { return nil }
func (hooks) Deleting(context.Context, Env, repo.Config, types.Item) error  { return nil }
func (hooks) Deleted(context.Context, Env, repo.Config, []types.Item) error { return nil }

// derived classifies everything as derived.
type derived struct{ hooks }

func (derived) Classify(repo.Config, string) Class { return ClassDerived }

func TestClassOf(t *testing.T) {
	if got := ClassOf(hooks{}, repo.Config{}, "a"); got != ClassUnique {
		t.Errorf("a format without a classifier: %v", got)
	}
	if got := ClassOf(derived{}, repo.Config{}, "a"); got != ClassDerived {
		t.Errorf("a classifier: %v", got)
	}
}

func TestReject(t *testing.T) {
	var err error = Reject(http.StatusConflict, "the repository '%s' refuses %s", "libs", "x")
	var r *Rejection
	if !errors.As(err, &r) || r.Status != http.StatusConflict ||
		err.Error() != "the repository 'libs' refuses x" {
		t.Fatalf("rejection %#v", err)
	}
}
