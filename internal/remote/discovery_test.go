package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/layout"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func discoveryFixture(
	t *testing.T,
	canonical, upstream string,
) (*policyWorld, *source.Service, source.Request, *Discovery) {
	t.Helper()
	w, service, q := listingFixture(t)
	q.Repo.RepoLayoutRef, q.Repo.RemoteRepoLayoutRef = canonical, upstream
	if err := w.db.Repos().Update(t.Context(), q.Repo); err != nil {
		t.Fatal(err)
	}
	d, err := NewDiscovery(t.Context(), &item.Service{Store: w.db}, q.Repo)
	if err != nil {
		t.Fatal(err)
	}
	service.Revalidate = func(ctx context.Context, q source.Request, p source.Permit) error {
		id, err := w.db.RemoteCache().Source(ctx, q.Repo.Key)
		if err != nil || id != p.Identity {
			return source.ErrSuperseded
		}
		access, err := authz.New(
			ctx,
			authz.Sources{Permissions: w.db.Permissions(), Groups: w.db.Groups()},
			q.Subject,
		)
		if err != nil {
			return source.ErrUnavailable
		}
		full, allows := q.Resource.Path, access.Allows
		if q.Resource.PermissionRoot {
			full = ""
		} else if q.Resource.PermissionFolder {
			full, allows = q.Resource.PermissionPath, access.AllowsFolder
		}
		if !allows(
			types.ResourceArtifact,
			authz.Repo{Key: q.Repo.Key, Class: string(q.Repo.Class)},
			full,
			types.ActionRead,
		) {
			return source.ErrDenied
		}
		return generic.CheckDownload(
			q.Repo,
			full,
			q.Resource.PermissionFolder || q.Resource.PermissionRoot,
		)
	}
	w.p.CheckCurrent = func(ctx context.Context, _ store.Store, q source.Request, p source.Permit, _ string) error {
		return service.Revalidate(ctx, q, p)
	}
	return w, service, q, d
}

func discoveryGrant(t *testing.T, w *policyWorld, q *source.Request, includes ...string) {
	t.Helper()
	grant := types.Permission{Name: "mapped", Resources: map[types.ResourceType]types.Resource{
		types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"mapped": types.ActionRead}},
			Targets: map[string]types.Target{q.Repo.Key: {Include: includes}},
		},
	}}
	err := w.db.Permissions().Create(t.Context(), grant)
	if errors.Is(err, store.ErrConflict) {
		err = w.db.Permissions().Update(t.Context(), grant)
	}
	if err != nil {
		t.Fatal(err)
	}
	q.Subject = authz.Subject{Name: "mapped"}
	q.Access, err = authz.New(
		t.Context(),
		authz.Sources{Permissions: w.db.Permissions(), Groups: w.db.Groups()},
		q.Subject,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestMappedListingCanonicalAuthorityCacheAndRevocation(t *testing.T) {
	w, service, q, d := discoveryFixture(t, "gradle-default", "maven-2-default")
	w.up.Set(
		"/org/acme/lib/1.0/",
		indexHTML(
			`<a href="lib-1.0.jar">jar</a><a href="lib-1.0-sources.jar">sources</a><a href="lib-1.0.pom">pom</a><a href="maven-metadata.xml">metadata</a><a href="child/">child</a>`,
		),
	)
	scope := d.Scopes(layout.Values{layout.TokenOrg: "org.acme", layout.TokenModule: "lib"})[0]
	if scope != (DiscoveryScope{Path: "org.acme/lib"}) {
		t.Fatal("wrong canonical query scope", scope)
	}
	for _, file := range []string{"lib-1.0.jar", "lib-1.0-sources.jar", "lib-1.0.pom"} {
		if _, ok, err := d.MapFile("org/acme/lib/1.0/" + file); err != nil || !ok {
			t.Fatal("lossless fixture refused", file, err)
		}
	}
	discoveryGrant(t, w, &q, "org.acme/lib/1.0/lib-1.0.jar")
	q.Repo.ExcludesPattern = ptr("org/acme/**")
	got, err := ListMappedFolder(
		t.Context(),
		service,
		q,
		d,
		scope,
		"org/acme/lib/1.0",
		&ListingBudget{},
	)
	if err != nil ||
		!reflect.DeepEqual(got.Files, []ListingEntry{{Path: "org.acme/lib/1.0/lib-1.0.jar"}}) {
		t.Fatal("upstream lexical exclusion replaced canonical authority", got, err)
	}
	encoded, err := json.Marshal(got)
	if err != nil || strings.Contains(string(encoded), "org/acme") ||
		strings.Contains(string(encoded), "child") {
		t.Fatal("upstream traversal cursor leaked", string(encoded), err)
	}
	entries, _, err := w.db.RemoteCache().List(t.Context(), q.Repo.Key, "", store.Page{Limit: 100})
	if err != nil || len(entries) != 1 ||
		!strings.HasPrefix(entries[0].Token.Key.Path, "org.acme/lib/.remote-discovery/") {
		t.Fatal("cache path used upstream authority", entries, err)
	}
	q.CacheOnly = true
	before := len(w.up.Requests())
	again, err := ListMappedFolder(
		t.Context(),
		service,
		q,
		d,
		scope,
		"org/acme/lib/1.0",
		&ListingBudget{},
	)
	if err != nil || !reflect.DeepEqual(got.Files, again.Files) || len(w.up.Requests()) != before {
		t.Fatal("mapped cache-only lookup fetched", again, err)
	}
	discoveryGrant(t, w, &q, "org/acme/lib/1.0/lib-1.0.jar")
	if err = got.Revalidate(t.Context()); !errors.Is(err, source.ErrDenied) {
		t.Fatal("revoked canonical grant survived via upstream grant", err)
	}
	q.CacheOnly = false
	if _, err = ListMappedFolder(
		t.Context(),
		service,
		q,
		d,
		scope,
		"org/acme/lib/1.0",
		&ListingBudget{},
	); !errors.Is(err, source.ErrDenied) ||
		len(w.up.Requests()) != before {
		t.Fatal("upstream-only grant initiated mapped read", err)
	}
}

func TestMappedListingRootScopeAndLossyAdmission(t *testing.T) {
	w, service, q, _ := discoveryFixture(t, "gradle-default", "maven-2-default")
	doc := types.ConfigDocument{
		Kind: types.ConfigLayout,
		Key:  "filename-module",
		Document: json.RawMessage(
			`{"artifactPathPattern":"[org]/[module]-[baseRev](-[classifier]).[ext]"}`,
		),
	}
	if err := w.db.Configs().Put(t.Context(), doc); err != nil {
		t.Fatal(err)
	}
	q.Repo.RepoLayoutRef = doc.Key
	if err := w.db.Repos().Update(t.Context(), q.Repo); err != nil {
		t.Fatal(err)
	}
	d, err := NewDiscovery(t.Context(), &item.Service{Store: w.db}, q.Repo)
	if err != nil {
		t.Fatal(err)
	}
	scope := d.Scopes(layout.Values{layout.TokenOrg: "org.acme", layout.TokenModule: "lib"})[0]
	if scope != (DiscoveryScope{Root: true}) {
		t.Fatal("filename module borrowed a folder", scope)
	}
	discoveryGrant(t, w, &q, "org.acme/unrelated-1.0.jar")
	if _, err = ListMappedFolder(
		t.Context(),
		service,
		q,
		d,
		scope,
		"org/acme/lib/1.0",
		&ListingBudget{},
	); !errors.Is(err, source.ErrDenied) ||
		len(w.up.Requests()) != 0 {
		t.Fatal("unrelated partial grant authorized root discovery", err)
	}
	discoveryGrant(t, w, &q, "**")
	w.up.Set("/org/acme/lib/1.0/", indexHTML(`<a href="lib-1.0.jar">jar</a>`))
	got, err := ListMappedFolder(
		t.Context(),
		service,
		q,
		d,
		scope,
		"org/acme/lib/1.0",
		&ListingBudget{},
	)
	if err != nil || len(got.Files) != 1 || got.Files[0].Path != "org.acme/lib-1.0.jar" {
		t.Fatal("authorized root mapping failed", got, err)
	}
	if _, ok := d.VersionScope("org.acme", "lib", "1.0"); ok {
		t.Fatal("filename version fabricated folder authority")
	}
	discoveryGrant(t, w, &q, "org.acme/lib-1.0.jar")
	if err = got.Revalidate(t.Context()); !errors.Is(err, source.ErrDenied) {
		t.Fatal("root revocation hidden by exact surviving grant", err)
	}
	for _, noStore := range []bool{false, true} {
		t.Run(fmt.Sprint("lossy no-store=", noStore), func(t *testing.T) {
			w, service, q, d := discoveryFixture(t, "simple-default", "maven-2-default")
			q.Repo.StoreArtifactsLocally = ptr(!noStore)
			w.up.Set("/org/acme/lib/1.0/", indexHTML(`<a href="lib-1.0-sources.jar">sources</a>`))
			scope := d.Scopes(layout.Values{layout.TokenOrg: "org.acme", layout.TokenModule: "lib"})[0]
			_, err := ListMappedFolder(
				t.Context(),
				service,
				q,
				d,
				scope,
				"org/acme/lib/1.0",
				&ListingBudget{},
			)
			var failure *source.HTTPError
			if !errors.As(err, &failure) || failure.Status != 501 {
				t.Fatal("lossy classifier was admitted", err)
			}
			entries, _, err := w.db.RemoteCache().
				List(t.Context(), q.Repo.Key, "", store.Page{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Blob.Checksums.SHA256 != "" {
					t.Fatal("lossy metadata entered durable cache")
				}
			}
		})
	}
}

func TestMappedLayoutEditFencesBeforeReadAndPublication(t *testing.T) {
	for _, noStore := range []bool{false, true} {
		t.Run(fmt.Sprint("no-store=", noStore), func(t *testing.T) {
			w, service, q, d := discoveryFixture(t, "gradle-default", "maven-2-default")
			q.Repo.StoreArtifactsLocally = ptr(!noStore)
			gate := new(fakeupstream.Barrier)
			resource := indexHTML(`<a href="lib-1.0.jar">jar</a>`)
			resource.Fault.BeforeBody = gate
			w.up.Set("/org/acme/lib/1.0/", resource)
			scope := d.Scopes(layout.Values{layout.TokenOrg: "org.acme", layout.TokenModule: "lib"})[0]
			done := make(chan error, 1)
			go func() {
				_, err := ListMappedFolder(
					t.Context(),
					service,
					q,
					d,
					scope,
					"org/acme/lib/1.0",
					&ListingBudget{},
				)
				done <- err
			}()
			<-gate.Reached()
			err := w.db.Configs().Put(t.Context(), types.ConfigDocument{
				Kind: types.ConfigLayout,
				Key:  "gradle-default",
				Document: json.RawMessage(
					`{"artifactPathPattern":"new/[org]/[module]/[baseRev]/[module]-[baseRev].[ext]"}`,
				),
			})
			gate.Release()
			if err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, source.ErrSuperseded) {
				t.Fatal("layout edit did not fence publication", err)
			}
			before := len(w.up.Requests())
			if _, err = ListMappedFolder(
				t.Context(),
				service,
				q,
				d,
				scope,
				"org/acme/lib/1.0",
				&ListingBudget{},
			); !errors.Is(err, source.ErrSuperseded) ||
				len(w.up.Requests()) != before {
				t.Fatal("old mapping acquired new source generation", err)
			}
			entries, _, err := w.db.RemoteCache().
				List(t.Context(), q.Repo.Key, "", store.Page{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Blob.Checksums.SHA256 != "" {
					t.Fatal("stale mapping published metadata")
				}
			}
		})
	}
}
