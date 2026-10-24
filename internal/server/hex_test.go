package server

import (
	"bytes"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	hexfmt "github.com/weaveplatform/artifactweave/internal/format/hex"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestHexLocalAndVirtualSignedRegistry(t *testing.T) {
	w := newItemWorld(t, false)
	keys := testKeys()
	if err := w.db.Keys().CreateKeypair(t.Context(), types.Keypair{
		Name: "hex-signing", Type: types.KeypairRSA,
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
	fixture, err := os.ReadFile("../format/hex/testdata/decimal-2.0.0.tar")
	if err != nil {
		t.Fatal(err)
	}
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
		List(t.Context(), "hex-local", store.Page{Limit: store.MaxLimit})
	if err != nil || len(packages) != 1 || packages[0].Name != "decimal" {
		t.Fatalf("reindexed packages: %+v %v", packages, err)
	}
	versions, err := w.db.Packages().Versions(t.Context(), packages[0].ID)
	if err != nil || len(versions) != 1 || versions[0].Version != "2.0.0" {
		t.Fatalf("reindexed versions: %+v %v", versions, err)
	}
	// Registry indexes must reflect the readable packages after lifecycle
	// operations, with duplicate virtual releases resolved by member order.
	ecto, err := os.ReadFile("../format/hex/testdata/ecto-3.12.5.tar")
	if err != nil {
		t.Fatal(err)
	}
	if put := w.do(http.MethodPut, "/artifactory/hex-local/tarballs/ecto-3.12.5.tar",
		"admin", string(ecto)); put.Code != http.StatusCreated {
		t.Fatalf("deploy Ecto %d: %s", put.Code, put.Body)
	}
	// A legacy non-package object in the store must not become registry data.
	if _, err := w.db.Items().Put(t.Context(), types.Item{
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

func TestHexInvalidRoutesAndRepositoryConfigurationFailClosed(t *testing.T) {
	w := newItemWorld(t, false)
	keys := testKeys()
	if err := w.db.Keys().CreateKeypair(t.Context(), types.Keypair{
		Name: "hex-signing", Type: types.KeypairRSA,
		PrivateKey: keys.private, PublicKey: keys.public,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.Config{
		{Key: "hex-no-key", Class: repo.Local, PackageType: repo.Hex},
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
	if _, err := w.db.Items().Put(t.Context(), types.Item{
		RepoKey: "hex-broken-local", Name: "tarballs/decimal-2.0.0.tar",
		Actual: types.Checksums{SHA256: strings.Repeat("a", 64)},
	}); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../format/hex/testdata/decimal-2.0.0.tar")
	if err != nil {
		t.Fatal(err)
	}
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
	if _, err := w.db.Items().Put(t.Context(), types.Item{
		RepoKey: "hex-mismatch-local", Name: "tarballs/other-2.0.0.tar",
		Actual: types.Checksums{SHA256: info.Digests.SHA256},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"hex-no-key/public_key", http.StatusServiceUnavailable},
		{"hex-no-key/names", http.StatusServiceUnavailable},
		{"hex-missing-key/public_key", http.StatusServiceUnavailable},
		{"hex-broken-local/packages/decimal", http.StatusInternalServerError},
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
		if got.Code != tc.want {
			t.Errorf("%s: got %d want %d: %s", tc.path, got.Code, tc.want, got.Body)
		}
	}
}

func TestHexFederatedClassRefused(t *testing.T) {
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
	fixture, err := os.ReadFile("../format/hex/testdata/decimal-2.0.0.tar")
	if err != nil {
		t.Fatal(err)
	}
	write := w.do(http.MethodPut, "/artifactory/hex-federated/tarballs/decimal-2.0.0.tar",
		"admin", string(fixture))
	if write.Code != http.StatusBadRequest {
		t.Fatalf("federated package bypassed Hex admission: %d %s", write.Code, write.Body)
	}
}
