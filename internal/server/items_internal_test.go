package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccServeFilePreservesProtocolDisposition(t *testing.T) {
	w := newItemWorld(t, false)
	deployed := w.do("PUT", "/artifactory/libs-local/org/app/app-1.0.jar", "admin", "jar bytes")
	wantStatus(t, deployed, http.StatusCreated)
	it := w.item("libs-local", false, "org/app/app-1.0.jar")
	s := artifactoryService{cfg: Config{Store: w.db, Blobs: w.blobs}}
	f := found{repo: repo.Config{Key: "libs-local", Class: repo.Local}, item: it}
	r := httptest.NewRequest(http.MethodGet, "/native/download", nil)
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Disposition", `attachment; filename="plugin-com-weave-fixture.jar"`)
	s.serveFile(rec, r, w.db, f, kernel.Principal{Name: "reader"})
	wantStatus(t, rec, http.StatusOK)
	got := rec.Header().Get("Content-Disposition")
	if got != `attachment; filename="plugin-com-weave-fixture.jar"` {
		t.Fatalf("native disposition overwritten: %q", got)
	}
	if got := rec.Body.String(); got != "jar bytes" {
		t.Fatalf("body %q", got)
	}
	ordinary := httptest.NewRecorder()
	s.serveFile(ordinary, r, w.db, f, kernel.Principal{Name: "reader"})
	wantStatus(t, ordinary, http.StatusOK)
	if got := ordinary.Header().Get("Content-Disposition"); got != blob.Disposition(it.Name) {
		t.Fatalf("default disposition %q, want %q", got, blob.Disposition(it.Name))
	}
}

// TestAccReadsContent covers what the HTTP tests cannot reach: a file in a
// repository that no longer exists counts for no one, a readable file past
// the first page is still found, and store failures are errors, which
// answer 500 rather than a 404 that would send the client to upload.
func TestAccReadsContent(t *testing.T) {
	ctx := context.Background()
	db := inmem.New()
	cheapSeed(t, db)
	libs := *repo.Defaults(&repo.Config{Key: "libs-local", Class: repo.Local})
	if err := db.Repos().Create(ctx, libs); err != nil {
		t.Fatal(err)
	}
	sum := sha256Hex("content")
	put := func(key, name string) {
		t.Helper()
		if _, err := db.Items().Put(ctx, types.Item{
			RepoKey: key, Name: name, Actual: types.Checksums{SHA256: sum},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for n := range store.MaxLimit {
		put("gone", fmt.Sprintf("f%d.bin", n))
	}
	acc, err := accessFor(
		ctx,
		authorizerOver(db),
		kernel.Principal{Name: "admin", Admin: true},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := readsContent(ctx, db, acc, sum); ok || err != nil {
		t.Fatalf("files of a deleted repository: %v %v", ok, err)
	}
	put("libs-local", "late.bin")
	if ok, err := readsContent(ctx, db, acc, sum); !ok || err != nil {
		t.Fatalf("a readable file on the second page: %v %v", ok, err)
	}
	for name, st := range map[string]store.Store{
		"find": buildSearchFaults{Store: db, fail: "find"},
		"repo": faultyStore{Store: db, fail: map[string]bool{"get": true}},
	} {
		if _, err := readsContent(ctx, st, acc, sum); !errors.Is(err, errBoom) {
			t.Errorf("%s failing: %v", name, err)
		}
	}
}

// Matrix values are form-decoded as the JFrog CLI encodes them (QueryEscape)
// and Artifactory decodes them (URLDecoder): "+" is a space and "%2B" a
// plus. Keys keep "+", whose trailing use marks a mandatory property.
func TestAccMatrixParameterDecoding(t *testing.T) {
	for name, c := range map[string]struct {
		escaped   string
		expected  types.Properties
		mandatory []string
	}{
		"plus is a space":       {"libs/a.txt;build.name=my+build", types.Properties{"build.name": {"my build"}}, nil},
		"encoded plus is plus":  {"libs/a.txt;v=a%2Bb", types.Properties{"v": {"a+b"}}, nil},
		"percent-encoded space": {"libs/a.txt;v=a%20b", types.Properties{"v": {"a b"}}, nil},
		"mandatory key":         {"libs/a.txt;k+=v", types.Properties{"k": {"v"}}, []string{"k"}},
		"mandatory, plus value": {"libs/a.txt;k+=x+y", types.Properties{"k": {"x y"}}, []string{"k"}},
		"several values":        {"libs/a.txt;k=a,b", types.Properties{"k": {"a", "b"}}, nil},
		"encoded separator":     {"libs/a.txt;k=a%2Cb", types.Properties{"k": {"a", "b"}}, nil},
		"escaped comma":         {"libs/a.txt;k=a%5C%2Cb%2Cc", types.Properties{"k": {"a,b", "c"}}, nil},
		"as jf sends a build": {
			"libs/a.txt;build.name=my+build;build.number=1;build.timestamp=1760000000000",
			types.Properties{
				"build.name": {"my build"}, "build.number": {"1"}, "build.timestamp": {"1760000000000"},
			},
			nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			req, err := parseEscapedItemPath(c.escaped)
			if err != nil {
				t.Fatal(err)
			}
			if len(req.props) != len(c.expected) {
				t.Fatalf("props = %v, want %v", req.props, c.expected)
			}
			for k, v := range c.expected {
				if !slices.Equal(req.props[k], v) {
					t.Errorf("%s = %q, want %q", k, req.props[k], v)
				}
			}
			for _, k := range c.mandatory {
				if !req.mandatory[k] {
					t.Errorf("%s is not mandatory", k)
				}
			}
			if req.path != "a.txt" {
				t.Errorf("path = %q", req.path)
			}
		})
	}
	if _, err := parseEscapedItemPath("libs/a.txt;k=%zz"); err == nil {
		t.Error("a malformed escape was accepted")
	}
}
