package dockerhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

type countedSortedNames struct {
	values []string
	reads  int
}

func (s *countedSortedNames) len() int        { return len(s.values) }
func (s *countedSortedNames) at(i int) string { s.reads++; return s.values[i] }
func TestUnitProjectedCatalogSeeksOnlyItsWindow(t *testing.T) {
	values := []string{}
	for _, project := range []string{"aaa", "project", "zzz"} {
		for i := range 10000 {
			values = append(values, fmt.Sprintf("%s/image-%05d", project, i))
		}
	}
	list := &countedSortedNames{values: values}
	start, end := listingWindow(list, "project", "virtual/", "virtual/image-09990")
	if start != 19991 || end != 20000 || list.reads > 48 {
		t.Fatal(start, end, list.reads)
	}
	start, end = listingWindow(list, "missing", "", "")
	if start != end {
		t.Fatal(start, end)
	}
}

func TestAccVirtualListsDedupLongPagesAndReadBounds(t *testing.T) {
	w := newBlobWorld(t)
	c, err := w.db.Repos().Get(t.Context(), "docker-virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.Repositories = []string{"docker-local", "oci-local"}
	if err = w.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	other, err := w.db.Repos().Get(t.Context(), "oci-local")
	if err != nil {
		t.Fatal(err)
	}
	other.PackageType = repo.Docker
	if err = w.db.Repos().Update(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	var folders []string
	for i := range listBuffer + 8 {
		folders = append(folders, fmt.Sprintf("big/v%04d", i))
	}
	w.putManifests("docker-local", folders...)
	w.putManifests("oci-local", folders...)
	for _, tc := range []struct {
		path  string
		count int
		bound int
	}{{"?n=2&last=v0900", 2, 6}, {"?n=0", 0, 1}, {"?n=2&last=zz", 0, 3}, {"?n=1003", 1003, 0}} {
		rows := 0
		g := New(Options{Store: itemsFault{Store: w.db, items: countPages{ItemStore: w.db.Items(), rows: &rows}}, Formats: ecosystem.For})
		rec := send(router(g), "GET", "/v2/docker-virtual/big/tags/list"+tc.path, "", nil)
		got := list(t, rec)
		if len(got.Tags) != tc.count {
			t.Fatal(tc.path, len(got.Tags), rec.Body.String())
		}
		if tc.bound > 0 && rows > tc.bound {
			t.Fatal(tc.path, rows, tc.bound)
		}
		if tc.count > 0 && len(slices.Compact(slices.Clone(got.Tags))) != tc.count {
			t.Fatal("duplicate tags")
		}
	}
}

type listingBackend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (b listingBackend) Read(ctx context.Context, q source.Request, p source.Permit) (source.Result, error) {
	return b(ctx, q, p)
}

func TestAccCatalogFinalSourceFence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		virtual int
	}{{"denied", source.ErrDenied, 200}, {"missing", kernel.ErrNotFound, 200}, {"storage", fmt.Errorf("copy: %w", format.ErrStorageFault), 500}} {
		for _, key := range []string{"docker-virtual", "docker-remote"} {
			t.Run(tc.name+"/"+key, func(t *testing.T) {
				w := newBlobWorld(t)
				c, err := w.db.Repos().Get(t.Context(), "docker-virtual")
				if err != nil {
					t.Fatal(err)
				}
				c.Repositories = []string{"docker-remote"}
				if err = w.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
				if err = w.db.Permissions().
					Create(t.Context(), accesstest.Permission{Name: "read", Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"reader": accessapi.ActionRead}}, Targets: map[string]accesstest.Target{"docker-remote": {}}}}}); err != nil {
					t.Fatal(err)
				}
				closed := false
				checks := 0
				service := &source.Service{Resolve: func(context.Context, repo.Config) (model.SourceIdentity, error) {
					return model.SourceIdentity{Kind: model.ExternalHTTP, Repository: "docker-remote", ID: "id", Generation: "1"}, nil
				}, HTTP: listingBackend(func(context.Context, source.Request, source.Permit) (source.Result, error) {
					return source.Result{
						Stream: &source.Stream{
							Body:           &listingClose{Reader: strings.NewReader(`{"repositories":["private"]}`), closed: &closed},
							Status:         200,
							Representation: source.Representation{Size: 28, MediaType: "application/json"},
						},
						Revalidate: func(context.Context) error {
							checks++
							if checks >= 3 {
								return tc.failure
							}
							return nil
						},
					}, nil
				})}
				g := New(Options{Store: w.db, Blobs: w.blobs, Sources: service, Formats: ecosystem.For})
				rec := httptest.NewRecorder()
				request := httptest.NewRequest("GET", "/v2/"+key+"/_catalog", nil)
				// Source.Read requires a decider; run as admin through the real evaluator.
				principalAs(t, w.db, router(g), "reader").ServeHTTP(rec, request)
				want := tc.virtual
				if key == "docker-remote" {
					switch tc.name {
					case "denied":
						want = 403
					case "missing":
						want = 404
					}
				}
				if rec.Code != want || strings.Contains(rec.Body.String(), "private") || !closed {
					t.Fatal(rec.Code, rec.Body.String(), closed, checks)
				}
			})
		}
	}
}

type listingClose struct {
	io.Reader
	closed *bool
}

func (s *listingClose) Close() error { *s.closed = true; return nil }

func TestUnitListingPassOverKeepsStorageFault(t *testing.T) {
	if listingPassedOver(errors.Join(format.ErrStorageFault, source.ErrUnavailable)) {
		t.Fatal("storage failure hidden")
	}
}
