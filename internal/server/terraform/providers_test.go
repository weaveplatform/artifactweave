package terraform

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	providerBase = prefix + "v1/providers/provider-rt-ns-hashicorp/null/"
	mirrorBase   = prefix + "provider/providers/registry.terraform.io/hashicorp/null/"
	releasePath  = "nested/HashiCorp/Null/3.3.2/"
)

func providerFixture(t *testing.T) (*fixture, terraformtest.Key, terraformtest.Release) {
	t.Helper()
	f := newFixture(t)
	k, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.Keys().
		CreateKeypair(t.Context(), types.Keypair{Name: "primary", Type: types.KeypairGPG, PublicKey: k.Public}); err != nil {
		t.Fatal(err)
	}
	c, err := f.db.Repos().Get(t.Context(), "provider")
	if err != nil {
		t.Fatal(err)
	}
	c.PrimaryKeyPairRef = "primary"
	if err = f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	rel := terraformtest.Release{
		Type:      "null",
		Version:   "3.3.2",
		Platforms: []string{"linux_amd64", "darwin_arm64"},
		Protocols: []string{"5.0", "6.0"},
	}
	for name, b := range rel.Files(k) {
		f.deploy("provider", releasePath+name, string(b))
	}
	return f, k, rel
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	v := map[string]any{}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func downloadDoc(t *testing.T, f *fixture) map[string]any {
	t.Helper()
	w := f.do("GET", providerBase+"3.3.2/download/linux/amd64", "admin")
	expect(t, w, 200)
	return decode(t, w.Body.Bytes())
}

func TestProviderRoutes(t *testing.T) {
	f, _, _ := providerFixture(t)
	for _, tc := range []struct {
		method, path, user string
		status             int
	}{
		{"GET", providerBase + "versions", "admin", 200},
		{"HEAD", providerBase + "versions", "admin", 200},
		{"GET", providerBase + "versions", "", 401},
		{"GET", providerBase + "versions", "reader", 403},
		{"POST", providerBase + "versions", "admin", 405},
		{"PUT", providerBase + "unknown", "admin", 404},
		{"GET", providerBase + "3.3.2/download/linux/amd64", "admin", 200},
		{"HEAD", providerBase + "3.3.2/download/linux/amd64", "admin", 200},
		{"GET", providerBase + "3.3.2/download/windows/amd64", "admin", 404},
		{"GET", providerBase + "1.0.0/download/linux/amd64", "admin", 404},
		{"GET", providerBase + "v3.3.2/download/linux/amd64", "admin", 404},
		{"GET", providerBase + "3.3.2/download/linux/x-y", "admin", 404},
		{"GET", prefix + "provider/v1/providers/HASHICORP/NULL/versions", "admin", 200},
		{"GET", prefix + "v1/providers/PROVIDER-rt-ns-hashicorp/null/versions", "admin", 200},
		{"GET", prefix + "v1/providers/mods-rt-ns-hashicorp/null/versions", "admin", 404},
		{"GET", prefix + "v1/providers/remote-rt-ns-hashicorp/null/versions", "admin", 501},
		{"GET", prefix + "v1/providers/virtual-rt-ns-hashicorp/null/versions", "admin", 501},
		{"GET", mirrorBase + "index.json", "admin", 200},
		{"HEAD", mirrorBase + "3.3.2.json", "admin", 200},
		{"POST", mirrorBase + "index.json", "admin", 405},
		{"GET", mirrorBase + "1.0.0.json", "admin", 404},
		{"GET", strings.ReplaceAll(mirrorBase, "registry.terraform.io", "private.example:8443") + "3.3.2.json", "admin", 200},
		{"GET", strings.ReplaceAll(mirrorBase, "registry.terraform.io", "bad_host") + "index.json", "admin", 404},
		{"GET", mirrorBase + "3.3.2", "admin", 404},
		{"GET", mirrorBase + "index.json/", "admin", 404},
		{"GET", strings.ReplaceAll(providerBase, "hashicorp", "bad.ns") + "versions", "admin", 404},
		{"GET", providerBase + "versions;prop=x", "admin", 404},
		{"GET", strings.ReplaceAll(providerBase, "hashicorp", "missing") + "versions", "admin", 404},
	} {
		t.Run(
			tc.method+tc.path+tc.user,
			func(t *testing.T) { expect(t, f.do(tc.method, tc.path, tc.user), tc.status) },
		)
	}
	w := f.do("GET", providerBase+"versions", "admin")
	if !strings.Contains(w.Body.String(), `"protocols":["5.0","6.0"]`) ||
		strings.Index(
			w.Body.String(),
			`"arch":"arm64"`,
		) > strings.Index(
			w.Body.String(),
			`"arch":"amd64"`,
		) {
		t.Fatal(w.Body)
	}
	doc := downloadDoc(t, f)
	if doc["filename"] != "terraform-provider-null_3.3.2_linux_amd64.zip" || doc["shasum"] == "" {
		t.Fatal(doc)
	}
	for _, field := range []string{"download_url", "shasums_url", "shasums_signature_url"} {
		u, ok := doc[field].(string)
		if !ok || !strings.Contains(u, "/provider/providers/nested/") {
			t.Fatal(doc)
		}
		expect(t, f.do("GET", u, ""), 200)
		expect(t, f.do("HEAD", u, ""), 200)
	}
	w = f.do("GET", mirrorBase+"3.3.2.json", "admin")
	if !strings.Contains(w.Body.String(), `"h1:`) {
		t.Fatal(w.Body)
	}
}

func TestProviderVerificationAndKeyRotation(t *testing.T) {
	f, k, rel := providerFixture(t)
	sums := rel.Name("SHA256SUMS")
	sig := rel.Name("SHA256SUMS.sig")
	arc := rel.Name("linux_amd64.zip")
	original := rel.Files(k)
	for _, tc := range []struct{ name, body string }{
		{sums, "missing filename\n"}, {sums, strings.Repeat("0", 64) + "  " + arc + "\n"}, {sig, "bad signature"},
	} {
		f.deploy("provider", releasePath+tc.name, tc.body)
		expect(t, f.do("GET", providerBase+"3.3.2/download/linux/amd64", "admin"), 409)
		expect(t, f.do("GET", mirrorBase+"3.3.2.json", "admin"), 200)
		f.deploy("provider", releasePath+tc.name, string(original[tc.name]))
	}
	other, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	pair := types.Keypair{Name: "primary", Type: types.KeypairGPG, PublicKey: other.Public}
	if err = f.db.Keys().UpdateKeypair(t.Context(), pair); err != nil {
		t.Fatal(err)
	}
	expect(t, f.do("GET", providerBase+"3.3.2/download/linux/amd64", "admin"), 409)
	pair.Name = "secondary"
	pair.PublicKey = k.Public
	if err = f.db.Keys().CreateKeypair(t.Context(), pair); err != nil {
		t.Fatal(err)
	}
	c, err := f.db.Repos().Get(t.Context(), "provider")
	if err != nil {
		t.Fatal(err)
	}
	c.SecondaryKeyPairRef = "secondary"
	if err = f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	doc := downloadDoc(t, f)
	encoded, _ := json.Marshal(doc["signing_keys"])
	if !strings.Contains(string(encoded), other.KeyID()) ||
		!strings.Contains(string(encoded), k.KeyID()) {
		t.Fatal(string(encoded))
	}
	for _, name := range []string{"", "absent", "rsa", "invalid"} {
		if name == "rsa" || name == "invalid" {
			kind := types.KeypairRSA
			if name == "invalid" {
				kind = types.KeypairGPG
			}
			if err = f.db.Keys().
				CreateKeypair(t.Context(), types.Keypair{Name: name, Type: kind, PublicKey: "bad"}); err != nil {
				t.Fatal(err)
			}
		}
		c.PrimaryKeyPairRef = name
		if err = f.db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		expect(t, f.do("GET", providerBase+"3.3.2/download/linux/amd64", "admin"), 409)
		expect(t, f.do("GET", mirrorBase+"3.3.2.json", "admin"), 200)
	}
}

func TestProviderVisibilityAndPermissionRevocation(t *testing.T) {
	f, _, rel := providerFixture(t)
	archive := releasePath + rel.Name("linux_amd64.zip")
	grant := func(paths ...string) {
		t.Helper()
		_ = f.db.Permissions().Delete(t.Context(), "providers")
		err := f.db.Permissions().
			Create(t.Context(), types.Permission{Name: "providers", Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {Actions: types.Grants{Users: map[string]types.Actions{"reader": types.ActionRead}}, Targets: map[string]types.Target{"provider": {Include: paths}}}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	grant(archive)
	expect(t, f.do("GET", providerBase+"versions", "reader"), 200)
	expect(t, f.do("GET", providerBase+"3.3.2/download/linux/amd64", "reader"), 403)
	w := f.do("GET", mirrorBase+"3.3.2.json", "reader")
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), "darwin") {
		t.Fatal(w.Body)
	}
	grant(archive, releasePath+rel.Name("SHA256SUMS"))
	expect(t, f.do("GET", providerBase+"versions", "reader"), 200)
	grant(archive, releasePath+rel.Name("SHA256SUMS"), releasePath+rel.Name("SHA256SUMS.sig"))
	w = f.do("GET", providerBase+"3.3.2/download/linux/amd64", "reader")
	expect(t, w, 200)
	doc := decode(t, w.Body.Bytes())
	if !strings.Contains(w.Body.String(), `"protocols":["5.0"]`) {
		t.Fatal(w.Body)
	}
	// Removing just one sidecar's Read does not leave its signed URL usable.
	grant(archive)
	for _, field := range []string{"shasums_url", "shasums_signature_url"} {
		u, _ := doc[field].(string)
		expect(t, f.do("GET", u, "admin"), 403)
	}
	u, _ := doc["download_url"].(string)
	expect(t, f.do("GET", u, ""), 200)
	grant("nothing")
	expect(t, f.do("GET", u, "admin"), 403)
}

func TestProviderSignedSnapshots(t *testing.T) {
	for _, field := range []string{"download_url", "shasums_url", "shasums_signature_url"} {
		t.Run(field, func(t *testing.T) {
			f, _, _ := providerFixture(t)
			doc := downloadDoc(t, f)
			u, _ := doc[field].(string)
			h := handler{o: Options{Keys: f.keys, Now: func() time.Time { return f.now }}}
			token := strings.Split(u, "?sig=")[1]
			c, ok := h.verify(token)
			if !ok {
				t.Fatal("invalid fixture")
			}
			f.deploy("provider", c.Path, "replacement")
			expect(t, f.do("GET", u, ""), 403)
		})
	}
	f, _, _ := providerFixture(t)
	doc := downloadDoc(t, f)
	u, _ := doc["download_url"].(string)
	expect(t, f.do("GET", strings.Replace(u, "/providers/", "/v1/modules/", 1), ""), 403)
	expect(t, f.do("GET", u+"changed", ""), 403)
	f.now = f.now.Add(90 * time.Second)
	expect(t, f.do("GET", u, ""), 403)
}

func TestProviderManifestAndFaults(t *testing.T) {
	f, _, rel := providerFixture(t)
	for _, body := range []string{"bad", `{"metadata":{"protocol_versions":[]}}`, `{"metadata":{"protocol_versions":["x"]}}`, `{"metadata":{"protocol_versions":["7.x"]}}`, `{"metadata":{"protocol_versions":["7."]}}`, `{"metadata":{"protocol_versions":["7.0"]}}`} {
		f.deploy("provider", releasePath+rel.Name("manifest.json"), body)
		w := f.do("GET", providerBase+"versions", "admin")
		expect(t, w, 200)
		want := "5.0"
		if strings.Contains(body, "7.0") {
			want = "7.0"
		}
		if !strings.Contains(w.Body.String(), `"protocols":["`+want+`"]`) {
			t.Fatal(w.Body)
		}
	}
	for _, fail := range []string{"package", "versions", "files", "item", "gone"} {
		f.read = failingStore{f.db, fail}
		f.h = f.router(f.keys)
		status := 500
		if fail == "gone" {
			status = 404
		}
		expect(t, f.do("GET", providerBase+"versions", "admin"), status)
	}
	f.read = f.db
	f.h = f.router(nil)
	expect(t, f.do("GET", mirrorBase+"3.3.2.json", "admin"), 501)
	expect(t, f.do("GET", providerBase+"3.3.2/download/linux/amd64", "admin"), 501)
	for _, host := range []string{"user@example.com", "bad..host", "-bad.test", "bad-.test", "bad.test:abc", "bad.test?x", "bad.test#x", strings.Repeat("x", 64) + ".test", strings.Repeat("x.", 130)} {
		if validHostname(host) {
			t.Errorf("accepted %s", host)
		}
	}
}

// Swap a sidecar after the handler captures its item but before it reads bytes.
// It must verify and sign the old digest; following that URL then refuses the
// replacement. Reading by path twice would sign unverified new bytes.
type (
	replacingStore struct {
		store.Store
		f        *fixture
		path     string
		replaced bool
	}
	replacingItems struct {
		store.ItemStore
		s *replacingStore
	}
)

func (s *replacingStore) Items() store.ItemStore { return replacingItems{s.Store.Items(), s} }
func (i replacingItems) GetByID(ctx context.Context, id int64) (types.Item, error) {
	it, err := i.ItemStore.GetByID(ctx, id)
	if err == nil && it.FullPath() == i.s.path && !i.s.replaced {
		i.s.replaced = true
		i.s.f.deploy("provider", i.s.path, "replacement during metadata")
	}
	return it, err
}

func TestProviderMetadataReplacement(t *testing.T) {
	for _, suffix := range []string{"SHA256SUMS", "SHA256SUMS.sig", "linux_amd64.zip"} {
		t.Run(suffix, func(t *testing.T) {
			f, _, rel := providerFixture(t)
			s := &replacingStore{Store: f.db, f: f, path: releasePath + rel.Name(suffix)}
			f.read = s
			f.h = f.router(f.keys)
			doc := downloadDoc(t, f)
			field := "download_url"
			if suffix == "SHA256SUMS" {
				field = "shasums_url"
			}
			if suffix == "SHA256SUMS.sig" {
				field = "shasums_signature_url"
			}
			u, _ := doc[field].(string)
			expect(t, f.do("GET", u, ""), 403)
		})
	}
}

type (
	staleMetaStore    struct{ store.Store }
	staleMetaPackages struct{ store.PackageStore }
)

func (s staleMetaStore) Packages() store.PackageStore { return staleMetaPackages{s.Store.Packages()} }

func (p staleMetaPackages) Versions(ctx context.Context, id int64) ([]types.PackageVersion, error) {
	vs, err := p.PackageStore.Versions(ctx, id)
	for i := range vs {
		vs[i].Metadata = []byte(`{"archives":{}}`)
	}
	return vs, err
}

func TestMirrorRehashesStaleMetadata(t *testing.T) {
	f, _, _ := providerFixture(t)
	before := f.do("GET", mirrorBase+"3.3.2.json", "admin")
	expect(t, before, 200)
	f.read = staleMetaStore{f.db}
	f.h = f.router(f.keys)
	after := f.do("GET", mirrorBase+"3.3.2.json", "admin")
	expect(t, after, 200)
	if before.Body.String() != after.Body.String() {
		t.Fatalf("stale metadata changed mirror hashes:\n%s\n%s", before.Body, after.Body)
	}
}
