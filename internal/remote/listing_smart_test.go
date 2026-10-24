package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

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
			[]ListingEntry{{"nested/child.bin", false}, {"nested/sub", true}},
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

func TestAccSmartListingUsesPreparedAuthenticatedStorageResource(t *testing.T) {
	_, s, q := listingFixture(t)
	q.Repo.URL = "https://peer.example/artifactory/api/nuget/packages"
	q.Repo.ContentSynchronisation = &repo.ContentSynchronisation{Enabled: true}
	var seen source.Resource
	s.HTTP = listingBackend(
		func(ctx context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			seen = q.Resource
			body := `{"repo":"packages","path":"/nested","children":[{"uri":"/child","folder":true},{"uri":"/a b.bin","folder":false},{"uri":"/../outside","folder":false}]}`
			response := source.MetadataResponse{BaseURL: q.Resource.ExternalURL}
			if err := q.Resource.ValidateMetadata(
				ctx,
				strings.NewReader(body),
				int64(len(body)),
				response,
			); err != nil {
				return source.Result{}, err
			}
			return source.Result{
				Metadata: response,
				Stream: &source.Stream{
					Status:         200,
					Body:           io.NopCloser(strings.NewReader(body)),
					Representation: source.Representation{Size: int64(len(body))},
				},
			}, nil
		},
	)
	out, err := ListFolder(t.Context(), s, q, "nested", &ListingBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		out.Entries,
		[]ListingEntry{{"nested/a b.bin", false}, {"nested/child", true}},
	) {
		t.Fatal(out.Entries)
	}
	if !seen.RequireSmart ||
		seen.ExternalURL != "https://peer.example/artifactory/api/storage/packages/nested" ||
		seen.Variant != "folder-listing-smart-v1" ||
		seen.PermissionPath != "nested" {
		t.Fatalf("wrong Smart source %+v", seen)
	}
	q.Repo.URL = "https://peer.example/artifactory/packages?ambiguous=true"
	if _, err = ListFolder(
		t.Context(),
		s,
		q,
		"nested",
		&ListingBudget{},
	); !errors.Is(
		err,
		source.ErrInvalidRequest,
	) {
		t.Fatal(err)
	}
}

func TestAccSmartListingRejectsMalformedOrWrongFolderDocuments(t *testing.T) {
	for _, body := range []string{
		`{}`, `[]`, `null`, `{"repo":"other","path":"/","children":[]}`,
		`{"repo":"up","path":"/elsewhere","children":[]}`,
		`{"repo":"up","path":"/","children":null}`,
		`{"repo":"up","path":"/","children":[{"uri":"/x"}]}`,
		`{"repo":"up","path":"/","children":[{"uri":"/x","folder":"true"}]}`,
		`{"repo":"up","path":"/","children":[{"uri":3,"folder":true}]}`,
		`{"repo":"up","path":"/","children":[]}{}`,
		`{"repo":"up","path":"/","children":[` + strings.TrimSuffix(strings.Repeat(`{"uri":"/a","folder":false},`, 10001), ",") + `]}`,
	} {
		if _, err := parseSmartListing(t.Context(), strings.NewReader(body), "up", ""); err == nil {
			t.Fatalf("accepted %.80s", body)
		}
	}
	for _, key := range []string{"up", "up-cache"} {
		body := fmt.Sprintf(`{"repo":%q,"path":"/","children":[]}`, key)
		out, err := parseSmartListing(t.Context(), strings.NewReader(body), "up", "")
		if err != nil || len(out.entries) != 0 {
			t.Fatal(out, err)
		}
	}
	unsafe := `{"repo":"up","path":"/","children":[{"uri":"/..","folder":true},{"uri":"/a/b","folder":true},{"uri":"//host","folder":true},{"uri":"relative","folder":false},{"uri":"/a\\b","folder":false},{"uri":"/a\u0000b","folder":false},{"uri":"/","folder":true}]}`
	if out, err := parseSmartListing(
		t.Context(),
		strings.NewReader(unsafe),
		"up",
		"",
	); err != nil ||
		len(out.entries) != 0 {
		t.Fatal(out, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := parseSmartListing(
		ctx,
		strings.NewReader(`{"repo":"up","path":"/","children":[{"uri":"/a","folder":true}]}`),
		"up",
		"",
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatal(err)
	}
	if _, err := parseSmartListing(t.Context(), failingListingReader{}, "up", ""); err == nil {
		t.Fatal("reader error hidden")
	}
}
