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

type hexFailingReadSeeker struct{ *bytes.Reader }

func (hexFailingReadSeeker) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func (f hexSource) Read(
	ctx context.Context,
	q source.Request,
	p source.Permit,
) (source.Result, error) {
	return f(ctx, q, p)
}

func TestUnitHexRemoteSignedMetadataFiltersDeniedVersion(t *testing.T) {
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
	legacyRelease := pkg
	legacyRelease.OuterChecksum = [32]byte{}
	legacyBytes, err := hexfmt.EncodePackage(hexfmt.RegistryPackage{
		Name: "decimal", Repository: "hexpm", Releases: []hexfmt.Package{legacyRelease},
	}, upstream)
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
	badMetadata := false
	corruptTarball := false
	failExactCheck := false
	denyEctoCheck := false
	denyDecimalCheck := false
	denyAllDecimalCheck := false
	emptyMetadataResult := false
	wrongMetadataIdentity := false
	failTarballFetch := false
	legacyMetadata := false
	badMetadataSize := false
	failMetadataRead := false
	cancelMetadataValidation := false
	corruptVersions := false
	backend := hexSource(
		func(ctx context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			reads[q.Resource.Path]++
			if failTarballFetch && q.Resource.Path == "tarballs/decimal-2.0.0.tar" {
				return source.Result{}, source.ErrUnavailable
			}
			if emptyMetadataResult && q.Resource.Path == "packages/decimal" {
				return source.Result{}, nil
			}
			body := packageBytes
			if q.Resource.Path == "versions" {
				body = versionBytes
			} else if legacyMetadata && q.Resource.Path == "packages/decimal" {
				body = legacyBytes
			} else if q.Resource.Path == "packages/ecto" {
				body = ectoBytes
			} else if wrongMetadataIdentity && q.Resource.Path == "packages/decimal" {
				body = ectoBytes
			} else if q.Resource.Path == "tarballs/decimal-2.0.0.tar" {
				body = fixture
				if corruptTarball {
					body = corrupt
				}
			}
			if badMetadata && q.Resource.Path == "packages/decimal" {
				body = bytes.Clone(packageBytes)
				body[len(body)-1] ^= 1
			}
			if corruptVersions && q.Resource.Path == "versions" {
				body = bytes.Clone(versionBytes)
				body[len(body)-1] ^= 1
			}
			// Even if a future source adapter skips its validator, the Hex
			// protocol handler must independently verify the signed index.
			if q.Resource.ValidateMetadata != nil && !corruptVersions {
				validationBody := io.ReadSeeker(bytes.NewReader(body))
				size := int64(len(body))
				validationCtx := ctx
				if badMetadataSize && q.Resource.Path == "packages/decimal" {
					size = 16<<20 + 1
				}
				if failMetadataRead && q.Resource.Path == "packages/decimal" {
					validationBody = hexFailingReadSeeker{bytes.NewReader(body)}
				}
				if cancelMetadataValidation && q.Resource.Path == "packages/decimal" {
					var cancel context.CancelFunc
					validationCtx, cancel = context.WithCancel(ctx)
					cancel()
				}
				if err := q.Resource.ValidateMetadata(validationCtx, validationBody, size,
					source.MetadataResponse{}); err != nil {
					return source.Result{}, err
				}
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
			Revalidate: func(_ context.Context, q source.Request, _ source.Permit) error {
				if denyAllDecimalCheck &&
					(q.Resource.Path == "tarballs/decimal-2.0.0.tar" ||
						q.Resource.Path == "tarballs/decimal-2.0.1.tar") {
					return source.ErrDenied
				}
				if denyDecimalCheck && q.Resource.Path == "tarballs/decimal-2.0.0.tar" {
					return source.ErrDenied
				}
				if denyEctoCheck && q.Resource.Path == "tarballs/ecto-3.12.5.tar" {
					return source.ErrDenied
				}
				if failExactCheck && q.Resource.Path == "tarballs/decimal-2.0.0.tar" {
					return source.ErrUnavailable
				}
				return nil
			},
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
	nameIndex := w.do(http.MethodGet, base+"names", "partial", "")
	nameRepo, names, err := hexfmt.DecodeNames(nameIndex.Body.Bytes(), public)
	if nameIndex.Code != http.StatusOK || err != nil || nameRepo != "hex-r" ||
		len(names) != 2 || names[0] != "decimal" || names[1] != "ecto" {
		t.Fatalf("remote names %d: %s, %q %+v %v", nameIndex.Code,
			nameIndex.Body, nameRepo, names, err)
	}
	denyEctoCheck = true
	filteredNames := w.do(http.MethodGet, base+"names", "partial", "")
	_, names, err = hexfmt.DecodeNames(filteredNames.Body.Bytes(), public)
	if filteredNames.Code != http.StatusOK || err != nil ||
		len(names) != 1 || names[0] != "decimal" {
		t.Fatalf("source-denied package appeared in names: %d, %+v %v",
			filteredNames.Code, names, err)
	}
	filteredVersions := w.do(http.MethodGet, base+"versions", "partial", "")
	_, versions, err = hexfmt.DecodeVersions(filteredVersions.Body.Bytes(), public)
	if filteredVersions.Code != http.StatusOK || err != nil ||
		len(versions) != 1 || versions[0].Name != "decimal" {
		t.Fatalf("source-denied package appeared in versions: %d, %+v %v",
			filteredVersions.Code, versions, err)
	}
	denyEctoCheck = false
	denyDecimalCheck = true
	deniedPackage := w.do(http.MethodGet, base+"packages/decimal", "admin", "")
	remaining, err := hexfmt.DecodePackage(deniedPackage.Body.Bytes(), public)
	if deniedPackage.Code != http.StatusOK || err != nil || len(remaining.Releases) != 1 ||
		remaining.Releases[0].Version != "2.0.1" {
		t.Fatalf("source-denied release remained in package metadata: %d, %+v %v",
			deniedPackage.Code, remaining, err)
	}
	deniedArchive := w.do(http.MethodGet, base+"tarballs/decimal-2.0.0.tar", "admin", "")
	if deniedArchive.Code != http.StatusNotFound || reads["tarballs/decimal-2.0.0.tar"] != 0 {
		t.Fatalf("source-denied release was fetched: %d, reads %d", deniedArchive.Code,
			reads["tarballs/decimal-2.0.0.tar"])
	}
	denyDecimalCheck = false
	denyAllDecimalCheck = true
	if got := w.do(
		http.MethodGet,
		base+"packages/decimal",
		"admin",
		"",
	); got.Code != http.StatusNotFound {
		t.Fatalf("source denied every release, package status %d", got.Code)
	}
	if got := w.do(
		http.MethodGet,
		base+"tarballs/decimal-2.0.0.tar",
		"admin",
		"",
	); got.Code != http.StatusNotFound ||
		reads["tarballs/decimal-2.0.0.tar"] != 0 {
		t.Fatalf("source denied every release, archive status %d, reads %d",
			got.Code, reads["tarballs/decimal-2.0.0.tar"])
	}
	denyAllDecimalCheck = false
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
	missingGet := w.do(http.MethodGet, base+"tarballs/decimal-9.9.9.tar", "admin", "")
	if missingGet.Code != http.StatusNotFound || reads["tarballs/decimal-9.9.9.tar"] != 0 {
		t.Fatalf("unlisted release fetched: %d, upstream reads %d",
			missingGet.Code, reads["tarballs/decimal-9.9.9.tar"])
	}
	validGet := w.do(http.MethodGet, base+"tarballs/decimal-2.0.0.tar", "partial", "")
	if validGet.Code != http.StatusOK || !bytes.Equal(validGet.Body.Bytes(), fixture) {
		t.Fatalf("verified upstream archive was not served: %d %s", validGet.Code, validGet.Body)
	}
	failTarballFetch = true
	failedGet := w.do(http.MethodGet, base+"tarballs/decimal-2.0.0.tar", "partial", "")
	if failedGet.Code == http.StatusOK || failedGet.Body.Len() >= len(fixture) {
		t.Fatalf("failed upstream archive fetch was served: %d, %d bytes",
			failedGet.Code, failedGet.Body.Len())
	}
	failTarballFetch = false
	legacyMetadata = true
	legacyIndex := w.do(http.MethodGet, base+"packages/decimal", "partial", "")
	legacy, err := hexfmt.DecodePackage(legacyIndex.Body.Bytes(), public)
	if legacyIndex.Code != http.StatusOK || err != nil || len(legacy.Releases) != 1 ||
		legacy.Releases[0].OuterChecksum != pkg.OuterChecksum ||
		legacy.Releases[0].InnerChecksum != pkg.InnerChecksum {
		t.Fatalf("legacy signed metadata was not safely completed: %d %+v %v",
			legacyIndex.Code, legacy, err)
	}
	legacyArchive := w.do(http.MethodGet, base+"tarballs/decimal-2.0.0.tar", "partial", "")
	if legacyArchive.Code != http.StatusOK || !bytes.Equal(legacyArchive.Body.Bytes(), fixture) {
		t.Fatalf("valid legacy archive was not served: %d, %d bytes",
			legacyArchive.Code, legacyArchive.Body.Len())
	}
	failTarballFetch = true
	if got := w.do(
		http.MethodGet,
		base+"packages/decimal",
		"partial",
		"",
	); got.Code == http.StatusOK {
		t.Fatal("legacy metadata was published without checking its archive")
	}
	failTarballFetch = false
	corruptTarball = true
	legacyCorrupt := w.do(http.MethodGet, base+"tarballs/decimal-2.0.0.tar", "partial", "")
	if legacyCorrupt.Code != http.StatusBadGateway {
		t.Fatalf("corrupt legacy archive accepted: %d", legacyCorrupt.Code)
	}
	corruptTarball = false
	legacyMetadata = false
	corruptTarball = true
	invalidGet := w.do(http.MethodGet, base+"tarballs/decimal-2.0.0.tar", "partial", "")
	if invalidGet.Code != http.StatusBadGateway || invalidGet.Body.Len() >= len(corrupt) {
		t.Fatalf(
			"corrupt upstream archive was served: %d, %d bytes",
			invalidGet.Code,
			invalidGet.Body.Len(),
		)
	}
	badMetadata = true
	invalidMetadata := w.do(http.MethodGet, base+"packages/decimal", "partial", "")
	if invalidMetadata.Code != http.StatusBadGateway {
		t.Fatalf(
			"tampered signed metadata was served: %d %s",
			invalidMetadata.Code,
			invalidMetadata.Body,
		)
	}
	badMetadata = false
	for name, flag := range map[string]*bool{
		"oversized signed metadata":  &badMetadataSize,
		"unreadable signed metadata": &failMetadataRead,
		"canceled validation":        &cancelMetadataValidation,
	} {
		t.Run(name, func(t *testing.T) {
			*flag = true
			defer func() { *flag = false }()
			if got := w.do(
				http.MethodGet,
				base+"packages/decimal",
				"partial",
				"",
			); got.Code == http.StatusOK {
				t.Fatal("unvalidated signed metadata was served")
			}
		})
	}
	corruptVersions = true
	if got := w.do(
		http.MethodGet,
		base+"versions",
		"partial",
		"",
	); got.Code != http.StatusBadGateway {
		t.Fatalf("forged signed versions index was served: %d", got.Code)
	}
	corruptVersions = false
	wrongMetadataIdentity = true
	wrongPackage := w.do(http.MethodGet, base+"packages/decimal", "partial", "")
	if wrongPackage.Code != http.StatusBadGateway {
		t.Fatalf("wrong signed package identity accepted: %d %s",
			wrongPackage.Code, wrongPackage.Body)
	}
	wrongMetadataIdentity = false
	emptyMetadataResult = true
	emptyMetadata := w.do(http.MethodGet, base+"packages/decimal", "partial", "")
	if emptyMetadata.Code == http.StatusOK {
		t.Fatal("missing source result was served as signed registry metadata")
	}
	emptyMetadataResult = false
	failExactCheck = true
	failedAuthority := w.do(http.MethodGet, base+"packages/decimal", "partial", "")
	if failedAuthority.Code == http.StatusOK || failedAuthority.Body.Len() >= len(packageBytes) {
		t.Fatalf("metadata published after exact-path authority failure: %d %s",
			failedAuthority.Code, failedAuthority.Body)
	}
	failedVersionAuthority := w.do(http.MethodGet, base+"versions", "partial", "")
	if failedVersionAuthority.Code == http.StatusOK {
		t.Fatal("versions index published after exact-path authority failure")
	}
	failExactCheck = false
	c.HexPublicKey = "not a PEM key"
	if err := w.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	badKey := w.do(http.MethodGet, base+"versions", "partial", "")
	if badKey.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid upstream signing key accepted: %d %s", badKey.Code, badKey.Body)
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
