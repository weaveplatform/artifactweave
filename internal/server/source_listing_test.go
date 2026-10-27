package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestAccRemoteBrowsingOptInAndCacheOnlyStorage(t *testing.T) {
	calls := 0
	var last source.Request
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(_ context.Context, q source.Request, p source.Permit) (source.Result, error) {
				calls++
				last = q
				if p.Intent != source.RetrievalIntentMayFetch {
					t.Fatal("cache alias reached upstream")
				}
				body := `<!DOCTYPE html><h1>Index of upstream</h1><a href="a%26b.bin?secret=private">a</a><a href="child/">child</a>`
				return source.Result{
					Metadata: source.MetadataResponse{BaseURL: q.Resource.ExternalURL},
					Stream: &source.Stream{
						Status:         200,
						Body:           io.NopCloser(strings.NewReader(body)),
						Representation: source.Representation{Size: int64(len(body))},
					},
				}, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	for _, endpoint := range []string{"/artifactory/up/", "/artifactory/api/storage/up", "/artifactory/up-cache/"} {
		if res := w.do("GET", endpoint, "reader", ""); res.Code != 200 {
			t.Fatal(endpoint, res.Code, res.Body)
		}
	}
	if calls != 0 {
		t.Fatal("default browsing fetched upstream")
	}
	c, err := w.db.Repos().Get(t.Context(), "up")
	if err != nil {
		t.Fatal(err)
	}
	c.ListRemoteFolderItems = repo.Flex(true)
	if err = w.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, folder := range []string{"", "uncached/"} {
			res := w.do(method, "/artifactory/up/"+folder, "reader", "")
			if res.Code != 200 {
				t.Fatal(method, folder, res.Code, res.Body)
			}
			if method == http.MethodGet &&
				(!strings.Contains(res.Body.String(), `href="a&amp;b.bin"`) || !strings.Contains(res.Body.String(), `href="child/"`) || strings.Contains(res.Body.String(), "secret")) {
				t.Fatal("unsafe/missing rewrite", res.Body)
			}
			if method == http.MethodHead && res.Body.Len() != 0 {
				t.Fatal("HEAD returned listing body")
			}
			if last.Resource.PermissionPath != strings.TrimSuffix(folder, "/") ||
				last.Resource.Kind != source.ResourceKindMetadata ||
				last.Resource.Method != http.MethodGet ||
				!last.Resource.PermissionFolder ||
				last.Resource.ValidateMetadata == nil {
				t.Fatalf("wrong source contract %+v", last.Resource)
			}
		}
	}
	before := calls
	for _, endpoint := range []string{"/artifactory/api/storage/up", "/artifactory/api/storage/up?list", "/artifactory/up-cache/"} {
		res := w.do("GET", endpoint, "reader", "")
		if res.Code != 200 || strings.Contains(res.Body.String(), "a&b") ||
			strings.Contains(res.Body.String(), "child/") {
			t.Fatal("cache-only storage changed", endpoint, res.Code, res.Body)
		}
	}
	if calls != before {
		t.Fatal("storage/alias initiated listing")
	}
	if _, err = w.db.Items().
		Get(t.Context(), "up", true, "a&b.bin"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("synthetic item", err)
	}
	for _, user := range []string{"user", ""} {
		res := w.do("GET", "/artifactory/up/", user, "")
		if res.Code != 401 && res.Code != 403 {
			t.Fatal("unauthorized listing", res.Code)
		}
	}
	if calls != before {
		t.Fatal("unauthorized call reached source")
	}
}

func TestAccRemoteBrowsingFailureDoesNotLookLikeEmptySuccess(t *testing.T) {
	for _, failure := range []error{source.ErrUnsupported, source.ErrSuperseded, &source.HTTPError{Status: 504}} {
		svc := &source.Service{
			Resolve: sourceIdentity,
			HTTP: sourceBackend(
				func(context.Context, source.Request, source.Permit) (source.Result, error) {
					return source.Result{}, failure
				},
			),
		}
		w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
		c, err := w.db.Repos().Get(t.Context(), "up")
		if err != nil {
			t.Fatal(err)
		}
		c.ListRemoteFolderItems = repo.Flex(true)
		if err = w.db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		res := w.do("GET", "/artifactory/up/", "reader", "")
		if res.Code < 500 || strings.Contains(res.Body.String(), "Index of") {
			t.Fatal("failure hidden", res.Code, res.Body)
		}
	}
}
