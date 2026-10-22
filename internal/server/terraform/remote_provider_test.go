package terraform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	tf "github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const remoteProviderBase = prefix + "remote/v1/providers/hashicorp/null/"

type remoteProviderFixture struct {
	*remoteFixture
	key                      terraformtest.Key
	archive, sums, signature []byte
	metadata                 map[string]any
	q                        providerOp
}

func newProviderFixture(t *testing.T) *remoteProviderFixture {
	t.Helper()
	f := newRemoteFixture(t)
	c, _ := f.db.Repos().Get(t.Context(), "remote")
	c.TerraformProvidersURL = f.up.URL
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	permission, _ := f.db.Permissions().Get(t.Context(), "remote-read")
	resource := permission.Resources[types.ResourceArtifact]
	resource.Targets["remote"] = types.Target{Include: []string{"hashicorp/null/**"}}
	permission.Resources[types.ResourceArtifact] = resource
	if err := f.db.Permissions().Update(t.Context(), permission); err != nil {
		t.Fatal(err)
	}
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	q := providerOp{name: "hashicorp/null", version: "1.0.0", os: "linux", arch: "amd64"}
	body := archiveZip(t, "terraform-provider-null_v1.0.0")
	digest := sha256.Sum256(body)
	filename := "terraform-provider-null_1.0.0_linux_amd64.zip"
	sums := []byte(hex.EncodeToString(digest[:]) + "  " + filename + "\n")
	p := &remoteProviderFixture{
		remoteFixture: f,
		key:           key,
		archive:       body,
		sums:          sums,
		signature:     key.Sign(sums),
		q:             q,
	}
	p.metadata = map[string]any{
		"protocols":             []string{"5.0"},
		"os":                    "linux",
		"arch":                  "amd64",
		"filename":              filename,
		"download_url":          "/storage/provider.zip",
		"shasums_url":           "/storage/SHA256SUMS",
		"shasums_signature_url": "/storage/SHA256SUMS.sig",
		"shasum":                hex.EncodeToString(digest[:]),
		"signing_keys": map[string]any{
			"gpg_public_keys": []map[string]string{
				{
					"key_id":          key.KeyID(),
					"ascii_armor":     key.Public,
					"source":          "controlled upstream",
					"source_url":      "https://upstream.example/security",
					"trust_signature": "preserved",
				},
			},
		},
	}
	f.json(
		"/.well-known/terraform.json",
		map[string]string{
			"providers.v1": "/registry/providers/",
			"modules.v1":   "/registry/modules/",
		},
	)
	f.json(
		"/registry/providers/hashicorp/null/versions",
		map[string]any{
			"versions": []remoteProviderVersion{
				{
					Version:   "1.0.0",
					Protocols: []string{"5.0"},
					Platforms: []remotePlatform{{"linux", "amd64"}},
				},
			},
		},
	)
	p.publish()
	return p
}

func (p *remoteProviderFixture) publish() {
	p.json("/registry/providers/hashicorp/null/1.0.0/download/linux/amd64", p.metadata)
	p.up.Set(
		"/storage/provider.zip",
		fakeupstream.Resource{
			Body:   p.archive,
			Header: http.Header{"Content-Type": {"application/zip"}},
		},
	)
	p.up.Set(
		"/storage/SHA256SUMS",
		fakeupstream.Resource{Body: p.sums, Header: http.Header{"Content-Type": {"text/plain"}}},
	)
	p.up.Set(
		"/storage/SHA256SUMS.sig",
		fakeupstream.Resource{
			Body:   p.signature,
			Header: http.Header{"Content-Type": {"application/octet-stream"}},
		},
	)
}

func TestRemoteProviderRegistryAndOffline(t *testing.T) {
	f := newProviderFixture(t)
	versions := f.get("reader", remoteProviderBase+"versions")
	if versions.Code != 200 || !strings.Contains(versions.Body.String(), "1.0.0") {
		t.Fatalf("versions %d %s", versions.Code, versions.Body)
	}
	response := f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
	if response.Code != 200 {
		t.Fatalf("metadata %d %s", response.Code, response.Body)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	upstreamKeys, _ := json.Marshal(f.metadata["signing_keys"])
	if !bytes.Equal(doc["signing_keys"], upstreamKeys) {
		t.Fatalf("keys changed: %s", doc["signing_keys"])
	}
	for key, want := range map[string][]byte{"download_url": f.archive, "shasums_url": f.sums, "shasums_signature_url": f.signature} {
		var target string
		_ = json.Unmarshal(doc[key], &target)
		got := f.get("", target)
		if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), want) {
			t.Fatalf("%s: %d %s", key, got.Code, got.Body)
		}
	}
	c, _ := f.db.Repos().Get(t.Context(), "remote")
	c.Offline = new(true)
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.up.Close()
	response = f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
	if response.Code != 200 {
		t.Fatalf("offline %d %s", response.Code, response.Body)
	}
	_ = json.Unmarshal(response.Body.Bytes(), &doc)
	for _, key := range []string{"download_url", "shasums_url", "shasums_signature_url"} {
		var target string
		_ = json.Unmarshal(doc[key], &target)
		got := f.get("", target)
		if got.Code != 200 {
			t.Fatalf("offline %s: %d %s", key, got.Code, got.Body)
		}
	}
}

func TestRemoteProviderInvalidBundleCanRetry(t *testing.T) {
	for _, fault := range []string{"archive", "sums", "signature", "wrong-signer", "missing-entry", "key-id", "host", "json"} {
		t.Run(fault, func(t *testing.T) {
			f := newProviderFixture(t)
			originalArchive, originalSums, originalSignature := bytes.Clone(
				f.archive,
			), bytes.Clone(
				f.sums,
			), bytes.Clone(
				f.signature,
			)
			switch fault {
			case "archive":
				f.archive = archiveZip(t, "tampered")
			case "sums":
				f.sums = []byte(
					strings.Repeat("0", 64) + "  " + f.metadata["filename"].(string) + "\n",
				)
			case "signature":
				f.signature = []byte("bad signature")
			case "wrong-signer":
				other, e := terraformtest.NewKey()
				if e != nil {
					t.Fatal(e)
				}
				f.signature = other.Sign(f.sums)
			case "missing-entry":
				f.sums = []byte(strings.Repeat("0", 64) + "  other.zip\n")
			case "key-id":
				f.metadata["signing_keys"].(map[string]any)["gpg_public_keys"].([]map[string]string)[0]["key_id"] = "0000000000000000"
			case "host":
				f.metadata["download_url"] = "https://hostile.invalid/provider.zip"
			}
			f.publish()
			if fault == "json" {
				raw, _ := json.Marshal(f.metadata)
				f.up.Set(
					"/registry/providers/hashicorp/null/1.0.0/download/linux/amd64",
					fakeupstream.Resource{
						Body:   append(raw, []byte(" {}")...),
						Header: http.Header{"Content-Type": {"application/json"}},
					},
				)
			}
			got := f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
			if got.Code == 200 || strings.Contains(got.Body.String(), "?sig=") {
				t.Fatalf("bad bundle advertised %d %s", got.Code, got.Body)
			}
			stage := map[string]string{"archive": "archive", "sums": "checksum", "signature": "signature", "wrong-signer": "signature", "missing-entry": "checksum", "key-id": "metadata", "host": "destination", "json": "metadata"}[fault]
			if !strings.Contains(got.Body.String(), stage) {
				t.Fatalf("missing actionable %s error: %s", stage, got.Body)
			}
			f.archive, f.sums, f.signature = originalArchive, originalSums, originalSignature
			f.metadata["download_url"] = "/storage/provider.zip"
			f.metadata["signing_keys"].(map[string]any)["gpg_public_keys"].([]map[string]string)[0]["key_id"] = f.key.KeyID()
			f.publish()
			got = f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
			if got.Code != 200 {
				t.Fatalf("corrected %d %s", got.Code, got.Body)
			}
		})
	}
}

func TestRemoteProviderMirrorAndCurrentCredentials(t *testing.T) {
	f := newProviderFixture(t)
	upstream, _ := url.Parse(f.up.URL)
	mirror := prefix + "remote/providers/" + upstream.Host + "/hashicorp/null/"
	got := f.get("reader", mirror+"index.json")
	if got.Code != 200 {
		t.Fatalf("index %d %s", got.Code, got.Body)
	}
	got = f.get("reader", mirror+"1.0.0.json")
	if got.Code != 200 {
		t.Fatalf("mirror %d %s", got.Code, got.Body)
	}
	var doc struct {
		Archives map[string]struct {
			URL    string   `json:"url"`
			Hashes []string `json:"hashes"`
		} `json:"archives"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	a := doc.Archives["linux_amd64"]
	if len(a.Hashes) != 2 || !strings.HasPrefix(a.Hashes[0], "h1:") ||
		!strings.HasPrefix(a.Hashes[1], "zh:") {
		t.Fatalf("hashes %v", a.Hashes)
	}
	got = f.get("", a.URL)
	if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), f.archive) {
		t.Fatalf("archive %d %s", got.Code, got.Body)
	}
	user, _ := f.db.Users().Get(t.Context(), "reader")
	user.PasswordHash = []byte("changed")
	if err := f.db.Users().Update(t.Context(), user); err != nil {
		t.Fatal(err)
	}
	got = f.get("", a.URL)
	if got.Code != 403 {
		t.Fatalf("password revocation %d %s", got.Code, got.Body)
	}
	f.now = f.now.Add(2 * time.Minute)
	got = f.get("", a.URL)
	if got.Code != 403 {
		t.Fatalf("expired %d %s", got.Code, got.Body)
	}
}

func (p *remoteProviderFixture) get(user, target string) *httptest.ResponseRecorder {
	return p.do(http.MethodGet, target, user)
}

func TestRemoteProviderUpstreamMirror(t *testing.T) {
	for _, hashes := range []string{"h1", "zh", "none"} {
		t.Run(hashes, func(t *testing.T) {
			f := newProviderFixture(t)
			c, _ := f.db.Repos().Get(t.Context(), "remote")
			c.TerraformRegistryURL = f.up.URL + "/smart"
			if err := f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			permission, _ := f.db.Permissions().Get(t.Context(), "remote-read")
			resource := permission.Resources[types.ResourceArtifact]
			resource.Targets["remote"] = types.Target{
				Include: []string{"provider-mirrors/registry.terraform.io/hashicorp/null/**"},
			}
			permission.Resources[types.ResourceArtifact] = resource
			if err := f.db.Permissions().Update(t.Context(), permission); err != nil {
				t.Fatal(err)
			}
			upstream := "/smart/providers/registry.terraform.io/hashicorp/null/"
			f.json(
				upstream+"index.json",
				map[string]any{"versions": map[string]any{"1.0.0": map[string]any{}}},
			)
			h1, ok, err := tf.H1(bytes.NewReader(f.archive), int64(len(f.archive)))
			if err != nil || !ok {
				t.Fatal(err)
			}
			list := []string{}
			switch hashes {
			case "h1":
				list = []string{h1, "zh:" + strings.Repeat("0", 64)}
			case "zh":
				list = []string{"zh:" + f.metadata["shasum"].(string)}
			}
			f.json(
				upstream+"1.0.0.json",
				map[string]any{
					"archives": map[string]remoteMirrorArchive{
						"linux_amd64": {URL: "/storage/provider.zip", Hashes: list},
					},
				},
			)
			base := prefix + "remote/providers/registry.terraform.io/hashicorp/null/"
			got := f.get("reader", base+"index.json")
			if got.Code != 200 {
				t.Fatalf("index %d %s", got.Code, got.Body)
			}
			got = f.get("reader", base+"1.0.0.json")
			if got.Code != 200 {
				t.Fatalf("metadata %d %s", got.Code, got.Body)
			}
			var doc struct {
				Archives map[string]remoteMirrorArchive `json:"archives"`
			}
			_ = json.Unmarshal(got.Body.Bytes(), &doc)
			entry := doc.Archives["linux_amd64"]
			if len(entry.Hashes) == 0 {
				t.Fatal("mirror output lacks integrity")
			}
			got = f.get("", entry.URL)
			if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), f.archive) {
				t.Fatalf("archive %d %s", got.Code, got.Body)
			}
			c.Offline = new(true)
			if err = f.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			f.up.Close()
			got = f.get("reader", base+"1.0.0.json")
			if got.Code != 200 {
				t.Fatalf("offline metadata %d %s", got.Code, got.Body)
			}
			_ = json.Unmarshal(got.Body.Bytes(), &doc)
			got = f.get("", doc.Archives["linux_amd64"].URL)
			if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), f.archive) {
				t.Fatalf("offline archive %d %s", got.Code, got.Body)
			}
		})
	}
}

func TestRemoteProviderMissingPlatformHints(t *testing.T) {
	f := newProviderFixture(t)
	f.json(
		"/registry/providers/hashicorp/null/versions",
		map[string]any{"versions": []any{map[string]string{"version": "1.0.0"}}},
	)
	got := f.get("reader", remoteProviderBase+"versions")
	if got.Code != 200 || !strings.Contains(got.Body.String(), "1.0.0") ||
		strings.Contains(got.Body.String(), "platforms") {
		t.Fatalf("optional hints %d %s", got.Code, got.Body)
	}
	if f.up.Count(
		http.MethodGet,
		"/registry/providers/hashicorp/null/1.0.0/download/linux/amd64",
	) != 1 {
		t.Fatal("missing actual readability proof")
	}
	for _, request := range f.up.Requests() {
		if strings.Contains(request.Path, "/download/") &&
			!strings.HasSuffix(request.Path, "/linux/amd64") {
			t.Fatal("unnecessary platform probing", request.Path)
		}
	}
	expect(
		t,
		f.get("reader", prefix+"remote/providers/arbitrary.invalid/hashicorp/null/index.json"),
		404,
	)
	expect(t, f.get("reader", remoteProviderBase+"2.0.0/download/linux/amd64"), 404)
	expect(t, f.do("POST", remoteProviderBase+"versions", "reader"), 405)
}

func TestRemoteProviderNoStoreAndRotation(t *testing.T) {
	for _, transient := range []string{"repository", "upstream"} {
		t.Run(transient, func(t *testing.T) {
			f := newProviderFixture(t)
			if transient == "repository" {
				c, _ := f.db.Repos().Get(t.Context(), "remote")
				c.StoreArtifactsLocally = new(false)
				if err := f.db.Repos().Update(t.Context(), c); err != nil {
					t.Fatal(err)
				}
			} else {
				f.up.Set(
					"/storage/provider.zip",
					fakeupstream.Resource{
						Body: f.archive,
						Header: http.Header{
							"Content-Type":  {"application/zip"},
							"Cache-Control": {"no-store"},
						},
					},
				)
			}
			got := f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
			if got.Code != 200 {
				t.Fatalf("no-store %d %s", got.Code, got.Body)
			}
			var raw map[string]json.RawMessage
			_ = json.Unmarshal(got.Body.Bytes(), &raw)
			var target string
			_ = json.Unmarshal(raw["download_url"], &target)
			got = f.get("", target)
			if got.Code != 200 {
				t.Fatalf("transient download %d %s", got.Code, got.Body)
			}
			if _, err := f.db.Items().
				Get(t.Context(), "remote", true, providerArchivePath(f.q)); err == nil {
				t.Fatal("no-store archive was persisted")
			}
		})
	}
	f := newProviderFixture(t)
	got := f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
	if got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	var before map[string]json.RawMessage
	_ = json.Unmarshal(got.Body.Bytes(), &before)
	var old string
	_ = json.Unmarshal(before["download_url"], &old)
	key, err := terraformtest.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	f.metadata["signing_keys"] = map[string]any{
		"gpg_public_keys": []map[string]string{{"key_id": key.KeyID(), "ascii_armor": key.Public}},
	}
	f.signature = key.Sign(f.sums)
	f.publish()
	f.clock.Advance(6 * time.Second)
	got = f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
	if got.Code != 200 || !strings.Contains(got.Body.String(), key.KeyID()) {
		t.Fatalf("rotation %d %s", got.Code, got.Body)
	}
	expect(t, f.get("", old), 403)
}
