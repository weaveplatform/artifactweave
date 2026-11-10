package debianhttp

// This file holds the fixtures of the tests that serve item GETs through
// the item kit with TryDebianItem as its one overlay. fixture_test.go's
// strict handler serves only the Debian operations, so these tests build
// the kit's item route beside it, as the router mounts both, and seed
// Debian repositories, their published metadata and the remote sources
// that stand in for an upstream.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The stable distribution's paths the overlay tests publish and read, and
// the packages its indexes list.
const (
	debRelease  = "dists/stable/Release"
	debPackages = "dists/stable/main/binary-amd64/Packages"
	debSources  = "dists/stable/main/source/Sources"
	debAlpha    = "pool/alpha_1.0_amd64.deb"
	debBeta     = "pool/beta_1.0_amd64.deb"
)

// debBody stands in for a package's bytes: the overlay serves a stored or
// relayed package without reading it.
var debBody = []byte("deb bytes")

// overlayConfig is the part of the item kit's dependencies an overlay test
// changes: a faulty store or blob store, or a remote source service.
type overlayConfig struct {
	Store         serverStore
	Blobs         blob.Store
	RemoteSources *source.Service
}

// overlayWorld is an item world with the item kit that serves its item
// GETs, TryDebianItem its one overlay, as the router's item route does.
type overlayWorld struct {
	*itemWorld
	kit *itemkit.Kit
}

// newOverlayWorld returns an item world and its kit, built over the
// world's stores after tune adjusts them, with the access module bound
// over the store the kit reads.
func newOverlayWorld(t *testing.T, tune ...func(*overlayConfig)) *overlayWorld {
	t.Helper()
	w := newItemWorld(t)
	c := overlayConfig{Store: w.db, Blobs: w.blobs}
	for _, f := range tune {
		f(&c)
	}
	return &overlayWorld{itemWorld: w, kit: itemkit.New(itemkit.Deps{
		Store:         c.Store,
		Blobs:         c.Blobs,
		RemoteSources: c.RemoteSources,
		Access:        withAccess(t, Config{Store: c.Store}).Access,
		Overlays:      []itemkit.Overlay{TryDebianItem},
	})}
}

// withSources gives the kit a remote source service whose HTTP transport
// is read, resolving every remote to one HTTP source.
func withSources(read sourceBackend) func(*overlayConfig) {
	return func(c *overlayConfig) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: read}
	}
}

// withStore makes the kit read through the store wrap returns.
func withStore(wrap func(serverStore) serverStore) func(*overlayConfig) {
	return func(c *overlayConfig) { c.Store = wrap(c.Store) }
}

// request is a GET of path in the repository key as the named caller:
// admin is kittest's administrator, whom every question allows, and any
// other name a user whom none allows.
func (w *overlayWorld) request(key, path, as string) *http.Request {
	w.t.Helper()
	p, d := kittest.Admin, kittest.AdminAccess().Decider
	if as != kittest.Admin.Name {
		p, d = kernel.Principal{Name: as}, &accessapitest.Decider{}
	}
	r := httptest.NewRequestWithContext(w.t.Context(), http.MethodGet,
		"/artifactory/"+key+"/"+path, nil)
	return r.WithContext(accessapi.WithCaller(r.Context(), p, d))
}

// get serves a GET of path in the repository key through the kit's item
// route as the named caller.
func (w *overlayWorld) get(key, path, as string) *httptest.ResponseRecorder {
	w.t.Helper()
	rec := httptest.NewRecorder()
	w.kit.GetItem(rec, w.request(key, path, as))
	return rec
}

// debRepos stores each of cs as a Debian repository with its defaults.
func (w *itemWorld) debRepos(cs ...repo.Config) {
	w.t.Helper()
	for _, c := range cs {
		c.PackageType = repo.Debian
		if c.Class == repo.Remote {
			c.URL = "https://deb.example"
		}
		if err := w.db.Repos().Create(w.t.Context(), *repo.Defaults(&c)); err != nil {
			w.t.Fatal(err)
		}
	}
}

// debRepo returns the repository key as stored.
func (w *itemWorld) debRepo(key string) repo.Config {
	w.t.Helper()
	c, err := w.db.Repos().Get(w.t.Context(), key)
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

// put stores body as the file at path in the repository key, blob first,
// as a deploy leaves it; cached puts it in a remote's cache.
func (w *itemWorld) put(key, path string, cached bool, body []byte) types.Item {
	w.t.Helper()
	ctx := w.t.Context()
	upload, err := w.blobs.BeginUpload(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := upload.Append(ctx, bytes.NewReader(body)); err != nil {
		w.t.Fatal(err)
	}
	info, err := upload.Commit(ctx, blob.Digests{})
	if err != nil {
		w.t.Fatal(err)
	}
	it, err := w.db.Items().Put(ctx, types.Item{
		RepoKey: key, Cached: cached, Name: path, Size: info.Size, CreatedBy: "admin",
		Actual: types.Checksums{SHA256: info.Digests.SHA256},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return it
}

// folderAt stores a folder at path in the repository key.
func (w *itemWorld) folderAt(key, path string) {
	w.t.Helper()
	if _, err := w.db.Items().Put(w.t.Context(), types.Item{
		RepoKey: key, Name: path, Folder: true, CreatedBy: "admin",
	}); err != nil {
		w.t.Fatal(err)
	}
}

// lostAt stores a file at path in the repository key whose content no
// blob store in these tests holds.
func (w *itemWorld) lostAt(key, path string) {
	w.t.Helper()
	if _, err := w.db.Items().Put(w.t.Context(), types.Item{
		RepoKey: key, Name: path, Size: 1, CreatedBy: "admin",
		Actual: types.Checksums{SHA256: strings.Repeat("0", 64)},
	}); err != nil {
		w.t.Fatal(err)
	}
}

// packagesIndex is a Packages index with one amd64 stanza for each row,
// a "name version filename" triple.
func packagesIndex(rows ...string) []byte {
	var out bytes.Buffer
	for _, row := range rows {
		f := strings.Fields(row)
		fmt.Fprintf(&out, "Package: %s\nVersion: %s\nArchitecture: amd64\nFilename: %s\n"+
			"Size: %d\nSHA256: %s\n\n", f[0], f[1], f[2], len(debBody), strings.Repeat("a", 64))
	}
	return out.Bytes()
}

// releaseFor is the stable distribution's Release, declaring index as its
// one main/binary-amd64 Packages file.
func releaseFor(index []byte) []byte {
	return fmt.Appendf(nil,
		"Suite: stable\nArchitectures: amd64\nComponents: main\nSHA256:\n %x %d %s\n",
		sha256.Sum256(index), len(index), strings.TrimPrefix(debPackages, "dists/stable/"))
}

// publish stores the stable distribution in the repository key: its
// Release and the Packages index it declares.
func (w *itemWorld) publish(key string, index []byte) {
	w.t.Helper()
	w.put(key, debRelease, false, releaseFor(index))
	w.put(key, debPackages, false, index)
}

// filenames returns the Filename of each row of a served Packages index,
// in order, failing the test when it does not decode.
func filenames(t *testing.T, body []byte) []string {
	t.Helper()
	rows, err := debian.DecodePackagesIndex(body)
	if err != nil {
		t.Fatalf("served Packages index does not decode: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Filename)
	}
	return out
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
// identity.
func sourceIdentity(context.Context, repo.Config) (types.SourceIdentity, error) {
	return types.SourceIdentity{
		Kind:       types.ExternalHTTP,
		Repository: "repo-1",
		ID:         "http-1",
		Generation: "revision-1",
	}, nil
}

// upstream is a remote source transport that answers each path it holds
// with a fresh result from at, and any other as an upstream 404.
func upstream(at map[string]func() (source.Result, error)) sourceBackend {
	return func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
		if answer, ok := at[q.Resource.Path]; ok {
			return answer()
		}
		return source.Result{}, store.ErrNotFound
	}
}

// streamed answers with a stream of body, rechecked by a fresh check from
// check when it is not nil.
func streamed(body []byte, check func() func(context.Context) error) func() (source.Result, error) {
	return func() (source.Result, error) {
		r := source.Result{Stream: &source.Stream{
			Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
			Representation: source.Representation{Size: int64(len(body))},
		}}
		if check != nil {
			r.Revalidate = check()
		}
		return r, nil
	}
}

// failing answers with err, as an upstream that cannot be read does.
func failing(err error) func() (source.Result, error) {
	return func() (source.Result, error) { return source.Result{}, err }
}

// failsOnCall is a backend revalidation that fails with err on its nth
// call and passes every other. The source service makes the first call
// itself before it returns the result, so each check after it is one more.
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

// faulty is a store whose lookup of the repository repo, and whose item
// reads in the repository items, answer errFault; every other call passes
// to the store it embeds.
type faulty struct {
	serverStore
	repo, items string
}

func (s faulty) Repos() store.RepoStore {
	return faultyRepos{RepoStore: s.serverStore.Repos(), fail: s.repo}
}

func (s faulty) Items() store.ItemStore {
	return faultyItems{ItemStore: s.serverStore.Items(), fail: s.items}
}

type faultyRepos struct {
	store.RepoStore
	fail string
}

func (r faultyRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if key == r.fail {
		return repo.Config{}, errFault
	}
	return r.RepoStore.Get(ctx, key)
}

type faultyItems struct {
	store.ItemStore
	fail string
}

func (i faultyItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	path string,
) (types.Item, error) {
	if key == i.fail {
		return types.Item{}, errFault
	}
	return i.ItemStore.Get(ctx, key, cached, path)
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
