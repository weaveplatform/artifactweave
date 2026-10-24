package server

import (
	"net/http"
	"os"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
	hexfmt "github.com/weaveplatform/artifactweave/internal/format/hex"
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
}
