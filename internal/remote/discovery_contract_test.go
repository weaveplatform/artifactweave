package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccMappedCustomIvyRetainsTypeClassifierAndRevision(t *testing.T) {
	w, service, q, _ := discoveryFixture(t, "gradle-default", "ivy-default")
	base, err := layout.Lookup("ivy-default")
	if err != nil {
		t.Fatal(err)
	}
	def := base.Definition()
	def.Name = "custom-ivy"
	def.ArtifactPathPattern = "cache/[orgPath]/[module]/[baseRev](-[folderItegRev])/[type]/[module](-[classifier])-[baseRev](-[fileItegRev]).[ext]"
	def.DescriptorPathPattern = "cache/[orgPath]/[module]/[baseRev](-[folderItegRev])/[type]/ivy-[baseRev](-[fileItegRev]).xml"
	data, err := json.Marshal(map[string]any{
		"artifactPathPattern":              def.ArtifactPathPattern,
		"descriptorPathPattern":            def.DescriptorPathPattern,
		"distinctiveDescriptorPathPattern": def.DistinctiveDescriptorPathPattern,
		"folderIntegrationRevisionRegExp":  def.FolderIntegrationRevisionRegExp,
		"fileIntegrationRevisionRegExp":    def.FileIntegrationRevisionRegExp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.db.Configs().
		Put(t.Context(), types.ConfigDocument{Kind: types.ConfigLayout, Key: def.Name, Document: data}); err != nil {
		t.Fatal(err)
	}
	q.Repo.RepoLayoutRef = def.Name
	if err = w.db.Repos().Update(t.Context(), q.Repo); err != nil {
		t.Fatal(err)
	}
	d, err := NewDiscovery(t.Context(), w.p.Items, q.Repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ upstream, canonical string }{
		{"org.acme/lib/1.0/jars/lib-1.0.jar", "cache/org/acme/lib/1.0/jar/lib-1.0.jar"},
		{"org.acme/lib/1.0/jars/lib-sources-1.0.jar", "cache/org/acme/lib/1.0/jar/lib-sources-1.0.jar"},
		{"org.acme/lib/1.0/ivys/ivy-1.0.xml", "cache/org/acme/lib/1.0/ivy/ivy-1.0.xml"},
		{"org.acme/lib/1.0-20260101000000/jars/lib-1.0-20260101000000.jar", "cache/org/acme/lib/1.0-20260101000000/jar/lib-1.0-20260101000000.jar"},
	} {
		got, ok, err := d.MapFile(c.upstream)
		if err != nil || !ok || got != c.canonical {
			t.Fatal(c, got, ok, err)
		}
	}
	scope := d.Scopes(layout.Values{layout.TokenOrg: "org.acme", layout.TokenModule: "lib"})[0]
	if scope.Path != "cache/org/acme/lib" || scope.Root {
		t.Fatal("custom scope did not use canonical coordinates", scope)
	}
	w.up.Set(
		"/org.acme/lib/1.0/jars/",
		indexHTML(`<a href="lib-1.0.jar">jar</a><a href="lib-sources-1.0.jar">sources</a>`),
	)
	discoveryGrant(t, w, &q, "cache/org/acme/lib/1.0/jar/lib-sources-1.0.jar")
	got, err := ListMappedFolder(
		t.Context(),
		service,
		q,
		d,
		scope,
		"org.acme/lib/1.0/jars",
		&ListingBudget{},
	)
	if err != nil ||
		!reflect.DeepEqual(
			got.Files,
			[]ListingEntry{{Path: "cache/org/acme/lib/1.0/jar/lib-sources-1.0.jar"}},
		) {
		t.Fatal(got, err)
	}
}

func TestAccDiscoveryInvalidScopeAndIdentity(t *testing.T) {
	w, service, q, d := discoveryFixture(t, "gradle-default", "maven-2-default")
	for _, scope := range []DiscoveryScope{{}, {Path: "/org"}, {Path: "../org"}, {Path: "org", Root: true}} {
		if scope.Allows(q) {
			t.Fatal("invalid scope authorized", scope)
		}
		if _, err := d.MetadataResource(
			scope,
			"org/acme/lib/maven-metadata.xml",
			"test-v1",
		); !errors.Is(
			err,
			source.ErrInvalidRequest,
		) {
			t.Fatal("invalid scope built metadata authority", scope, err)
		}
	}
	scope := DiscoveryScope{Path: "org.acme/lib"}
	for _, upstream := range []string{"", "../outside", "/absolute"} {
		if _, err := d.MetadataResource(
			scope,
			upstream,
			"test-v1",
		); !errors.Is(
			err,
			source.ErrInvalidRequest,
		) {
			t.Fatal("invalid upstream path admitted", upstream, err)
		}
	}
	if _, err := d.MetadataResource(scope, "a", ""); !errors.Is(err, source.ErrInvalidRequest) {
		t.Fatal(err)
	}
	if _, err := NewDiscovery(t.Context(), nil, q.Repo); !errors.Is(err, source.ErrInvalidRequest) {
		t.Fatal(err)
	}
	q.Repo.URL += "/changed"
	if _, err := NewDiscovery(
		t.Context(),
		w.p.Items,
		q.Repo,
	); !errors.Is(
		err,
		source.ErrSuperseded,
	) {
		t.Fatal("stale config adopted current source", err)
	}
	q.Repo.Key = "other"
	if _, err := d.Read(t.Context(), service, q); !errors.Is(err, source.ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := d.Read(t.Context(), nil, q); !errors.Is(err, source.ErrUnsupported) {
		t.Fatal(err)
	}
	if err := d.Revalidate(t.Context()); err != nil {
		t.Fatal(err)
	}
	w.c.RepoLayoutRef, w.c.RemoteRepoLayoutRef = "gradle-default", "maven-2-default"
	w.c.URL += "/new-source"
	if err := w.db.Repos().Update(t.Context(), w.c); err != nil {
		t.Fatal(err)
	}
	if err := d.Revalidate(t.Context()); !errors.Is(err, source.ErrSuperseded) {
		t.Fatal(err)
	}
}

func TestAccMappedSmartListingKeepsCanonicalAuthority(t *testing.T) {
	w := smartFixture(t)
	w.c.RepoLayoutRef, w.c.RemoteRepoLayoutRef = "gradle-default", "maven-2-default"
	if err := w.db.Repos().Update(w.ctx, w.c); err != nil {
		t.Fatal(err)
	}
	w.service.HTTP = w.s
	w.service.Revalidate = func(ctx context.Context, q source.Request, permit source.Permit) error {
		opts, err := w.p.Options(ctx, q.Repo)
		if err != nil {
			return err
		}
		return w.p.CheckCurrent(ctx, w.db, q, permit, opts.Revision)
	}
	w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/artifactory/api/storage/source/org/acme/lib/1.0" {
			return false
		}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(
			rw,
			`{"repo":"source","path":"/org/acme/lib/1.0","children":[{"uri":"/lib-1.0.jar","folder":false},{"uri":"/lib-1.0.pom","folder":false}]}`,
		)
		return true
	}
	q := w.smartRequest()
	d, err := NewDiscovery(w.ctx, w.p.Items, w.c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ListMappedFolder(
		w.ctx,
		w.service,
		q,
		d,
		DiscoveryScope{Path: "org.acme/lib"},
		"org/acme/lib/1.0",
		&ListingBudget{},
	)
	if err != nil || len(got.Files) != 2 {
		t.Fatal(got, err)
	}
	for _, f := range got.Files {
		if !strings.HasPrefix(f.Path, "org.acme/lib/") {
			t.Fatal("Smart listing disclosed upstream authority", f)
		}
	}
	q.CacheOnly = true
	if _, err = ListMappedFolder(
		w.ctx,
		w.service,
		q,
		d,
		DiscoveryScope{Path: "org.acme/lib"},
		"org/acme/lib/1.0",
		&ListingBudget{},
	); err != nil {
		t.Fatal("Smart mapped cache miss", err)
	}
}

func TestAccMappedListingBoundedFailures(t *testing.T) {
	for _, budget := range []ListingBudget{{pages: 100}, {entries: 10000}} {
		w, service, q, d := discoveryFixture(t, "gradle-default", "maven-2-default")
		w.up.Set("/org/acme/lib/1.0/", indexHTML(`<a href="lib-1.0.jar">jar</a>`))
		if _, err := ListMappedFolder(
			t.Context(),
			service,
			q,
			d,
			DiscoveryScope{Path: "org.acme/lib"},
			"org/acme/lib/1.0",
			&budget,
		); err == nil {
			t.Fatal("mapped traversal exceeded shared budget")
		}
	}
	_, service, q, d := discoveryFixture(t, "gradle-default", "maven-2-default")
	q.Repo.Class = repo.Local
	if _, err := NewDiscovery(
		t.Context(),
		&item.Service{},
		q.Repo,
	); !errors.Is(
		err,
		source.ErrInvalidRequest,
	) {
		t.Fatal(err)
	}
	if _, err := ListMappedFolder(
		t.Context(),
		service,
		q,
		d,
		DiscoveryScope{Root: true},
		"",
		nil,
	); !errors.Is(
		err,
		source.ErrUnsupported,
	) {
		t.Fatal(err)
	}
}

func TestAccMappedPaginationNeverReturnsPartialResults(t *testing.T) {
	for _, mode := range []string{"cycle", "pages", "entries", "complete"} {
		t.Run(mode, func(t *testing.T) {
			_, service, q, d := discoveryFixture(t, "gradle-default", "maven-2-default")
			calls := 0
			service.HTTP = listingBackend(
				func(_ context.Context, request source.Request, _ source.Permit) (source.Result, error) {
					calls++
					body := `<h1>Index of</h1><a href="lib-1.0.jar">jar</a><a href="child/">child</a><a href="maven-metadata.xml">metadata</a>`
					if mode != "complete" || calls == 1 {
						next := calls + 1
						if mode == "cycle" {
							next = 2
						}
						body += fmt.Sprintf(`<link rel="next" href="?page=%d">`, next)
					}
					return source.Result{
						Metadata: source.MetadataResponse{BaseURL: request.Resource.ExternalURL},
						Stream: &source.Stream{
							Status:         200,
							Body:           io.NopCloser(strings.NewReader(body)),
							Representation: source.Representation{Size: int64(len(body))},
						},
					}, nil
				},
			)
			budget := new(ListingBudget)
			if mode == "entries" {
				budget.entries = 9996
			}
			got, err := ListMappedFolder(
				t.Context(),
				service,
				q,
				d,
				DiscoveryScope{Path: "org.acme/lib"},
				"org/acme/lib/1.0",
				budget,
			)
			if mode == "complete" {
				if err != nil || calls != 2 || len(got.Files) != 1 || len(got.Folders) != 1 ||
					len(got.Documents) != 1 {
					t.Fatal(got, calls, err)
				}
				return
			}
			if err == nil || !reflect.DeepEqual(got, MappedFolderListing{}) {
				t.Fatal("partial mapped results escaped", got, err)
			}
			if mode == "pages" && calls != 100 || mode != "pages" && calls != 2 {
				t.Fatal("unexpected page bound", calls)
			}
		})
	}
}
