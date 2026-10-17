package generic

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func config(patch func(*repo.Config)) repo.Config {
	c := repo.Config{Key: "libs", Class: repo.Local}
	if patch != nil {
		patch(&c)
	}
	return *repo.Defaults(&c)
}

func ptr[T any](v T) *T { return &v }

// Admit applies blackedOut, includesPattern and excludesPattern with
// Artifactory's statuses and wording.
func TestAdmit(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		patch  func(*repo.Config)
		path   string
		status int
		msg    string
	}{
		{name: "defaults admit everything", path: "a/b/c.jar"},
		{name: "a root file", path: "c.jar"},
		{
			name:   "blacked out",
			patch:  func(c *repo.Config) { c.BlackedOut = ptr(true) },
			path:   "a.jar",
			status: http.StatusNotFound,
			msg:    "The repository 'libs' is blacked out and cannot serve artifact 'libs:a.jar'.",
		},
		{
			name:   "excluded",
			patch:  func(c *repo.Config) { c.ExcludesPattern = ptr("**/*.tmp, secret/**") },
			path:   "secret/a.jar",
			status: http.StatusConflict,
			msg: "The repository 'libs' rejected the artifact 'libs:secret/a.jar' due to its " +
				"include/exclude pattern settings.",
		},
		{
			name:  "not excluded",
			patch: func(c *repo.Config) { c.ExcludesPattern = ptr("**/*.tmp") },
			path:  "a/b.jar",
		},
		{
			name:   "not included",
			patch:  func(c *repo.Config) { c.IncludesPattern = ptr("org/**") },
			path:   "com/a.jar",
			status: http.StatusConflict,
			msg: "The repository 'libs' rejected the artifact 'libs:com/a.jar' due to its " +
				"include/exclude pattern settings.",
		},
		{
			name:  "an empty include is everything",
			patch: func(c *repo.Config) { c.IncludesPattern = ptr("") },
			path:  "com/a.jar",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := format.Write{
				Op:         format.OpDeploy,
				Path:       tc.path,
				Properties: types.Properties{"k": {"v"}},
			}
			got, err := Handler{}.Admit(ctx, format.Env{}, config(tc.patch), w)
			var refused *format.Rejection
			switch {
			case tc.status == 0 && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.status == 0 && (got.Path != w.Path || !got.Properties.Has("k", "v")):
				t.Fatalf("admitted %+v", got)
			case tc.status != 0 && (!errors.As(err, &refused) || refused.Status != tc.status ||
				refused.Message != tc.msg):
				t.Fatalf("refusal %v, want %d %q", err, tc.status, tc.msg)
			}
		})
	}
}

func TestHooksDoNothing(t *testing.T) {
	ctx := context.Background()
	h := Handler{}
	if h.Deployed(ctx, format.Env{}, repo.Config{}, types.Item{}) != nil ||
		h.Deleting(ctx, format.Env{}, repo.Config{}, types.Item{}) != nil ||
		h.Deleted(ctx, format.Env{}, repo.Config{}, []types.Item{{Name: "c"}}) != nil {
		t.Fatal("a generic hook failed")
	}
	if got := format.ClassOf(h, repo.Config{}, "a/b"); got != format.ClassUnique {
		t.Errorf("class %v", got)
	}
}
