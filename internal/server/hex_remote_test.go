package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"

	hexfmt "github.com/weaveplatform/artifactweave/internal/format/hex"
	"github.com/weaveplatform/artifactweave/internal/keymaterial"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type hexSource func(context.Context, source.Request, source.Permit) (source.Result, error)

func (f hexSource) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return f(ctx, q, p)
}

func TestHexRemoteSignedMetadataFiltersDeniedVersion(t *testing.T) {
	keys := testKeys()
	upstream, err := keymaterial.RSAPrivateKey(keys.private, "")
	if err != nil {
		t.Fatal(err)
	}
	fixture := mustReadHexFixture(t)
	corrupt := bytes.Clone(fixture)
	corrupt[100] ^= 1
	pkg, err := hexfmt.InspectTarball(bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	denied := pkg
	denied.Version = "2.0.1"
	reg := hexfmt.RegistryPackage{
		Name:       "decimal",
		Repository: "hexpm",
		Releases:   []hexfmt.Package{pkg, denied},
	}
	ectoArchive, err := os.ReadFile("../format/hex/testdata/ecto-3.12.5.tar")
	if err != nil {
		t.Fatal(err)
	}
	ecto, err := hexfmt.InspectTarball(bytes.NewReader(ectoArchive))
	if err != nil {
		t.Fatal(err)
	}
	ectoRegistry := hexfmt.RegistryPackage{
		Name: "ecto", Repository: "hexpm", Releases: []hexfmt.Package{ecto},
	}
	packageBytes, err := hexfmt.EncodePackage(reg, upstream)
	if err != nil {
		t.Fatal(err)
	}
	ectoBytes, err := hexfmt.EncodePackage(ectoRegistry, upstream)
	if err != nil {
		t.Fatal(err)
	}
	versionBytes, err := hexfmt.EncodeVersions(
		"hexpm",
		[]hexfmt.RegistryPackage{reg, ectoRegistry},
		upstream,
	)
	if err != nil {
		t.Fatal(err)
	}
	reads := map[string]int{}
	backend := hexSource(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			reads[q.Resource.Path]++
			body := packageBytes
			if q.Resource.Path == "versions" {
				body = versionBytes
			} else if q.Resource.Path == "packages/ecto" {
				body = ectoBytes
			} else if q.Resource.Path == "tarballs/decimal-2.0.0.tar" {
				body = corrupt
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		},
	)
	identity := types.SourceIdentity{
		Kind:       types.ExternalHTTP,
		Repository: "hex-r",
		ID:         "upstream",
		Generation: "rev-1",
	}
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{
			HTTP:    backend,
			Resolve: func(context.Context, repo.Config) (types.SourceIdentity, error) { return identity, nil },
		}
	})
	if err := w.db.Keys().CreateKeypair(t.Context(), types.Keypair{
		Name: "hex-key", Type: types.KeypairRSA, PrivateKey: keys.private, PublicKey: keys.public,
	}); err != nil {
		t.Fatal(err)
	}
	c := *repo.Defaults(&repo.Config{
		Key: "hex-r", Class: repo.Remote, PackageType: repo.Hex,
		URL: "https://up.example/", PrimaryKeyPairRef: "hex-key", HexPublicKey: keys.public,
	})
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Users().
		Create(t.Context(), types.User{Name: "partial", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "hex-one-version",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"partial": types.ActionRead}},
			Targets: map[string]types.Target{
				"hex-r": {Include: []string{
					"tarballs/decimal-2.0.0.tar", "tarballs/ecto-3.12.5.tar",
				}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	base := "/artifactory/api/hex/hex-r/"
	got := w.do(http.MethodGet, base+"packages/decimal", "partial", "")
	if got.Code != http.StatusOK {
		t.Fatalf("metadata %d: %s", got.Code, got.Body)
	}
	public, err := hexfmt.ParsePublicKey(keys.public)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := hexfmt.DecodePackage(got.Body.Bytes(), public)
	if err != nil || len(filtered.Releases) != 1 || filtered.Releases[0].Version != "2.0.0" {
		t.Fatalf("denied release disclosed: %+v %v", filtered, err)
	}
	got = w.do(http.MethodGet, base+"versions", "partial", "")
	if got.Code != http.StatusOK {
		t.Fatalf("versions %d: %s", got.Code, got.Body)
	}
	_, versions, err := hexfmt.DecodeVersions(got.Body.Bytes(), public)
	if err != nil || len(versions) != 2 || versions[0].Name != "decimal" ||
		len(versions[0].Releases) != 1 || versions[0].Releases[0].Version != "2.0.0" ||
		versions[1].Name != "ecto" || len(versions[1].Releases) != 1 {
		t.Fatalf("denied version disclosed: %+v %v", versions, err)
	}
	ectoGet := w.do(http.MethodGet, base+"packages/ecto", "partial", "")
	if ectoGet.Code != http.StatusOK {
		t.Fatalf("Ecto metadata %d: %s", ectoGet.Code, ectoGet.Body)
	}
	rebound, err := hexfmt.DecodePackage(ectoGet.Body.Bytes(), public)
	if err != nil || len(rebound.Releases) != 1 || len(rebound.Releases[0].Requirements) != 3 {
		t.Fatalf("Ecto metadata incomplete: %+v %v", rebound, err)
	}
	for _, requirement := range rebound.Releases[0].Requirements {
		if requirement.Repository != "hex-r" {
			t.Errorf("dependency %s bypasses the authorized proxy: %s",
				requirement.Package, requirement.Repository)
		}
	}
	deniedGet := w.do(http.MethodGet, base+"tarballs/decimal-2.0.1.tar", "partial", "")
	if deniedGet.Code != http.StatusForbidden || reads["tarballs/decimal-2.0.1.tar"] != 0 {
		t.Fatalf(
			"denied tarball: %d, upstream reads %d",
			deniedGet.Code,
			reads["tarballs/decimal-2.0.1.tar"],
		)
	}
	invalidGet := w.do(http.MethodGet, base+"tarballs/decimal-2.0.0.tar", "partial", "")
	if invalidGet.Code != http.StatusBadGateway || invalidGet.Body.Len() >= len(corrupt) {
		t.Fatalf(
			"corrupt upstream archive was served: %d, %d bytes",
			invalidGet.Code,
			invalidGet.Body.Len(),
		)
	}
}

func mustReadHexFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../format/hex/testdata/decimal-2.0.0.tar")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
