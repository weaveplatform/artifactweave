package remote

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

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

func TestAccSmartListingUsesRealDiscoveryTransportAndCacheFences(t *testing.T) {
	w := smartFixture(t)
	w.service.HTTP = w.s
	w.service.Revalidate = func(ctx context.Context, q source.Request, permit source.Permit) error {
		opts, err := w.p.Options(ctx, q.Repo)
		if err != nil {
			return err
		}
		return w.p.CheckCurrent(ctx, w.db, q, permit, opts.Revision)
	}
	listingHeads, listingGets, binaryCalls := 0, 0, 0
	w.reply = func(rw http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/artifactory/api/storage/source/nested":
			if r.Method == http.MethodHead {
				listingHeads++
			} else if r.Method == http.MethodGet {
				listingGets++
			}
			rw.Header().
				Set("Content-Type", "application/vnd.org.jfrog.artifactory.storage.FolderInfo+json")
			_, _ = io.WriteString(
				rw,
				`{"repo":"source","path":"/nested","children":[{"uri":"/child.bin","folder":false},{"uri":"/sub","folder":true}]}`,
			)
			return true
		case "/artifactory/source/nested/child.bin":
			binaryCalls++
		}
		return false
	}
	q := w.smartRequest()
	got, err := ListFolder(w.ctx, w.service, q, "nested", &ListingBudget{})
	if err != nil ||
		!reflect.DeepEqual(
			got.Entries,
			[]ListingEntry{
				{Path: "nested/child.bin", Folder: false},
				{Path: "nested/sub", Folder: true},
			},
		) {
		t.Fatal(got, err)
	}
	w.mu.Lock()
	before, unauthorized := w.calls, w.unauthorized
	w.mu.Unlock()
	if unauthorized != 0 || before != 4 || listingHeads != 1 || listingGets != 1 ||
		binaryCalls != 0 {
		t.Fatalf(
			"calls=%d unauthorized=%d listing HEAD=%d GET=%d binaries=%d",
			before,
			unauthorized,
			listingHeads,
			listingGets,
			binaryCalls,
		)
	}
	items, _, err := w.db.Items().Children(w.ctx, w.c.Key, true, "", store.Page{})
	if err != nil || len(items) != 0 {
		t.Fatal("listing created items", items, err)
	}
	q.CacheOnly = true
	got, err = ListFolder(w.ctx, w.service, q, "nested", &ListingBudget{})
	if err != nil || len(got.Entries) != 2 {
		t.Fatal(got, err)
	}
	w.mu.Lock()
	after := w.calls
	w.mu.Unlock()
	if after != before {
		t.Fatal("cache-only discovery reached peer", before, after)
	}
	if err = w.db.Permissions().Delete(w.ctx, "read"); err != nil {
		t.Fatal(err)
	}
	if err = got.Revalidate(w.ctx); !errors.Is(err, source.ErrDenied) {
		t.Fatal("revoked listing disclosed", err)
	}
}
