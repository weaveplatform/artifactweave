package hex_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	hexfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/hex"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

func TestUnitHexLocalAndVirtualSignedRegistry(t *testing.T) {
	w := newItemWorld(t, false)
	keys := testKeys()
	if err := w.db.Keys().CreateKeypair(t.Context(), api.Keypair{
		Name: "hex-signing", Type: api.KeypairRSA,
		PrivateKey: keys.private, PublicKey: keys.public,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.Config{
		{Key: "hex-local", Class: repo.Local, PackageType: repo.Hex, PrimaryKeyPairRef: "hex-signing"},
		{Key: "hex-virtual", Class: repo.Virtual, PackageType: repo.Hex, KeyPair: "hex-signing", Repositories: []string{"hex-local"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	fixture := repopath.Read(t, "internal/artifacts/ecosystem/hex/testdata/decimal-2.0.0.tar")
	put := w.do(
		http.MethodPut,
		"/artifactory/hex-local/tarballs/decimal-2.0.0.tar",
		"admin",
		string(fixture),
	)
	if put.Code != http.StatusCreated {
		t.Fatalf("deploy %d: %s", put.Code, put.Body)
	}
	for _, key := range []string{"hex-local", "hex-virtual"} {
		prefix := "/artifactory/api/hex/" + key + "/"
		pub := w.do(http.MethodGet, prefix+"public_key", "reader", "")
		if pub.Code != http.StatusOK || pub.Body.String() != keys.public {
			t.Fatalf("%s key %d: %s", key, pub.Code, pub.Body)
		}
		parsed, err := hexfmt.ParsePublicKey(pub.Body.String())
		if err != nil {
			t.Fatal(err)
		}
		pack := w.do(http.MethodGet, prefix+"packages/decimal", "reader", "")
		if pack.Code != http.StatusOK {
			t.Fatalf("%s package %d: %s", key, pack.Code, pack.Body)
		}
		reg, err := hexfmt.DecodePackage(pack.Body.Bytes(), parsed)
		if err != nil || reg.Name != "decimal" || reg.Repository != key || len(reg.Releases) != 1 ||
			reg.Releases[0].Version != "2.0.0" {
			t.Fatalf("%s registry %+v: %v", key, reg, err)
		}
		got := w.do(http.MethodGet, prefix+"tarballs/decimal-2.0.0.tar", "reader", "")
		if got.Code != http.StatusOK || string(got.Body.Bytes()) != string(fixture) {
			t.Fatalf("%s tarball %d", key, got.Code)
		}
		head := w.do(http.MethodHead, prefix+"packages/decimal", "reader", "")
		if head.Code != http.StatusOK || head.Body.Len() != 0 ||
			head.Header().Get("Content-Length") == "" {
			t.Fatalf("%s HEAD package %d: %s", key, head.Code, head.Body)
		}
		denied := w.do(http.MethodGet, prefix+"packages/decimal", "user", "")
		if denied.Code != http.StatusForbidden {
			t.Fatalf("%s denied %d", key, denied.Code)
		}
	}
	wrong := w.do(
		http.MethodPut,
		"/artifactory/hex-local/tarballs/wrong-2.0.0.tar",
		"admin",
		string(fixture),
	)
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("identity mismatch %d: %s", wrong.Code, wrong.Body)
	}
	malformed := w.do(
		http.MethodPut,
		"/artifactory/hex-local/tarballs/decimal-3.0.0.tar",
		"admin",
		"bad",
	)
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed tarball %d: %s", malformed.Code, malformed.Body)
	}
	if derived := w.do(
		http.MethodPut,
		"/artifactory/hex-local/packages/decimal",
		"admin",
		"forged",
	); derived.Code != http.StatusConflict {
		t.Fatalf("derived metadata was writable: %d %s", derived.Code, derived.Body)
	}
	if misplaced := w.do(
		http.MethodPut,
		"/artifactory/hex-local/decimal-2.0.0.tar",
		"admin",
		string(fixture),
	); misplaced.Code != http.StatusBadRequest {
		t.Fatalf("noncanonical package path was accepted: %d %s", misplaced.Code, misplaced.Body)
	}
	path := "/artifactory/hex-local/tarballs/decimal-2.0.0.tar"
	if deleted := w.do(http.MethodDelete, path, "admin", ""); deleted.Code != http.StatusNoContent {
		t.Fatalf("delete %d: %s", deleted.Code, deleted.Body)
	}
	if missing := w.do(
		http.MethodGet,
		"/artifactory/api/hex/hex-local/packages/decimal",
		"reader",
		"",
	); missing.Code != http.StatusNotFound {
		t.Fatalf("deleted package remains listed %d: %s", missing.Code, missing.Body)
	}
	if restored := w.do(http.MethodPost,
		"/artifactory/api/trash/restore/hex-local/tarballs/decimal-2.0.0.tar?to=hex-local",
		"admin", ""); restored.Code != http.StatusAccepted {
		t.Fatalf("restore %d: %s", restored.Code, restored.Body)
	}
	if got := w.do(
		http.MethodGet,
		"/artifactory/api/hex/hex-local/packages/decimal",
		"reader",
		"",
	); got.Code != http.StatusOK {
		t.Fatalf("restored package %d: %s", got.Code, got.Body)
	}
	c, err := w.db.Repos().Get(t.Context(), "hex-local")
	if err != nil {
		t.Fatal(err)
	}
	if err := (hexfmt.Handler{}).Reindex(
		t.Context(),
		format.Env{Store: w.db, Blobs: w.blobs},
		c,
	); err != nil {
		t.Fatal(err)
	}
	packages, _, err := w.db.Packages().
		List(t.Context(), "hex-local", kernel.Page{Limit: kernel.MaxLimit})
	if err != nil || len(packages) != 1 || packages[0].Name != "decimal" {
		t.Fatalf("reindexed packages: %+v %v", packages, err)
	}
	versions, err := w.db.Packages().Versions(t.Context(), packages[0].ID)
	if err != nil || len(versions) != 1 || versions[0].Version != "2.0.0" {
		t.Fatalf("reindexed versions: %+v %v", versions, err)
	}
	// Registry indexes must reflect the readable packages after lifecycle
	// operations, with duplicate virtual releases resolved by member order.
	ecto := repopath.Read(t, "internal/artifacts/ecosystem/hex/testdata/ecto-3.12.5.tar")
	if put := w.do(http.MethodPut, "/artifactory/hex-local/tarballs/ecto-3.12.5.tar",
		"admin", string(ecto)); put.Code != http.StatusCreated {
		t.Fatalf("deploy Ecto %d: %s", put.Code, put.Body)
	}
	// A legacy non-package object in the store must not become registry data.
	if _, err := w.db.Items().Put(t.Context(), model.Item{
		RepoKey: "hex-local", Name: "notes.txt",
	}); err != nil {
		t.Fatal(err)
	}
	second := *repo.Defaults(&repo.Config{
		Key: "hex-second", Class: repo.Local, PackageType: repo.Hex,
		PrimaryKeyPairRef: "hex-signing",
	})
	if err := w.db.Repos().Create(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if put := w.do(http.MethodPut, "/artifactory/hex-second/tarballs/decimal-2.0.0.tar",
		"admin", string(fixture)); put.Code != http.StatusCreated {
		t.Fatalf("deploy duplicate Decimal %d: %s", put.Code, put.Body)
	}
	virtual, err := w.db.Repos().Get(t.Context(), "hex-virtual")
	if err != nil {
		t.Fatal(err)
	}
	virtual.Repositories = []string{"hex-local", "hex-second"}
	if err := w.db.Repos().Update(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	public, err := hexfmt.ParsePublicKey(keys.public)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"hex-local", "hex-virtual"} {
		prefix := "/artifactory/api/hex/" + key + "/"
		decimal := w.do(http.MethodGet, prefix+"packages/decimal", "reader", "")
		decimalRegistry, err := hexfmt.DecodePackage(decimal.Body.Bytes(), public)
		if decimal.Code != http.StatusOK || err != nil || decimalRegistry.Name != "decimal" ||
			len(decimalRegistry.Releases) != 1 {
			t.Fatalf("%s package escaped filtering: %d, %+v %v", key,
				decimal.Code, decimalRegistry, err)
		}
		names := w.do(http.MethodGet, prefix+"names", "reader", "")
		gotRepo, gotNames, err := hexfmt.DecodeNames(names.Body.Bytes(), public)
		if names.Code != http.StatusOK || err != nil || gotRepo != key ||
			!slices.Equal(gotNames, []string{"decimal", "ecto"}) {
			t.Fatalf("%s names %d: %s, %q %v", key, names.Code, names.Body, gotNames, err)
		}
		index := w.do(http.MethodGet, prefix+"versions", "reader", "")
		gotRepo, gotPackages, err := hexfmt.DecodeVersions(index.Body.Bytes(), public)
		if index.Code != http.StatusOK || err != nil || gotRepo != key ||
			len(gotPackages) != 2 || gotPackages[0].Name != "decimal" ||
			len(gotPackages[0].Releases) != 1 || gotPackages[1].Name != "ecto" ||
			len(gotPackages[1].Releases) != 1 {
			t.Fatalf("%s versions %d: %s, %+v %v", key, index.Code, index.Body,
				gotPackages, err)
		}
	}
}

func TestUnitHexInvalidRoutesAndRepositoryConfigurationFailClosed(t *testing.T) {
	w := newItemWorld(t, false)
	keys := testKeys()
	for _, pair := range []api.Keypair{
		{Name: "hex-signing", PrivateKey: keys.private, PublicKey: keys.public},
		{Name: "hex-garbled", PrivateKey: "not an RSA key", PublicKey: keys.public},
	} {
		pair.Type = api.KeypairRSA
		if err := w.db.Keys().CreateKeypair(t.Context(), pair); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []repo.Config{
		{Key: "hex-no-key", Class: repo.Local, PackageType: repo.Hex},
		{
			Key: "hex-garbled-key", Class: repo.Local, PackageType: repo.Hex,
			PrimaryKeyPairRef: "hex-garbled",
		},
		{
			Key: "hex-garbage-local", Class: repo.Local, PackageType: repo.Hex,
			PrimaryKeyPairRef: "hex-signing",
		},
		{
			Key: "hex-plain-remote", Class: repo.Remote, PackageType: repo.Hex,
			URL: "https://up.example/",
		},
		{
			Key: "hex-remote-member", Class: repo.Virtual, PackageType: repo.Hex,
			Repositories: []string{"hex-plain-remote"}, KeyPair: "hex-signing",
		},
		{
			Key: "hex-missing-key", Class: repo.Local, PackageType: repo.Hex,
			PrimaryKeyPairRef: "missing",
		},
		{
			Key: "hex-broken-local", Class: repo.Local, PackageType: repo.Hex,
			PrimaryKeyPairRef: "hex-signing",
		},
		{
			Key: "hex-mismatch-local", Class: repo.Local, PackageType: repo.Hex,
			PrimaryKeyPairRef: "hex-signing",
		},
		{
			Key: "hex-bad-member", Class: repo.Virtual, PackageType: repo.Hex,
			Repositories: []string{"libs-local"},
		},
		{
			Key: "hex-duplicate-member", Class: repo.Virtual, PackageType: repo.Hex,
			Repositories: []string{"hex-no-key", "hex-no-key"},
		},
		{
			Key: "hex-missing-member", Class: repo.Virtual, PackageType: repo.Hex,
			Repositories: []string{"missing"}, KeyPair: "hex-signing",
		},
		{
			Key: "hex-empty-virtual", Class: repo.Virtual, PackageType: repo.Hex,
			Repositories: []string{"hex-no-key"}, KeyPair: "hex-signing",
		},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.db.Items().Put(t.Context(), model.Item{
		RepoKey: "hex-broken-local", Name: "tarballs/decimal-2.0.0.tar",
		Actual: kernel.Checksums{SHA256: strings.Repeat("a", 64)},
	}); err != nil {
		t.Fatal(err)
	}
	fixture := repopath.Read(t, "internal/artifacts/ecosystem/hex/testdata/decimal-2.0.0.tar")
	upload, err := w.blobs.BeginUpload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Append(t.Context(), bytes.NewReader(fixture)); err != nil {
		t.Fatal(err)
	}
	info, err := upload.Commit(t.Context(), blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Items().Put(t.Context(), model.Item{
		RepoKey: "hex-mismatch-local", Name: "tarballs/other-2.0.0.tar",
		Actual: kernel.Checksums{SHA256: info.Digests.SHA256},
	}); err != nil {
		t.Fatal(err)
	}
	garbage, err := w.blobs.BeginUpload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := garbage.Append(t.Context(), strings.NewReader("not a tarball")); err != nil {
		t.Fatal(err)
	}
	garbageInfo, err := garbage.Commit(t.Context(), blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Items().Put(t.Context(), model.Item{
		RepoKey: "hex-garbage-local", Name: "tarballs/decimal-2.0.0.tar",
		Actual: kernel.Checksums{SHA256: garbageInfo.Digests.SHA256},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path     string
		expected int
	}{
		{"hex-no-key/public_key", http.StatusServiceUnavailable},
		{"hex-no-key/names", http.StatusServiceUnavailable},
		{"hex-missing-key/public_key", http.StatusServiceUnavailable},
		{"hex-garbled-key/public_key", http.StatusServiceUnavailable},
		{"hex-broken-local/packages/decimal", http.StatusInternalServerError},
		{"hex-garbage-local/packages/decimal", http.StatusInternalServerError},
		{"hex-remote-member/versions", http.StatusBadRequest},
		{"hex-remote-member/tarballs/decimal-2.0.0.tar", http.StatusBadRequest},
		{"hex-mismatch-local/packages/other", http.StatusInternalServerError},
		{"hex-no-key/packages/../secret", http.StatusNotFound},
		{"hex-no-key/packages/not!name", http.StatusNotFound},
		{"hex-no-key/versions?sort=asc", http.StatusBadRequest},
		{"hex-bad-member/versions", http.StatusBadRequest},
		{"hex-bad-member/tarballs/decimal-2.0.0.tar", http.StatusBadRequest},
		{"hex-duplicate-member/versions", http.StatusBadRequest},
		{"hex-duplicate-member/tarballs/decimal-2.0.0.tar", http.StatusBadRequest},
		{"hex-missing-member/packages/decimal", http.StatusNotFound},
		{"hex-missing-member/tarballs/decimal-2.0.0.tar", http.StatusNotFound},
		{"hex-empty-virtual/tarballs/decimal-2.0.0.tar", http.StatusNotFound},
	} {
		got := w.do(http.MethodGet, "/artifactory/api/hex/"+tc.path, "admin", "")
		if got.Code != tc.expected {
			t.Errorf("%s: got %d want %d: %s", tc.path, got.Code, tc.expected, got.Body)
		}
	}
	if got := w.do(http.MethodGet, "/artifactory/api/hex/hex-garbled-key/public_key", "user",
		""); got.Code != http.StatusForbidden {
		t.Errorf("public key without read: got %d: %s", got.Code, got.Body)
	}
}

func TestUnitHexFederatedClassRefused(t *testing.T) {
	if slices.Contains(repo.AllowedPackageTypes(repo.Federated), repo.Hex) {
		t.Fatal("federated Hex class advertised")
	}
	w := newItemWorld(t, false)
	create := w.do(http.MethodPut, "/artifactory/api/repositories/hex-federated-create", "admin",
		`{"rclass":"federated","packageType":"hex"}`)
	if create.Code != http.StatusBadRequest {
		t.Fatalf("federated create %d: %s", create.Code, create.Body)
	}
	c := *repo.Defaults(&repo.Config{Key: "hex-federated", Class: repo.Federated, PackageType: repo.Hex})
	// Even if an older store contains a now-unsupported class, the native
	// protocol must fail the class gate instead of serving generic items.
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	got := w.do(http.MethodGet, "/artifactory/api/hex/hex-federated/packages/decimal", "admin", "")
	if got.Code != http.StatusNotImplemented {
		t.Fatalf("federated route %d: %s", got.Code, got.Body)
	}
	fixture := repopath.Read(t, "internal/artifacts/ecosystem/hex/testdata/decimal-2.0.0.tar")
	write := w.do(http.MethodPut, "/artifactory/hex-federated/tarballs/decimal-2.0.0.tar",
		"admin", string(fixture))
	if write.Code != http.StatusBadRequest {
		t.Fatalf("federated package bypassed Hex admission: %d %s", write.Code, write.Body)
	}
}

// hexRefusal checks the download policy's 404 in Artifactory's envelope,
// with none of the success answer's headers.
func hexRefusal(t *testing.T, label string, got *httptest.ResponseRecorder, message string) {
	t.Helper()
	want := `{"errors":[{"message":"` + message + `","status":404}]}` + "\n"
	if got.Code != http.StatusNotFound || got.Body.String() != want ||
		got.Header().Get("Cache-Control") != "" {
		t.Errorf("%s: %d %q (Cache-Control %q), want 404 %q", label, got.Code, got.Body,
			got.Header().Get("Cache-Control"), want)
	}
}

func hexBlackedOut(key, path string) string {
	return "The repository '" + key + "' is blacked out and cannot serve artifact '" +
		key + ":" + path + "'."
}

func hexRejected(key, path string) string {
	return "The repository '" + key + "' rejected the artifact '" + key + ":" + path +
		"' due to its include/exclude pattern settings."
}

func hexNoRepository(key, path string) string {
	return "No repository found to serve the request for " + key + ":" + path
}

// patchHexRepo changes a repository's configuration in place.
func patchHexRepo(t *testing.T, w *itemWorld, key string, change func(*repo.Config)) {
	t.Helper()
	c, err := w.db.Repos().Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	change(&c)
	if err := w.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

// hexListing decodes a signed answer to path, names or packages/NAME, with
// key and checks it lists want: package names for names, the versions of a
// package's releases otherwise.
func hexListing(
	t *testing.T,
	label, path string,
	got *httptest.ResponseRecorder,
	key string,
	want ...string,
) []hexfmt.Package {
	t.Helper()
	public, err := hexfmt.ParsePublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Code != http.StatusOK {
		t.Errorf("%s: %d %s, want 200", label, got.Code, got.Body)
		return nil
	}
	var listed []string
	var releases []hexfmt.Package
	if path == "names" {
		_, listed, err = hexfmt.DecodeNames(got.Body.Bytes(), public)
	} else {
		var reg hexfmt.RegistryPackage
		reg, err = hexfmt.DecodePackage(got.Body.Bytes(), public)
		releases = reg.Releases
		for _, release := range reg.Releases {
			listed = append(listed, release.Version)
		}
	}
	if err != nil || !slices.Equal(listed, want) {
		t.Errorf("%s lists %q (%v), want %q", label, listed, err, want)
	}
	return releases
}

// TestUnitHexDownloadPolicyOfLocalAndVirtualRepositories: a blacked-out
// local repository refuses names, versions and packages/NAME with the
// blackout's 404 naming each path, after read permission; a pattern on a
// document's path refuses it, and one on a tarball leaves its release
// listed. A virtual repository leaves out a member that is blacked out or
// whose patterns reject the document's path, keeps a member that rejects
// only a tarball, and answers its own patterns on a document or a tarball
// with "No repository found to serve the request" (#1237).
func TestUnitHexDownloadPolicyOfLocalAndVirtualRepositories(t *testing.T) {
	w := newItemWorld(t, false)
	keys := testKeys()
	if err := w.db.Keys().CreateKeypair(t.Context(), api.Keypair{
		Name: "hex-signing", Type: api.KeypairRSA,
		PrivateKey: keys.private, PublicKey: keys.public,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.Config{
		{Key: "hex-a", Class: repo.Local, PrimaryKeyPairRef: "hex-signing"},
		{Key: "hex-b", Class: repo.Local, PrimaryKeyPairRef: "hex-signing"},
		{Key: "hex-v", Class: repo.Virtual, KeyPair: "hex-signing", Repositories: []string{"hex-a", "hex-b"}},
	} {
		c.PackageType = repo.Hex
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for key, file := range map[string]string{"hex-a": "decimal-2.0.0.tar", "hex-b": "ecto-3.12.5.tar"} {
		body := repopath.Read(t, "internal/artifacts/ecosystem/hex/testdata/"+file)
		if put := w.do(http.MethodPut, "/artifactory/"+key+"/tarballs/"+file, "admin",
			string(body)); put.Code != http.StatusCreated {
			t.Fatalf("deploy %s: %d %s", file, put.Code, put.Body)
		}
	}
	get := func(key, path string) *httptest.ResponseRecorder {
		return w.do(http.MethodGet, "/artifactory/api/hex/"+key+"/"+path, "reader", "")
	}
	const tarball = "tarballs/decimal-2.0.0.tar"
	patchHexRepo(t, w, "hex-a", func(c *repo.Config) { c.BlackedOut = new(true) })
	for _, path := range []string{"names", "versions", "packages/decimal"} {
		hexRefusal(t, "blacked out "+path, get("hex-a", path), hexBlackedOut("hex-a", path))
	}
	if got := w.do(http.MethodHead, "/artifactory/api/hex/hex-a/packages/decimal", "reader",
		""); got.Code != http.StatusNotFound || got.Header().Get("Cache-Control") != "" {
		t.Errorf("blacked out HEAD: %d %v", got.Code, got.Header())
	}
	if got := w.do(http.MethodGet, "/artifactory/api/hex/hex-a/names", "user",
		""); got.Code != http.StatusForbidden {
		t.Errorf("blacked out names, no permission: %d %s", got.Code, got.Body)
	}
	// public_key is not a 7.x Hex route: 7.x serves the key from
	// /api/security/keypair/public/repositories/<repo>.
	if got := get("hex-a", "public_key"); got.Code != http.StatusOK {
		t.Errorf("blacked out public_key: %d %s", got.Code, got.Body)
	}
	hexListing(
		t,
		"virtual past a blacked-out member, names",
		"names",
		get("hex-v", "names"),
		keys.public,
		"ecto",
	)
	if got := get("hex-v", "packages/decimal"); got.Code != http.StatusNotFound ||
		!strings.Contains(got.Body.String(), "Hex package not found") {
		t.Errorf("virtual past a blacked-out member, package: %d %s", got.Code, got.Body)
	}
	patchHexRepo(t, w, "hex-a", func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = new(false), new(tarball)
	})
	hexListing(
		t,
		"excluded tarball's package",
		"packages/decimal",
		get("hex-a", "packages/decimal"),
		keys.public,
		"2.0.0",
	)
	hexListing(
		t,
		"excluded tarball's names",
		"names",
		get("hex-a", "names"),
		keys.public,
		"decimal",
	)
	hexRefusal(t, "excluded tarball", get("hex-a", tarball), hexRejected("hex-a", tarball))
	hexListing(
		t,
		"virtual over an excluded tarball",
		"packages/decimal",
		get("hex-v", "packages/decimal"),
		keys.public,
		"2.0.0",
	)
	patchHexRepo(t, w, "hex-a", func(c *repo.Config) { c.ExcludesPattern = new("packages/**") })
	hexRefusal(t, "excluded package", get("hex-a", "packages/decimal"),
		hexRejected("hex-a", "packages/decimal"))
	hexListing(
		t,
		"names beside an excluded package",
		"names",
		get("hex-a", "names"),
		keys.public,
		"decimal",
	)
	if got := get("hex-a", "versions"); got.Code != http.StatusOK {
		t.Errorf("versions beside an excluded package: %d %s", got.Code, got.Body)
	}
	if got := get("hex-v", "packages/decimal"); got.Code != http.StatusNotFound ||
		!strings.Contains(got.Body.String(), "Hex package not found") {
		t.Errorf("virtual past a member that excludes the package: %d %s", got.Code, got.Body)
	}
	hexListing(t, "virtual names beside an excluded package", "names", get("hex-v", "names"),
		keys.public, "decimal", "ecto")
	patchHexRepo(t, w, "hex-a", func(c *repo.Config) { c.ExcludesPattern = new("names") })
	hexRefusal(t, "excluded names", get("hex-a", "names"), hexRejected("hex-a", "names"))
	hexListing(t, "virtual past a member that excludes names", "names", get("hex-v", "names"),
		keys.public, "ecto")
	patchHexRepo(t, w, "hex-a", func(c *repo.Config) { c.ExcludesPattern = new("") })
	patchHexRepo(t, w, "hex-v", func(c *repo.Config) { c.ExcludesPattern = new("packages/**") })
	hexRefusal(t, "virtual's own pattern on a package", get("hex-v", "packages/decimal"),
		hexNoRepository("hex-v", "packages/decimal"))
	hexListing(t, "virtual names beside its own pattern", "names", get("hex-v", "names"),
		keys.public, "decimal", "ecto")
	patchHexRepo(t, w, "hex-v", func(c *repo.Config) { c.ExcludesPattern = new(tarball) })
	hexRefusal(t, "virtual's own pattern on a tarball", get("hex-v", tarball),
		hexNoRepository("hex-v", tarball))
	hexListing(t, "virtual package beside its own pattern on a tarball", "packages/decimal",
		get("hex-v", "packages/decimal"), keys.public, "2.0.0")
}
