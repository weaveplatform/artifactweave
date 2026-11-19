package itemkit_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// listingFenceStore reaches the later member only after the remote's complete
// ListFolder operation, including its body close and first revalidation.
type listingFenceStore struct {
	itemkit.Store
	after func()
}

func (s listingFenceStore) Items() port.ItemStore { return listingFenceItems{s.Store.Items(), s.after} }

type listingFenceItems struct {
	port.ItemStore
	after func()
}

func (s listingFenceItems) Children(ctx context.Context, key string, cached bool, p string, page kernel.Page) ([]model.Item, string, error) {
	items, next, err := s.ItemStore.Children(ctx, key, cached, p, page)
	if key == "later-local" && p == "dir" {
		s.after()
	}
	return items, next, err
}

func listingFenceWorld(t *testing.T) *memberWorld {
	t.Helper()
	w := newMemberWorld(t)
	w.Remote.ListRemoteFolderItems = repo.Flex(true)
	if err := w.Store.Repos().Update(t.Context(), w.Remote); err != nil {
		t.Fatal(err)
	}
	later := *repo.Defaults(&repo.Config{Key: "later-local", Class: repo.Local})
	for _, c := range []repo.Config{later, *repo.Defaults(&repo.Config{Key: "phase", Class: repo.Virtual, Repositories: []string{kittest.RemoteKey, later.Key}})} {
		if err := w.Store.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		repo   repo.Config
		cached bool
		file   string
	}{{w.Remote, true, "dir/cached.bin"}, {later, false, "dir/healthy.bin"}} {
		putFolders(t, w.Store, c.repo.Key, c.cached, c.file)
		w.Put(t, c.repo, c.cached, c.file, []byte("kept"))
	}
	return w
}

func listingFenceReply(q source.Request, closed *atomic.Int32) source.Result {
	out := streamed(`<!DOCTYPE html><title>Index of dir</title><a href="live.bin">live.bin</a>`, kernel.Checksums{}, closed)
	out.Stream.Representation.MediaType = "text/html"
	out.Metadata = source.MetadataResponse{BaseURL: q.Resource.ExternalURL}
	return out
}

func TestUnitVirtualFolderFinalAuthorityFailures(t *testing.T) {
	own := fmt.Errorf("source lookup: %w: %w", kernel.ErrStore, errFault)
	for _, tc := range []struct {
		name    string
		failure error
		status  int
		end     bool
	}{
		{"own-store", own, 500, false},
		{"own-store-joined-driver-deadline", errors.Join(own, context.DeadlineExceeded), 500, false},
		{"permission", source.ErrDenied, 403, false},
		{"policy", format.Rejectf(404, "current policy refusal"), 404, false},
		{"superseded", source.ErrSuperseded, 503, false},
		{"final-check-http-error", &source.HTTPError{Status: 503}, 503, false},
		{"final-check-security-kind", &source.HTTPError{Status: 502, Kind: source.FailureKindSecurity}, 502, false},
		{"parent-ended-before-final-check", own, 502, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := listingFenceWorld(t)
			phase := false
			observed := 0
			var closed atomic.Int32
			s := *w.k.RemoteSources
			s.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
				if phase {
					observed++
					if q.Repo.Key != kittest.RemoteKey {
						t.Errorf("final fence bound to %s", q.Repo.Key)
					}
					return tc.failure
				}
				return nil
			}
			w.src.answer = func(q source.Request) (source.Result, error) { return listingFenceReply(q, &closed), nil }
			req := kittest.Request(t, http.MethodGet, "/artifactory/phase/dir/")
			ctx, cancel := context.WithCancel(req.Context())
			defer cancel()
			req = req.WithContext(ctx)
			k := w.Kit(itemkit.Deps{Store: listingFenceStore{w.Store, func() {
				if closed.Load() != 1 {
					t.Fatalf("final phase reached before body close: %d", closed.Load())
				}
				phase = true
				if tc.end {
					cancel()
				}
			}}, RemoteSources: &s})
			got := httptest.NewRecorder()
			k.GetItem(got, req)
			if !phase || (!tc.end && observed == 0) {
				t.Fatal("did not reach separate final member fence")
			}
			if got.Code != tc.status || strings.Contains(got.Body.String(), "<a ") {
				t.Fatalf("final authority failure hid behind partial HTML: %d %s, want%d", got.Code, got.Body, tc.status)
			}
			if closed.Load() != 1 {
				t.Fatalf("listing body closed%d times", closed.Load())
			}
			if tc.name == "superseded" && got.Header().Get("Retry-After") != "1" {
				t.Fatalf("superseded retry header: %v", got.Header())
			}
		})
	}
}

func TestUnitVirtualFolderReadFailureBoundaries(t *testing.T) {
	own := fmt.Errorf("source lookup: %w: %w", kernel.ErrStore, errFault)
	for _, tc := range []struct {
		name, phase string
		failure     error
		status      int
		end         bool
	}{
		{"own-lookup", "lookup", own, 500, false},
		{"own-lookup-joined-driver-deadline", "lookup", errors.Join(own, context.DeadlineExceeded), 500, false},
		{"current-permission", "authority", source.ErrDenied, 403, false},
		{"current-policy", "authority", format.Rejectf(403, "current policy refusal"), 403, false},
		{"current-source-replaced", "authority", source.ErrSuperseded, 503, false},
		{"parent-ended", "lookup", own, 502, true},
		{"origin-503", "upstream", &source.HTTPError{Status: 503}, 200, false},
		{"origin-403", "upstream", &source.HTTPError{Status: 403}, 200, false},
		{"origin-401", "upstream", &source.HTTPError{Status: 401}, 200, false},
		{"acquisition-security-kind", "upstream", &source.HTTPError{Status: 502, Kind: source.FailureKindSecurity}, 200, false},
		{"origin-timeout-live-parent", "upstream", context.DeadlineExceeded, 200, false},
		{"origin-unavailable", "upstream", source.ErrUnavailable, 200, false},
		{"source-content-open", "upstream", &source.ContentOpenError{Err: own}, 200, false},
		{"absent-live-folder", "upstream", kernel.ErrNotFound, 200, false},
		{"unsupported-upstream-index", "upstream", &source.HTTPError{Status: 501}, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := listingFenceWorld(t)
			s := *w.k.RemoteSources
			req := kittest.Request(t, http.MethodGet, "/artifactory/phase/dir/")
			ctx, cancel := context.WithCancel(req.Context())
			defer cancel()
			req = req.WithContext(ctx)
			switch tc.phase {
			case "lookup":
				s.Resolve = func(context.Context, repo.Config) (model.SourceIdentity, error) {
					if tc.end {
						cancel()
					}
					return model.SourceIdentity{}, tc.failure
				}
			case "authority":
				s.Revalidate = func(context.Context, source.Request, source.Permit) error { return tc.failure }
			case "upstream":
				w.src.answer = func(source.Request) (source.Result, error) { return source.Result{}, tc.failure }
			}
			k := w.Kit(itemkit.Deps{RemoteSources: &s})
			got := httptest.NewRecorder()
			k.GetItem(got, req)
			if got.Code != tc.status {
				t.Fatalf("read phase: %d %s, want%d", got.Code, got.Body, tc.status)
			}
			if tc.status == 200 {
				for _, name := range []string{"cached.bin", "healthy.bin"} {
					if !strings.Contains(got.Body.String(), name) {
						t.Fatalf("ordinary acquisition failure lost%s: %s", name, got.Body)
					}
				}
			} else if strings.Contains(got.Body.String(), "<a ") {
				t.Fatalf("authority failure returned partial index: %s", got.Body)
			}
			if tc.phase != "upstream" && len(w.src.reads) != 0 {
				t.Fatalf("own failure reached upstream: %d", len(w.src.reads))
			}
			if !tc.end && ctx.Err() != nil {
				t.Fatal("live parent ended")
			}
		})
	}
}
