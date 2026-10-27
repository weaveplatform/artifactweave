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
func TestUnitAdmit(t *testing.T) {
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
			var refused *format.RejectionError
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

func TestUnitHooksDoNothing(t *testing.T) {
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

// A download is refused 404, existing or not, from a blacked-out repository
// and of a path the patterns reject; a virtual repository is never blacked
// out and has its own wording.
func TestUnitCheckDownload(t *testing.T) {
	const excludedMsg = "The repository 'libs' rejected the artifact 'libs:secret/a.jar' due to " +
		"its include/exclude pattern settings."
	dark := func(c *repo.Config) { c.BlackedOut = ptr(true) }
	picky := func(c *repo.Config) { c.ExcludesPattern = ptr("secret/**") }
	virtual := func(patch func(*repo.Config)) func(*repo.Config) {
		return func(c *repo.Config) {
			c.Class = repo.Virtual
			if patch != nil {
				patch(c)
			}
		}
	}
	for _, tc := range []struct {
		name   string
		patch  func(*repo.Config)
		path   string
		folder bool
		msg    string
	}{
		{name: "defaults allow everything", path: "a/b.jar"},
		{name: "the root of a picky repository", patch: picky, path: "", folder: true},
		{
			name:  "blacked out",
			patch: dark, path: "a.jar",
			msg: "The repository 'libs' is blacked out and cannot serve artifact 'libs:a.jar'.",
		},
		{
			name:  "the root of a blacked-out repository",
			patch: dark, path: "", folder: true,
			msg: "The repository 'libs' is blacked out and cannot serve artifact 'libs:'.",
		},
		{name: "excluded", patch: picky, path: "secret/a.jar", msg: excludedMsg},
		{
			name:  "an excluded folder",
			patch: picky, path: "secret", folder: true,
			msg: "The repository 'libs' rejected the artifact 'libs:secret' due to its " +
				"include/exclude pattern settings.",
		},
		{
			name:  "a folder leading to included files",
			patch: func(c *repo.Config) { c.IncludesPattern = ptr("org/acme/**") },
			path:  "org", folder: true,
		},
		{
			name:  "the same path as a file",
			patch: func(c *repo.Config) { c.IncludesPattern = ptr("org/acme/**") },
			path:  "org",
			msg: "The repository 'libs' rejected the artifact 'libs:org' due to its " +
				"include/exclude pattern settings.",
		},
		{name: "a blacked-out virtual repository serves", patch: virtual(dark), path: "a.jar"},
		{
			name:  "a virtual repository's patterns",
			patch: virtual(picky), path: "secret/a.jar",
			msg: "No repository found to serve the request for libs:secret/a.jar",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckDownload(config(tc.patch), tc.path, tc.folder)
			var refused *format.RejectionError
			switch {
			case tc.msg == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.msg != "" && (!errors.As(err, &refused) ||
				refused.Status != http.StatusNotFound || refused.Message != tc.msg):
				t.Fatalf("refusal %v, want 404 %q", err, tc.msg)
			}
		})
	}
}
