package rpmhttp

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
	"slices"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit/kittest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/rpm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// rpmAlphaPath is where the createrepo_c fixture's primary places the one
// package it lists, below the metadata root.
const rpmAlphaPath = "pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm"

// rpmAlpha is the bytes of the package the fixture's primary lists.
func rpmAlpha(t *testing.T) []byte {
	t.Helper()
	return repopath.Read(
		t,
		"internal/artifacts/ecosystem/rpm/testdata/artifactweave-alpha-1.0-1.fc42.noarch.rpm",
	)
}

// rpmRepos stores each of cs as an RPM repository, with its defaults.
func (w *itemWorld) rpmRepos(cs ...repo.Config) {
	w.t.Helper()
	for _, c := range cs {
		c.PackageType = repo.RPM
		if c.Class == repo.Remote {
			c.URL = "https://up.example"
		}
		if err := w.db.Repos().Create(w.t.Context(), *repo.Defaults(&c)); err != nil {
			w.t.Fatal(err)
		}
	}
}

// rpmRepo returns the repository key as stored.
func (w *itemWorld) rpmRepo(key string) repo.Config {
	w.t.Helper()
	c, err := w.db.Repos().Get(w.t.Context(), key)
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

// storeFile stores body as the file at path in the repository key, blob
// first, as a deploy leaves it; cached puts it in a remote's cache.
func (w *itemWorld) storeFile(key, path string, cached bool, body []byte) model.Item {
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
	it, err := w.db.Items().Put(ctx, model.Item{
		RepoKey: key, Cached: cached, Name: path, Size: info.Size,
		Actual: kernel.Checksums{SHA256: info.Digests.SHA256},
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return it
}

// publish stores the createrepo_c fixture's repomd.xml and primary below
// root in the repository key, with the package they list.
func (w *itemWorld) publish(key, root string) {
	w.t.Helper()
	manifest, primary, primaryPath := rpmUpstreamFixture(w.t)
	w.storeFile(key, root+"repodata/repomd.xml", false, manifest)
	w.storeFile(key, root+primaryPath, false, primary)
	w.storeFile(key, root+rpmAlphaPath, false, rpmAlpha(w.t))
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

// failsOnCall is a backend revalidation that fails with err on its nth
// call and passes every other. The source service makes the first call
// itself before it returns the result, so the delivery check is the second.
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

// TestUnitRPMOverlayLeavesOtherVirtualPathsAlone: in a virtual RPM
// repository the overlay answers only repodata and .rpm paths; it leaves
// any other path, with nothing written, to the generic lookup after it.
func TestUnitRPMOverlayLeavesOtherVirtualPathsAlone(t *testing.T) {
	w := newItemWorld(t)
	w.rpmRepos(repo.Config{Key: "rpm-virtual", Class: repo.Virtual})
	rec := httptest.NewRecorder()
	r := kittest.Request(t, http.MethodGet, "/artifactory/rpm-virtual/docs/readme.txt")
	req := itemkit.ItemRequest{Key: "rpm-virtual", Path: "docs/readme.txt"}
	c := w.rpmRepo("rpm-virtual")
	if TryRPMItem(w.kit, rec, r, w.db, c, false, req, kittest.Admin, kittest.AdminAccess()) {
		t.Error("the overlay answered a path that is neither repodata nor a package")
	}
	if rec.Body.Len() != 0 || len(rec.Header()) != 0 {
		t.Errorf("the overlay wrote %q with headers %v for a path it left", rec.Body, rec.Header())
	}
}

// TestAccRPMVirtualPackageMustBeListedBelowItsRoot: a virtual repository
// serves a package only when a member's repodata under the package's yum
// root lists it. With a root depth the root is the path's leading folders,
// so a package below a root with no repodata, or one no repodata lists, is
// not found.
func TestAccRPMVirtualPackageMustBeListedBelowItsRoot(t *testing.T) {
	w := newItemWorld(t)
	w.rpmRepos(
		repo.Config{Key: "rpm-member", Class: repo.Local},
		repo.Config{Key: "rpm-virtual", Class: repo.Virtual, Repositories: []string{"rpm-member"}},
		repo.Config{
			Key: "rpm-deep", Class: repo.Virtual, Repositories: []string{"rpm-member"},
			YumRootDepth: new(2),
		},
	)
	w.publish("rpm-member", "")
	w.publish("rpm-member", "el/9/")
	alpha := rpmAlpha(t)
	for _, tc := range []struct {
		target string
		status int
	}{
		{"/artifactory/rpm-virtual/" + rpmAlphaPath, http.StatusOK},
		{
			"/artifactory/rpm-virtual/pool/artifactweave-beta-1.0-1.fc42.noarch.rpm",
			http.StatusNotFound,
		},
		{"/artifactory/rpm-deep/el/9/" + rpmAlphaPath, http.StatusOK},
		{"/artifactory/rpm-deep/el/8/" + rpmAlphaPath, http.StatusNotFound},
	} {
		res := w.do(http.MethodGet, tc.target, "admin", "")
		if tc.status != http.StatusOK {
			wantError(t, res, tc.status, "Could not find resource")
			continue
		}
		if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), alpha) {
			t.Errorf("%s: answered %d with %d bytes, want the listed package",
				tc.target, res.Code, res.Body.Len())
		}
	}
}

// TestAccRPMVirtualRepodataSkipsMembersItCannotRead: a virtual repository's
// repodata leaves out a member that is blacked out, one whose exclude
// pattern hides its primary, and one whose repomd.xml path is a folder,
// instead of failing for them. A package GET checks each member at the
// package's own path, never at the repodata it reads to find the package,
// so the member whose patterns hide only its primary serves the package;
// the blacked-out member, which refuses every file, does not.
func TestAccRPMVirtualRepodataSkipsMembersItCannotRead(t *testing.T) {
	w := newItemWorld(t)
	w.rpmRepos(
		repo.Config{Key: "rpm-dark", Class: repo.Local, BlackedOut: new(true)},
		repo.Config{
			Key: "rpm-hidden", Class: repo.Local, ExcludesPattern: new("**/*-primary.xml.zst"),
		},
		repo.Config{Key: "rpm-folder", Class: repo.Local},
		repo.Config{
			Key: "rpm-virtual", Class: repo.Virtual,
			Repositories: []string{"rpm-dark", "rpm-hidden", "rpm-folder"},
		},
	)
	w.publish("rpm-dark", "")
	w.publish("rpm-hidden", "")
	if _, err := w.db.Items().Put(t.Context(), model.Item{
		RepoKey: "rpm-folder", Name: "repodata/repomd.xml", Folder: true,
	}); err != nil {
		t.Fatal(err)
	}
	w.storeFile("rpm-folder", rpmAlphaPath, false, rpmAlpha(t))

	res := w.do(http.MethodGet, "/artifactory/rpm-virtual/repodata/repomd.xml", "admin", "")
	if res.Code != http.StatusOK {
		t.Fatalf("virtual repomd.xml answered %d %s, want 200", res.Code, res.Body)
	}
	primaryPath, sum, err := rpm.DecodeRepomd(res.Body.Bytes(), "")
	if err != nil {
		t.Fatalf("virtual repomd.xml does not decode: %v", err)
	}
	res = w.do(http.MethodGet, "/artifactory/rpm-virtual/"+primaryPath, "admin", "")
	packages, err := rpm.DecodePrimary(res.Body.Bytes(), "", sum)
	if res.Code != http.StatusOK || err != nil || len(packages) != 0 {
		t.Errorf("virtual primary answered %d listing %v (%v), want no packages",
			res.Code, packages, err)
	}
	res = w.do(http.MethodGet, "/artifactory/rpm-virtual/"+rpmAlphaPath, "admin", "")
	if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), rpmAlpha(t)) {
		t.Errorf("alpha answered %d, want rpm-hidden's package", res.Code)
	}
}

// TestAccRPMVirtualPackageChecksAMemberAtThePackagesPath: a virtual package
// GET checks each member at the package's own path, never at the repodata
// it reads to find the package. A remote member, or a nested virtual one,
// whose patterns reject only its repodata serves the package, while the
// virtual repodata still leaves that member out; a blacked-out member,
// which refuses every file, takes no part.
func TestAccRPMVirtualPackageChecksAMemberAtThePackagesPath(t *testing.T) {
	manifest, primary, primaryPath := rpmUpstreamFixture(t)
	responses := map[string][]byte{
		"repodata/repomd.xml": manifest, primaryPath: primary, rpmAlphaPath: rpmAlpha(t),
	}
	w := newItemWorld(t, func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(func(
			_ context.Context, q source.Request, _ source.Permit,
		) (source.Result, error) {
			body, ok := responses[q.Resource.Path]
			if !ok {
				return source.Result{}, kernel.ErrNotFound
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		})}
	})
	w.rpmRepos(
		repo.Config{Key: "rpm-remote", Class: repo.Remote, ExcludesPattern: new("repodata/**")},
		repo.Config{Key: "rpm-dark", Class: repo.Local, BlackedOut: new(true)},
		repo.Config{Key: "rpm-member", Class: repo.Local},
		repo.Config{
			Key: "rpm-closed", Class: repo.Virtual, Repositories: []string{"rpm-member"},
			ExcludesPattern: new("repodata/**"),
		},
		repo.Config{
			Key:          "rpm-over-remote",
			Class:        repo.Virtual,
			Repositories: []string{"rpm-remote"},
		},
		repo.Config{Key: "rpm-over-dark", Class: repo.Virtual, Repositories: []string{"rpm-dark"}},
		repo.Config{
			Key:          "rpm-over-closed",
			Class:        repo.Virtual,
			Repositories: []string{"rpm-closed"},
		},
	)
	w.publish("rpm-dark", "")
	w.publish("rpm-member", "")
	for _, key := range []string{"rpm-over-remote", "rpm-over-dark", "rpm-over-closed"} {
		if got := rpmListed(t, w, key); len(got) != 0 {
			t.Errorf("%s repodata lists %v, want the refusing member left out", key, got)
		}
	}
	for _, key := range []string{"rpm-over-remote", "rpm-over-closed"} {
		res := w.do(http.MethodGet, "/artifactory/"+key+"/"+rpmAlphaPath, "admin", "")
		if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), rpmAlpha(t)) {
			t.Errorf("%s alpha answered %d, want the member's package", key, res.Code)
		}
	}
	wantError(t, w.do(http.MethodGet, "/artifactory/rpm-over-dark/"+rpmAlphaPath, "admin", ""),
		http.StatusNotFound, "Could not find resource")
}

// rpmListed returns the paths of the packages the virtual repository key's
// repodata lists, read as kittest's administrator, and fails the test when
// the repodata is not served or does not decode.
func rpmListed(t *testing.T, w *itemWorld, key string) []string {
	t.Helper()
	res := w.do(http.MethodGet, "/artifactory/"+key+"/repodata/repomd.xml", "admin", "")
	if res.Code != http.StatusOK {
		t.Fatalf("%s repomd.xml answered %d %s, want 200", key, res.Code, res.Body)
	}
	primaryPath, sum, err := rpm.DecodeRepomd(res.Body.Bytes(), "")
	if err != nil {
		t.Fatalf("%s repomd.xml does not decode: %v", key, err)
	}
	res = w.do(http.MethodGet, "/artifactory/"+key+"/"+primaryPath, "admin", "")
	packages, err := rpm.DecodePrimary(res.Body.Bytes(), "", sum)
	if res.Code != http.StatusOK || err != nil {
		t.Fatalf("%s primary answered %d (%v), want 200", key, res.Code, err)
	}
	paths := make([]string, 0, len(packages))
	for _, pkg := range packages {
		paths = append(paths, pkg.Path)
	}
	return paths
}

// TestAccRPMVirtualOwnPatternsAnswerNoRepository: a virtual repository's
// own patterns rejecting a repodata file or a package leave no repository
// to serve it, 404 "No repository found to serve the request" (6.20
// VirtualRepoDownloadStrategy), once read permission is settled: a caller
// who may not read the path is refused first (DV-057).
func TestAccRPMVirtualOwnPatternsAnswerNoRepository(t *testing.T) {
	w := newItemWorld(t)
	w.rpmRepos(
		repo.Config{Key: "rpm-member", Class: repo.Local},
		repo.Config{
			Key: "rpm-closed", Class: repo.Virtual, Repositories: []string{"rpm-member"},
			ExcludesPattern: new("repodata/**,pool/**"),
		},
	)
	w.publish("rpm-member", "")
	for _, path := range []string{"repodata/repomd.xml", rpmAlphaPath} {
		wantError(t, w.do(http.MethodGet, "/artifactory/rpm-closed/"+path, "admin", ""),
			http.StatusNotFound, "No repository found to serve the request for rpm-closed:"+path)
		wantError(t, w.do(http.MethodGet, "/artifactory/rpm-closed/"+path, "user", ""),
			http.StatusForbidden, "Forbidden")
	}
}

// TestAccRPMVirtualKeepsPackagesThePatternsReject: a virtual repository's
// repodata keeps a package whose file a member's patterns, or its own,
// reject, as 7.x's virtual primary.xml.gz does (RTFACT-30656). The
// package's GET passes over the refusing member to the next that lists it,
// answers not found when none serves it, and answers that no repository
// serves it when the virtual repository's own patterns reject it.
func TestAccRPMVirtualKeepsPackagesThePatternsReject(t *testing.T) {
	w := newItemWorld(t)
	w.rpmRepos(
		repo.Config{Key: "rpm-hiding", Class: repo.Local, ExcludesPattern: new("pool/**")},
		repo.Config{Key: "rpm-member", Class: repo.Local},
		repo.Config{
			Key: "rpm-virtual", Class: repo.Virtual,
			Repositories: []string{"rpm-hiding", "rpm-member"},
		},
		repo.Config{
			Key:          "rpm-only-hiding",
			Class:        repo.Virtual,
			Repositories: []string{"rpm-hiding"},
		},
		repo.Config{
			Key: "rpm-picky", Class: repo.Virtual, Repositories: []string{"rpm-member"},
			ExcludesPattern: new("pool/**"),
		},
		repo.Config{Key: "rpm-nested", Class: repo.Virtual, Repositories: []string{"rpm-picky"}},
	)
	w.publish("rpm-hiding", "")
	w.publish("rpm-member", "")
	for _, key := range []string{"rpm-virtual", "rpm-only-hiding", "rpm-picky", "rpm-nested"} {
		if got := rpmListed(t, w, key); !slices.Equal(got, []string{rpmAlphaPath}) {
			t.Errorf("%s repodata lists %v, want alpha", key, got)
		}
	}
	res := w.do(http.MethodGet, "/artifactory/rpm-virtual/"+rpmAlphaPath, "admin", "")
	if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), rpmAlpha(t)) {
		t.Errorf("rpm-virtual alpha answered %d, want rpm-member's package", res.Code)
	}
	for _, key := range []string{"rpm-only-hiding", "rpm-nested"} {
		wantError(t, w.do(http.MethodGet, "/artifactory/"+key+"/"+rpmAlphaPath, "admin", ""),
			http.StatusNotFound, "Could not find resource")
	}
	wantError(t, w.do(http.MethodGet, "/artifactory/rpm-picky/"+rpmAlphaPath, "admin", ""),
		http.StatusNotFound, "No repository found to serve the request for rpm-picky:"+rpmAlphaPath)
}

// TestAccRPMVirtualKeepsARemoteMembersRefusedPackage: a remote member whose
// patterns reject a package it lists keeps it in the virtual repodata, and
// the package's GET passes over that member without asking its source.
func TestAccRPMVirtualKeepsARemoteMembersRefusedPackage(t *testing.T) {
	manifest, primary, primaryPath := rpmUpstreamFixture(t)
	responses := map[string][]byte{"repodata/repomd.xml": manifest, primaryPath: primary}
	w := newItemWorld(t, func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(func(
			_ context.Context, q source.Request, _ source.Permit,
		) (source.Result, error) {
			body, ok := responses[q.Resource.Path]
			if !ok {
				t.Errorf("the source was asked for %s", q.Resource.Path)
				return source.Result{}, kernel.ErrNotFound
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		})}
	})
	w.rpmRepos(
		repo.Config{Key: "rpm-remote", Class: repo.Remote, ExcludesPattern: new("pool/**")},
		repo.Config{Key: "rpm-virtual", Class: repo.Virtual, Repositories: []string{"rpm-remote"}},
	)
	if got := rpmListed(t, w, "rpm-virtual"); !slices.Equal(got, []string{rpmAlphaPath}) {
		t.Errorf("virtual repodata lists %v, want alpha", got)
	}
	wantError(t, w.do(http.MethodGet, "/artifactory/rpm-virtual/"+rpmAlphaPath, "admin", ""),
		http.StatusNotFound, "Could not find resource")
}

// TestUnitRPMVirtualRemotePackageRefusesBadBytesAndRevokedCopies: a remote
// member's package is relayed only when the upstream bytes parse as an
// RPM, and a cached copy only while the source's authority holds at
// delivery; otherwise the answer is the source's 502 or 403.
func TestUnitRPMVirtualRemotePackageRefusesBadBytesAndRevokedCopies(t *testing.T) {
	alpha := rpmAlpha(t)
	sha := fmt.Sprintf("%x", sha256.Sum256(alpha))
	w := newItemWorld(t)
	w.rpmRepos(repo.Config{Key: "rpm-remote", Class: repo.Remote})
	remote := w.rpmRepo("rpm-remote")
	cached := w.storeFile("rpm-remote", rpmAlphaPath, true, alpha)
	for _, tc := range []struct {
		name    string
		read    sourceBackend
		status  int
		message string
	}{
		{
			name: "upstream bytes are not an RPM",
			// The HTTP backend runs the request's check on the bytes before
			// it hands them on; this one does the same.
			read: func(
				ctx context.Context, q source.Request, _ source.Permit,
			) (source.Result, error) {
				body := []byte("not an rpm")
				err := q.Resource.Validate(ctx, bytes.NewReader(body), int64(len(body)))
				if err != nil {
					return source.Result{}, err
				}
				return source.Result{Stream: &source.Stream{
					Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
					Representation: source.Representation{Size: int64(len(body))},
				}}, nil
			},
			status: http.StatusBadGateway, message: "Bad Gateway",
		},
		{
			name: "cached copy revoked at delivery",
			read: func(context.Context, source.Request, source.Permit) (source.Result, error) {
				revoked := failsOnCall(2, source.ErrDenied)
				return source.Result{Item: &cached, Revalidate: revoked}, nil
			},
			status: http.StatusForbidden, message: "Forbidden",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := RPMAPI{newKit(withAccess(t, Config{
				Store: w.db, Blobs: w.blobs,
				RemoteSources: &source.Service{Resolve: sourceIdentity, HTTP: tc.read},
			}))}
			rec := httptest.NewRecorder()
			r := kittest.Request(t, http.MethodGet, "/artifactory/rpm-virtual/"+rpmAlphaPath)
			req := itemkit.ItemRequest{Key: "rpm-virtual", Path: rpmAlphaPath}
			svc.serveVirtualRemoteRPMPackage(rec, r, remote, req, sha,
				kittest.Admin, kittest.AdminAccess())
			wantError(t, rec, tc.status, tc.message)
		})
	}
}

// TestUnitRPMCalculateYumRepositoryMetadataChecksPathAndVirtualView: the
// YUM calculation takes a path only for a virtual repository, and only as a
// repository-relative repodata directory, and it verifies the virtual view
// that path names: a member cycle or an absent signing key is a server
// fault, never a calculation reported as done.
func TestUnitRPMCalculateYumRepositoryMetadataChecksPathAndVirtualView(t *testing.T) {
	w := newItemWorld(t)
	w.rpmRepos(
		repo.Config{Key: "rpm-member", Class: repo.Local},
		repo.Config{Key: "rpm-virtual", Class: repo.Virtual, Repositories: []string{"rpm-member"}},
		repo.Config{Key: "rpm-cycle", Class: repo.Virtual, Repositories: []string{"rpm-cycle"}},
		repo.Config{Key: "rpm-unsigned", Class: repo.Virtual, KeyPair: "absent"},
	)
	w.publish("rpm-member", "el/9/")
	ctx := accessapi.WithCaller(t.Context(), kittest.Admin, kittest.AdminAccess().Decider)
	const badPath = "YUM path must name a repository-relative repodata directory"
	for _, tc := range []struct {
		key, path string
		status    int
		message   string
	}{
		{
			"rpm-virtual", "el/9/repodata/", http.StatusOK,
			"YUM metadata for virtual repository rpm-virtual was calculated.",
		},
		{"rpm-virtual", "/el/9/repodata", http.StatusBadRequest, badPath},
		{"rpm-virtual", "el/9/pool", http.StatusBadRequest, badPath},
		{"rpm-cycle", "", http.StatusInternalServerError, "internal error"},
		{"rpm-unsigned", "", http.StatusInternalServerError, "internal error"},
		{
			"rpm-member", "repodata", http.StatusBadRequest,
			"RPM metadata path calculation is only valid on a virtual repository",
		},
	} {
		res, err := RPMAPI{w.kit}.CalculateYumRepositoryMetadata(ctx,
			artgen.CalculateYumRepositoryMetadataRequestObject{
				RepoKey: tc.key,
				Params:  artgen.CalculateYumRepositoryMetadataParams{Path: &tc.path},
			})
		if tc.status == http.StatusOK {
			done := artgen.CalculateYumRepositoryMetadata200TextResponse(tc.message)
			if err != nil || res != done {
				t.Errorf("%s at %q: answered %v, %v, want %q",
					tc.key, tc.path, res, err, tc.message)
			}
			continue
		}
		var api *apierror.Error
		if !errors.As(err, &api) || api.Status != tc.status || api.Message != tc.message {
			t.Errorf("%s at %q: answered %v, want %d %q",
				tc.key, tc.path, err, tc.status, tc.message)
		}
	}
}
