package nix_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	nixfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nix"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const (
	hash     = "fyli4q2yj6vh01bs0jxqbvj5hir5zvqc"
	native   = "/artifactory/api/nix/nix-local/"
	generic  = "/artifactory/nix-local/"
	narinfo  = hash + ".narinfo"
	folder   = "binary-cache/" + hash
	fixtures = "internal/artifacts/ecosystem/nix/testdata/cache/"
)

func hostedWorld(t *testing.T) *harness.ItemWorld {
	t.Helper()
	w := harness.NewItemWorld(t, false)
	if err := w.DB.Repos().Create(context.Background(), *repo.Defaults(&repo.Config{
		Key: "nix-local", Class: repo.Local, PackageType: repo.Nix,
	})); err != nil {
		t.Fatal(err)
	}
	return w
}

func wantBody(t *testing.T, r *httptest.ResponseRecorder, code int, text string) {
	t.Helper()
	harness.WantStatus(t, r, code)
	if !strings.Contains(r.Body.String(), text) {
		t.Fatalf("body %s, want %q", r.Body, text)
	}
}

func fixture(t *testing.T) (nixfmt.Info, []byte, []byte) {
	t.Helper()
	meta := repopath.Read(t, fixtures+narinfo)
	info, err := nixfmt.Parse(meta, narinfo)
	if err != nil {
		t.Fatal(err)
	}
	return info, meta, repopath.Read(t, fixtures+info.URL)
}

// nix copy --to stores a store path's narinfo and NAR in its folder under
// binary-cache, as 7.x does, so the storage API lists both there and the
// AQL query jf nix runs for a store path's NAR finds it (#1296).
func TestAccNixHostedStoresAStorePathInItsBinaryCacheFolder(t *testing.T) {
	w := hostedWorld(t)
	info, meta, archive := fixture(t)
	file := strings.TrimPrefix(info.URL, "nar/")
	harness.WantStatus(t, w.Do("PUT", native+info.URL, "admin", string(archive)), 201)
	harness.WantStatus(t, w.Do("PUT", native+narinfo, "admin", string(meta)), 201)
	listing := w.Do("GET", "/artifactory/api/storage/nix-local/"+folder, "admin", "")
	for _, child := range []string{narinfo, file} {
		wantBody(t, listing, 200, `"uri":"/`+child+`"`)
	}
	root := w.Do("GET", "/artifactory/api/storage/nix-local/", "admin", "")
	wantBody(t, root, 200, `"uri":"/binary-cache"`)
	if strings.Contains(root.Body.String(), `"uri":"/nar"`) ||
		strings.Contains(root.Body.String(), `"uri":"/`+narinfo+`"`) {
		t.Fatalf("the client's upload paths kept: %s", root.Body)
	}
	harness.WantStatus(t, w.Do("GET", "/artifactory/api/storage/nix-local/nar", "admin", ""), 404)
	// jf nix's query for the NAR of a store path (jfrog-cli-artifactory
	// artifactory/commands/nix/command.go, buildClosureAql).
	aql := `items.find({"repo":"nix-local","$or":[{"$and":[{"path":"` + folder +
		`"},{"name":{"$match":"*.nar.xz"}}]}]})`
	found := w.Do("POST", "/artifactory/api/search/aql", "admin", aql, "Content-Type", "text/plain")
	wantBody(t, found, 200, `"path": "`+folder+`",
      "name": "`+file+`"`)
	stored := w.Do("GET", generic+folder+"/"+narinfo, "admin", "")
	wantBody(t, stored, 200, "StorePath: "+info.StorePath)
	harness.WantStatus(t, w.Do("GET", generic+folder+"/"+file, "admin", ""), 200)
	wantBody(t, w.Do("GET", native+narinfo, "admin", ""), 200, "URL: nar/"+hash+"/"+file)
	// The item API stores what it is sent where it is sent, so a narinfo
	// deployed to the root, where a hosted repository stores none, is
	// refused, and one deployed to its folder is admitted.
	wantBody(
		t,
		w.Do("PUT", generic+narinfo, "admin", string(meta)),
		400,
		"invalid Nix archive path",
	)
	harness.WantStatus(t, w.Do("PUT", generic+folder+"/"+narinfo, "admin", string(meta)), 201)
}

// cut is data without the index and footer that end its one xz stream:
// the footer's backward size, plus the footer.
func cut(t *testing.T, data []byte) []byte {
	t.Helper()
	footer := data[len(data)-12:]
	if string(footer[10:]) != "YZ" {
		t.Fatal("no xz footer at the end")
	}
	return data[:len(data)-int(binary.LittleEndian.Uint32(footer[4:8])+1)*4-12]
}

func nar(data []byte) string {
	sum := sha256.Sum256(data)
	return "nar/" + hex.EncodeToString(sum[:]) + ".nar.xz"
}

// An xz stream that lost its index and footer is refused, as xz -t and
// Nix's decompressor refuse it, wherever it is stored: a NAR natively and
// through the item API, at the name its own SHA-256 gives and at the name
// of the whole file, and a channel's tarball (#1297). The whole files are
// still admitted.
func TestAccNixRefusesAnXZStreamWithoutItsIndex(t *testing.T) {
	w := hostedWorld(t)
	info, _, archive := fixture(t)
	short := string(cut(t, archive))
	for _, path := range []string{
		native + nar([]byte(short)),
		native + info.URL,
		generic + nar([]byte(short)),
		generic + folder + "/" + strings.TrimPrefix(nar([]byte(short)), "nar/"),
	} {
		wantBody(t, w.Do("PUT", path, "admin", short), 400, `"message":"invalid Nix archive"`)
	}
	harness.WantStatus(t, w.Do("PUT", native+info.URL, "admin", string(archive)), 201)
	var tarball bytes.Buffer
	tw := tar.NewWriter(&tarball)
	if err := tw.WriteHeader(
		&tar.Header{Name: "nixexprs/default.nix", Mode: 0o644, Size: 3},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	xw, err := xz.NewWriter(&compressed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = xw.Write(tarball.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err = xw.Close(); err != nil {
		t.Fatal(err)
	}
	whole := compressed.String()
	channel := string(cut(t, compressed.Bytes()))
	for _, path := range []string{
		native + "channels/stable/nixpkgs/first",
		generic + "channels/stable/nixpkgs/first/nixexprs.tar.xz",
	} {
		wantBody(t, w.Do("PUT", path, "admin", channel), 400, "invalid Nix channel archive")
	}
	harness.WantStatus(t, w.Do("PUT", native+"channels/stable/nixpkgs/first", "admin", whole), 201)
}
