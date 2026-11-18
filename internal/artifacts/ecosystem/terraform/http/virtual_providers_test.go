package terraformhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	tf "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

const virtualProviderBase = prefix + "v1/providers/virtual-rt-ns-hashicorp/null/"

func virtualProviderFixture(t *testing.T) (*remoteProviderFixture, terraformtest.Key) {
	t.Helper()
	f := newProviderFixture(t)
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Keys().CreateKeypair(t.Context(), api.Keypair{Name: "hosted", Type: api.KeypairGPG, PublicKey: key.Public}); err != nil {
		t.Fatal(err)
	}
	c, err := f.db.Repos().Get(t.Context(), "provider")
	if err != nil {
		t.Fatal(err)
	}
	c.PrimaryKeyPairRef = "hosted"
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	release := terraformtest.Release{Type: "null", Version: "2.0.0", Platforms: []string{"linux_amd64"}, Protocols: []string{"6.0"}}
	for name, body := range release.Files(key) {
		f.deploy("provider", "hosted/hashicorp/null/2.0.0/"+name, string(body))
	}
	c, err = f.db.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.Repositories = []string{"mods", "provider", "remote"}
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	return f, key
}

func TestAccTerraformVirtualProviderUnionBaseline(t *testing.T) {
	f, _ := virtualProviderFixture(t)
	for _, path := range []string{prefix + "provider/v1/providers/hashicorp/null/versions", remoteProviderBase + "versions"} {
		response := f.get("admin", path)
		if response.Code != 200 {
			t.Fatal(path, response.Code, response.Body.String())
		}
	}
	response := f.get("admin", virtualProviderBase+"versions")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "1.0.0") || !strings.Contains(response.Body.String(), "2.0.0") {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestAccRemoteProviderSignedSourceStoreFault(t *testing.T) {
	f := newProviderFixture(t)
	metadata := f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
	if metadata.Code != 200 {
		t.Fatal(metadata.Code, metadata.Body.String())
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(metadata.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	var signed string
	if err := json.Unmarshal(doc["download_url"], &signed); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("source store unavailable")
	f.svc.Resolve = func(context.Context, repo.Config) (model.SourceIdentity, error) {
		return model.SourceIdentity{}, errors.Join(format.ErrStorageFault, failure)
	}
	response := f.get("", signed)
	if response.Code != 500 {
		t.Fatal("valid signed capability hid a real source-store fault", response.Code, response.Body.String())
	}
}

func TestAccRemoteModuleSignedSourceStoreFault(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0")
	signed := f.signed("1.0.0", "reader")
	f.svc.Resolve = func(context.Context, repo.Config) (model.SourceIdentity, error) {
		return model.SourceIdentity{}, errors.Join(format.ErrStorageFault, errors.New("module source store unavailable"))
	}
	response := f.do("GET", signed, "")
	if response.Code != 500 {
		t.Fatal("valid module capability hid source-store fault", response.Code, response.Body.String())
	}
}

func TestAccVirtualModuleSignedPrincipalStoreFault(t *testing.T) {
	for _, failure := range []string{"user", "permissions"} {
		t.Run(failure, func(t *testing.T) {
			f := newFixture(t)
			virtualMembers(t, f, "mods")
			local := f.do("GET", prefix+"mods/v1/modules/ns/name/aws/1.0.0/download", "reader")
			expect(t, local, 204)
			virtual := f.do("GET", prefix+"virtual/v1/modules/ns/name/aws/1.0.0/download", "reader")
			expect(t, virtual, 204)
			f.read = failingStore{f.db, failure}
			f.h = f.router(f.keys)
			expect(t, f.do("GET", local.Header().Get("X-Terraform-Get"), ""), 500)
			response := f.do("GET", virtual.Header().Get("X-Terraform-Get"), "")
			if response.Code != 500 {
				t.Fatal("virtual URL masked principal-store fault unlike local control", response.Code, response.Body.String())
			}
		})
	}
}

func providerURLClaim(t *testing.T, f *remoteProviderFixture, target string) claims {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	h := handler{o: Options{Keys: f.keys, Now: func() time.Time { return f.now }}}
	c, ok := h.verify(u.Query().Get("sig"))
	if !ok {
		t.Fatal("invalid claim", target)
	}
	return c
}

func TestAccVirtualProvidersRegistryMirrorAndSignedBytes(t *testing.T) {
	f, localKey := virtualProviderFixture(t)
	c, _ := f.db.Repos().Get(t.Context(), "virtual")
	c.TerraformType = repo.TerraformModule // A virtual aggregates both kinds.
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	list := f.get("admin", virtualProviderBase+"versions")
	expect(t, list, 200)
	if strings.Count(list.Body.String(), `"version"`) != 2 {
		t.Fatal(list.Body.String())
	}
	var remoteURL, localURL string
	for _, tc := range []struct{ version, member, key string }{{"2.0.0", "provider", localKey.Public}, {"1.0.0", "remote", f.key.Public}} {
		t.Run(tc.member, func(t *testing.T) {
			response := f.get("admin", virtualProviderBase+tc.version+"/download/linux/amd64")
			expect(t, response, 200)
			doc := decode(t, response.Body.Bytes())
			keys := doc["signing_keys"].(map[string]any)["gpg_public_keys"].([]any)
			if len(keys) != 1 || keys[0].(map[string]any)["ascii_armor"] != tc.key {
				t.Fatal(doc)
			}
			for index, key := range []string{"download_url", "shasums_url", "shasums_signature_url"} {
				target := doc[key].(string)
				if !strings.Contains(target, "/virtual/providers/") {
					t.Fatal(target)
				}
				claim := providerURLClaim(t, f, target)
				if claim.Repo != "virtual" || claim.Member != tc.member || claim.Provider == nil || claim.Provider.Kind != index || claim.Expires-claim.Issued != 90 {
					t.Fatal(claim)
				}
				var expected []byte
				if tc.member == "provider" {
					if claim.Source != nil || claim.Credential != nil {
						t.Fatal("hosted URL required remote authority", claim)
					}
					it, err := f.db.Items().Get(t.Context(), "provider", false, claim.Path)
					if err != nil {
						t.Fatal(err)
					}
					body, _, err := f.items.Blobs.Open(t.Context(), it.Actual.SHA256)
					if err != nil {
						t.Fatal(err)
					}
					expected, err = io.ReadAll(body)
					_ = body.Close()
					if err != nil {
						t.Fatal(err)
					}
					localURL = target
				} else {
					if claim.Source == nil || claim.Credential == nil || claim.Provider.Metadata == "" {
						t.Fatal(claim)
					}
					expected = [][]byte{f.archive, f.sums, f.signature}[index]
					remoteURL = target
				}
				delivered := f.get("", target)
				expect(t, delivered, 200)
				if !bytes.Equal(delivered.Body.Bytes(), expected) {
					t.Fatal("provider representation changed", key)
				}
			}
		})
	}
	host, _ := url.Parse(f.up.URL)
	mirror := prefix + "virtual/providers/" + host.Host + "/hashicorp/null/"
	index := f.get("admin", mirror+"index.json")
	expect(t, index, 200)
	if versions := decode(t, index.Body.Bytes())["versions"].(map[string]any); len(versions) != 2 {
		t.Fatal(versions)
	}
	for _, version := range []string{"1.0.0", "2.0.0"} {
		metadata := f.get("admin", mirror+version+".json")
		expect(t, metadata, 200)
		archive := decode(t, metadata.Body.Bytes())["archives"].(map[string]any)["linux_amd64"].(map[string]any)
		if len(archive["hashes"].([]any)) < 1 || !strings.HasPrefix(archive["hashes"].([]any)[0].(string), "h1:") {
			t.Fatal(archive)
		}
		target := archive["url"].(string)
		if !strings.Contains(target, "/virtual/providers/") {
			t.Fatal(target)
		}
		expect(t, f.get("", target), 200)
	}
	// Removing an edge invalidates the member capability, while a retained
	// hosted member remains valid. Every refusal uses the exact URL profile.
	virtualMembers(t, f.fixture, "mods", "provider")
	refused := f.get("", remoteURL)
	expect(t, refused, 403)
	if refused.Body.String() != `{"errors":["Invalid or expired Terraform download URL"]}`+"\n" {
		t.Fatal(refused.Body.String())
	}
	expect(t, f.get("", localURL), 200)
	f.now = f.now.Add(91 * time.Second)
	expect(t, f.get("", localURL), 403)
}

func TestAccVirtualProviderVersionOwnerAndPlatformOwnerDiffer(t *testing.T) {
	f, key := virtualProviderFixture(t)
	local := terraformtest.Release{Type: "null", Version: "1.0.0", Platforms: []string{"linux_amd64"}, Protocols: []string{"6.0"}}
	for name, body := range local.Files(key) {
		f.deploy("provider", "hosted/hashicorp/null/1.0.0/"+name, string(body))
	}
	f.metadata["os"], f.metadata["arch"] = "darwin", "arm64"
	filename := "terraform-provider-null_1.0.0_darwin_arm64.zip"
	f.metadata["filename"] = filename
	f.sums = []byte(f.metadata["shasum"].(string) + "  " + filename + "\n")
	f.signature = f.key.Sign(f.sums)
	f.publish()
	f.json("/registry/providers/hashicorp/null/1.0.0/download/darwin/arm64", f.metadata)
	f.json(
		"/registry/providers/hashicorp/null/versions",
		map[string]any{"versions": []remoteProviderVersion{{Version: "1.0.0", Protocols: []string{"5.0"}, Platforms: []remotePlatform{{OS: "darwin", Arch: "arm64"}}}}},
	)
	response := f.get("admin", virtualProviderBase+"versions")
	expect(t, response, 200)
	var list struct {
		Versions []remoteProviderVersion `json:"versions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, version := range list.Versions {
		if version.Version == "1.0.0" && (!slices.Equal(version.Protocols, []string{"6.0"}) || !slices.Equal(version.Platforms, []remotePlatform{{OS: "linux", Arch: "amd64"}})) {
			t.Fatal("mixed whole version ownership", version)
		}
	}
	metadata := f.get("admin", virtualProviderBase+"1.0.0/download/darwin/arm64")
	expect(t, metadata, 200)
	doc := decode(t, metadata.Body.Bytes())
	if doc["filename"] != filename || providerURLClaim(t, f, doc["download_url"].(string)).Member != "remote" {
		t.Fatal("exact platform did not fall through", doc)
	}
	host, _ := url.Parse(f.up.URL)
	mirror := f.get("admin", prefix+"virtual/providers/"+host.Host+"/hashicorp/null/1.0.0.json")
	expect(t, mirror, 200)
	archives := decode(t, mirror.Body.Bytes())["archives"].(map[string]any)
	if len(archives) != 1 || archives["linux_amd64"] == nil || archives["darwin_arm64"] != nil {
		t.Fatal("mirror mixed later platforms", archives)
	}
	// An earlier priority remote owns the whole duplicate version, but exact
	// Linux download can still use the later hosted holder.
	patchRepo(t, f.fixture, "remote", func(c *repo.Config) { c.PriorityResolution = repo.Flex(true) })
	response = f.get("admin", virtualProviderBase+"versions")
	expect(t, response, 200)
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, version := range list.Versions {
		if version.Version == "1.0.0" && !slices.Equal(version.Platforms, []remotePlatform{{OS: "darwin", Arch: "arm64"}}) {
			t.Fatal(version)
		}
	}
	metadata = f.get("admin", virtualProviderBase+"1.0.0/download/linux/amd64")
	expect(t, metadata, 200)
	doc = decode(t, metadata.Body.Bytes())
	if providerURLClaim(t, f, doc["download_url"].(string)).Member != "provider" {
		t.Fatal(doc)
	}
}

func TestAccVirtualModuleRemoteClaimCannotBecomeHosted(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0")
	virtualMembers(t, f.fixture, "remote")
	metadata := f.do("GET", prefix+"virtual/v1/modules/ns/name/aws/1.0.0/download", "reader")
	expect(t, metadata, 204)
	public := metadata.Header().Get("X-Terraform-Get")
	claim := decodeVirtualClaim(t, public)
	if claim.Source == nil || claim.Member != "remote" {
		t.Fatal(claim)
	}
	item, err := f.db.Items().Get(t.Context(), "remote", true, claim.Path)
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := f.items.Blobs.Open(t.Context(), item.Actual.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.db.Repos().Get(t.Context(), "remote")
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce a legacy/direct-store class replacement. The API's normal
	// repository update validation is deliberately not bypassed in production.
	c.Class, c.TerraformType = repo.Local, repo.TerraformModule
	c.URL, c.TerraformRegistryURL, c.TerraformProvidersURL = "", "", ""
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.deploy("remote", claim.Path, string(data))
	response := f.do("GET", public, "")
	if response.Code != 403 {
		t.Fatal("remote source capability became hosted authority", response.Code, response.Body.String())
	}
}

type providerRevocationBlobs struct {
	blob.Store
	digest string
	once   sync.Once
	revoke func()
}

func (b *providerRevocationBlobs) Open(ctx context.Context, digest string) (io.ReadSeekCloser, blob.Info, error) {
	body, info, err := b.Store.Open(ctx, digest)
	if err == nil && digest == b.digest {
		b.once.Do(b.revoke)
	}
	return body, info, err
}

func scopedProviderPrincipal(t *testing.T, f *remoteProviderFixture) kernel.Principal {
	t.Helper()
	scope, err := kernel.ParseScope("artifact:provider/hosted/hashicorp/null/2.0.0/**:r")
	if err != nil {
		t.Fatal(err)
	}
	token := accesstest.Token{ID: "scoped-provider", Subject: "jfac@test/users/admin", Scope: scope, ExpiresAt: f.now.Add(time.Hour)}
	if err := f.db.Tokens().Create(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	p, err := f.kit.API().Authenticator.RestoreDownload(t.Context(), "admin", kernel.DownloadCredential{Kind: "token", TokenID: token.ID})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAccVirtualProviderCurrentMemberKeepsRealTokenScope(t *testing.T) {
	f, _ := virtualProviderFixture(t)
	p := scopedProviderPrincipal(t, f)
	apiBindings := f.kit.API()
	o := Options{
		Keys:            f.keys,
		Now:             func() time.Time { return f.now },
		Authenticator:   apiBindings.Authenticator,
		Authorizer:      apiBindings.Authorizer,
		Identity:        apiBindings.Identity,
		Restrictions:    apiBindings.Restrictions,
		AnonymousAccess: apiBindings.AnonymousAccess,
	}
	k := pkgroute.ItemKit(pkgroute.Options{Store: f.db, Items: func() *item.Service { return f.items }}, Protocol(o))
	r := httptest.NewRequest("GET", virtualProviderBase+"versions", nil)
	ctx, err := f.kit.Authorize(r.Context(), p, false)
	if err != nil {
		t.Fatal(err)
	}
	v, err := f.db.Repos().Get(t.Context(), "virtual")
	if err != nil {
		t.Fatal(err)
	}
	member, err := f.db.Repos().Get(t.Context(), "provider")
	if err != nil {
		t.Fatal(err)
	}
	r = k.Bind(r.WithContext(ctx), v)
	mr, err := k.MemberRequest(r, member)
	if err != nil {
		t.Fatal(err)
	}
	h := handler{k: k, o: o}
	current, err := h.currentProviderMember(r, mr)
	if err != nil {
		t.Fatal(err)
	}
	if pkgroute.RequestFrom(current).Repo.Key != "provider" || !k.Can(current, "hosted/hashicorp/null/2.0.0/archive.zip", api.ActionRead) {
		t.Fatal("lost real member binding")
	}
	if k.Can(current, "hosted/hashicorp/null/1.0.0/archive.zip", api.ActionRead) {
		t.Fatal("current member widened the real token scope")
	}
}

func TestAccVirtualProviderMetadataRefusesMidReadTokenRevocation(t *testing.T) {
	f, _ := virtualProviderFixture(t)
	p := scopedProviderPrincipal(t, f)
	sums, err := f.db.Items().Get(t.Context(), "provider", false, "hosted/hashicorp/null/2.0.0/terraform-provider-null_2.0.0_SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	f.items.Blobs = &providerRevocationBlobs{Store: f.items.Blobs, digest: sums.Actual.SHA256, revoke: func() {
		if err := f.db.Tokens().Delete(t.Context(), "scoped-provider"); err != nil {
			t.Fatal(err)
		}
	}}
	response := f.doPrincipal("GET", virtualProviderBase+"2.0.0/download/linux/amd64", p)
	if response.Code != 404 {
		t.Fatal("revoked credential issued hosted provider capability", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "?sig=") {
		t.Fatal("refused metadata disclosed capabilities")
	}
}

func TestAccVirtualProviderRawMirrorKeepsOwnerHashes(t *testing.T) {
	for _, algorithm := range []string{"h1", "zh", "none"} {
		t.Run(algorithm, func(t *testing.T) {
			f := newProviderFixture(t)
			virtualMembers(t, f.fixture, "mods", "remote")
			patchRepo(t, f.fixture, "remote", func(c *repo.Config) { c.TerraformRegistryURL = f.up.URL + "/smart" })
			permission, err := f.db.Permissions().Get(t.Context(), "remote-read")
			if err != nil {
				t.Fatal(err)
			}
			resource := permission.Resources[api.ResourceArtifact]
			resource.Targets["remote"] = accesstest.Target{Include: []string{"provider-mirrors/registry.terraform.io/hashicorp/null/**", "storage/**"}}
			permission.Resources[api.ResourceArtifact] = resource
			if err := f.db.Permissions().Update(t.Context(), permission); err != nil {
				t.Fatal(err)
			}
			h1, ok, err := tf.H1(bytes.NewReader(f.archive), int64(len(f.archive)))
			if err != nil || !ok {
				t.Fatal(err)
			}
			hashes := []string{}
			if algorithm == "h1" {
				hashes = []string{h1, "zh:" + strings.Repeat("0", 64)}
			}
			if algorithm == "zh" {
				hashes = []string{"zh:" + f.metadata["shasum"].(string)}
			}
			upstream := "/smart/providers/registry.terraform.io/hashicorp/null/"
			f.json(upstream+"index.json", map[string]any{"versions": map[string]any{"1.0.0": map[string]any{}}})
			f.json(upstream+"1.0.0.json", map[string]any{"archives": map[string]remoteMirrorArchive{"linux_amd64": {URL: "/storage/provider.zip", Hashes: hashes}}})
			base := prefix + "virtual/providers/registry.terraform.io/hashicorp/null/"
			expect(t, f.get("reader", base+"index.json"), 200)
			response := f.get("reader", base+"1.0.0.json")
			expect(t, response, 200)
			var doc struct {
				Archives map[string]remoteMirrorArchive `json:"archives"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			entry := doc.Archives["linux_amd64"]
			want := hashes
			if algorithm == "none" {
				want = []string{h1}
			}
			if !slices.Equal(entry.Hashes, want) || !strings.Contains(entry.URL, "/virtual/providers/storage/provider.zip?sig=") {
				t.Fatal(entry)
			}
			claim := providerURLClaim(t, f, entry.URL)
			if claim.Member != "remote" || claim.Source == nil || claim.Provider == nil || !claim.Provider.Mirror || claim.Provider.Host != "registry.terraform.io" {
				t.Fatal(claim)
			}
			delivered := f.get("", entry.URL)
			expect(t, delivered, 200)
			if !bytes.Equal(delivered.Body.Bytes(), f.archive) {
				t.Fatal("raw mirror representation changed")
			}
			patchRepo(t, f.fixture, "virtual", func(c *repo.Config) { c.ExcludesPattern = new("provider-mirrors/**") })
			expect(t, f.get("reader", base+"index.json"), 404)
		})
	}
}

func TestAccVirtualProviderMemberAndVirtualPolicies(t *testing.T) {
	for _, refusal := range []string{"module-only", "member-blackout", "member-pattern", "virtual-pattern", "nested-pattern", "unreadable"} {
		t.Run(refusal, func(t *testing.T) {
			f, _ := virtualProviderFixture(t)
			user := "admin"
			virtualMembers(t, f.fixture, "provider")
			switch refusal {
			case "module-only":
				virtualMembers(t, f.fixture, "mods")
			case "member-blackout":
				patchRepo(t, f.fixture, "provider", func(c *repo.Config) { c.BlackedOut = new(true) })
			case "member-pattern":
				patchRepo(t, f.fixture, "provider", func(c *repo.Config) { c.ExcludesPattern = new("**/*.zip") })
			case "virtual-pattern":
				patchRepo(t, f.fixture, "virtual", func(c *repo.Config) { c.ExcludesPattern = new("**/2.0.0/**") })
			case "nested-pattern":
				c := *repo.Defaults(&repo.Config{Key: "inner", Class: repo.Virtual, PackageType: repo.Terraform, Repositories: []string{"provider"}, ExcludesPattern: new("**/*.zip")})
				if err := f.db.Repos().Create(t.Context(), c); err != nil {
					t.Fatal(err)
				}
				virtualMembers(t, f.fixture, "inner")
			case "unreadable":
				user = "reader"
			}
			expect(t, f.get(user, virtualProviderBase+"versions"), 404)
			expect(t, f.get(user, virtualProviderBase+"2.0.0/download/linux/amd64"), 404)
			if len(f.up.Requests()) != 0 {
				t.Fatal("refused hosted members consulted upstream", f.up.Requests())
			}
		})
	}
}

func TestAccVirtualProviderRemoteKeepsRealDownloadPolicyPath(t *testing.T) {
	f := newProviderFixture(t)
	virtualMembers(t, f.fixture, "remote")
	patchRepo(t, f.fixture, "remote", func(c *repo.Config) {
		c.IncludesPattern = new(".terraform/**,storage/**")
		c.ExcludesPattern = new("hashicorp/null/**")
	})
	expect(t, f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64"), 200)
	response := f.get("reader", virtualProviderBase+"1.0.0/download/linux/amd64")
	expect(t, response, 200)
	doc := decode(t, response.Body.Bytes())
	expect(t, f.get("", doc["download_url"].(string)), 200)
	// Virtual and nested policy still name the delivered file's real path.
	patchRepo(t, f.fixture, "virtual", func(c *repo.Config) { c.ExcludesPattern = new("storage/**") })
	expect(t, f.get("reader", virtualProviderBase+"1.0.0/download/linux/amd64"), 404)
	expect(t, f.get("", doc["download_url"].(string)), 403)
}

func TestUnitTerraformStorageFaultNeverBecomesMemberMiss(t *testing.T) {
	for _, err := range []error{errors.Join(kernel.ErrStore, &source.HTTPError{Status: 403, Kind: source.FailureKindHTTP}), errors.Join(format.ErrStorageFault, kernel.ErrNotFound), errors.Join(kernel.ErrStore, forbidden)} {
		if modulePassedOver(err) || signedAuthorityRefusal(err) {
			t.Fatal("store fault became a refusal", err)
		}
	}
}
