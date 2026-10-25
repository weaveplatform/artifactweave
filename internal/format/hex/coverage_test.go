package hex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitHexSignedRegistryPreservesAuthenticTransitiveRequirements(t *testing.T) {
	blobs := hexCoverageBlobs{}
	ecto, _, _ := hexCoverageArchive(t, blobs, "ecto-3.12.5.tar")
	key := testKey()
	reg := RegistryPackage{
		Name: ecto.Name, Repository: "hex-remote",
		Releases: []Package{ecto},
	}
	signed, err := EncodePackage(reg, key)
	if err != nil {
		t.Fatalf("sign authentic Ecto release: %v", err)
	}
	decoded, err := DecodePackage(signed, &key.PublicKey)
	if err != nil || decoded.Name != ecto.Name || decoded.Repository != reg.Repository ||
		len(decoded.Releases) != 1 || len(decoded.Releases[0].Requirements) != 3 {
		t.Fatalf("authentic dependencies lost on signed proxy metadata: %+v, %v", decoded, err)
	}
	for _, want := range ecto.Requirements {
		matched := false
		for _, got := range decoded.Releases[0].Requirements {
			if got.Package == want.Package && got.Requirement == want.Requirement &&
				got.Optional == want.Optional && got.Repository == want.Repository {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("signed metadata lost dependency %+v", want)
		}
	}
	invalid := reg
	bad := ecto
	bad.Requirements = append([]Requirement(nil), ecto.Requirements...)
	bad.Requirements[0].Package = "../secret"
	invalid.Releases = []Package{bad}
	if _, err := EncodePackage(invalid, key); err == nil {
		t.Fatal("signed metadata admitted an unsafe dependency")
	}
	if _, err := EncodePackage(reg, nil); err == nil {
		t.Fatal("signed metadata accepted a missing signing key")
	}
	pkcs1 := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PUBLIC KEY",
		Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey),
	})
	parsed, err := ParsePublicKey(string(pkcs1))
	if err != nil || parsed.N.Cmp(key.PublicKey.N) != 0 {
		t.Fatalf("Hex RSA public-key form rejected: %v", err)
	}
}

func TestUnitHexSignedRegistryRejectsAmbiguousOrForgedMetadata(t *testing.T) {
	blobs := hexCoverageBlobs{}
	pkg, _, _ := hexCoverageArchive(t, blobs, "decimal-2.0.0.tar")
	key := testKey()
	release := appendString(nil, 1, pkg.Version)
	release = appendBytes(release, 2, pkg.InnerChecksum[:])
	release = appendBytes(release, 5, pkg.OuterChecksum[:])
	packagePayload := func(rows ...[]byte) []byte {
		var payload []byte
		for _, row := range rows {
			payload = appendBytes(payload, 1, row)
		}
		payload = appendString(payload, 2, pkg.Name)
		return appendString(payload, 3, "hexpm")
	}
	badIndex := protowire.AppendTag(bytes.Clone(release), 6, protowire.VarintType)
	badIndex = protowire.AppendVarint(badIndex, 0)
	for name, payload := range map[string][]byte{
		"duplicate release":      packagePayload(release, release),
		"unbacked advisory":      packagePayload(badIndex),
		"malformed retirement":   packagePayload(appendBytes(bytes.Clone(release), 4, []byte{0xff})),
		"malformed publish time": packagePayload(appendBytes(bytes.Clone(release), 7, []byte{0xff})),
		"duplicate package name": appendString(packagePayload(release), 2, pkg.Name),
	} {
		signed, err := sign(payload, key)
		if err != nil {
			t.Fatalf("sign %s: %v", name, err)
		}
		if _, err := DecodePackage(signed, &key.PublicKey); err == nil {
			t.Errorf("accepted correctly signed but %s metadata", name)
		}
	}
	for name, poisoned := range map[string]func(*Package){
		"invalid retirement":      func(p *Package) { p.Retirement = []byte{0xff} },
		"invalid publish time":    func(p *Package) { p.PublishedAt = []byte{0xff} },
		"unbacked advisory index": func(p *Package) { p.AdvisoryIndexes = []uint64{0} },
	} {
		bad := pkg
		poisoned(&bad)
		if _, err := EncodePackage(RegistryPackage{
			Name: pkg.Name, Repository: "hexpm", Releases: []Package{bad},
		}, key); err == nil {
			t.Errorf("re-signed %s in an authentic release", name)
		}
	}
	if _, err := EncodePackage(RegistryPackage{
		Name: pkg.Name, Repository: "hexpm", Releases: []Package{pkg},
		Advisories: [][]byte{{0xff}},
	}, key); err == nil {
		t.Fatal("re-signed a malformed security advisory")
	}
	if _, ok := TarballPath("../decimal", pkg.Version); ok {
		t.Fatal("unsafe package path was constructible")
	}
	if _, ok := TarballPath(pkg.Name, "../2.0.0"); ok {
		t.Fatal("unsafe version path was constructible")
	}
}

func TestUnitHexSignedIndexesRejectAmbiguousWireFields(t *testing.T) {
	key := testKey()
	badType := protowire.AppendTag(nil, 1, protowire.VarintType)
	badType = protowire.AppendVarint(badType, 1)
	badType = appendString(badType, 2, "hexpm")
	duplicateRepository := appendString(nil, 2, "hexpm")
	duplicateRepository = appendString(duplicateRepository, 2, "other")
	duplicateName := appendString(nil, 1, "decimal")
	duplicateName = appendString(duplicateName, 1, "telemetry")
	duplicateName = appendBytes(nil, 1, duplicateName)
	duplicateName = appendString(duplicateName, 2, "hexpm")
	for name, payload := range map[string][]byte{
		"wrong top-level wire type": badType,
		"ambiguous repository":      duplicateRepository,
		"ambiguous package name":    duplicateName,
	} {
		signed, err := sign(payload, key)
		if err != nil {
			t.Fatalf("sign %s: %v", name, err)
		}
		if _, _, err := DecodeNames(signed, &key.PublicKey); err == nil {
			t.Errorf("names index accepted %s", name)
		}
		if _, _, err := DecodeVersions(signed, &key.PublicKey); err == nil {
			t.Errorf("versions index accepted %s", name)
		}
	}
}

type hexCoverageBlob struct{ *bytes.Reader }

func (*hexCoverageBlob) Close() error { return nil }

type hexCoverageBlobs map[string][]byte

func (b hexCoverageBlobs) Open(
	_ context.Context,
	digest string,
) (io.ReadSeekCloser, blob.Info, error) {
	data, ok := b[digest]
	if !ok {
		return nil, blob.Info{}, store.ErrNotFound
	}
	return &hexCoverageBlob{bytes.NewReader(data)}, blob.Info{Size: int64(len(data))}, nil
}

func (b hexCoverageBlobs) Stat(_ context.Context, digest string) (blob.Info, error) {
	data, ok := b[digest]
	if !ok {
		return blob.Info{}, store.ErrNotFound
	}
	return blob.Info{Size: int64(len(data))}, nil
}

func hexCoverageArchive(
	t *testing.T,
	blobs hexCoverageBlobs,
	filename string,
) (Package, string, int64) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", filename))
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := InspectTarball(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	key := hex.EncodeToString(digest[:])
	blobs[key] = data
	return pkg, key, int64(len(data))
}

func hexCoverageItem(
	t *testing.T,
	st *inmem.Store,
	c repo.Config,
	pkg Package,
	sha string,
	size int64,
) types.Item {
	t.Helper()
	p, ok := TarballPath(pkg.Name, pkg.Version)
	if !ok {
		t.Fatalf("invalid authentic Hex package identity: %+v", pkg)
	}
	it, err := st.Items().Put(t.Context(), types.Item{
		RepoKey: c.Key, Path: "tarballs", Name: p[len("tarballs/"):],
		Size: size, Actual: types.Checksums{SHA256: sha}, Created: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func TestUnitHexAdmissionAuthenticatesPackageBytesAndClass(t *testing.T) {
	ctx := t.Context()
	h := Handler{}
	if !Serves(repo.Hex) || Serves(repo.Maven) {
		t.Fatal("Hex handler selection did not honor package type")
	}
	c := *repo.Defaults(&repo.Config{Key: "hex-local", Class: repo.Local, PackageType: repo.Hex})
	st := inmem.New()
	blobs := hexCoverageBlobs{}
	pkg, sha, size := hexCoverageArchive(t, blobs, "decimal-2.0.0.tar")
	p, _ := TarballPath(pkg.Name, pkg.Version)
	w := format.Write{
		Op: format.OpDeploy, Path: p, Size: size,
		Checksums: types.Checksums{SHA256: sha},
	}
	env := format.Env{Store: st, Blobs: blobs}
	for _, skip := range []format.Write{
		{Op: format.OpChecksum, Path: p},
		{Op: format.OpDeploy, Path: "README.md"},
	} {
		if got, err := h.Inspect(ctx, env, c, skip); got != nil || err != nil {
			t.Fatalf("non-package inspection %q: %#v, %v", skip.Path, got, err)
		}
	}
	if _, err := h.Inspect(ctx, env, c, format.Write{
		Op: format.OpDeploy, Path: p,
		Checksums: types.Checksums{SHA256: "missing"},
	}); err == nil {
		t.Fatal("missing package blob accepted")
	}
	inspected, err := h.Inspect(ctx, env, c, w)
	if err != nil || inspected == nil {
		t.Fatalf("authentic decimal inspection: %#v, %v", inspected, err)
	}
	env.Inspection = inspected
	if _, err := h.Admit(ctx, env, c, w); err != nil {
		t.Fatalf("authentic decimal package rejected: %v", err)
	}
	if _, err := h.Admit(ctx, env, c, format.Write{Op: format.OpChecksum, Path: p}); err != nil {
		t.Fatalf("checksum sidecar rejected: %v", err)
	}
	federated := c
	federated.Class = repo.Federated
	if _, err := h.Admit(ctx, env, federated, w); err == nil {
		t.Fatal("unsupported federated Hex write accepted")
	}
	for _, path := range []string{"versions", "packages/decimal", "README.md"} {
		if _, err := h.Admit(
			ctx,
			env,
			c,
			format.Write{Op: format.OpDeploy, Path: path},
		); err == nil {
			t.Fatalf("non-tarball Hex write %q accepted", path)
		}
	}
	wrongPath := format.Write{
		Op: format.OpCopy, Path: "tarballs/other-2.0.0.tar", Size: size,
		Checksums: types.Checksums{SHA256: sha},
	}
	if _, err := h.Admit(ctx, format.Env{Store: st, Blobs: blobs}, c, wrongPath); err == nil {
		t.Fatal("real decimal bytes accepted under another package identity")
	}
	if _, err := h.Admit(ctx, format.Env{Store: st, Blobs: blobs}, c,
		format.Write{
			Op: format.OpCopy, Path: p,
			Checksums: types.Checksums{SHA256: "missing"},
		}); err == nil {
		t.Fatal("copy from missing archive accepted")
	}
	if _, err := h.Admit(ctx, format.Env{Store: st, Blobs: blobs}, c, w); err != nil {
		t.Fatalf("valid copy without prior inspection: %v", err)
	}
	item := hexCoverageItem(t, st, c, pkg, sha, size)
	if err := h.Deployed(ctx, env, c, item); err != nil {
		t.Fatalf("link authentic package: %v", err)
	}
	for _, unrelated := range []types.Item{
		{RepoKey: c.Key, Folder: true, Path: "tarballs", Name: item.Name},
		{RepoKey: c.Key, Name: "README.md"},
	} {
		if err := h.Deployed(ctx, env, c, unrelated); err != nil {
			t.Fatalf("unrelated item changed Hex index: %v", err)
		}
	}
	indexed, err := st.Packages().Find(ctx, c.Key, pkg.Name)
	if err != nil {
		t.Fatalf("deployed package missing from index: %v", err)
	}
	versions, err := st.Packages().Versions(ctx, indexed.ID)
	if err != nil || len(versions) != 1 || versions[0].Version != pkg.Version {
		t.Fatalf("deployed version index: %+v, %v", versions, err)
	}
	for path, want := range map[string]format.Class{
		"names": format.ClassDerived, "versions": format.ClassDerived,
		"public_key": format.ClassDerived, "packages/decimal": format.ClassDerived,
		p: format.ClassUnique,
	} {
		if got := h.Classify(c, path); got != want {
			t.Errorf("classify %q: %v, want %v", path, got, want)
		}
	}
}

func TestUnitHexReindexPreservesOnlyAuthenticStoredPackages(t *testing.T) {
	ctx := t.Context()
	h := Handler{}
	c := *repo.Defaults(&repo.Config{Key: "hex-local", Class: repo.Local, PackageType: repo.Hex})
	st := inmem.New()
	blobs := hexCoverageBlobs{}
	decimal, decimalSHA, decimalSize := hexCoverageArchive(t, blobs, "decimal-2.0.0.tar")
	ecto, ectoSHA, ectoSize := hexCoverageArchive(t, blobs, "ecto-3.12.5.tar")
	decimalItem := hexCoverageItem(t, st, c, decimal, decimalSHA, decimalSize)
	_ = hexCoverageItem(t, st, c, ecto, ectoSHA, ectoSize)
	if _, err := st.Items().Put(ctx, types.Item{RepoKey: c.Key, Name: "README.md"}); err != nil {
		t.Fatal(err)
	}
	env := format.Env{Store: st, Blobs: blobs}
	if err := h.Reindex(ctx, env, c); err != nil {
		t.Fatalf("reindex authentic Hex archives: %v", err)
	}
	for _, pkg := range []Package{decimal, ecto} {
		indexed, err := st.Packages().Find(ctx, c.Key, pkg.Name)
		if err != nil {
			t.Fatalf("authentic package %s omitted: %v", pkg.Name, err)
		}
		versions, err := st.Packages().Versions(ctx, indexed.ID)
		if err != nil || len(versions) != 1 || versions[0].Version != pkg.Version {
			t.Fatalf("index for %s: %+v, %v", pkg.Name, versions, err)
		}
		if pkg.Name == decimal.Name {
			files, err := st.Packages().Files(ctx, versions[0].ID)
			if err != nil || len(files) != 1 || files[0] != decimalItem.ID {
				t.Fatalf("decimal archive link: %v, %v", files, err)
			}
		}
	}
	if err := h.Reindex(ctx, env, c); err != nil {
		t.Fatalf("idempotent reindex: %v", err)
	}
	if _, err := st.Items().Put(ctx, types.Item{
		RepoKey: c.Key, Path: "tarballs",
		Name: "tampered-1.0.0.tar", Actual: types.Checksums{SHA256: "missing"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.Reindex(ctx, env, c); err == nil || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reindex silently advertised missing archive: %v", err)
	}
}
