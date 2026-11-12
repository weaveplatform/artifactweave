package luarockshttp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	luafmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/luarocks"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// manifestNames are the files the manifest work writes, which the client
// reads.
var manifestNames = []string{
	"manifest", "manifest-5.1", "manifest-5.2", "manifest-5.3", "manifest-5.4", "manifest-5.5",
}

// countedWork is the fixture's work queue, counting the manifest work the
// item service queues.
type countedWork struct {
	*work.Queue
	manifests atomic.Int32
}

func (c *countedWork) Enqueue(ctx context.Context, kind, key string, payload []byte) error {
	if kind == luafmt.KindManifest {
		c.manifests.Add(1)
	}
	return c.Queue.Enqueue(ctx, kind, key, payload)
}

// watched is a local or federated fixture whose manifest work is counted
// and whose faults, the errors the router kit logs as unexpected, are kept.
type watched struct {
	*fixture
	work   *countedWork
	faults []error
}

func newWatched(t *testing.T, class repo.Class) *watched {
	t.Helper()
	w := &watched{}
	w.fixture = newFixture(t, class, func(o *pkgroute.Options, items *item.Service) {
		w.work = &countedWork{Queue: items.Work.(*work.Queue)}
		items.Work = w.work
		o.Fault = func(_ *http.Request, err error) { w.faults = append(w.faults, err) }
	})
	return w
}

// deployRock stores name's rockspec and source rock at version through the
// item service, as the item routes deploy them.
func deployRock(t *testing.T, f *fixture, name, version string) {
	t.Helper()
	for path, body := range map[string][]byte{
		name + "-" + version + ".rockspec": spec(name, version),
		name + "-" + version + ".src.rock": rock(t, name, version),
	} {
		if _, err := f.items.Deploy(t.Context(), item.Deploy{
			Target: item.Target{Repo: f.c, Path: path}, Body: bytes.NewReader(body), By: "admin",
		}); err != nil {
			t.Fatalf("deploy %s: %v", path, err)
		}
	}
}

// The client asks for manifest-5.4.zip, and for manifest-5.4.json first
// under LuaJIT, before manifest-5.4, and moves on to the next name when one
// fails (luarocks 3.13.0 manif.lua). The manifest work writes neither, so
// a local or federated repository holding a rock answers each, GET and
// HEAD alike, 404 "Could not find resource", as a file not stored answers:
// no manifest work is queued for it and no fault is logged. Any other
// manifest-5. name the work does not write answers so too.
func TestAccLocalManifestNamesTheWorkDoesNotWriteAnswer404(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newWatched(t, class)
			const base = "/artifactory/api/luarocks/rocks/"
			deployRock(t, f.fixture, "hello", "1.0-1")
			wantBody(t, f.do(http.MethodGet, base+"manifest-5.4", nil, "", "admin"),
				[]string{`["hello"]`, `arch = "src"`}, nil)
			queued := f.work.manifests.Load()
			for _, name := range []string{
				"manifest-5.4.zip", "manifest-5.4.json", "manifest-5.1.zip", "manifest-5.9",
			} {
				wantError(t, f.do(http.MethodGet, base+name, nil, "", "admin"),
					http.StatusNotFound, "Could not find resource")
				head := f.do(http.MethodHead, base+name, nil, "", "admin")
				if head.Code != http.StatusNotFound {
					t.Fatalf("HEAD %s: %d %s, want 404", name, head.Code, head.Body)
				}
			}
			if got := f.work.manifests.Load() - queued; got != 0 {
				t.Fatalf("the requests queued manifest work %d times", got)
			}
			if len(f.faults) != 0 {
				t.Fatalf("faults logged: %v", f.faults)
			}
		})
	}
}

// A manifest-5.4.zip deployed through the item routes is a stored file: GET
// serves its bytes and HEAD its status, as before.
func TestAccDeployedManifestArchiveIsServed(t *testing.T) {
	f := newWatched(t, repo.Local)
	const base = "/artifactory/api/luarocks/rocks/"
	body := []byte("PK\x05\x06" + string(make([]byte, 18))) // an empty ZIP
	if _, err := f.items.Deploy(t.Context(), item.Deploy{
		Target: item.Target{Repo: f.c, Path: "manifest-5.4.zip"},
		Body:   bytes.NewReader(body), By: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	get := f.do(http.MethodGet, base+"manifest-5.4.zip", nil, "", "admin")
	if get.Code != http.StatusOK || !bytes.Equal(get.Body.Bytes(), body) {
		t.Fatalf("GET: %d %q", get.Code, get.Body)
	}
	head := f.do(http.MethodHead, base+"manifest-5.4.zip", nil, "", "admin")
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", head.Code, head.Body)
	}
	if len(f.faults) != 0 {
		t.Fatalf("faults logged: %v", f.faults)
	}
}

// The names the manifest work writes answer as before: a local or
// federated repository builds them, a remote serves its upstream's and a
// virtual repository merges its members'. A remote or virtual repository
// already answered the names the work does not write 404.
func TestAccManifestNamesTheWorkWritesAnswerAsBefore(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newWatched(t, class)
			deployRock(t, f.fixture, "hello", "1.0-1")
			for _, name := range manifestNames {
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					rec := f.do(method, "/artifactory/api/luarocks/rocks/"+name, nil, "", "admin")
					if method == http.MethodHead {
						if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
							t.Fatalf("HEAD %s: %d %q", name, rec.Code, rec.Body)
						}
						continue
					}
					wantBody(t, rec, []string{`["hello"]`, `arch = "src"`}, nil)
				}
			}
			if len(f.faults) != 0 {
				t.Fatalf("faults logged: %v", f.faults)
			}
		})
	}
	t.Run("remote and virtual", func(t *testing.T) {
		f := newVirtualFixture(t)
		listing := []byte("commands = {}\nmodules = {}\n" +
			`repository = { ["visible"] = { ["1.0-1"] = { { arch = "rockspec" } } } }` + "\n")
		for _, name := range manifestNames {
			f.up.Set("/rocks/"+name, fakeupstream.Resource{Body: listing})
		}
		uploadSpecTo(t, f.fixture, "local", "local-only", "2.0-1", spec("local-only", "2.0-1"))
		for _, name := range manifestNames {
			wantBody(t, f.do(http.MethodGet, remoteBase+name, nil, "", "admin"),
				[]string{`["visible"]`}, []string{"local-only"})
			wantBody(t, f.do(http.MethodGet, virtualBase+name, nil, "", "admin"),
				[]string{`["visible"]`, `["local-only"]`}, nil)
		}
		for _, base := range []string{remoteBase, virtualBase} {
			for _, name := range []string{"manifest-5.4.zip", "manifest-5.4.json"} {
				wantError(t, f.do(http.MethodGet, base+name, nil, "", "admin"),
					http.StatusNotFound, "Could not find resource")
			}
		}
	})
}

// The names the manifest work does not write keep the manifest branch's
// checks, which come before the lookup: a caller who may not read the
// repository is refused 403, or 401 with a challenge when anonymous; then
// the download policy refuses a blacked-out repository, and patterns that
// reject the name, at the name asked for.
func TestAccManifestNamesTheWorkDoesNotWriteKeepTheirRefusals(t *testing.T) {
	f := newWatched(t, repo.Local)
	const base = "/artifactory/api/luarocks/rocks/"
	get := func(path, user string) *httptest.ResponseRecorder {
		return f.do(http.MethodGet, base+path, nil, "", user)
	}
	deployRock(t, f.fixture, "hello", "1.0-1")
	for _, name := range []string{"manifest-5.4.zip", "manifest-5.4.json"} {
		wantError(t, get(name, "nobody"), http.StatusForbidden, "Forbidden")
		rec := get(name, kernel.AnonymousUser)
		wantError(t, rec, http.StatusUnauthorized, "Authentication is required")
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("anonymous %s: no challenge", name)
		}
	}
	patchRepo(t, f.fixture, "rocks", blackOut)
	for _, name := range []string{"manifest-5.4.zip", "manifest-5.4.json"} {
		wantError(t, get(name, "admin"), http.StatusNotFound,
			fmt.Sprintf(blackedOut, "rocks", name))
	}
	patchRepo(t, f.fixture, "rocks", restorePolicy)
	patchRepo(t, f.fixture, "rocks", includes("**/*.rock,**/*.rockspec"))
	for _, name := range []string{"manifest-5.4.zip", "manifest-5.4.json"} {
		wantError(t, get(name, "admin"), http.StatusNotFound, fmt.Sprintf(rejected, "rocks", name))
	}
	if len(f.faults) != 0 {
		t.Fatalf("faults logged: %v", f.faults)
	}
}

// itemFaultStore is the router kit's store, whose every lookup of the item
// at path fails, as a store whose connection drops does.
type itemFaultStore struct {
	pkgroute.Store
	path string
}

func (s itemFaultStore) Items() port.ItemStore { return itemFaults{s.Store.Items(), s.path} }

type itemFaults struct {
	port.ItemStore
	path string
}

func (s itemFaults) Get(
	ctx context.Context, key string, cached bool, fullPath string,
) (model.Item, error) {
	if fullPath == s.path {
		return model.Item{}, errDropped
	}
	return s.ItemStore.Get(ctx, key, cached, fullPath)
}

// A store that fails the lookup of a manifest file is a server fault,
// whether the work writes the name or not: the read answers 500 "internal
// error" and logs the fault, as before.
func TestAccManifestLookupStoreFaultAnswers500(t *testing.T) {
	for _, name := range []string{"manifest-5.4", "manifest-5.4.zip"} {
		var faults []error
		f := newFixture(t, repo.Local, func(o *pkgroute.Options, _ *item.Service) {
			o.Store = itemFaultStore{Store: o.Store, path: name}
			o.Fault = func(_ *http.Request, err error) { faults = append(faults, err) }
		})
		deployRock(t, f, "hello", "1.0-1")
		rec := f.do(http.MethodGet, "/artifactory/api/luarocks/rocks/"+name, nil, "", "admin")
		wantError(t, rec, http.StatusInternalServerError, "internal error")
		if len(faults) != 1 || !errors.Is(faults[0], errDropped) {
			t.Fatalf("%s: faults %v", name, faults)
		}
	}
}
