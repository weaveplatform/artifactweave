package pkgroute_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const (
	blackedOut = "The repository 'gen-local' is blacked out and cannot serve artifact 'gen-local:%s'."
	excluded   = "The repository 'gen-local' rejected the artifact 'gen-local:%s' due to " +
		"its include/exclude pattern settings."
	noMember = "no member serves it"
)

// policyFormat is the test protocol with one route per download helper of
// the kit, so a test can see the download policy at each: ServeItem; Item,
// ResolveItem and Read answering their error; ServeFound of a file read
// from the store without Item; a virtual repository's search through its
// members in order, as Hex's is; and a member the protocol chose, served
// with ServeItem, as Pub's is, checked with CheckDownload, or served with
// ServeFound under the virtual repository's own request. An error that is
// kernel.ErrNotFound sets X-Not-Found.
func policyFormat(sources map[repo.Class]pkgroute.Source) pkgroute.Protocol {
	p := testFormat(sources)
	p.Routes = func(r chi.Router, k *pkgroute.Kit) {
		path := func(r *http.Request) string {
			_, rest, _ := strings.Cut(pkgroute.RequestFrom(r).Rest, "/")
			return rest
		}
		fail := func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, kernel.ErrNotFound) {
				w.Header().Set("X-Not-Found", "true")
			}
			k.Error(w, r, err)
		}
		answer := func(w http.ResponseWriter, r *http.Request, f pkgroute.Found, err error) {
			if err != nil {
				fail(w, r, err)
				return
			}
			k.ServeFound(w, r, f)
		}
		check := func(w http.ResponseWriter, r *http.Request) {
			if err := k.CheckDownload(r, path(r)); err != nil {
				fail(w, r, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}
		r.Get("/check/*", check)
		r.Get("/first-check/*", func(w http.ResponseWriter, r *http.Request) {
			if mr := member(w, r, k, pkgroute.RequestFrom(r).Repo.Repositories[0]); mr != nil {
				check(w, mr)
			}
		})
		r.Get("/first-found/*", func(w http.ResponseWriter, r *http.Request) {
			key := pkgroute.RequestFrom(r).Repo.Repositories[0]
			c, err := k.DB().Repos().Get(r.Context(), key)
			if err != nil {
				k.Error(w, r, err)
				return
			}
			it, err := k.DB().Items().Get(r.Context(), key, false, path(r))
			if err != nil {
				k.Error(w, r, apierror.New(http.StatusNotFound, "not stored"))
				return
			}
			k.ServeFound(w, r, pkgroute.Found{Repo: c, Item: it})
		})
		r.Get("/files/*", func(w http.ResponseWriter, r *http.Request) {
			k.ServeItem(w, r, path(r))
		})
		r.Get("/item/*", func(w http.ResponseWriter, r *http.Request) {
			f, err := k.Item(r, path(r))
			answer(w, r, f, err)
		})
		r.Get("/resolve/*", func(w http.ResponseWriter, r *http.Request) {
			f, err := k.ResolveItem(r, path(r))
			answer(w, r, f, err)
		})
		r.Get("/read/*", func(w http.ResponseWriter, r *http.Request) {
			result, err := k.Read(r, pkgroute.Resource(r, path(r), path(r)))
			if err != nil {
				k.Error(w, r, pkgroute.SourceError(err, false))
				return
			}
			k.ServeResult(w, r, result)
		})
		r.Get("/found/*", func(w http.ResponseWriter, r *http.Request) {
			c := pkgroute.RequestFrom(r).Repo
			it, err := k.DB().Items().Get(r.Context(), c.Key, false, path(r))
			if err != nil {
				k.Error(w, r, apierror.New(http.StatusNotFound, "not stored"))
				return
			}
			k.ServeFound(w, r, pkgroute.Found{Repo: c, Item: it})
		})
		r.Get("/members/*", func(w http.ResponseWriter, r *http.Request) {
			for _, key := range pkgroute.RequestFrom(r).Repo.Repositories {
				mr := member(w, r, k, key)
				if mr == nil {
					return
				}
				if !k.Can(mr, path(r), api.ActionRead) {
					continue
				}
				f, err := k.Item(mr, path(r))
				if errors.Is(err, kernel.ErrNotFound) {
					continue
				}
				answer(w, mr, f, err)
				return
			}
			k.Error(w, r, apierror.New(http.StatusNotFound, noMember))
		})
		r.Get("/first/*", func(w http.ResponseWriter, r *http.Request) {
			if mr := member(w, r, k, pkgroute.RequestFrom(r).Repo.Repositories[0]); mr != nil {
				k.ServeItem(w, mr, path(r))
			}
		})
	}
	return p
}

// member is the request for the virtual repository's member key, or nil
// once the failure is answered.
func member(w http.ResponseWriter, r *http.Request, k *pkgroute.Kit, key string) *http.Request {
	c, err := k.DB().Repos().Get(r.Context(), key)
	if err == nil {
		var mr *http.Request
		if mr, err = k.MemberRequest(r, c); err == nil {
			return mr
		}
	}
	k.Error(w, r, err)
	return nil
}

// policyFixture is the kit fixture serving policyFormat, with "root" an
// administrator, and gen-virtual over gen-local and then gen-fed, which
// both hold a/b.txt.
func policyFixture(t *testing.T, sources map[repo.Class]pkgroute.Source) *kitFixture {
	t.Helper()
	if sources == nil {
		sources = map[repo.Class]pkgroute.Source{
			repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
			repo.Virtual: pkgroute.Local{},
		}
	}
	f := newKitFixture(t, sources)
	f.deploy(t, federated, "a/b.txt", "content of the federated a/b.txt")
	f.patch(t, "gen-virtual", func(c *repo.Config) { c.Repositories = []string{local, federated} })
	principalOf := func(r *http.Request) kernel.Principal {
		name := r.Header.Get("X-Test-User")
		return kernel.Principal{Name: name, Admin: name == "root"}
	}
	authorize := accesstest.Over(t, f.db).Middleware(t, true, principalOf)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		authorized := authorize(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-User") == "" {
				next.ServeHTTP(w, r)
				return
			}
			authorized.ServeHTTP(w, r)
		})
	})
	pkgroute.Mount(r, f.o, policyFormat(sources))
	f.h = r
	return f
}

func (f *kitFixture) patch(t *testing.T, key string, change func(*repo.Config)) {
	t.Helper()
	c, err := f.db.Repos().Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

// wantPolicy checks that GET path as user answers status with message in
// the protocol's envelope, and that nothing was served.
func (f *kitFixture) wantPolicy(t *testing.T, path, user string, status int, message string) {
	t.Helper()
	served := len(f.served)
	rec := f.do(http.MethodGet, base+path, user, "")
	if rec.Code != status {
		t.Fatalf("GET %s as %q: status %d, want %d: %s", path, user, rec.Code, status, rec.Body)
	}
	if message != "" {
		if got := npmError(t, rec); got != message {
			t.Fatalf("GET %s as %q: message %q, want %q", path, user, got, message)
		}
	}
	if len(f.served) != served {
		t.Fatalf("GET %s as %q served %v", path, user, f.served[served:])
	}
}

// wantServed checks that GET path as user served the file key/file.
func (f *kitFixture) wantServed(t *testing.T, path, user, file string) {
	t.Helper()
	rec := f.do(http.MethodGet, base+path, user, "")
	if rec.Code != http.StatusOK || len(f.served) == 0 || f.served[len(f.served)-1] != file {
		t.Fatalf("GET %s as %q: %d %s, served %v, want %s", path, user, rec.Code, rec.Body,
			f.served, file)
	}
}

func boolPtr(b bool) *bool       { return &b }
func strPtr(s string) *string    { return &s }
func blackOut(c *repo.Config)    { c.BlackedOut = boolPtr(true) }
func restore(c *repo.Config)     { c.BlackedOut, c.ExcludesPattern = boolPtr(false), strPtr("") }
func excludeA(c *repo.Config)    { c.ExcludesPattern = strPtr("a/**") }
func includeOnly(c *repo.Config) { c.IncludesPattern = strPtr("dir/**") }

// Every download helper refuses any path of a blacked-out repository, the
// item there or not, administrators included, after the caller's read
// permission (DV-057). The refusal is kernel.ErrNotFound to the protocol,
// keeps CheckDownload's message through Error and SourceError, and is no
// fault.
func TestAccDownloadPolicyBlackedOut(t *testing.T) {
	f := policyFixture(t, nil)
	f.patch(t, local, blackOut)
	for _, route := range []string{"files", "item", "resolve", "read", "found"} {
		for _, user := range []string{"reader", "root"} {
			f.wantPolicy(t, local+"/"+route+"/a/b.txt", user, http.StatusNotFound,
				fmt.Sprintf(blackedOut, "a/b.txt"))
		}
	}
	f.wantPolicy(t, local+"/files/a/missing.txt", "root", http.StatusNotFound,
		fmt.Sprintf(blackedOut, "a/missing.txt"))
	rec := f.do(http.MethodGet, base+local+"/item/a/b.txt", "reader", "")
	if rec.Header().Get("X-Not-Found") != "true" {
		t.Fatalf("the refusal is not kernel.ErrNotFound to the protocol: %d %s", rec.Code, rec.Body)
	}
	for _, route := range []string{"files", "found"} {
		f.wantPolicy(t, local+"/"+route+"/a/b.txt", "nobody", http.StatusForbidden, "")
		f.wantPolicy(t, local+"/"+route+"/a/b.txt", "", http.StatusUnauthorized, "")
	}
	if len(f.faults) != 0 {
		t.Fatalf("a refusal was recorded as a fault: %v", f.faults)
	}
	// A federated repository holds its own items, and is refused the same.
	f.wantServed(t, federated+"/files/a/b.txt", "reader", federated+"/a/b.txt")
	f.patch(t, local, restore)
	f.wantServed(t, local+"/files/a/b.txt", "root", local+"/a/b.txt")
}

// A path outside the include patterns or inside the exclude patterns is
// refused, whether it is stored or not; a path they accept is served.
func TestAccDownloadPolicyPatterns(t *testing.T) {
	f := policyFixture(t, nil)
	f.patch(t, local, excludeA)
	for _, route := range []string{"files", "item", "resolve", "read", "found"} {
		f.wantPolicy(t, local+"/"+route+"/a/b.txt", "root", http.StatusNotFound,
			fmt.Sprintf(excluded, "a/b.txt"))
		f.wantServed(t, local+"/"+route+"/dir/inner.txt", "reader", local+"/dir/inner.txt")
	}
	f.wantPolicy(t, local+"/files/a/missing.txt", "reader", http.StatusNotFound,
		fmt.Sprintf(excluded, "a/missing.txt"))
	f.patch(t, local, restore)
	f.patch(t, local, includeOnly)
	f.wantPolicy(t, local+"/files/work/old.txt", "reader", http.StatusNotFound,
		fmt.Sprintf(excluded, "work/old.txt"))
	f.wantServed(t, local+"/files/dir/inner.txt", "reader", local+"/dir/inner.txt")
}

// A virtual repository's search passes over a member that refuses as it
// passes over one without the file, and the virtual's answer then says
// nothing of how the member is set. Its own patterns rejecting the path
// leave it no member to search. Its blackedOut flag means nothing.
func TestAccDownloadPolicyVirtualMembers(t *testing.T) {
	f := policyFixture(t, nil)
	const virtual = "gen-virtual/"
	f.wantServed(t, virtual+"members/a/b.txt", "reader", local+"/a/b.txt")
	for _, refuse := range []func(*repo.Config){blackOut, excludeA} {
		f.patch(t, local, refuse)
		f.wantServed(t, virtual+"members/a/b.txt", "reader", federated+"/a/b.txt")
		f.wantPolicy(t, virtual+"first/a/b.txt", "reader", http.StatusNotFound,
			"Could not find resource")
		f.patch(t, federated, refuse)
		f.wantPolicy(t, virtual+"members/a/b.txt", "root", http.StatusNotFound, noMember)
		f.patch(t, local, restore)
		f.patch(t, federated, restore)
	}
	f.patch(t, "gen-virtual", excludeA)
	f.wantPolicy(t, virtual+"members/a/b.txt", "reader", http.StatusNotFound, noMember)
	f.wantPolicy(t, virtual+"first/a/b.txt", "reader", http.StatusNotFound,
		"No repository found to serve the request for gen-virtual:a/b.txt")
	f.wantServed(t, virtual+"members/dir/inner.txt", "reader", local+"/dir/inner.txt")
	f.patch(t, "gen-virtual", restore)
	f.patch(t, "gen-virtual", blackOut)
	f.wantServed(t, virtual+"members/a/b.txt", "reader", local+"/a/b.txt")
}

// CheckDownload answers what Item would, for a protocol that reads a
// stored blob itself: nothing for a path the policy accepts, the refusal
// for one it refuses, and through a virtual repository the pass-over of a
// refusing member as a 404 that is no fault. ServeFound of a member's file
// under the virtual repository's own request checks it as the member
// request would.
func TestAccDownloadPolicyCheckAndMemberFound(t *testing.T) {
	f := policyFixture(t, nil)
	const virtual = "gen-virtual/"
	wantChecked := func(path string) {
		t.Helper()
		if rec := f.do(http.MethodGet, base+path, "reader", ""); rec.Code != http.StatusNoContent {
			t.Fatalf("GET %s: %d %s, want 204", path, rec.Code, rec.Body)
		}
	}
	wantChecked(local + "/check/a/b.txt")
	wantChecked(virtual + "first-check/a/b.txt")
	f.wantServed(t, virtual+"first-found/a/b.txt", "reader", local+"/a/b.txt")
	for _, c := range []struct {
		refuse  func(*repo.Config)
		message string
	}{{blackOut, blackedOut}, {excludeA, excluded}} {
		f.patch(t, local, c.refuse)
		f.wantPolicy(t, local+"/check/a/b.txt", "reader", http.StatusNotFound,
			fmt.Sprintf(c.message, "a/b.txt"))
		f.wantPolicy(t, virtual+"first-check/a/b.txt", "reader", http.StatusNotFound,
			"Could not find resource")
		rec := f.do(http.MethodGet, base+virtual+"first-check/a/b.txt", "reader", "")
		if rec.Header().Get("X-Not-Found") != "true" {
			t.Fatalf("a refusing member is not kernel.ErrNotFound to the protocol: %s", rec.Body)
		}
		f.wantPolicy(t, virtual+"first-found/a/b.txt", "reader", http.StatusNotFound,
			"Could not find resource")
		f.patch(t, local, restore)
	}
	f.patch(t, "gen-virtual", excludeA)
	const none = "No repository found to serve the request for gen-virtual:a/b.txt"
	f.wantPolicy(t, virtual+"check/a/b.txt", "reader", http.StatusNotFound, none)
	f.wantPolicy(t, virtual+"first-check/a/b.txt", "reader", http.StatusNotFound, none)
	f.wantPolicy(t, virtual+"first-found/a/b.txt", "reader", http.StatusNotFound, none)
	if len(f.faults) != 0 {
		t.Fatalf("a refusal was recorded as a fault: %v", f.faults)
	}
}

// remoteSource reads a remote repository as a source boundary that applies
// no policy would, counting the reads, so a test can tell that the kit
// leaves the policy of remote reads to the boundary.
type remoteSource struct{ reads *int }

func (remoteSource) Item(
	context.Context,
	pkgroute.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

func (s remoteSource) Read(*http.Request, repo.Config, source.Resource) (source.Result, error) {
	*s.reads++
	return source.Result{Stream: &source.Stream{
		Status: http.StatusOK, Body: io.NopCloser(strings.NewReader("remote")),
		Representation: source.Representation{Size: int64(len("remote"))},
	}}, nil
}

// The source boundary applies the policy to a remote repository's reads,
// so the kit does not apply it a second time, nor a remote member's own.
// It applies the virtual repository's patterns to a remote member's file,
// which the boundary never sees.
func TestAccDownloadPolicyLeavesRemoteReads(t *testing.T) {
	reads := 0
	remote := remoteSource{reads: &reads}
	f := policyFixture(t, map[repo.Class]pkgroute.Source{
		repo.Local: pkgroute.Local{}, repo.Federated: pkgroute.Local{},
		repo.Virtual: pkgroute.Local{}, repo.Remote: remote,
	})
	f.patch(t, "gen-remote", blackOut)
	f.patch(t, "gen-remote", excludeA)
	rec := f.do(http.MethodGet, base+"gen-remote/files/a/b.txt", "root", "")
	if rec.Code != http.StatusOK || rec.Body.String() != "remote" || reads != 1 {
		t.Fatalf("remote read: %d %s after %d reads", rec.Code, rec.Body, reads)
	}
	// Item reaches the remote source, which has no single-item lookup.
	f.wantPolicy(t, "gen-remote/item/a/b.txt", "root", http.StatusInternalServerError, "")
	if len(f.faults) != 1 || !errors.Is(f.faults[0], source.ErrUnsupported) {
		t.Fatalf("remote Item: faults %v", f.faults)
	}
	f.patch(t, "gen-virtual", func(c *repo.Config) { c.Repositories = []string{"gen-remote"} })
	rec = f.do(http.MethodGet, base+"gen-virtual/first/a/b.txt", "root", "")
	if rec.Code != http.StatusOK || reads != 2 {
		t.Fatalf("remote member read: %d %s after %d reads", rec.Code, rec.Body, reads)
	}
	f.patch(t, "gen-virtual", excludeA)
	f.wantPolicy(t, "gen-virtual/first/a/b.txt", "root", http.StatusNotFound,
		"No repository found to serve the request for gen-virtual:a/b.txt")
	f.wantPolicy(t, "gen-virtual/first-check/a/b.txt", "root", http.StatusNotFound,
		"No repository found to serve the request for gen-virtual:a/b.txt")
	wantChecked := f.do(http.MethodGet, base+"gen-virtual/first-check/dir/inner.txt", "root", "")
	if wantChecked.Code != http.StatusNoContent || reads != 2 {
		t.Fatalf("remote member check: %d %s after %d reads", wantChecked.Code, wantChecked.Body,
			reads)
	}
}
