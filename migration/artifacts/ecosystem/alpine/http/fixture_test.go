package alpinehttp

// This file holds the fixtures of the tests that call the overlay and its
// helpers without the router, modelled on rpmhttp's. A test here cannot
// build the router, which imports this package, so the item world is a
// kittest world, and do serves a request through the kit's item GET with
// TryAlpineItem as its overlay, as the router's item route does once it has
// authenticated the caller. The access module is accesstest's, over the
// world's store.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/alpine"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The x86_64 index of branch v3.20's main component, and the package of
// "hello" it lists.
const (
	apkIndexPath = "v3.20/main/x86_64/APKINDEX.tar.gz"
	apkHelloPath = "v3.20/main/x86_64/hello-1.0-r0.apk"
)

// Config is the part of the router's configuration that the tests here
// set, which newKit passes on to the item kit as the router does.
type Config struct {
	Store         store.Store
	Blobs         blob.Store
	RemoteSources *source.Service
	Access        accessapi.API
}

// newKit returns the item kit over c, with TryAlpineItem as its one
// overlay: of the router's overlays, only it serves an Alpine repository's
// indexes and a virtual repository's packages.
func newKit(c Config) *itemkit.Kit {
	return itemkit.New(itemkit.Deps{
		Store:         c.Store,
		Blobs:         c.Blobs,
		RemoteSources: c.RemoteSources,
		Access:        c.Access,
		Overlays:      []itemkit.Overlay{TryAlpineItem},
	})
}

// withAccess returns c with the access module over its store, as the
// router binds it for a configuration that sets none.
func withAccess(t *testing.T, c Config) Config {
	t.Helper()
	c.Access = accesstest.Over(t, c.Store).API()
	return c
}

// itemWorld is a kittest world, and the kit that serves its item requests
// as the router would.
type itemWorld struct {
	t     *testing.T
	world *kittest.World
	db    store.Store
	blobs blob.Store
	kit   *itemkit.Kit
}

// newItemWorld returns a kittest world, with the kit that do serves
// through built from it as the router builds its own: tune adjusts the
// configuration first, and the access module is bound over the store.
func newItemWorld(t *testing.T, tune ...func(*Config)) *itemWorld {
	t.Helper()
	k := kittest.New(t)
	c := Config{Store: k.Store, Blobs: k.Blobs}
	for _, f := range tune {
		f(&c)
	}
	return &itemWorld{t: t, world: k, db: k.Store, blobs: k.Blobs, kit: newKit(withAccess(t, c))}
}

// withSource gives the kit a remote source service whose HTTP transport is
// read, resolving every remote to one HTTP source.
func withSource(read sourceBackend) func(*Config) {
	return func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: read}
	}
}

// do serves method on path through the kit's item GET as the named caller:
// admin is kittest's administrator, whom every question allows, and any
// other name a user whom none allows, as the router's world seeds user.
func (w *itemWorld) do(method, path, as string) *httptest.ResponseRecorder {
	w.t.Helper()
	p, d := kittest.Admin, kittest.AdminAccess().Decider
	if as != kittest.Admin.Name {
		p, d = kernel.Principal{Name: as}, &accessapitest.Decider{}
	}
	r := httptest.NewRequestWithContext(w.t.Context(), method, path, nil)
	r = r.WithContext(accessapi.WithCaller(r.Context(), p, d))
	rec := httptest.NewRecorder()
	w.kit.GetItem(rec, r)
	return rec
}

// repos stores each of cs with its defaults and returns them as stored.
func (w *itemWorld) repos(cs ...repo.Config) []repo.Config {
	w.t.Helper()
	out := make([]repo.Config, 0, len(cs))
	for _, c := range cs {
		c = *repo.Defaults(&c)
		if err := w.db.Repos().Create(w.t.Context(), c); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// put stores body as the file at path in the repository key, blob and item.
func (w *itemWorld) put(key, path string, body []byte) types.Item {
	w.t.Helper()
	return w.world.Put(w.t, repo.Config{Key: key}, false, path, body)
}

// folder stores a folder at path in the repository key.
func (w *itemWorld) folder(key, path string) {
	w.t.Helper()
	if _, err := w.db.Items().Put(w.t.Context(), types.Item{
		RepoKey: key, Name: path, Folder: true, CreatedBy: kittest.Admin.Name,
	}); err != nil {
		w.t.Fatal(err)
	}
}

// lost stores a file at path in the repository key whose content the blob
// store does not hold.
func (w *itemWorld) lost(key, path string) {
	w.t.Helper()
	if _, err := w.db.Items().Put(w.t.Context(), types.Item{
		RepoKey: key, Name: path, Size: 1, CreatedBy: kittest.Admin.Name,
		Actual: types.Checksums{SHA256: absentSHA256},
	}); err != nil {
		w.t.Fatal(err)
	}
}

// absentSHA256 is a well-formed digest that no blob store in these tests
// holds.
const absentSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"

// apkIndex is an unsigned x86_64 APKINDEX.tar.gz listing version 1.0-r0 of
// each named package, as alpine.EncodeIndex writes one.
func apkIndex(t *testing.T, names ...string) []byte {
	t.Helper()
	packages := make([]alpine.Package, 0, len(names))
	for _, name := range names {
		packages = append(packages, alpine.Package{
			Name: name, Version: "1.0-r0", Architecture: "x86_64",
			ArchiveSize: 4, InstalledSize: 4,
			ControlSHA1: base64.StdEncoding.EncodeToString(make([]byte, 20)),
		})
	}
	body, err := alpine.EncodeIndex(packages, "test", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// indexNames returns the names of the packages an index served at
// apkIndexPath lists, failing the test when it does not decode.
func indexNames(t *testing.T, body []byte) []string {
	t.Helper()
	packages, err := alpine.DecodeIndex(t.Context(), body, apkIndexPath)
	if err != nil {
		t.Fatalf("served index does not decode: %v", err)
	}
	names := make([]string, 0, len(packages))
	for _, p := range packages {
		names = append(names, p.Name)
	}
	return names
}

// wantError fails the test unless rec answered status with the Artifactory
// error envelope that carries message, as the item route writes an error.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 || body.Errors[0].Status != status ||
		body.Errors[0].Message != message {
		t.Errorf("answered %d %s, want %d %q", rec.Code, rec.Body, status, message)
	}
}

// sourceBackend is a remote source transport made of one function.
type sourceBackend func(context.Context, source.Request, source.Permit) (source.Result, error)

func (f sourceBackend) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return f(ctx, q, p)
}

// sourceIdentity resolves every remote repository to one fixed HTTP source
// identity, as rpmhttp's fixture of the same name does.
func sourceIdentity(context.Context, repo.Config) (types.SourceIdentity, error) {
	return types.SourceIdentity{
		Kind:       types.ExternalHTTP,
		Repository: "repo-1",
		ID:         "http-1",
		Generation: "revision-1",
	}, nil
}

// stream is a complete 200 response of body from the upstream.
func stream(body []byte) *source.Stream {
	return &source.Stream{
		Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
		Representation: source.Representation{Size: int64(len(body))},
	}
}

// failsOnCall is a backend revalidation that fails with err on its nth
// call and passes every other. The source service makes the first call
// itself before it returns the result, so n counts from there: the
// overlay's own recheck of an index is the second, and each package read
// check after it one more.
func failsOnCall(n int, err error) func(context.Context) error {
	calls := 0
	return func(context.Context) error {
		calls++
		if calls == n {
			return err
		}
		return nil
	}
}

// errFault is the failure the faulty stores here answer.
var errFault = errors.New("the store failed")

// faultyRepos is a store whose repository lookup of the key fail answers
// errFault; every other call passes to Store.
type faultyRepos struct {
	store.Store
	fail string
}

func (s faultyRepos) Repos() store.RepoStore {
	return failingRepoGet{RepoStore: s.Store.Repos(), fail: s.fail}
}

type failingRepoGet struct {
	store.RepoStore
	fail string
}

func (r failingRepoGet) Get(ctx context.Context, key string) (repo.Config, error) {
	if key == r.fail {
		return repo.Config{}, errFault
	}
	return r.RepoStore.Get(ctx, key)
}

// brokenBlobs is a blob store whose opened content fails every read, as a
// disk that fails part way through does.
type brokenBlobs struct{ blob.Store }

func (b brokenBlobs) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	rc, info, err := b.Store.Open(ctx, sha)
	if err != nil {
		return nil, info, err
	}
	_ = rc.Close()
	return brokenContent{}, info, nil
}

type brokenContent struct{}

func (brokenContent) Read([]byte) (int, error)       { return 0, errFault }
func (brokenContent) Seek(int64, int) (int64, error) { return 0, errFault }
func (brokenContent) Close() error                   { return nil }
