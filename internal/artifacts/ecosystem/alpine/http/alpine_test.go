package alpinehttp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"testing/iotest"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/alpine"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// The keys of the Alpine repositories the tests here store, beside
// kittest's generic ones.
const (
	alpineLocal   = "alpine-local"
	alpineRemote  = "alpine-remote"
	alpineVirtual = "alpine"
)

// apkBody stands in for a package's bytes: the overlay serves a stored or
// relayed package without reading it.
var apkBody = []byte("apk bytes")

// alpineRepo is an Alpine repository of class under key, a virtual one
// over members in order.
func alpineRepo(key string, class repo.Class, members ...string) repo.Config {
	c := repo.Config{Key: key, Class: class, PackageType: repo.Alpine, Repositories: members}
	if class == repo.Remote {
		c.URL = "https://alpine.example"
	}
	return c
}

// get serves GET on path in the repository key as the administrator, so
// that only the repository's content and configuration decide the answer.
func (w *itemWorld) get(key, path string) *httptest.ResponseRecorder {
	w.t.Helper()
	return w.do(http.MethodGet, "/artifactory/"+key+"/"+path, kittest.Admin.Name)
}

// upstream is a remote source transport that answers each path it holds
// with a fresh result from at, and any other as an upstream 404.
func upstream(at map[string]func() (source.Result, error)) sourceBackend {
	return func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
		if answer, ok := at[q.Resource.Path]; ok {
			return answer()
		}
		return source.Result{}, kernel.ErrNotFound
	}
}

// streamed answers with a stream of body, rechecked by check when it is
// not nil.
func streamed(body []byte, check func() func(context.Context) error) func() (source.Result, error) {
	return func() (source.Result, error) {
		r := source.Result{Stream: stream(body)}
		if check != nil {
			r.Revalidate = check()
		}
		return r, nil
	}
}

// rsaKeyPEM is a fresh RSA private key in the PKCS #1 PEM form a stored
// keypair holds. 1024 bits keeps the test quick: the index it is named for
// is refused before anything is signed.
func rsaKeyPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
}

// TestAccAlpineRefusesPathsOutsideTheLayout: an index or package path
// outside apk's branch/component/architecture layout, or one that carries
// a query or matrix parameters, is not found, as Artifactory answers a
// path its Alpine layout does not name.
func TestAccAlpineRefusesPathsOutsideTheLayout(t *testing.T) {
	w := newItemWorld(t)
	w.repos(
		alpineRepo(alpineLocal, repo.Local),
		alpineRepo(alpineVirtual, repo.Virtual, alpineLocal),
	)
	w.put(alpineLocal, apkIndexPath, apkIndex(t, "hello"))
	w.put(alpineLocal, apkHelloPath, apkBody)
	for _, target := range []string{
		"/artifactory/alpine-local/hello-1.0-r0.apk",
		"/artifactory/alpine-local/v3.20/main/APKINDEX.tar.gz",
		"/artifactory/alpine-local/" + apkIndexPath + "?x=1",
		"/artifactory/alpine-local/" + apkIndexPath + ";x=1",
		"/artifactory/alpine/" + apkHelloPath + "?x=1",
	} {
		t.Run(target, func(t *testing.T) {
			rec := w.do(http.MethodGet, target, kittest.Admin.Name)
			wantError(t, rec, http.StatusNotFound, "Could not find resource")
		})
	}
}

// TestAccAlpineRefusesCallersWhoMayNotDownload: an index, and a virtual
// package, is refused to a caller who may not read it before anything is
// read, the same 403 whatever the repository holds or its settings say
// (DV-057). Past that, a blacked-out local or remote repository's index,
// through its -cache alias too, and one whose patterns reject the index's
// own path answer the download policy's 404 and message, and a virtual
// repository whose own patterns reject the index or the package answers
// that no repository serves it.
func TestAccAlpineRefusesCallersWhoMayNotDownload(t *testing.T) {
	w := newItemWorld(t)
	dark := alpineRepo("alpine-dark", repo.Local)
	dark.BlackedOut = new(true)
	darkRemote := alpineRepo("alpine-dark-remote", repo.Remote)
	darkRemote.BlackedOut = new(true)
	hidden := alpineRepo("alpine-hidden", repo.Local)
	hidden.ExcludesPattern = new("**/" + alpine.IndexName)
	closed := alpineRepo("alpine-closed", repo.Virtual, alpineLocal)
	closed.ExcludesPattern = new("v3.20/**")
	w.repos(
		alpineRepo(alpineLocal, repo.Local), dark, darkRemote, hidden, closed,
		alpineRepo(alpineVirtual, repo.Virtual, alpineLocal),
	)
	for _, key := range []string{alpineLocal, "alpine-dark", "alpine-hidden"} {
		w.put(key, apkIndexPath, apkIndex(t, "hello"))
	}
	w.put(alpineLocal, apkHelloPath, apkBody)
	blackedOut := func(key string) string {
		return "The repository '" + key + "' is blacked out and cannot serve artifact '" +
			key + ":" + apkIndexPath + "'."
	}
	for _, tc := range []struct {
		name, key, path, as string
		status              int
		message             string
	}{
		{
			"user reads an index", alpineLocal, apkIndexPath, "user",
			http.StatusForbidden, "Forbidden",
		},
		{
			"user reads a blacked-out index", "alpine-dark", apkIndexPath, "user",
			http.StatusForbidden, "Forbidden",
		},
		{
			"user reads a virtual package", alpineVirtual, apkHelloPath, "user",
			http.StatusForbidden, "Forbidden",
		},
		{
			"user reads a package the virtual's patterns reject", "alpine-closed", apkHelloPath,
			"user", http.StatusForbidden, "Forbidden",
		},
		{
			"blacked-out index", "alpine-dark", apkIndexPath, kittest.Admin.Name,
			http.StatusNotFound, blackedOut("alpine-dark"),
		},
		{
			"blacked-out remote index", "alpine-dark-remote", apkIndexPath, kittest.Admin.Name,
			http.StatusNotFound, blackedOut("alpine-dark-remote"),
		},
		{
			"blacked-out remote's cache", "alpine-dark-remote-cache", apkIndexPath,
			kittest.Admin.Name, http.StatusNotFound, blackedOut("alpine-dark-remote"),
		},
		{
			"index the patterns reject", "alpine-hidden", apkIndexPath, kittest.Admin.Name,
			http.StatusNotFound, "The repository 'alpine-hidden' rejected the artifact " +
				"'alpine-hidden:" + apkIndexPath + "' due to its include/exclude pattern settings.",
		},
		{
			"virtual index its patterns reject", "alpine-closed", apkIndexPath, kittest.Admin.Name,
			http.StatusNotFound,
			"No repository found to serve the request for alpine-closed:" + apkIndexPath,
		},
		{
			"virtual package its patterns reject", "alpine-closed", apkHelloPath,
			kittest.Admin.Name, http.StatusNotFound,
			"No repository found to serve the request for alpine-closed:" + apkHelloPath,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := w.do(http.MethodGet, "/artifactory/"+tc.key+"/"+tc.path, tc.as)
			wantError(t, rec, tc.status, tc.message)
		})
	}
}

// TestAccAlpineIndexNeedsReadPermissionOnEveryListedPackage: a caller who
// may read an index but not a package it lists is refused a local index,
// and a virtual index leaves that package out, the permission rule the
// generic item route cannot apply.
func TestAccAlpineIndexNeedsReadPermissionOnEveryListedPackage(t *testing.T) {
	w := newItemWorld(t)
	w.repos(
		alpineRepo(alpineLocal, repo.Local),
		alpineRepo(alpineVirtual, repo.Virtual, alpineLocal),
	)
	w.put(alpineLocal, apkIndexPath, apkIndex(t, "hello", "world"))
	notHello := &accessapitest.Decider{Admin: true, AllowsFunc: func(
		_ api.ResourceType, _ api.Repo, path string, _ api.Actions,
	) bool {
		return path != apkHelloPath
	}}
	get := func(key string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
			"/artifactory/"+key+"/"+apkIndexPath, nil)
		r = r.WithContext(api.WithCaller(r.Context(), kernel.Principal{Name: "reader"}, notHello))
		rec := httptest.NewRecorder()
		w.kit.GetItem(rec, r)
		return rec
	}
	wantError(t, get(alpineLocal), http.StatusForbidden, "Forbidden")
	rec := get(alpineVirtual)
	if rec.Code != http.StatusOK {
		t.Fatalf("virtual index answered %d %s, want 200", rec.Code, rec.Body)
	}
	if got := indexNames(t, rec.Body.Bytes()); !slices.Equal(got, []string{"world"}) {
		t.Errorf("virtual index lists %v, want only world", got)
	}
}

// TestAccAlpineIndexKeepsAPackageThePatternsReject: a local or remote index
// is served as it is, listing a package whose file the repository's
// patterns reject, as 7.x keeps such a package in its indexes
// (RTFACT-30830, RTFACT-30656); only that file's GET answers the policy's
// 404.
func TestAccAlpineIndexKeepsAPackageThePatternsReject(t *testing.T) {
	index := apkIndex(t, "hello", "world")
	w := newItemWorld(t, withSource(upstream(map[string]func() (source.Result, error){
		apkIndexPath: streamed(index, nil),
		apkHelloPath: streamed(apkBody, nil),
	})))
	local := alpineRepo(alpineLocal, repo.Local)
	local.ExcludesPattern = new("**/hello-*")
	remote := alpineRepo(alpineRemote, repo.Remote)
	remote.ExcludesPattern = new("**/hello-*")
	w.repos(local, remote)
	w.put(alpineLocal, apkIndexPath, index)
	w.put(alpineLocal, apkHelloPath, apkBody)
	for _, key := range []string{alpineLocal, alpineRemote} {
		t.Run(key, func(t *testing.T) {
			rec := w.get(key, apkIndexPath)
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), index) {
				t.Errorf("index answered %d with %d bytes, want the index as it is",
					rec.Code, rec.Body.Len())
			}
			wantError(t, w.get(key, apkHelloPath), http.StatusNotFound,
				"The repository '"+key+"' rejected the artifact '"+key+":"+apkHelloPath+
					"' due to its include/exclude pattern settings.")
		})
	}
}

// TestAccAlpineVirtualKeepsPackagesThePatternsReject: a virtual index keeps
// a package whose file a local or remote member's patterns, or a virtual
// repository's own, reject, nested ones included. The package's GET passes
// over the refusing member to the next that lists it, without asking a
// refusing remote's source, answers not found when none serves it, and
// answers that no repository serves it when the virtual repository's own
// patterns reject it.
func TestAccAlpineVirtualKeepsPackagesThePatternsReject(t *testing.T) {
	w := newItemWorld(t, withSource(upstream(map[string]func() (source.Result, error){
		apkIndexPath: streamed(apkIndex(t, "hello"), nil),
		apkHelloPath: func() (source.Result, error) {
			t.Error("the source was asked for a package its remote's patterns reject")
			return source.Result{Stream: stream(apkBody)}, nil
		},
	})))
	hiding := alpineRepo("alpine-hiding", repo.Local)
	hiding.ExcludesPattern = new("**/hello-*")
	remote := alpineRepo(alpineRemote, repo.Remote)
	remote.ExcludesPattern = new("**/hello-*")
	picky := alpineRepo("alpine-picky", repo.Virtual, alpineLocal)
	picky.ExcludesPattern = new("**/hello-*")
	w.repos(
		hiding, remote, picky, alpineRepo(alpineLocal, repo.Local),
		alpineRepo(alpineVirtual, repo.Virtual, "alpine-hiding", alpineLocal),
		alpineRepo("alpine-only-hiding", repo.Virtual, "alpine-hiding"),
		alpineRepo("alpine-over-remote", repo.Virtual, alpineRemote),
		alpineRepo("alpine-nested", repo.Virtual, "alpine-picky"),
	)
	for _, key := range []string{"alpine-hiding", alpineLocal} {
		w.put(key, apkIndexPath, apkIndex(t, "hello"))
	}
	w.put("alpine-hiding", apkHelloPath, []byte("hidden bytes"))
	w.put(alpineLocal, apkHelloPath, apkBody)
	for _, key := range []string{
		alpineVirtual, "alpine-only-hiding", "alpine-picky", "alpine-over-remote", "alpine-nested",
	} {
		rec := w.get(key, apkIndexPath)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s index answered %d %s, want 200", key, rec.Code, rec.Body)
		}
		if got := indexNames(t, rec.Body.Bytes()); !slices.Equal(got, []string{"hello"}) {
			t.Errorf("%s index lists %v, want hello", key, got)
		}
	}
	rec := w.get(alpineVirtual, apkHelloPath)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), apkBody) {
		t.Errorf("hello answered %d %q, want alpine-local's package", rec.Code, rec.Body)
	}
	for _, key := range []string{"alpine-only-hiding", "alpine-over-remote", "alpine-nested"} {
		wantError(t, w.get(key, apkHelloPath), http.StatusNotFound, "Could not find resource")
	}
	wantError(t, w.get("alpine-picky", apkHelloPath), http.StatusNotFound,
		"No repository found to serve the request for alpine-picky:"+apkHelloPath)
}

// TestAccAlpineVirtualPackageChecksAMemberAtThePackagesPath: a virtual
// package GET checks each member at the package's own path, never at the
// index it reads to find the package. A local or remote member, or a
// nested virtual one, whose patterns reject only its APKINDEX.tar.gz serves
// the package, while the virtual index still leaves that member out; a
// blacked-out member, which refuses every file, takes no part.
func TestAccAlpineVirtualPackageChecksAMemberAtThePackagesPath(t *testing.T) {
	w := newItemWorld(t, withSource(upstream(map[string]func() (source.Result, error){
		apkIndexPath: streamed(apkIndex(t, "hello"), nil),
		apkHelloPath: func() (source.Result, error) { return source.Result{Stream: stream(apkBody)}, nil },
	})))
	indexless := alpineRepo("alpine-indexless", repo.Local)
	indexless.ExcludesPattern = new("**/APKINDEX.tar.gz")
	remote := alpineRepo(alpineRemote, repo.Remote)
	remote.ExcludesPattern = new("**/APKINDEX.tar.gz")
	dark := alpineRepo("alpine-dark", repo.Local)
	dark.BlackedOut = new(true)
	closed := alpineRepo("alpine-closed", repo.Virtual, alpineLocal)
	closed.ExcludesPattern = new("**/APKINDEX.tar.gz")
	w.repos(
		indexless, remote, dark, closed, alpineRepo(alpineLocal, repo.Local),
		alpineRepo("alpine-over-indexless", repo.Virtual, "alpine-indexless"),
		alpineRepo("alpine-over-remote", repo.Virtual, alpineRemote),
		alpineRepo("alpine-over-dark", repo.Virtual, "alpine-dark"),
		alpineRepo("alpine-over-closed", repo.Virtual, "alpine-closed"),
	)
	for _, key := range []string{"alpine-indexless", "alpine-dark", alpineLocal} {
		w.put(key, apkIndexPath, apkIndex(t, "hello"))
		w.put(key, apkHelloPath, apkBody)
	}
	for _, key := range []string{
		"alpine-over-indexless", "alpine-over-remote", "alpine-over-dark", "alpine-over-closed",
	} {
		rec := w.get(key, apkIndexPath)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s index answered %d %s, want 200", key, rec.Code, rec.Body)
		}
		if got := indexNames(t, rec.Body.Bytes()); len(got) != 0 {
			t.Errorf("%s index lists %v, want the refusing member left out", key, got)
		}
	}
	for _, key := range []string{"alpine-over-indexless", "alpine-over-remote", "alpine-over-closed"} {
		rec := w.get(key, apkHelloPath)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), apkBody) {
			t.Errorf("%s answered %d %q, want the member's package", key, rec.Code, rec.Body)
		}
	}
	wantError(t, w.get("alpine-over-dark", apkHelloPath), http.StatusNotFound,
		"Could not find resource")
}

// TestUnitAlpineCacheAliasOfALocalRepositoryHasNoIndex: only a remote
// repository has a cache, so the overlay answers 404 for an index in any
// other repository's cache. The item route already refuses such an alias
// before the overlays run, so only a direct call reaches this refusal.
func TestUnitAlpineCacheAliasOfALocalRepositoryHasNoIndex(t *testing.T) {
	w := newItemWorld(t)
	local := w.repos(alpineRepo(alpineLocal, repo.Local))[0]
	w.put(alpineLocal, apkIndexPath, apkIndex(t, "hello"))
	rec := httptest.NewRecorder()
	r := kittest.Request(t, http.MethodGet, "/artifactory/alpine-local-cache/"+apkIndexPath)
	req := itemkit.ItemRequest{Key: "alpine-local-cache", Path: apkIndexPath}
	if !TryAlpineItem(w.kit, rec, r, w.db, local, true, req, kittest.Admin, kittest.AdminAccess()) {
		t.Fatal("the overlay left an index request to the generic lookup")
	}
	wantError(t, rec, http.StatusNotFound, "Could not find resource")
}

// TestAccAlpineLocalIndexFailsClosed: a local index is served only from a
// file whose stored content decodes as an index. A folder at the path is
// not found; content that is lost, empty, unreadable or malformed is a
// server fault, never an index apk would be handed.
func TestAccAlpineLocalIndexFailsClosed(t *testing.T) {
	brokenStore := func(c *Config) { c.Blobs = brokenBlobs{c.Blobs} }
	for _, tc := range []struct {
		name    string
		tune    []func(*Config)
		seed    func(*itemWorld)
		status  int
		message string
	}{
		{
			name:   "folder at the index path",
			seed:   func(w *itemWorld) { w.folder(alpineLocal, apkIndexPath) },
			status: http.StatusNotFound, message: "Could not find resource",
		},
		{
			name:   "content lost",
			seed:   func(w *itemWorld) { w.lost(alpineLocal, apkIndexPath) },
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name:   "empty index",
			seed:   func(w *itemWorld) { w.put(alpineLocal, apkIndexPath, nil) },
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name:   "malformed index",
			seed:   func(w *itemWorld) { w.put(alpineLocal, apkIndexPath, []byte("not an index")) },
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name:   "content unreadable",
			tune:   []func(*Config){brokenStore},
			seed:   func(w *itemWorld) { w.put(alpineLocal, apkIndexPath, apkIndex(w.t, "hello")) },
			status: http.StatusInternalServerError, message: "internal error",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t, tc.tune...)
			w.repos(alpineRepo(alpineLocal, repo.Local))
			tc.seed(w)
			wantError(t, w.get(alpineLocal, apkIndexPath), tc.status, tc.message)
		})
	}
}

// TestAccAlpineRemoteIndexRechecksAuthority: a remote index is served only
// once the source has rechecked its authority for the index and for each
// package the index lists. A refusal answers 403 as the source's own does,
// and a check that cannot be made is a server fault, so the index is never
// served past a revoked or unknown grant.
func TestAccAlpineRemoteIndexRechecksAuthority(t *testing.T) {
	index := apkIndex(t, "hello")
	for _, tc := range []struct {
		name    string
		check   func() func(context.Context) error
		status  int
		message string
	}{
		{
			name:   "index refused at delivery",
			check:  func() func(context.Context) error { return failsOnCall(2, source.ErrDenied) },
			status: http.StatusForbidden, message: "Forbidden",
		},
		{
			name:   "listed package refused",
			check:  func() func(context.Context) error { return failsOnCall(3, source.ErrDenied) },
			status: http.StatusForbidden, message: "Forbidden",
		},
		{
			name: "listed package cannot be checked",
			check: func() func(context.Context) error {
				return failsOnCall(3, source.ErrUnavailable)
			},
			status: http.StatusInternalServerError, message: "internal error",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t, withSource(upstream(map[string]func() (source.Result, error){
				apkIndexPath: streamed(index, tc.check),
			})))
			w.repos(alpineRepo(alpineRemote, repo.Remote))
			wantError(t, w.get(alpineRemote, apkIndexPath), tc.status, tc.message)
		})
	}
	t.Run("every check passes", func(t *testing.T) {
		w := newItemWorld(t, withSource(upstream(map[string]func() (source.Result, error){
			apkIndexPath: streamed(index, nil),
		})))
		w.repos(alpineRepo(alpineRemote, repo.Remote))
		rec := w.get(alpineRemote, apkIndexPath)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), index) {
			t.Fatalf("answered %d with %d bytes, want 200 and the upstream index",
				rec.Code, rec.Body.Len())
		}
		// apk must not reuse an index past the authority checked for it.
		if got := rec.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("Cache-Control %q, want private, no-store", got)
		}
	})
}

// TestAccAlpineRemoteIndexFailures: a remote index comes from the source's
// stream or from the item it cached. A server with no remote sources, a
// stream that cannot be read and a cached item whose content is lost are
// server faults; a cached item whose content is there is served as is.
func TestAccAlpineRemoteIndexFailures(t *testing.T) {
	index := apkIndex(t, "hello")
	cachedAt := func(w *itemWorld, body []byte) *model.Item {
		it := w.world.Put(w.t, alpineRepo(alpineRemote, repo.Remote), true, apkIndexPath, body)
		return &it
	}
	for _, tc := range []struct {
		name    string
		sources bool
		answer  func(*itemWorld) (source.Result, error)
		status  int
		message string
	}{
		{
			name:   "no remote sources",
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "stream read fails", sources: true,
			answer: func(*itemWorld) (source.Result, error) {
				return source.Result{Stream: &source.Stream{
					Body: io.NopCloser(iotest.ErrReader(errFault)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(index))},
				}}, nil
			},
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "cached content lost", sources: true,
			answer: func(*itemWorld) (source.Result, error) {
				return source.Result{Item: &model.Item{
					RepoKey: alpineRemote, Cached: true, Path: "v3.20/main/x86_64",
					Name: "APKINDEX.tar.gz", Actual: kernel.Checksums{SHA256: absentSHA256},
				}}, nil
			},
			status: http.StatusInternalServerError, message: "internal error",
		},
		{
			name: "cached index", sources: true,
			answer: func(w *itemWorld) (source.Result, error) {
				return source.Result{Item: cachedAt(w, index)}, nil
			},
			status: http.StatusOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w *itemWorld
			var tune []func(*Config)
			if tc.sources {
				tune = append(tune, withSource(func(
					_ context.Context, _ source.Request, _ source.Permit,
				) (source.Result, error) {
					return tc.answer(w)
				}))
			}
			w = newItemWorld(t, tune...)
			w.repos(alpineRepo(alpineRemote, repo.Remote))
			rec := w.get(alpineRemote, apkIndexPath)
			if tc.status != http.StatusOK {
				wantError(t, rec, tc.status, tc.message)
				return
			}
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), index) {
				t.Errorf("answered %d with %d bytes, want 200 and the cached index",
					rec.Code, rec.Body.Len())
			}
		})
	}
}

// TestUnitAlpineMemberIndexRefusesWhatHasNoIndex: the member index read
// answers not found, without asking the source, for a remote index path
// outside the layout and for a repository that holds no index of its own:
// a virtual one, or a local one's cache. The overlay's callers never pass
// these, so only a direct call reaches the refusals.
func TestUnitAlpineMemberIndexRefusesWhatHasNoIndex(t *testing.T) {
	w := newItemWorld(t, withSource(func(
		context.Context, source.Request, source.Permit,
	) (source.Result, error) {
		t.Error("the source was asked for an index the read should refuse first")
		return source.Result{}, source.ErrUnavailable
	}))
	for _, tc := range []struct {
		name   string
		c      repo.Config
		cached bool
		path   string
	}{
		{
			"remote index outside the layout", alpineRepo(alpineRemote, repo.Remote), false,
			"v3.20/APKINDEX.tar.gz",
		},
		{"virtual repository", alpineRepo(alpineVirtual, repo.Virtual), false, apkIndexPath},
		{"local repository's cache", alpineRepo(alpineLocal, repo.Local), true, apkIndexPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := alpineItems{w.kit}.alpineMemberIndex(
				t.Context(), w.db, tc.c, tc.cached, tc.path, kittest.Admin, kittest.AdminAccess())
			if !errors.Is(err, kernel.ErrNotFound) {
				t.Errorf("read returned %v, want not found", err)
			}
		})
	}
}

// TestAccAlpineVirtualIndexSkipsMembersWithoutAReadableIndex: a virtual
// index lists the packages of each member, nested virtual ones included,
// that has an index the caller may download, and skips a member that is
// absent, has no index or is blacked out instead of failing for it.
func TestAccAlpineVirtualIndexSkipsMembersWithoutAReadableIndex(t *testing.T) {
	w := newItemWorld(t)
	dark := alpineRepo("alpine-dark", repo.Local)
	dark.BlackedOut = new(true)
	w.repos(
		alpineRepo(alpineLocal, repo.Local),
		alpineRepo("alpine-empty", repo.Local),
		dark,
		alpineRepo("alpine-inner", repo.Virtual, alpineLocal),
		alpineRepo(alpineVirtual, repo.Virtual,
			"absent", "alpine-empty", "alpine-dark", "alpine-inner"),
	)
	w.put(alpineLocal, apkIndexPath, apkIndex(t, "hello"))
	w.put("alpine-dark", apkIndexPath, apkIndex(t, "dark"))
	rec := w.get(alpineVirtual, apkIndexPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d %s, want 200", rec.Code, rec.Body)
	}
	if got := indexNames(t, rec.Body.Bytes()); !slices.Equal(got, []string{"hello"}) {
		t.Errorf("virtual index lists %v, want only the readable member's hello", got)
	}
}

// TestAccAlpineVirtualIndexFailsClosed: a virtual index is never built
// from part of its member graph, nor left unsigned when a key is named. A
// member cycle, a member of another package type, a member that cannot be
// looked up or whose index is malformed, a remote member whose listed
// package cannot be checked, and a signing key that is absent or whose
// name apk cannot use each answer 500.
func TestAccAlpineVirtualIndexFailsClosed(t *testing.T) {
	index := apkIndex(t, "hello")
	unknownPackage := withSource(upstream(map[string]func() (source.Result, error){
		apkIndexPath: streamed(index, func() func(context.Context) error {
			return failsOnCall(3, source.ErrUnavailable)
		}),
	}))
	signedBy := func(pair string) repo.Config {
		c := alpineRepo(alpineVirtual, repo.Virtual)
		c.KeyPair = pair
		return c
	}
	for _, tc := range []struct {
		name    string
		tune    []func(*Config)
		virtual repo.Config
		seed    func(*itemWorld)
	}{
		{name: "member cycle", virtual: alpineRepo(alpineVirtual, repo.Virtual, alpineVirtual)},
		{
			name:    "member of another package type",
			virtual: alpineRepo(alpineVirtual, repo.Virtual, kittest.LocalKey),
		},
		{
			name: "member lookup fails",
			tune: []func(*Config){func(c *Config) {
				c.Store = faultyRepos{Store: c.Store, fail: alpineLocal}
			}},
			virtual: alpineRepo(alpineVirtual, repo.Virtual, alpineLocal),
		},
		{
			name:    "member index malformed",
			virtual: alpineRepo(alpineVirtual, repo.Virtual, alpineLocal),
			seed: func(w *itemWorld) {
				w.put(alpineLocal, apkIndexPath, []byte("not an index"))
			},
		},
		{
			name:    "remote member's package cannot be checked",
			tune:    []func(*Config){unknownPackage},
			virtual: alpineRepo(alpineVirtual, repo.Virtual, alpineRemote),
		},
		{name: "signing key absent", virtual: signedBy("absent")},
		{
			name:    "signing key named outside apk's key file names",
			virtual: signedBy("apk"),
			seed: func(w *itemWorld) {
				if err := w.db.Keys().CreateKeypair(w.t.Context(), api.Keypair{
					Name: "apk", Type: api.KeypairRSA, Alias: "bad alias",
					PrivateKey: rsaKeyPEM(w.t),
				}); err != nil {
					w.t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t, tc.tune...)
			w.repos(alpineRepo(alpineLocal, repo.Local), alpineRepo(alpineRemote, repo.Remote),
				tc.virtual)
			if tc.seed != nil {
				tc.seed(w)
			}
			wantError(t, w.get(alpineVirtual, apkIndexPath),
				http.StatusInternalServerError, "internal error")
		})
	}
}

// TestAccAlpineVirtualPackageNeedsAListedStoredFile: a virtual repository
// serves a package from the first member whose index lists it and which
// stores it as a file. A package no member lists, one listed but not
// stored, and a folder at its path are not found; a member cycle is a
// server fault.
func TestAccAlpineVirtualPackageNeedsAListedStoredFile(t *testing.T) {
	other := "v3.20/main/x86_64/other-1.0-r0.apk"
	for _, tc := range []struct {
		name    string
		members []string
		seed    func(*itemWorld)
		path    string
		status  int
		message string
	}{
		{
			name: "listed and stored", members: []string{alpineLocal}, path: apkHelloPath,
			seed:   func(w *itemWorld) { w.put(alpineLocal, apkHelloPath, apkBody) },
			status: http.StatusOK,
		},
		{
			name: "not listed", members: []string{alpineLocal}, path: other,
			seed:   func(w *itemWorld) { w.put(alpineLocal, apkHelloPath, apkBody) },
			status: http.StatusNotFound, message: "Could not find resource",
		},
		{
			name: "listed but not stored", members: []string{alpineLocal}, path: apkHelloPath,
			status: http.StatusNotFound, message: "Could not find resource",
		},
		{
			name: "folder at the package path", members: []string{alpineLocal}, path: apkHelloPath,
			seed:   func(w *itemWorld) { w.folder(alpineLocal, apkHelloPath) },
			status: http.StatusNotFound, message: "Could not find resource",
		},
		{
			name: "member cycle", members: []string{alpineVirtual}, path: apkHelloPath,
			status: http.StatusInternalServerError, message: "internal error",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t)
			w.repos(alpineRepo(alpineLocal, repo.Local),
				alpineRepo(alpineVirtual, repo.Virtual, tc.members...))
			w.put(alpineLocal, apkIndexPath, apkIndex(t, "hello"))
			if tc.seed != nil {
				tc.seed(w)
			}
			rec := w.get(alpineVirtual, tc.path)
			if tc.status != http.StatusOK {
				wantError(t, rec, tc.status, tc.message)
				return
			}
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), apkBody) {
				t.Errorf("answered %d %q, want 200 and the member's package", rec.Code, rec.Body)
			}
		})
	}
}

// TestAccAlpineVirtualRemotePackage: a package that a remote member's index
// lists is read through that member's source. A stream is relayed, an
// upstream that cannot be reached answers 502, and a cached copy whose
// authority is revoked by delivery time is refused.
func TestAccAlpineVirtualRemotePackage(t *testing.T) {
	index := apkIndex(t, "hello")
	for _, tc := range []struct {
		name    string
		answer  func(*itemWorld) (source.Result, error)
		status  int
		message string
	}{
		{
			name: "upstream stream",
			answer: func(*itemWorld) (source.Result, error) {
				return source.Result{Stream: stream(apkBody)}, nil
			},
			status: http.StatusOK,
		},
		{
			name: "upstream unavailable",
			answer: func(*itemWorld) (source.Result, error) {
				return source.Result{}, source.ErrUnavailable
			},
			status: http.StatusBadGateway, message: "Bad Gateway",
		},
		{
			name: "cached copy revoked at delivery",
			answer: func(w *itemWorld) (source.Result, error) {
				it := w.world.Put(w.t, alpineRepo(alpineRemote, repo.Remote), true,
					apkHelloPath, apkBody)
				return source.Result{Item: &it, Revalidate: failsOnCall(2, source.ErrDenied)}, nil
			},
			status: http.StatusForbidden, message: "Forbidden",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var w *itemWorld
			w = newItemWorld(t, withSource(upstream(map[string]func() (source.Result, error){
				apkIndexPath: streamed(index, nil),
				apkHelloPath: func() (source.Result, error) { return tc.answer(w) },
			})))
			w.repos(alpineRepo(alpineRemote, repo.Remote),
				alpineRepo(alpineVirtual, repo.Virtual, alpineRemote))
			rec := w.get(alpineVirtual, apkHelloPath)
			if tc.status != http.StatusOK {
				wantError(t, rec, tc.status, tc.message)
				return
			}
			if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), apkBody) {
				t.Errorf("answered %d %q, want 200 and the upstream package", rec.Code, rec.Body)
			}
		})
	}
}

// TestUnitAlpineRemotePackageRefusesAPathOutsideTheLayout: the remote
// package read answers not found, without asking the source, for a path
// outside apk's layout. The virtual package route refuses such a path
// first, so only a direct call reaches this refusal.
func TestUnitAlpineRemotePackageRefusesAPathOutsideTheLayout(t *testing.T) {
	w := newItemWorld(t, withSource(func(
		context.Context, source.Request, source.Permit,
	) (source.Result, error) {
		t.Error("the source was asked for a package the read should refuse first")
		return source.Result{}, source.ErrUnavailable
	}))
	remote := w.repos(alpineRepo(alpineRemote, repo.Remote))[0]
	rec := httptest.NewRecorder()
	r := kittest.Request(t, http.MethodGet, "/artifactory/alpine/hello-1.0-r0.apk")
	req := itemkit.ItemRequest{Key: alpineVirtual, Path: "hello-1.0-r0.apk"}
	alpineItems{w.kit}.serveRemoteAlpinePackage(rec, r, remote, req,
		kittest.Admin, kittest.AdminAccess())
	wantError(t, rec, http.StatusNotFound, "Could not find resource")
}

// TestUnitAlpineRemoteReadsWithoutRemoteSources: on a server built without
// a remote source service, a remote package read answers as a remote index
// read does, 500 for the missing service, rather than calling through it.
// A virtual repository reads its remote member's index before any package
// and fails there, so only a direct call reaches the package read's own
// refusal.
func TestUnitAlpineRemoteReadsWithoutRemoteSources(t *testing.T) {
	w := newItemWorld(t)
	remote := w.repos(alpineRepo(alpineRemote, repo.Remote),
		alpineRepo(alpineVirtual, repo.Virtual, alpineRemote))[0]
	for _, tc := range []struct {
		name  string
		serve func() *httptest.ResponseRecorder
	}{
		{"remote index", func() *httptest.ResponseRecorder {
			return w.get(alpineRemote, apkIndexPath)
		}},
		{"virtual package", func() *httptest.ResponseRecorder {
			return w.get(alpineVirtual, apkHelloPath)
		}},
		{"remote package read", func() *httptest.ResponseRecorder {
			rec := httptest.NewRecorder()
			r := kittest.Request(t, http.MethodGet, "/artifactory/alpine/"+apkHelloPath)
			req := itemkit.ItemRequest{Key: alpineVirtual, Path: apkHelloPath}
			alpineItems{w.kit}.serveRemoteAlpinePackage(rec, r, remote, req,
				kittest.Admin, kittest.AdminAccess())
			return rec
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantError(t, tc.serve(), http.StatusInternalServerError, "internal error")
		})
	}
}
