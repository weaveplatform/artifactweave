package hex_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	hexfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/hex"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/keymaterial"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
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
	ectoArchive := repopath.Read(t, "internal/artifacts/ecosystem/hex/testdata/ecto-3.12.5.tar")
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
	identity := artmodel.SourceIdentity{
		Kind:       artmodel.ExternalHTTP,
		Repository: "hex-r",
		ID:         "upstream",
		Generation: "rev-1",
	}
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{
			HTTP:    backend,
			Resolve: func(context.Context, repo.Config) (artmodel.SourceIdentity, error) { return identity, nil },
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
	if err := w.db.Keys().CreateKeypair(t.Context(), api.Keypair{
		Name: "hex-key", Type: api.KeypairRSA, PrivateKey: keys.private, PublicKey: keys.public,
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
		Create(t.Context(), accesstest.User{Name: "partial", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "hex-one-version",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"partial": api.ActionRead}},
			Targets: map[string]accesstest.Target{
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

// TestUnitHexVirtualRemoteMembers: a virtual repository merges its remote
// members' signed releases and fetches a tarball through the member that
// lists its version, passing over a member that lists other versions only
// and members the caller may not read; it refuses a corrupt tarball from
// the remote with 502, and its index fails while a remote member's upstream
// key is invalid.
func TestUnitHexVirtualRemoteMembers(t *testing.T) {
	keys := testKeys()
	upstream, err := keymaterial.RSAPrivateKey(keys.private, "")
	if err != nil {
		t.Fatal(err)
	}
	fixture := mustReadHexFixture(t)
	pkg, err := hexfmt.InspectTarball(bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	older := pkg
	older.Version = "1.0.0"
	listings := map[string][]byte{}
	for key, release := range map[string]hexfmt.Package{"hex-old": older, "hex-r": pkg} {
		listings[key], err = hexfmt.EncodePackage(hexfmt.RegistryPackage{
			Name: "decimal", Repository: "hexpm", Releases: []hexfmt.Package{release},
		}, upstream)
		if err != nil {
			t.Fatal(err)
		}
	}
	corrupt := false
	backend := hexSource(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			body := listings[q.Repo.Key]
			if q.Resource.Path == "tarballs/decimal-2.0.0.tar" {
				body = fixture
				if corrupt {
					body = bytes.Clone(fixture)
					body[100] ^= 1
				}
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		},
	)
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{
			HTTP: backend,
			Resolve: func(_ context.Context, c repo.Config) (artmodel.SourceIdentity, error) {
				return artmodel.SourceIdentity{
					Kind: artmodel.ExternalHTTP, Repository: artmodel.RepositoryIncarnation(c.Key),
					ID: "upstream", Generation: "rev-1",
				}, nil
			},
		}
	})
	if err := w.db.Keys().CreateKeypair(t.Context(), api.Keypair{
		Name: "hex-key", Type: api.KeypairRSA, PrivateKey: keys.private, PublicKey: keys.public,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.Config{
		{Key: "hex-old", Class: repo.Remote, HexPublicKey: keys.public},
		{Key: "hex-r", Class: repo.Remote, HexPublicKey: keys.public},
		{Key: "hex-unkeyed", Class: repo.Remote, HexPublicKey: "not a PEM key"},
		{Key: "hex-v", Class: repo.Virtual, Repositories: []string{"hex-old", "hex-r"}},
		{Key: "hex-v-unkeyed", Class: repo.Virtual, Repositories: []string{"hex-unkeyed"}},
	} {
		c.PackageType, c.KeyPair = repo.Hex, "hex-key"
		if c.Class == repo.Remote {
			c.URL, c.KeyPair = "https://up.example/", ""
		}
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.db.Users().
		Create(t.Context(), accesstest.User{Name: "virtual-only", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "hex-virtual-only",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{
				Users: map[string]api.Actions{"virtual-only": api.ActionRead},
			},
			Targets: map[string]accesstest.Target{"hex-v": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	public, err := hexfmt.ParsePublicKey(keys.public)
	if err != nil {
		t.Fatal(err)
	}
	base := "/artifactory/api/hex/hex-v/"
	index := w.do(http.MethodGet, base+"packages/decimal", "admin", "")
	reg, err := hexfmt.DecodePackage(index.Body.Bytes(), public)
	if index.Code != http.StatusOK || err != nil || reg.Repository != "hex-v" ||
		len(reg.Releases) != 2 || reg.Releases[0].Version != "1.0.0" ||
		reg.Releases[1].Version != "2.0.0" {
		t.Fatalf("virtual package over remote members: %d %+v %v", index.Code, reg, err)
	}
	got := w.do(http.MethodGet, base+"tarballs/decimal-2.0.0.tar", "admin", "")
	if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), fixture) {
		t.Fatalf("remote member's tarball: %d, %d bytes", got.Code, got.Body.Len())
	}
	for _, tc := range []struct{ path, as string }{
		{"tarballs/decimal-9.9.9.tar", "admin"},
		{"tarballs/decimal-2.0.0.tar", "virtual-only"},
	} {
		got := w.do(http.MethodGet, base+tc.path, tc.as, "")
		if got.Code != http.StatusNotFound ||
			!strings.Contains(got.Body.String(), "Hex tarball not found") {
			t.Errorf("%s as %s: %d %s", tc.path, tc.as, got.Code, got.Body)
		}
	}
	corrupt = true
	got = w.do(http.MethodGet, base+"tarballs/decimal-2.0.0.tar", "admin", "")
	if got.Code != http.StatusBadGateway ||
		!strings.Contains(got.Body.String(), "invalid upstream Hex registry resource") {
		t.Fatalf("corrupt remote member tarball: %d %s", got.Code, got.Body)
	}
	got = w.do(http.MethodGet, "/artifactory/api/hex/hex-v-unkeyed/versions", "admin", "")
	if got.Code != http.StatusServiceUnavailable ||
		!strings.Contains(got.Body.String(), "upstream Hex public key is missing or invalid") {
		t.Fatalf("remote member without a valid upstream key: %d %s", got.Code, got.Body)
	}
}

// TestUnitHexVirtualPassesOverMissingAndUnreadableMembers: a virtual
// repository passes over a remote member whose upstream lacks the package,
// before or after a remote or a local member that holds it, and over a
// member the caller may not read, without asking that member's upstream,
// on packages/NAME, names, versions and the tarball (decision 0008).
func TestUnitHexVirtualPassesOverMissingAndUnreadableMembers(t *testing.T) {
	keys := testKeys()
	upstream, err := keymaterial.RSAPrivateKey(keys.private, "")
	if err != nil {
		t.Fatal(err)
	}
	archives := map[string][]byte{
		"decimal": mustReadHexFixture(t),
		"ecto":    repopath.Read(t, "internal/artifacts/ecosystem/hex/testdata/ecto-3.12.5.tar"),
	}
	releases := map[string]string{}
	bodies := map[string][]byte{}
	var registries []hexfmt.RegistryPackage
	for _, name := range []string{"decimal", "ecto"} {
		release, err := hexfmt.InspectTarball(bytes.NewReader(archives[name]))
		if err != nil {
			t.Fatal(err)
		}
		releases[name] = release.Version
		reg := hexfmt.RegistryPackage{
			Name: name, Repository: "hexpm", Releases: []hexfmt.Package{release},
		}
		registries = append(registries, reg)
		if bodies["packages/"+name], err = hexfmt.EncodePackage(reg, upstream); err != nil {
			t.Fatal(err)
		}
		bodies["tarballs/"+name+"-"+release.Version+".tar"] = archives[name]
	}
	if bodies["versions"], err = hexfmt.EncodeVersions("hexpm", registries, upstream); err != nil {
		t.Fatal(err)
	}
	reads := map[string]int{}
	backend := hexSource(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			reads[q.Repo.Key]++
			body, ok := bodies[q.Resource.Path]
			if !ok || q.Repo.Key == "hex-missing" {
				return source.Result{}, kernel.ErrNotFound
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		},
	)
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{
			HTTP: backend,
			Resolve: func(_ context.Context, c repo.Config) (artmodel.SourceIdentity, error) {
				return artmodel.SourceIdentity{
					Kind: artmodel.ExternalHTTP, Repository: artmodel.RepositoryIncarnation(c.Key),
					ID: "upstream", Generation: "rev-1",
				}, nil
			},
		}
	})
	if err := w.db.Keys().CreateKeypair(t.Context(), api.Keypair{
		Name: "hex-key", Type: api.KeypairRSA, PrivateKey: keys.private, PublicKey: keys.public,
	}); err != nil {
		t.Fatal(err)
	}
	virtuals := map[string][]string{
		"hex-v-missing-first": {"hex-missing", "hex-r"},
		"hex-v-missing-last":  {"hex-r", "hex-missing"},
		"hex-v-local-second":  {"hex-missing", "hex-local"},
		"hex-v-local-first":   {"hex-local", "hex-missing"},
		"hex-v-partial":       {"hex-local", "hex-r"},
		"hex-v-partial-flip":  {"hex-r", "hex-local"},
	}
	configs := []repo.Config{
		{Key: "hex-local", Class: repo.Local, PrimaryKeyPairRef: "hex-key"},
		{Key: "hex-missing", Class: repo.Remote},
		{Key: "hex-r", Class: repo.Remote},
	}
	for key, members := range virtuals {
		configs = append(configs, repo.Config{
			Key: key, Class: repo.Virtual, Repositories: members, KeyPair: "hex-key",
		})
	}
	for _, c := range configs {
		c.PackageType = repo.Hex
		if c.Class == repo.Remote {
			c.URL, c.HexPublicKey = "https://up.example/", keys.public
		}
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	if put := w.do(http.MethodPut, "/artifactory/hex-local/tarballs/decimal-2.0.0.tar",
		"admin", string(archives["decimal"])); put.Code != http.StatusCreated {
		t.Fatalf("deploy Decimal %d: %s", put.Code, put.Body)
	}
	if err := w.db.Users().
		Create(t.Context(), accesstest.User{Name: "partial", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "hex-virtual-and-local",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"partial": api.ActionRead}},
			Targets: map[string]accesstest.Target{
				"hex-v-partial":      {Include: []string{"**"}},
				"hex-v-partial-flip": {Include: []string{"**"}},
				"hex-local":          {Include: []string{"**"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	public, err := hexfmt.ParsePublicKey(keys.public)
	if err != nil {
		t.Fatal(err)
	}
	both, local := []string{"decimal", "ecto"}, []string{"decimal"}
	for _, tc := range []struct {
		virtual, as string
		// listed are the packages the caller gets through the virtual: Ecto
		// only from the remote member hex-r, Decimal from either.
		listed []string
	}{
		{"hex-v-missing-first", "reader", both},
		{"hex-v-missing-last", "reader", both},
		{"hex-v-local-second", "reader", local},
		{"hex-v-local-first", "reader", local},
		{"hex-v-partial", "partial", local},
		{"hex-v-partial-flip", "partial", local},
		{"hex-v-partial", "reader", both},
	} {
		t.Run(tc.virtual+" as "+tc.as, func(t *testing.T) {
			base := "/artifactory/api/hex/" + tc.virtual + "/"
			missingReads, remoteReads := reads["hex-missing"], reads["hex-r"]
			for _, name := range both {
				pkg := w.do(http.MethodGet, base+"packages/"+name, tc.as, "")
				archive := w.do(http.MethodGet,
					base+"tarballs/"+name+"-"+releases[name]+".tar", tc.as, "")
				if !slices.Contains(tc.listed, name) {
					if pkg.Code != http.StatusNotFound ||
						!strings.Contains(pkg.Body.String(), "Hex package not found") {
						t.Errorf("packages/%s: %d %s", name, pkg.Code, pkg.Body)
					}
					if archive.Code != http.StatusNotFound ||
						!strings.Contains(archive.Body.String(), "Hex tarball not found") {
						t.Errorf("%s tarball: %d %s", name, archive.Code, archive.Body)
					}
					continue
				}
				reg, err := hexfmt.DecodePackage(pkg.Body.Bytes(), public)
				if pkg.Code != http.StatusOK || err != nil || reg.Name != name ||
					reg.Repository != tc.virtual || len(reg.Releases) != 1 ||
					reg.Releases[0].Version != releases[name] {
					t.Errorf("packages/%s: %d %s, %+v %v", name, pkg.Code, pkg.Body, reg, err)
				}
				if archive.Code != http.StatusOK ||
					!bytes.Equal(archive.Body.Bytes(), archives[name]) {
					t.Errorf("%s tarball: %d, %d bytes", name, archive.Code, archive.Body.Len())
				}
			}
			names := w.do(http.MethodGet, base+"names", tc.as, "")
			gotRepo, gotNames, err := hexfmt.DecodeNames(names.Body.Bytes(), public)
			if names.Code != http.StatusOK || err != nil || gotRepo != tc.virtual ||
				!slices.Equal(gotNames, tc.listed) {
				t.Errorf("names: %d %s, %q %q %v", names.Code, names.Body, gotRepo, gotNames, err)
			}
			index := w.do(http.MethodGet, base+"versions", tc.as, "")
			gotRepo, packages, err := hexfmt.DecodeVersions(index.Body.Bytes(), public)
			var gotPackages []string
			for _, p := range packages {
				if len(p.Releases) != 1 || p.Releases[0].Version != releases[p.Name] {
					t.Errorf("versions lists %s at %+v", p.Name, p.Releases)
				}
				gotPackages = append(gotPackages, p.Name)
			}
			if index.Code != http.StatusOK || err != nil || gotRepo != tc.virtual ||
				!slices.Equal(gotPackages, tc.listed) {
				t.Errorf("versions: %d %s, %q %q %v",
					index.Code, index.Body, gotRepo, gotPackages, err)
			}
			// The member that lacks the package was asked and passed over,
			// not skipped unasked; the member the caller may not read was
			// never asked.
			if slices.Contains(virtuals[tc.virtual], "hex-missing") &&
				reads["hex-missing"] == missingReads {
				t.Error("the member whose upstream lacks the package was never asked")
			}
			if tc.as == "partial" && reads["hex-r"] != remoteReads {
				t.Errorf("the member the caller may not read was asked %d times",
					reads["hex-r"]-remoteReads)
			}
		})
	}
}

// TestUnitHexVirtualListsOnlyReleasesReadableThroughTheVirtual: a caller
// who may read both members whole, but through the virtual only Decimal's
// tarballs, gets Decimal alone in names, versions and packages/NAME,
// whichever member comes first, each signed with the virtual's own key;
// Ecto is not found there, and its tarball through the virtual is refused
// with 403 (decision 0008). A caller who may read the whole virtual gets
// both packages.
func TestUnitHexVirtualListsOnlyReleasesReadableThroughTheVirtual(t *testing.T) {
	memberKeys, virtualKeys := testKeys(), otherKeys()
	upstream, err := keymaterial.RSAPrivateKey(memberKeys.private, "")
	if err != nil {
		t.Fatal(err)
	}
	both := []string{"decimal", "ecto"}
	archives := map[string][]byte{
		"decimal": mustReadHexFixture(t),
		"ecto":    repopath.Read(t, "internal/artifacts/ecosystem/hex/testdata/ecto-3.12.5.tar"),
	}
	releases, tarballs := map[string]string{}, map[string]string{}
	bodies := map[string][]byte{}
	var registries []hexfmt.RegistryPackage
	for _, name := range both {
		release, err := hexfmt.InspectTarball(bytes.NewReader(archives[name]))
		if err != nil {
			t.Fatal(err)
		}
		releases[name] = release.Version
		tarballs[name] = "tarballs/" + name + "-" + release.Version + ".tar"
		reg := hexfmt.RegistryPackage{
			Name: name, Repository: "hexpm", Releases: []hexfmt.Package{release},
		}
		registries = append(registries, reg)
		if bodies["packages/"+name], err = hexfmt.EncodePackage(reg, upstream); err != nil {
			t.Fatal(err)
		}
		bodies[tarballs[name]] = archives[name]
	}
	if bodies["versions"], err = hexfmt.EncodeVersions("hexpm", registries, upstream); err != nil {
		t.Fatal(err)
	}
	backend := hexSource(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			body, ok := bodies[q.Resource.Path]
			if !ok {
				return source.Result{}, kernel.ErrNotFound
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		},
	)
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{
			HTTP: backend,
			Resolve: func(_ context.Context, c repo.Config) (artmodel.SourceIdentity, error) {
				return artmodel.SourceIdentity{
					Kind: artmodel.ExternalHTTP, Repository: artmodel.RepositoryIncarnation(c.Key),
					ID: "upstream", Generation: "rev-1",
				}, nil
			},
		}
	})
	for name, pair := range map[string]keyMaterial{
		"hex-member-key": memberKeys, "hex-virtual-key": virtualKeys,
	} {
		if err := w.db.Keys().CreateKeypair(t.Context(), api.Keypair{
			Name: name, Type: api.KeypairRSA, PrivateKey: pair.private, PublicKey: pair.public,
		}); err != nil {
			t.Fatal(err)
		}
	}
	orders := map[string][]string{
		"hex-v-local-first":  {"hex-local", "hex-r"},
		"hex-v-remote-first": {"hex-r", "hex-local"},
	}
	configs := []repo.Config{
		{Key: "hex-local", Class: repo.Local, PrimaryKeyPairRef: "hex-member-key"},
		{
			Key: "hex-r", Class: repo.Remote,
			URL: "https://up.example/", HexPublicKey: memberKeys.public,
		},
	}
	for key, members := range orders {
		configs = append(configs, repo.Config{
			Key: key, Class: repo.Virtual, Repositories: members, KeyPair: "hex-virtual-key",
		})
	}
	for _, c := range configs {
		c.PackageType = repo.Hex
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range both {
		if put := w.do(http.MethodPut, "/artifactory/hex-local/"+tarballs[name],
			"admin", string(archives[name])); put.Code != http.StatusCreated {
			t.Fatalf("deploy %s %d: %s", name, put.Code, put.Body)
		}
	}
	if err := w.db.Users().
		Create(t.Context(), accesstest.User{Name: "decimal-only", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	// Both members whole, so only the virtual's own permissions can narrow
	// what the caller gets through it.
	targets := map[string]accesstest.Target{
		"hex-local": {Include: []string{"**"}},
		"hex-r":     {Include: []string{"**"}},
	}
	for key := range orders {
		targets[key] = accesstest.Target{Include: []string{"tarballs/decimal-*"}}
	}
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "hex-decimal-through-virtuals",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{
				Users: map[string]api.Actions{"decimal-only": api.ActionRead},
			},
			Targets: targets,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	public, err := hexfmt.ParsePublicKey(virtualKeys.public)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		virtual, as string
		listed      []string
	}{
		{"hex-v-local-first", "decimal-only", []string{"decimal"}},
		{"hex-v-remote-first", "decimal-only", []string{"decimal"}},
		{"hex-v-local-first", "reader", both},
		{"hex-v-remote-first", "reader", both},
	} {
		t.Run(tc.virtual+" as "+tc.as, func(t *testing.T) {
			base := "/artifactory/api/hex/" + tc.virtual + "/"
			for _, name := range both {
				pkg := w.do(http.MethodGet, base+"packages/"+name, tc.as, "")
				archive := w.do(http.MethodGet, base+tarballs[name], tc.as, "")
				if !slices.Contains(tc.listed, name) {
					if pkg.Code != http.StatusNotFound ||
						!strings.Contains(pkg.Body.String(), "Hex package not found") {
						t.Errorf("packages/%s: %d %s", name, pkg.Code, pkg.Body)
					}
					if archive.Code != http.StatusForbidden {
						t.Errorf("%s tarball: %d %s", name, archive.Code, archive.Body)
					}
					continue
				}
				reg, err := hexfmt.DecodePackage(pkg.Body.Bytes(), public)
				if pkg.Code != http.StatusOK || err != nil || reg.Name != name ||
					reg.Repository != tc.virtual || len(reg.Releases) != 1 ||
					reg.Releases[0].Version != releases[name] {
					t.Errorf("packages/%s: %d %s, %+v %v", name, pkg.Code, pkg.Body, reg, err)
				}
				if archive.Code != http.StatusOK ||
					!bytes.Equal(archive.Body.Bytes(), archives[name]) {
					t.Errorf("%s tarball: %d, %d bytes", name, archive.Code, archive.Body.Len())
				}
			}
			names := w.do(http.MethodGet, base+"names", tc.as, "")
			gotRepo, gotNames, err := hexfmt.DecodeNames(names.Body.Bytes(), public)
			if names.Code != http.StatusOK || err != nil || gotRepo != tc.virtual ||
				!slices.Equal(gotNames, tc.listed) {
				t.Errorf("names: %d %s, %q %q %v", names.Code, names.Body, gotRepo, gotNames, err)
			}
			index := w.do(http.MethodGet, base+"versions", tc.as, "")
			gotRepo, packages, err := hexfmt.DecodeVersions(index.Body.Bytes(), public)
			var gotPackages []string
			for _, p := range packages {
				if len(p.Releases) != 1 || p.Releases[0].Version != releases[p.Name] {
					t.Errorf("versions lists %s at %+v", p.Name, p.Releases)
				}
				gotPackages = append(gotPackages, p.Name)
			}
			if index.Code != http.StatusOK || err != nil || gotRepo != tc.virtual ||
				!slices.Equal(gotPackages, tc.listed) {
				t.Errorf("versions: %d %s, %q %q %v",
					index.Code, index.Body, gotRepo, gotPackages, err)
			}
		})
	}
}

// TestUnitHexDownloadPolicyOfRemoteRepositories: a blacked-out remote
// refuses names, versions, packages/NAME and a tarball with the blackout's
// 404 naming each one's path, before its upstream is read; its patterns
// refuse packages/NAME at that path and a tarball at its own, while a
// release whose tarball they reject stays listed, a legacy one without its
// outer checksum, and the package metadata a tarball's download reads is
// not refused by them. A virtual repository leaves out a blacked-out remote
// member and passes its tarball over to the next member, keeps a member
// that rejects only a tarball, and completes a legacy release whose tarball
// only its own patterns reject. A refusal that the revalidation of the
// metadata returns answers its own 404 (#1237).
func TestUnitHexDownloadPolicyOfRemoteRepositories(t *testing.T) {
	keys := testKeys()
	upstream, err := keymaterial.RSAPrivateKey(keys.private, "")
	if err != nil {
		t.Fatal(err)
	}
	fixture := mustReadHexFixture(t)
	pkg, err := hexfmt.InspectTarball(bytes.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	other, legacyRelease := pkg, pkg
	other.Version, legacyRelease.OuterChecksum = "2.0.1", [32]byte{}
	encode := func(releases ...hexfmt.Package) []byte {
		body, err := hexfmt.EncodePackage(hexfmt.RegistryPackage{
			Name: "decimal", Repository: "hexpm", Releases: releases,
		}, upstream)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	current, legacyBody := encode(pkg, other), encode(legacyRelease, other)
	versions, err := hexfmt.EncodeVersions("hexpm", []hexfmt.RegistryPackage{{
		Name: "decimal", Repository: "hexpm", Releases: []hexfmt.Package{pkg, other},
	}}, upstream)
	if err != nil {
		t.Fatal(err)
	}
	legacy := false
	reads := map[string]int{}
	backend := hexSource(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			reads[q.Resource.Path]++
			body := map[string][]byte{
				"packages/decimal": current, "versions": versions,
				"tarballs/decimal-2.0.0.tar": fixture,
			}[q.Resource.Path]
			if legacy && q.Resource.Path == "packages/decimal" {
				body = legacyBody
			}
			if body == nil {
				return source.Result{}, kernel.ErrNotFound
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		},
	)
	// The hook counts its calls and refuses from call number refuseFrom on.
	calls, refuseFrom := 0, 0
	var refusal error
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{
			HTTP: backend,
			Resolve: func(_ context.Context, c repo.Config) (artmodel.SourceIdentity, error) {
				return artmodel.SourceIdentity{
					Kind: artmodel.ExternalHTTP, Repository: artmodel.RepositoryIncarnation(c.Key),
					ID: "upstream", Generation: "rev-1",
				}, nil
			},
			Revalidate: func(context.Context, source.Request, source.Permit) error {
				calls++
				if refuseFrom > 0 && calls >= refuseFrom {
					return refusal
				}
				return nil
			},
		}
	})
	if err := w.db.Keys().CreateKeypair(t.Context(), api.Keypair{
		Name: "hex-key", Type: api.KeypairRSA, PrivateKey: keys.private, PublicKey: keys.public,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []repo.Config{
		{
			Key: "hex-r", Class: repo.Remote, URL: "https://up.example/",
			HexPublicKey: keys.public, PrimaryKeyPairRef: "hex-key",
		},
		{Key: "hex-l", Class: repo.Local, PrimaryKeyPairRef: "hex-key"},
		{Key: "hex-v", Class: repo.Virtual, KeyPair: "hex-key", Repositories: []string{"hex-r", "hex-l"}},
	} {
		c.PackageType = repo.Hex
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	if put := w.do(http.MethodPut, "/artifactory/hex-l/tarballs/decimal-2.0.0.tar", "admin",
		string(fixture)); put.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", put.Code, put.Body)
	}
	get := func(key, path string) *httptest.ResponseRecorder {
		return w.do(http.MethodGet, "/artifactory/api/hex/"+key+"/"+path, "reader", "")
	}
	const tarball, sibling = "tarballs/decimal-2.0.0.tar", "tarballs/decimal-2.0.1.tar"
	patchHexRepo(t, w, "hex-r", func(c *repo.Config) { c.BlackedOut = new(true) })
	for _, path := range []string{"names", "versions", "packages/decimal", tarball} {
		hexRefusal(t, "blacked out "+path, get("hex-r", path), hexBlackedOut("hex-r", path))
	}
	if reads["packages/decimal"] != 0 || reads["versions"] != 0 {
		t.Errorf("blacked-out remote's upstream read: %v", reads)
	}
	hexListing(t, "virtual past a blacked-out remote", "packages/decimal",
		get("hex-v", "packages/decimal"), keys.public, "2.0.0")
	if got := get("hex-v", tarball); got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), fixture) {
		t.Errorf("tarball past a blacked-out remote: %d %s", got.Code, got.Body)
	}
	patchHexRepo(t, w, "hex-r", func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = new(false), new(sibling)
	})
	hexListing(t, "excluded tarball's package", "packages/decimal", get("hex-r", "packages/decimal"),
		keys.public, "2.0.0", "2.0.1")
	if got := get("hex-r", "versions"); got.Code != http.StatusOK {
		t.Errorf("excluded tarball's versions: %d %s", got.Code, got.Body)
	}
	hexRefusal(t, "excluded tarball", get("hex-r", sibling), hexRejected("hex-r", sibling))
	if got := get("hex-r", tarball); got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), fixture) {
		t.Errorf("sibling of an excluded tarball: %d %s", got.Code, got.Body)
	}
	hexListing(t, "virtual over an excluded tarball", "packages/decimal",
		get("hex-v", "packages/decimal"), keys.public, "2.0.0", "2.0.1")
	patchHexRepo(t, w, "hex-r", func(c *repo.Config) { c.ExcludesPattern = new("packages/**") })
	hexRefusal(t, "excluded package", get("hex-r", "packages/decimal"),
		hexRejected("hex-r", "packages/decimal"))
	if got := get("hex-r", tarball); got.Code != http.StatusOK {
		t.Errorf("tarball found through an excluded package: %d %s", got.Code, got.Body)
	}
	hexListing(t, "names beside an excluded package", "names", get("hex-r", "names"),
		keys.public, "decimal")
	hexListing(t, "virtual past a remote that excludes the package", "packages/decimal",
		get("hex-v", "packages/decimal"), keys.public, "2.0.0")
	legacy = true
	fetched := reads[tarball]
	patchHexRepo(t, w, "hex-r", func(c *repo.Config) { c.ExcludesPattern = new(tarball) })
	listed := hexListing(t, "legacy release whose tarball is excluded", "packages/decimal",
		get("hex-r", "packages/decimal"), keys.public, "2.0.0", "2.0.1")
	if len(listed) == 2 && listed[0].OuterChecksum != ([32]byte{}) || reads[tarball] != fetched {
		t.Errorf("excluded legacy tarball read %d times to complete %+v", reads[tarball]-fetched, listed)
	}
	patchHexRepo(t, w, "hex-r", func(c *repo.Config) { c.ExcludesPattern = new("") })
	patchHexRepo(t, w, "hex-v", func(c *repo.Config) { c.ExcludesPattern = new(tarball) })
	listed = hexListing(t, "legacy release under the virtual's own pattern", "packages/decimal",
		get("hex-v", "packages/decimal"), keys.public, "2.0.0", "2.0.1")
	if len(listed) == 2 && listed[0].OuterChecksum != pkg.OuterChecksum {
		t.Errorf("legacy release not completed under the virtual's pattern: %+v", listed[0])
	}
	hexRefusal(t, "virtual's own pattern on a tarball", get("hex-v", tarball),
		hexNoRepository("hex-v", tarball))
	legacy = false
	// A caller who may read no tarball lists none, so the hook's last call
	// is the revalidation of the metadata.
	if err := w.db.Users().
		Create(t.Context(), accesstest.User{Name: "no-tarballs", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "hex-no-tarballs",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"no-tarballs": api.ActionRead}},
			Targets: map[string]accesstest.Target{"hex-r": {Include: []string{"tarballs/none.tar"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	before := calls
	got := w.do(http.MethodGet, "/artifactory/api/hex/hex-r/packages/decimal", "no-tarballs", "")
	if got.Code != http.StatusNotFound || !strings.Contains(got.Body.String(), "Hex package not found") {
		t.Fatalf("no readable tarball: %d %s", got.Code, got.Body)
	}
	message := hexBlackedOut("hex-r", "packages/decimal")
	refuseFrom, refusal = calls+(calls-before), format.Rejectf(http.StatusNotFound, "%s", message)
	hexRefusal(t, "refused at revalidation",
		w.do(http.MethodGet, "/artifactory/api/hex/hex-r/packages/decimal", "no-tarballs", ""), message)
}

func mustReadHexFixture(t *testing.T) []byte {
	t.Helper()
	return repopath.Read(t, "internal/artifacts/ecosystem/hex/testdata/decimal-2.0.0.tar")
}
