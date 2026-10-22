package terraform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/remote"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (f *remoteProviderFixture) configure(update func(*repo.Config)) {
	f.t.Helper()
	c, err := f.db.Repos().Get(f.t.Context(), "remote")
	if err != nil {
		f.t.Fatal(err)
	}
	update(&c)
	if err = f.db.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

func (f *remoteProviderFixture) allowPrivate(origins ...string) {
	f.t.Helper()
	resolver, err := remote.NewResolver(f.db, nil, remote.ResolverOptions{
		RevisionKey: []byte(strings.Repeat("k", 32)), AllowedPrivateOrigins: origins,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.policy.Options = resolver.Options
}

func TestRemoteProviderRedirectsAndCredentials(t *testing.T) {
	endpoints := []string{
		"/.well-known/terraform.json",
		"/registry/providers/hashicorp/null/versions",
		"/registry/providers/hashicorp/null/1.0.0/download/linux/amd64",
		"/storage/SHA256SUMS",
		"/storage/SHA256SUMS.sig",
		"/storage/provider.zip",
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			f := newProviderFixture(t)
			other := fakeupstream.New(t, fakeupstream.Options{})
			f.allowPrivate(other.URL)
			f.up.Set(endpoint, fakeupstream.Resource{Redirect: other.URL + "/hostile"})
			expect(t, f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64"), 502)
			if len(other.Requests()) != 0 {
				t.Fatal("hostile redirect was contacted")
			}
			f.json(
				"/.well-known/terraform.json",
				map[string]string{"providers.v1": "/registry/providers/"},
			)
			f.json(
				"/registry/providers/hashicorp/null/versions",
				map[string]any{
					"versions": []remoteProviderVersion{
						{Version: "1.0.0", Platforms: []remotePlatform{{"linux", "amd64"}}},
					},
				},
			)
			f.publish()
			expect(t, f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64"), 200)
		})
	}
	t.Run("allowed storage origin receives no registry credentials", func(t *testing.T) {
		f := newProviderFixture(t)
		storage := fakeupstream.New(t, fakeupstream.Options{})
		f.allowPrivate(storage.URL)
		f.configure(func(c *repo.Config) {
			c.TerraformProvidersURL = storage.URL + "/packages"
			c.Username, c.Password = "upstream-user", "upstream-secret"
		})
		f.metadata["download_url"] = storage.URL + "/packages/provider.zip"
		f.metadata["shasums_url"] = storage.URL + "/packages/SHA256SUMS"
		f.metadata["shasums_signature_url"] = storage.URL + "/packages/SHA256SUMS.sig"
		f.publish()
		storage.Set(
			"/packages/provider.zip",
			fakeupstream.Resource{
				Body:   f.archive,
				Header: http.Header{"Content-Type": {"application/zip"}},
			},
		)
		storage.Set("/packages/SHA256SUMS", fakeupstream.Resource{Body: f.sums})
		storage.Set("/packages/SHA256SUMS.sig", fakeupstream.Resource{Body: f.signature})
		expect(t, f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64"), 200)
		credential := "Basic " + base64.StdEncoding.EncodeToString(
			[]byte("upstream-user:upstream-secret"),
		)
		for _, request := range f.up.Requests() {
			if !request.HeaderMatches("Authorization", credential) {
				t.Fatal("configured registry credential missing", request.Path)
			}
		}
		if storage.Count("GET", "") != 3 {
			t.Fatal("wrong storage request count", storage.Requests())
		}
		for _, request := range storage.Requests() {
			if request.Header.Get("Authorization") != "" {
				t.Fatal("registry credentials sent to storage")
			}
		}
	})
}

func TestRemoteProviderDiscoveryModesAndAdmission(t *testing.T) {
	for _, mode := range []string{"fallback", "api-prefix", "redirect-base", "discovery-500", "wrong-mime"} {
		t.Run(mode, func(t *testing.T) {
			f := newProviderFixture(t)
			service := "/v1/providers/"
			switch mode {
			case "fallback":
				f.up.Delete("/.well-known/terraform.json")
			case "api-prefix":
				f.configure(
					func(c *repo.Config) { c.TerraformRegistryURL = f.up.URL + "/api/terraform/upstream" },
				)
				service = "/api/terraform/upstream/v1/providers/"
			case "redirect-base":
				f.up.Set(
					"/.well-known/terraform.json",
					fakeupstream.Resource{Redirect: "/moved/discovery.json"},
				)
				f.json("/moved/discovery.json", map[string]string{"providers.v1": "providers/"})
				service = "/moved/providers/"
				f.metadata["download_url"] = "../../../../../../../storage/provider.zip"
			case "discovery-500":
				f.up.Set("/.well-known/terraform.json", fakeupstream.Resource{Status: 500})
			case "wrong-mime":
				data, _ := json.Marshal(f.metadata)
				f.up.Set(
					"/registry/providers/hashicorp/null/1.0.0/download/linux/amd64",
					fakeupstream.Resource{
						Body:   data,
						Header: http.Header{"Content-Type": {"text/html"}},
					},
				)
			}
			if mode == "discovery-500" || mode == "wrong-mime" {
				got := f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
				if got.Code == 200 {
					t.Fatal("invalid upstream admitted")
				}
				if mode == "wrong-mime" {
					f.publish()
					expect(t, f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64"), 200)
				}
				return
			}
			f.json(
				service+"hashicorp/null/versions",
				map[string]any{
					"versions": []remoteProviderVersion{
						{Version: "1.0.0", Platforms: []remotePlatform{{"linux", "amd64"}}},
					},
				},
			)
			f.json(service+"hashicorp/null/1.0.0/download/linux/amd64", f.metadata)
			expect(t, f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64"), 200)
		})
	}
}

func TestRemoteProviderCurrentDisclosureAndSource(t *testing.T) {
	f := newProviderFixture(t)
	second := make(map[string]any)
	for k, v := range f.metadata {
		second[k] = v
	}
	second["arch"] = "arm64"
	f.json("/registry/providers/hashicorp/null/1.0.0/download/linux/arm64", second)
	f.json(
		"/registry/providers/hashicorp/null/versions",
		map[string]any{
			"versions": []remoteProviderVersion{
				{
					Version:   "1.0.0",
					Platforms: []remotePlatform{{"linux", "amd64"}, {"linux", "arm64"}},
				},
			},
		},
	)
	original := f.svc.Revalidate
	f.svc.Revalidate = func(ctx context.Context, q source.Request, p source.Permit) error {
		if q.Resource.Kind == source.Artifact &&
			strings.Contains(q.Resource.Path, "linux_arm64.zip") {
			return source.ErrDenied
		}
		return original(ctx, q, p)
	}
	got := f.get("reader", remoteProviderBase+"versions")
	expect(t, got, 403)
	if strings.Contains(got.Body.String(), "arm64") {
		t.Fatal("revoked platform disclosed")
	}
	f.svc.Revalidate = original
	permission, _ := f.db.Permissions().Get(t.Context(), "remote-read")
	resource := permission.Resources[types.ResourceArtifact]
	resource.Targets["remote"] = types.Target{
		Include: []string{"hashicorp/null/**"},
		Exclude: []string{"**/*_arm64.zip"},
	}
	permission.Resources[types.ResourceArtifact] = resource
	if err := f.db.Permissions().Update(t.Context(), permission); err != nil {
		t.Fatal(err)
	}
	got = f.get("reader", remoteProviderBase+"versions")
	expect(t, got, 200)
	if strings.Contains(got.Body.String(), "arm64") {
		t.Fatal("excluded platform disclosed")
	}
	got = f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
	expect(t, got, 200)
	var doc map[string]json.RawMessage
	_ = json.Unmarshal(got.Body.Bytes(), &doc)
	var target string
	_ = json.Unmarshal(doc["download_url"], &target)
	other := fakeupstream.New(t, fakeupstream.Options{})
	f.configure(func(c *repo.Config) { c.TerraformProvidersURL = other.URL })
	expect(t, f.get("", target), 403)
	if len(other.Requests()) != 0 {
		t.Fatal("stale signed URL contacted replacement source")
	}
}

func TestProviderDestinationPolicy(t *testing.T) {
	c := repo.Config{
		URL:                   "https://modules.example/",
		TerraformRegistryURL:  "https://registry.example/api/",
		TerraformProvidersURL: "https://storage.example/packages/",
	}
	guard := providerURLGuard(c, "https://registry.example/api/package")
	for _, raw := range []string{"https://storage.example/packages/a.zip", "https://registry.example/api/versions"} {
		u, _ := url.Parse(raw)
		if err := guard(t.Context(), u); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{"https://modules.example/a.zip", "https://storage.example/other/a.zip", "https://codeload.github.com/a/b/zip/v1", "http://storage.example/packages/a.zip", "https://user:secret@storage.example/packages/a.zip"} {
		u, _ := url.Parse(raw)
		if err := guard(t.Context(), u); err == nil {
			t.Fatal("allowed", raw)
		}
	}
	if err := guard(t.Context(), nil); err == nil {
		t.Fatal("nil destination accepted")
	}
}

func TestRemoteProviderPartialBundleStaysPrivate(t *testing.T) {
	f := newProviderFixture(t)
	f.up.Delete("/storage/provider.zip")
	got := f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
	if got.Code == 200 || bytes.Contains(got.Body.Bytes(), []byte("?sig=")) {
		t.Fatal("missing package advertised", got.Code)
	}
	f.publish()
	// A real upstream 404 follows the shared negative-cache policy.
	f.clock.Advance(31 * time.Minute)
	expect(t, f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64"), 200)
}

func TestRemoteProviderSignedCredentialRevocation(t *testing.T) {
	f := newProviderFixture(t)
	token := types.Token{
		ID:        "provider-parent",
		Subject:   "jfac@test/users/reader",
		Scope:     types.UserScope(),
		ExpiresAt: f.now.Add(time.Hour),
	}
	if err := f.db.Tokens().Create(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	principal, err := f.auth.RestoreDownload(
		t.Context(),
		"reader",
		auth.DownloadCredential{Kind: "token", TokenID: token.ID},
		f.db.Tokens(),
	)
	if err != nil {
		t.Fatal(err)
	}
	response := f.doPrincipal("GET", remoteProviderBase+"1.0.0/download/linux/amd64", principal)
	expect(t, response, 200)
	var metadata map[string]json.RawMessage
	_ = json.Unmarshal(response.Body.Bytes(), &metadata)
	var target string
	_ = json.Unmarshal(metadata["download_url"], &target)
	expect(t, f.get("", target), 200)
	before := len(f.up.Requests())
	if err = f.db.Tokens().Delete(t.Context(), token.ID); err != nil {
		t.Fatal(err)
	}
	expect(t, f.get("", target), 403)
	if len(f.up.Requests()) != before {
		t.Fatal("revoked token caused upstream contact")
	}
}

func TestRemoteProviderPlatformAndVersionSelection(t *testing.T) {
	f := newProviderFixture(t)
	q := providerOp{name: "hashicorp/null", version: "2.0.0", os: "darwin", arch: "arm64"}
	body := archiveZip(t, "terraform-provider-null_v2.0.0")
	digest := sha256.Sum256(body)
	filename := "terraform-provider-null_2.0.0_darwin_arm64.zip"
	sums := []byte(hex.EncodeToString(digest[:]) + "  " + filename + "\n")
	doc := map[string]any{}
	for k, v := range f.metadata {
		doc[k] = v
	}
	doc["os"], doc["arch"], doc["filename"], doc["shasum"] = q.os, q.arch, filename, hex.EncodeToString(
		digest[:],
	)
	doc["download_url"], doc["shasums_url"], doc["shasums_signature_url"] = "/storage/v2/provider.zip", "/storage/v2/SHA256SUMS", "/storage/v2/SHA256SUMS.sig"
	f.json("/registry/providers/hashicorp/null/2.0.0/download/darwin/arm64", doc)
	f.json(
		"/registry/providers/hashicorp/null/versions",
		map[string]any{
			"versions": []remoteProviderVersion{
				{Version: "1.0.0", Platforms: []remotePlatform{{"linux", "amd64"}}},
				{Version: "2.0.0", Platforms: []remotePlatform{{"darwin", "arm64"}}},
			},
		},
	)
	f.up.Set(
		"/storage/v2/provider.zip",
		fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": {"application/zip"}}},
	)
	f.up.Set("/storage/v2/SHA256SUMS", fakeupstream.Resource{Body: sums})
	f.up.Set("/storage/v2/SHA256SUMS.sig", fakeupstream.Resource{Body: f.key.Sign(sums)})
	for _, platform := range []struct {
		path string
		want []byte
	}{{"1.0.0/download/linux/amd64", f.archive}, {"2.0.0/download/darwin/arm64", body}} {
		response := f.get("reader", remoteProviderBase+platform.path)
		expect(t, response, 200)
		var metadata map[string]json.RawMessage
		_ = json.Unmarshal(response.Body.Bytes(), &metadata)
		var target string
		_ = json.Unmarshal(metadata["download_url"], &target)
		got := f.get("", target)
		expect(t, got, 200)
		if !bytes.Equal(got.Body.Bytes(), platform.want) {
			t.Fatal("wrong platform/version package")
		}
	}
	expect(t, f.get("reader", remoteProviderBase+"2.0.0/download/linux/amd64"), 404)
}

func TestRemoteProviderCachedIndexDoesNotPublishIncompleteBundle(t *testing.T) {
	for _, mode := range []string{"missing", "tampered", "no-store", "repository-no-store"} {
		t.Run(mode, func(t *testing.T) {
			f := newProviderFixture(t)
			switch mode {
			case "missing":
				f.up.Delete("/storage/provider.zip")
			case "tampered":
				f.up.Set(
					"/storage/provider.zip",
					fakeupstream.Resource{
						Body:   archiveZip(t, "other"),
						Header: http.Header{"Content-Type": {"application/zip"}},
					},
				)
			case "no-store":
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
			case "repository-no-store":
				f.configure(func(c *repo.Config) { c.StoreArtifactsLocally = new(false) })
			}
			// Discovery is authoritative metadata, not a claim that a full installable
			// package has already been fetched. It must not prefetch binary candidates.
			expect(t, f.get("reader", remoteProviderBase+"versions"), 200)
			if f.up.Count("GET", "/storage/provider.zip") != 0 {
				t.Fatal("versions prefetched archive")
			}
			packageResponse := f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
			if mode == "missing" || mode == "tampered" {
				if packageResponse.Code == 200 ||
					strings.Contains(packageResponse.Body.String(), "?sig=") {
					t.Fatal(
						"incomplete package advertised",
						packageResponse.Code,
						packageResponse.Body,
					)
				}
			} else {
				expect(t, packageResponse, 200)
			}
			f.configure(func(c *repo.Config) { c.Offline = new(true) })
			f.up.Close()
			index := f.get("reader", remoteProviderBase+"versions")
			if mode == "repository-no-store" {
				if index.Code == 200 {
					t.Fatal("no-store metadata persisted")
				}
			} else {
				expect(t, index, 200)
			}
			packageResponse = f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64")
			if packageResponse.Code == 200 ||
				strings.Contains(packageResponse.Body.String(), "?sig=") {
				t.Fatal(
					"offline missing bundle advertised",
					packageResponse.Code,
					packageResponse.Body,
				)
			}
		})
	}
}

func TestRemoteProviderLargeAuthoritativePlatformIndex(t *testing.T) {
	f := newProviderFixture(t)
	versions := make([]remoteProviderVersion, 40)
	for index := range versions {
		versions[index] = remoteProviderVersion{
			Version:   fmt.Sprintf("1.%d.0", index),
			Protocols: []string{"5.0"},
			Platforms: providerPlatforms,
		}
	}
	f.json("/registry/providers/hashicorp/null/versions", map[string]any{"versions": versions})
	got := f.get("reader", remoteProviderBase+"versions")
	expect(t, got, 200)
	var doc struct {
		Versions []remoteProviderVersion `json:"versions"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Versions) != len(versions) {
		t.Fatal("versions lost", len(doc.Versions))
	}
	combinations := 0
	for _, version := range doc.Versions {
		combinations += len(version.Platforms)
	}
	if combinations != len(versions)*len(providerPlatforms) ||
		combinations <= moduleDiscoveryLimit {
		t.Fatal("authoritative platforms lost", combinations)
	}
	for _, request := range f.up.Requests() {
		if strings.Contains(request.Path, "/download/") ||
			strings.HasPrefix(request.Path, "/storage/") {
			t.Fatal("availability listing prefetched a package", request.Path)
		}
	}
}

func TestRemoteProviderMissingHintsStopsAtReadablePlatform(t *testing.T) {
	f := newProviderFixture(t)
	f.json(
		"/registry/providers/hashicorp/null/versions",
		map[string]any{"versions": []remoteProviderVersion{{Version: "1.0.0"}}},
	)
	f.up.Delete("/registry/providers/hashicorp/null/1.0.0/download/linux/amd64")
	metadata := make(map[string]any)
	for key, value := range f.metadata {
		metadata[key] = value
	}
	metadata["arch"] = "arm64"
	metadata["filename"] = "terraform-provider-null_1.0.0_linux_arm64.zip"
	f.json("/registry/providers/hashicorp/null/1.0.0/download/linux/arm64", metadata)
	got := f.get("reader", remoteProviderBase+"versions")
	expect(t, got, 200)
	if strings.Contains(got.Body.String(), "platforms") {
		t.Fatal("unobserved platform list invented", got.Body)
	}
	reads := map[string]bool{}
	for _, request := range f.up.Requests() {
		if strings.Contains(request.Path, "/download/") {
			reads[request.Path] = true
		}
	}
	if len(reads) != 2 {
		t.Fatalf("wanted one missing platform then first readable proof, got %d paths", len(reads))
	}
	original := f.svc.Revalidate
	f.svc.Revalidate = func(ctx context.Context, q source.Request, p source.Permit) error {
		if q.Resource.Kind == source.Artifact &&
			strings.HasSuffix(q.Resource.Path, "linux_arm64.zip") {
			return source.ErrDenied
		}
		return original(ctx, q, p)
	}
	expect(t, f.get("reader", remoteProviderBase+"versions"), 403)
}
