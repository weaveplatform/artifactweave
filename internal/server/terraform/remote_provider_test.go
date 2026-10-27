package terraform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	tf "github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
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

func (f *remoteProviderFixture) publish() {
	f.json("/registry/providers/hashicorp/null/1.0.0/download/linux/amd64", f.metadata)
	f.up.Set(
		"/storage/provider.zip",
		fakeupstream.Resource{
			Body:   f.archive,
			Header: http.Header{"Content-Type": {"application/zip"}},
		},
	)
	f.up.Set(
		"/storage/SHA256SUMS",
		fakeupstream.Resource{Body: f.sums, Header: http.Header{"Content-Type": {"text/plain"}}},
	)
	f.up.Set(
		"/storage/SHA256SUMS.sig",
		fakeupstream.Resource{
			Body:   f.signature,
			Header: http.Header{"Content-Type": {"application/octet-stream"}},
		},
	)
}

func TestAccRemoteProviderRegistryAndOffline(t *testing.T) {
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

func TestAccRemoteProviderInvalidBundleCanRetry(t *testing.T) {
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

func TestAccRemoteProviderRegistryRejectsMissingAndOversizedMaterial(t *testing.T) {
	const target = remoteProviderBase + "1.0.0/download/linux/amd64"
	for _, tc := range []struct {
		name  string
		fault func(*remoteProviderFixture)
	}{
		{
			name: "invalid registry discovery",
			fault: func(f *remoteProviderFixture) {
				f.json("/.well-known/terraform.json", map[string]any{"providers.v1": "https://foreign.invalid/private/"})
			},
		},
		{
			name: "invalid version document",
			fault: func(f *remoteProviderFixture) {
				f.json("/registry/providers/hashicorp/null/versions", map[string]any{"versions": "1.0.0"})
			},
		},
		{
			name: "invalid package metadata",
			fault: func(f *remoteProviderFixture) {
				delete(f.metadata, "download_url")
				f.publish()
			},
		},
		{
			name: "oversized checksums",
			fault: func(f *remoteProviderFixture) {
				f.sums = bytes.Repeat([]byte("x"), moduleMetadataLimit+1)
				f.publish()
			},
		},
		{
			name: "oversized detached signature",
			fault: func(f *remoteProviderFixture) {
				f.signature = bytes.Repeat([]byte("x"), moduleMetadataLimit+1)
				f.publish()
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProviderFixture(t)
			tc.fault(f)
			response := f.get("reader", target)
			if response.Code != http.StatusBadGateway ||
				strings.Contains(response.Body.String(), "?sig=") ||
				strings.Contains(response.Body.String(), "foreign.invalid") {
				t.Fatalf(
					"invalid upstream material was disclosed: %d %s",
					response.Code,
					response.Body.String(),
				)
			}
			if _, err := f.db.Items().
				Get(t.Context(), "remote", true, providerArchivePath(f.q)); err == nil {
				t.Fatal("provider archive was published from an invalid bundle")
			}
		})
	}
}

func TestAccRemoteProviderMirrorAndCurrentCredentials(t *testing.T) {
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

func (f *remoteProviderFixture) get(user, target string) *httptest.ResponseRecorder {
	return f.do(http.MethodGet, target, user)
}

func TestAccRemoteProviderUpstreamMirror(t *testing.T) {
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

func configureProviderMirrorFaultFixture(t *testing.T, f *remoteProviderFixture) {
	t.Helper()
	f.configure(func(c *repo.Config) { c.TerraformRegistryURL = f.up.URL + "/smart" })
	permission, err := f.db.Permissions().Get(t.Context(), "remote-read")
	if err != nil {
		t.Fatal(err)
	}
	resource := permission.Resources[types.ResourceArtifact]
	resource.Targets["remote"] = types.Target{
		Include: []string{"provider-mirrors/registry.terraform.io/hashicorp/null/**"},
	}
	permission.Resources[types.ResourceArtifact] = resource
	if err := f.db.Permissions().Update(t.Context(), permission); err != nil {
		t.Fatal(err)
	}
	const upstream = "/smart/providers/registry.terraform.io/hashicorp/null/"
	f.json(
		upstream+"index.json",
		map[string]any{"versions": map[string]any{"1.0.0": map[string]any{}}},
	)
	f.json(upstream+"1.0.0.json", map[string]any{"archives": map[string]any{
		"linux_amd64": map[string]any{
			"url":    "/storage/provider.zip",
			"hashes": []string{"zh:" + f.metadata["shasum"].(string)},
		},
	}})
}

func TestAccRemoteProviderMirrorFaultsDoNotPublish(t *testing.T) {
	const upstream = "/smart/providers/registry.terraform.io/hashicorp/null/"
	const target = prefix + "remote/providers/registry.terraform.io/hashicorp/null/1.0.0.json"
	for _, tc := range []struct {
		name   string
		status int
		fault  func(*remoteProviderFixture)
	}{
		{
			name: "missing version in index", status: http.StatusNotFound,
			fault: func(f *remoteProviderFixture) {
				f.json(upstream+"index.json", map[string]any{"versions": map[string]any{"2.0.0": map[string]any{}}})
			},
		},
		{
			name: "invalid version index", status: http.StatusBadGateway,
			fault: func(f *remoteProviderFixture) {
				f.json(upstream+"index.json", map[string]any{"versions": []string{"1.0.0"}})
			},
		},
		{
			name: "missing platform", status: http.StatusNotFound,
			fault: func(f *remoteProviderFixture) {
				f.json(upstream+"1.0.0.json", map[string]any{"archives": map[string]any{}})
			},
		},
		{
			name: "foreign archive", status: http.StatusBadGateway,
			fault: func(f *remoteProviderFixture) {
				f.json(upstream+"1.0.0.json", map[string]any{"archives": map[string]any{
					"linux_amd64": map[string]any{"url": "https://foreign.invalid/private.zip", "hashes": []string{"zh:" + f.metadata["shasum"].(string)}},
				}})
			},
		},
		{
			name: "mismatched zip hash", status: http.StatusBadGateway,
			fault: func(f *remoteProviderFixture) {
				f.json(upstream+"1.0.0.json", map[string]any{"archives": map[string]any{
					"linux_amd64": map[string]any{"url": "/storage/provider.zip", "hashes": []string{"zh:" + strings.Repeat("0", 64)}},
				}})
			},
		},
		{
			name: "corrupt archive", status: http.StatusBadGateway,
			fault: func(f *remoteProviderFixture) {
				f.up.Set("/storage/provider.zip", fakeupstream.Resource{
					Body: []byte("not a zip"), Header: http.Header{"Content-Type": {"application/zip"}},
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProviderFixture(t)
			configureProviderMirrorFaultFixture(t, f)
			tc.fault(f)
			response := f.get("reader", target)
			if response.Code != tc.status || strings.Contains(response.Body.String(), "?sig=") ||
				strings.Contains(response.Body.String(), "foreign.invalid") {
				t.Fatalf(
					"fault disclosed a mirror archive: %d %s",
					response.Code,
					response.Body.String(),
				)
			}
			if f.up.Count(http.MethodGet, "/storage/provider.zip") > 0 &&
				(tc.name == "missing version in index" || tc.name == "invalid version index" ||
					tc.name == "missing platform" || tc.name == "foreign archive") {
				t.Fatal("unnecessary archive fetch before mirror metadata admission")
			}
			if _, err := f.db.Items().Get(t.Context(), "remote", true,
				"provider-mirrors/registry.terraform.io/hashicorp/null/1.0.0/linux_amd64.zip"); err == nil {
				t.Fatal("failed mirror archive was published")
			}
			f.json(
				upstream+"index.json",
				map[string]any{"versions": map[string]any{"1.0.0": map[string]any{}}},
			)
			f.json(upstream+"1.0.0.json", map[string]any{"archives": map[string]any{
				"linux_amd64": map[string]any{
					"url":    "/storage/provider.zip",
					"hashes": []string{"zh:" + f.metadata["shasum"].(string)},
				},
			}})
			f.publish()
			// Upstream 404s and rejected artifacts use the shared bounded cache.
			// Retry after that window instead of treating stale negatives as a defect.
			f.clock.Advance(31 * time.Minute)
			recovered := f.get("reader", target)
			if recovered.Code != http.StatusOK {
				t.Fatalf(
					"valid mirror was not available after repair: %d %s",
					recovered.Code,
					recovered.Body.String(),
				)
			}
			var doc struct {
				Archives map[string]remoteMirrorArchive `json:"archives"`
			}
			if err := json.Unmarshal(recovered.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			entry := doc.Archives["linux_amd64"]
			if entry.URL == "" || len(entry.Hashes) == 0 {
				t.Fatalf(
					"recovered mirror lacks signed archive and integrity hash: %s",
					recovered.Body.String(),
				)
			}
			archive := f.get("", entry.URL)
			if archive.Code != http.StatusOK || !bytes.Equal(archive.Body.Bytes(), f.archive) {
				t.Fatalf("recovered archive changed: %d %s", archive.Code, archive.Body.String())
			}
		})
	}
}

func TestAccRemoteProviderMirrorIndexBoundsAndPlatformPermissions(t *testing.T) {
	const upstream = "/smart/providers/registry.terraform.io/hashicorp/null/"
	const base = prefix + "remote/providers/registry.terraform.io/hashicorp/null/"
	t.Run("version list bound", func(t *testing.T) {
		f := newProviderFixture(t)
		configureProviderMirrorFaultFixture(t, f)
		versions := make(map[string]any, moduleDiscoveryLimit+1)
		for i := range moduleDiscoveryLimit + 1 {
			versions[fmt.Sprintf("1.0.%d", i)] = map[string]any{}
		}
		f.json(upstream+"index.json", map[string]any{"versions": versions})
		response := f.get("reader", base+"index.json")
		if response.Code != http.StatusBadGateway ||
			!strings.Contains(response.Body.String(), "bound") {
			t.Fatalf("unbounded mirror index: %d %s", response.Code, response.Body.String())
		}
		if f.up.Count(http.MethodGet, upstream+"1.0.0.json") != 0 {
			t.Fatal("mirror version metadata fetched past index bound")
		}
	})
	t.Run("missing version metadata", func(t *testing.T) {
		f := newProviderFixture(t)
		configureProviderMirrorFaultFixture(t, f)
		f.up.Delete(upstream + "1.0.0.json")
		response := f.get("reader", base+"index.json")
		if response.Code == http.StatusOK || strings.Contains(response.Body.String(), "?sig=") {
			t.Fatalf(
				"incomplete mirror index was published: %d %s",
				response.Code,
				response.Body.String(),
			)
		}
	})
	t.Run("unreadable platform is hidden", func(t *testing.T) {
		f := newProviderFixture(t)
		configureProviderMirrorFaultFixture(t, f)
		permission, err := f.db.Permissions().Get(t.Context(), "remote-read")
		if err != nil {
			t.Fatal(err)
		}
		resource := permission.Resources[types.ResourceArtifact]
		resource.Targets["remote"] = types.Target{Include: []string{
			"provider-mirrors/registry.terraform.io/hashicorp/null/index.json",
			"provider-mirrors/registry.terraform.io/hashicorp/null/1.0.0.json",
		}}
		permission.Resources[types.ResourceArtifact] = resource
		if err := f.db.Permissions().Update(t.Context(), permission); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{base + "index.json", base + "1.0.0.json"} {
			response := f.get("reader", path)
			if response.Code != http.StatusNotFound ||
				strings.Contains(response.Body.String(), "linux_amd64") {
				t.Fatalf(
					"denied platform disclosed by %s: %d %s",
					path,
					response.Code,
					response.Body.String(),
				)
			}
		}
		if f.up.Count(http.MethodGet, "/storage/provider.zip") != 0 {
			t.Fatal("denied platform archive was fetched")
		}
	})
}

type brokenProviderReader struct {
	*bytes.Reader
	readError bool
	seekError bool
}

func (r *brokenProviderReader) Read(p []byte) (int, error) {
	if r.readError {
		return 0, errors.New("private upstream read failure")
	}
	return r.Reader.Read(p)
}

func (r *brokenProviderReader) Seek(offset int64, whence int) (int64, error) {
	if r.seekError {
		return 0, errors.New("private upstream seek failure")
	}
	return r.Reader.Seek(offset, whence)
}

func (*brokenProviderReader) Close() error { return nil }

func TestUnitRemoteProviderCompanionReadFaults(t *testing.T) {
	h := handler{}
	r := httptest.NewRequest(http.MethodGet, "https://registry.test/provider", nil)
	for _, tc := range []struct {
		name   string
		result source.Result
	}{
		{"missing bytes", source.Result{}},
		{"failed read", source.Result{Stream: &source.Stream{Body: &brokenProviderReader{
			Reader: bytes.NewReader([]byte("secret")), readError: true,
		}}}},
		{"oversized bytes", source.Result{Stream: &source.Stream{Body: io.NopCloser(bytes.NewReader([]byte("oversized")))}}},
		{"failed rewind", source.Result{Stream: &source.Stream{Body: &brokenProviderReader{
			Reader: bytes.NewReader([]byte("signature")), seekError: true,
		}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit := int64(64)
			if tc.name == "oversized bytes" {
				limit = 3
			}
			data, err := h.providerBytes(r, tc.result, limit)
			if len(data) != 0 || err == nil {
				t.Fatalf("companion %q leaked data or error: %q, %v", tc.name, data, err)
			}
			var api *apierror.Error
			if errors.As(err, &api) && api.Status != http.StatusBadGateway {
				t.Fatalf("companion %q returned status %d", tc.name, api.Status)
			}
		})
	}
	body := &brokenProviderReader{Reader: bytes.NewReader([]byte("archive")), seekError: true}
	sum := sha256.Sum256([]byte("archive"))
	if err := verifyMirrorArchive(
		t.Context(),
		body,
		7,
		[]string{"zh:" + hex.EncodeToString(sum[:])},
	); err == nil ||
		strings.Contains(err.Error(), "private upstream") {
		t.Fatalf("unseekable mirror archive accepted or leaked: %v", err)
	}
}

func TestAccRemoteSignedProviderRejectsChangedClaimsAndMissingArchive(t *testing.T) {
	f := newProviderFixture(t)
	response := f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
	if response.Code != http.StatusOK {
		t.Fatalf("valid package descriptor failed: %d %s", response.Code, response.Body.String())
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := json.Unmarshal(doc["download_url"], &original); err != nil || original == "" {
		t.Fatalf("missing signed provider URL: %v", err)
	}
	u, err := url.Parse(original)
	if err != nil {
		t.Fatal(err)
	}
	h := handler{o: Options{Keys: f.keys, Now: func() time.Time { return f.now }}}
	base, ok := h.verify(u.Query().Get("sig"))
	if !ok || base.Provider == nil || base.Credential == nil || base.Source == nil {
		t.Fatal("fixture did not issue a fully bound provider URL")
	}
	for _, tc := range []struct {
		name   string
		change func(*claims)
	}{
		{"missing credential", func(c *claims) { c.Credential = nil }},
		{"missing source", func(c *claims) { c.Source = nil }},
		{"invalid package kind", func(c *claims) { p := *c.Provider; p.Kind = 3; c.Provider = &p }},
		{"different provider path", func(c *claims) { p := *c.Provider; p.Name = "hashicorp/other"; c.Provider = &p }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.change(&c)
			before := len(f.up.Requests())
			got := f.get("", f.signedClaimURL(t, original, "providers/", c))
			if got.Code != http.StatusForbidden || len(f.up.Requests()) != before {
				t.Fatalf("invalid signed claim used upstream: %d %s", got.Code, got.Body.String())
			}
		})
	}
	f.up.Delete("/storage/provider.zip")
	f.clock.Advance(31 * time.Minute)
	cached := f.get("", original)
	if cached.Code != http.StatusOK || !bytes.Equal(cached.Body.Bytes(), f.archive) {
		t.Fatalf("validated cached archive was lost: %d %s", cached.Code, cached.Body.String())
	}
	transient := newProviderFixture(t)
	transient.configure(func(c *repo.Config) { c.StoreArtifactsLocally = new(false) })
	descriptor := transient.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
	if descriptor.Code != http.StatusOK {
		t.Fatalf("transient descriptor failed: %d %s", descriptor.Code, descriptor.Body.String())
	}
	if err := json.Unmarshal(descriptor.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	var transientURL string
	if err := json.Unmarshal(doc["download_url"], &transientURL); err != nil || transientURL == "" {
		t.Fatalf("missing transient signed URL: %v", err)
	}
	transient.up.Delete("/storage/provider.zip")
	transient.clock.Advance(31 * time.Minute)
	got := transient.get("", transientURL)
	if got.Code == http.StatusOK || strings.Contains(got.Body.String(), "?sig=") {
		t.Fatalf("uncached missing provider archive delivered: %d %s", got.Code, got.Body.String())
	}
}

func TestAccRemoteProviderVisibilityRequiresReadablePlatform(t *testing.T) {
	t.Run("unreadable platform stays private", func(t *testing.T) {
		f := newProviderFixture(t)
		permission, err := f.db.Permissions().Get(t.Context(), "remote-read")
		if err != nil {
			t.Fatal(err)
		}
		resource := permission.Resources[types.ResourceArtifact]
		resource.Targets["remote"] = types.Target{Include: []string{
			"hashicorp/null/1.0.0/terraform-provider-null_1.0.0_windows_amd64.zip",
		}}
		permission.Resources[types.ResourceArtifact] = resource
		if err := f.db.Permissions().Update(t.Context(), permission); err != nil {
			t.Fatal(err)
		}
		host, err := url.Parse(f.up.URL)
		if err != nil {
			t.Fatal(err)
		}
		mirror := prefix + "remote/providers/" + host.Host + "/hashicorp/null/"
		for _, path := range []string{remoteProviderBase + "versions", mirror + "index.json", mirror + "1.0.0.json"} {
			response := f.get("reader", path)
			if response.Code != http.StatusNotFound ||
				strings.Contains(response.Body.String(), "linux_amd64") {
				t.Fatalf(
					"unreadable provider platform disclosed by %s: %d %s",
					path,
					response.Code,
					response.Body.String(),
				)
			}
		}
		if f.up.Count(http.MethodGet, "/storage/provider.zip") != 0 {
			t.Fatal("unreadable platform fetched its archive")
		}
	})
	t.Run("missing package does not populate mirror", func(t *testing.T) {
		f := newProviderFixture(t)
		f.up.Delete("/registry/providers/hashicorp/null/1.0.0/download/linux/amd64")
		host, err := url.Parse(f.up.URL)
		if err != nil {
			t.Fatal(err)
		}
		path := prefix + "remote/providers/" + host.Host + "/hashicorp/null/1.0.0.json"
		response := f.get("reader", path)
		if response.Code != http.StatusNotFound ||
			strings.Contains(response.Body.String(), "?sig=") {
			t.Fatalf(
				"incomplete mirror version advertised: %d %s",
				response.Code,
				response.Body.String(),
			)
		}
	})
	t.Run("malformed descriptor with missing hints is not advertised", func(t *testing.T) {
		f := newProviderFixture(t)
		f.json("/registry/providers/hashicorp/null/versions", map[string]any{
			"versions": []any{map[string]string{"version": "1.0.0"}},
		})
		f.json(
			"/registry/providers/hashicorp/null/1.0.0/download/linux/amd64",
			map[string]any{"filename": "wrong.zip"},
		)
		response := f.get("reader", remoteProviderBase+"versions")
		if response.Code != http.StatusBadGateway ||
			strings.Contains(response.Body.String(), "?sig=") {
			t.Fatalf(
				"unvalidated provider version advertised: %d %s",
				response.Code,
				response.Body.String(),
			)
		}
	})
}

func TestAccRemoteRegistryRejectsUnsafeConfiguredURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  func(*remoteProviderFixture) string
	}{
		{"non-http registry", func(*remoteProviderFixture) string { return "file:///private/registry" }},
		{"query-bearing registry", func(f *remoteProviderFixture) string { return f.up.URL + "/?token=private" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProviderFixture(t)
			f.configure(func(c *repo.Config) { c.TerraformRegistryURL = tc.url(f) })
			before := len(f.up.Requests())
			for _, path := range []string{remoteModuleBase + "versions", remoteProviderBase + "versions"} {
				response := f.get("admin", path)
				if response.Code != http.StatusBadGateway ||
					strings.Contains(response.Body.String(), "private") {
					t.Fatalf(
						"unsafe registry URL accepted or leaked by %s: %d %s",
						path,
						response.Code,
						response.Body.String(),
					)
				}
			}
			if len(f.up.Requests()) != before {
				t.Fatal("unsafe registry URL contacted upstream")
			}
		})
	}
}

func TestAccRemoteProviderMissingPlatformHints(t *testing.T) {
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

func TestAccRemoteProviderNoStoreAndRotation(t *testing.T) {
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
