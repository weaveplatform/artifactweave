package hex_test

import (
	"bytes"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	hexfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/hex"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/keymaterial"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// A remote Hex tarball whose publication meets a statement the database
// failed to run, as Hex's Deployed links the release into the package
// index, answers 500 "internal error" and caches nothing; the circuit is
// untouched, and once the database runs the statement again the next
// request fetches the tarball again. It answered 502 "Bad Gateway", which
// a bad upstream tarball still answers (#1470). The runtime's own SQLite
// database refuses the statement, through a trigger, so the failure comes
// up through the SQL store as a driver's does.
func TestAccRemoteHexTarballAnswersAStoreFailureUnderItsHookAsAServerFault(t *testing.T) {
	const tarball = "/artifactory/api/hex/remote/tarballs/decimal-2.0.0.tar"
	ctx := t.Context()
	keys := testKeys()
	signer, err := keymaterial.RSAPrivateKey(keys.private, "")
	if err != nil {
		t.Fatal(err)
	}
	fixture := mustReadHexFixture(t)
	pkg, err := hexfmt.InspectTarball(bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := hexfmt.EncodePackage(hexfmt.RegistryPackage{
		Name: "decimal", Repository: "hexpm", Releases: []hexfmt.Package{pkg},
	}, signer)
	if err != nil {
		t.Fatal(err)
	}
	w := harness.NewRemoteWorld(t)
	if err = w.Runtime.Store.Keys().CreateKeypair(ctx, api.Keypair{
		Name: "hex-key", Type: api.KeypairRSA, PrivateKey: keys.private, PublicKey: keys.public,
	}); err != nil {
		t.Fatal(err)
	}
	w.Repo = *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: repo.Hex, URL: w.Upstream.URL,
		HexPublicKey: keys.public, PrimaryKeyPairRef: "hex-key",
	})
	w.Update()
	w.Upstream.Set("/packages/decimal", fakeupstream.Resource{Body: registry})
	w.Upstream.Set("/tarballs/decimal-2.0.0.tar", fakeupstream.Resource{Body: fixture})
	harness.WantStatus(t, w.Request(http.MethodGet, "/artifactory/api/hex/remote/packages/decimal",
		"reader", ""), http.StatusOK)

	db := w.Runtime.Store.DB()
	if _, err = db.ExecContext(ctx, `CREATE TRIGGER refuse_packages BEFORE INSERT ON packages
		BEGIN SELECT RAISE(ABORT, 'statement refused'); END`); err != nil {
		t.Fatal(err)
	}
	rec := w.Request(http.MethodGet, tarball, "reader", "")
	if rec.Code != http.StatusInternalServerError ||
		rec.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
		t.Fatalf("refused statement: %d %s", rec.Code, rec.Body)
	}
	if _, err = w.Runtime.Store.Items().Get(ctx, "remote", true, "tarballs/decimal-2.0.0.tar"); !errors.Is(
		err, kernel.ErrNotFound) {
		t.Fatalf("the tarball was cached: %v", err)
	}
	state, err := w.Runtime.Store.RemoteCache().State(ctx, "remote")
	if err != nil || state.ConnectionFailures != 0 {
		t.Fatalf("circuit %d: %v", state.ConnectionFailures, err)
	}

	if _, err = db.ExecContext(ctx, `DROP TRIGGER refuse_packages`); err != nil {
		t.Fatal(err)
	}
	rec = w.Request(http.MethodGet, tarball, "reader", "")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), fixture) {
		t.Fatalf("cured: %d %d bytes", rec.Code, rec.Body.Len())
	}
	if got := w.Upstream.Count(http.MethodGet, "/tarballs/decimal-2.0.0.tar"); got != 2 {
		t.Fatalf("upstream fetched the tarball %d times, want 2", got)
	}
}
