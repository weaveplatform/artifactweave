package terraformhttp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
)

// grant adds paths to the ones the reader may read in the remote.
func (f *remoteFixture) grant(paths ...string) {
	f.t.Helper()
	permission, err := f.db.Permissions().Get(f.t.Context(), "remote-read")
	if err != nil {
		f.t.Fatal(err)
	}
	resource := permission.Resources[accessapi.ResourceArtifact]
	target := resource.Targets["remote"]
	target.Include = append(append([]string{}, target.Include...), paths...)
	resource.Targets["remote"] = target
	permission.Resources[accessapi.ResourceArtifact] = resource
	if err := f.db.Permissions().Update(f.t.Context(), permission); err != nil {
		f.t.Fatal(err)
	}
}

// release publishes version of the provider name for linux_amd64: the
// registry's version list and package metadata on the fixture's upstream,
// and the archive, SHA256SUMS and signature on files in dir, under the
// names a registry gives them, the signature's with HashiCorp's key ID as
// registry.terraform.io names it. It returns the archive.
func (f *remoteProviderFixture) release(
	files *fakeupstream.Server,
	name, version, dir string,
) []byte {
	f.t.Helper()
	_, typ, _ := strings.Cut(name, "/")
	stem := "terraform-provider-" + typ + "_" + version
	archive := archiveZip(f.t, "terraform-provider-"+typ+"_v"+version)
	digest := sha256.Sum256(archive)
	sums := []byte(hex.EncodeToString(digest[:]) + "  " + stem + "_linux_amd64.zip\n")
	urls := map[string]string{}
	for key, file := range map[string]struct {
		name string
		body []byte
	}{
		"download_url":          {stem + "_linux_amd64.zip", archive},
		"shasums_url":           {stem + "_SHA256SUMS", sums},
		"shasums_signature_url": {stem + "_SHA256SUMS.72D7468F.sig", f.key.Sign(sums)},
	} {
		files.Set(dir+"/"+file.name, fakeupstream.Resource{
			Body:   file.body,
			Header: http.Header{"Content-Type": {"application/octet-stream"}},
		})
		urls[key] = files.URL + dir + "/" + file.name
	}
	metadata := map[string]any{}
	for key, value := range f.metadata {
		metadata[key] = value
	}
	for key, value := range urls {
		metadata[key] = value
	}
	metadata["filename"] = stem + "_linux_amd64.zip"
	metadata["shasum"] = hex.EncodeToString(digest[:])
	f.json("/registry/providers/"+name+"/"+version+"/download/linux/amd64", metadata)
	f.json("/registry/providers/"+name+"/versions", map[string]any{
		"versions": []remoteProviderVersion{{
			Version:   version,
			Protocols: []string{"5.0"},
			Platforms: []remotePlatform{{"linux", "amd64"}},
		}},
	})
	return archive
}

// providerURLs is the package metadata answer's three signed URLs.
type providerURLs struct {
	Download  string `json:"download_url"`
	Sums      string `json:"shasums_url"`
	Signature string `json:"shasums_signature_url"`
}

func packageURLs(t *testing.T, got *httptest.ResponseRecorder) providerURLs {
	t.Helper()
	var doc providerURLs
	if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &doc) != nil {
		t.Fatalf("package metadata %d %s", got.Code, got.Body)
	}
	return doc
}

// resolve asks for name's versions and its package metadata as user, and
// downloads the three files the metadata names, checking the archive's
// bytes. It returns the metadata's URLs.
func (f *remoteProviderFixture) resolve(user, name, version string, archive []byte) providerURLs {
	f.t.Helper()
	base := prefix + "remote/v1/providers/" + name + "/"
	wantListed(f.t, f.get(user, base+"versions"), version)
	got := f.get(user, base+version+"/download/linux/amd64")
	urls := packageURLs(f.t, got)
	for _, target := range []string{urls.Download, urls.Sums, urls.Signature} {
		expect(f.t, f.get("", target), http.StatusOK)
	}
	if download := f.get("", urls.Download); !bytes.Equal(download.Body.Bytes(), archive) {
		f.t.Fatalf("archive bytes changed: %s", download.Body)
	}
	return urls
}

// cached fails unless the remote's cache holds each of paths.
func (f *remoteFixture) cached(paths ...string) {
	f.t.Helper()
	for _, path := range paths {
		if _, err := f.db.Items().Get(f.t.Context(), "remote", true, path); err != nil {
			f.t.Fatalf("%s is not cached: %v", path, err)
		}
	}
}

// uncached fails if the remote's cache holds any of paths.
func (f *remoteFixture) uncached(paths ...string) {
	f.t.Helper()
	for _, path := range paths {
		_, err := f.db.Items().Get(f.t.Context(), "remote", true, path)
		if !errors.Is(err, kernel.ErrNotFound) {
			f.t.Fatalf("%s is cached: %v", path, err)
		}
	}
}

// JFrog's include pattern examples for a HashiCorp provider name its
// metadata under .terraform/providers/hashicorp/<type>/ and its files under
// terraform-provider-<type>/ (Terraform repositories doc, "Configure a
// Remote Terraform Repository"). A remote caches each file at its URL's
// path below the Providers URL, so the files pass those patterns, read
// permission is checked there, and the signed URLs name those paths.
func TestAccRemoteProviderDocumentedPatternsAdmitHashiCorpFiles(t *testing.T) {
	f := newProviderFixture(t)
	archive := f.release(f.up, "hashicorp/null", "1.0.0", "/terraform-provider-null/1.0.0")
	f.grant("terraform-provider-null/**")
	patchRepo(t, f.fixture, "remote", func(c *repo.Config) {
		c.IncludesPattern = new(
			"**/.terraform/providers/hashicorp/null/**,**/terraform-provider-null/**",
		)
	})
	urls := f.resolve("reader", "hashicorp/null", "1.0.0", archive)
	files := []string{
		"terraform-provider-null/1.0.0/terraform-provider-null_1.0.0_linux_amd64.zip",
		"terraform-provider-null/1.0.0/terraform-provider-null_1.0.0_SHA256SUMS",
		"terraform-provider-null/1.0.0/terraform-provider-null_1.0.0_SHA256SUMS.72D7468F.sig",
	}
	for index, target := range []string{urls.Download, urls.Sums, urls.Signature} {
		u, err := url.Parse(target)
		if err != nil || u.Path != prefix+"remote/providers/"+files[index] {
			t.Fatalf("signed URL %q does not name the cache path %s", target, files[index])
		}
	}
	f.cached(files...)
	f.uncached("hashicorp/null/1.0.0/terraform-provider-null_1.0.0_linux_amd64.zip")
}

// RTFACT-30802's include patterns name HashiCorp providers' files at the
// cache's root and other publishers' under external/github.com/; JFrog's
// fix for the refused metadata is to add .terraform/**. A second upstream
// stands for github.com.
func TestAccRemoteProviderRTFACT30802IncludesResolveProviders(t *testing.T) {
	f := newProviderFixture(t)
	github := fakeupstream.New(t, fakeupstream.Options{})
	f.allowPrivate(github.URL)
	host, _ := url.Parse(github.URL)
	f.configure(func(c *repo.Config) {
		c.ExternalDependenciesEnabled = new(true)
		c.ExternalDependenciesPatterns = []string{github.URL + "/**"}
	})
	timeArchive := f.release(f.up, "hashicorp/time", "0.9.1", "/terraform-provider-time/0.9.1")
	confluentArchive := f.release(github, "confluentinc/confluent", "2.0.0",
		"/confluentinc/terraform-provider-confluent/releases/download/v2.0.0")
	includes := strings.Join([]string{
		"terraform-provider-time/**/*",
		"terraform-provider-tls/**/*",
		"terraform-provider-vault/**/*",
		"external/" + host.Host + "/confluentinc/terraform-provider-confluent/**/*",
		"external/" + host.Host + "/elastic/terraform-provider-ec/**/*",
		"external/" + host.Host + "/jfrog/terraform-provider-artifactory/**/*",
		"external/" + host.Host + "/Mongey/terraform-provider-kafka/**/*",
	}, ",")
	set := func(patterns string) {
		patchRepo(
			t,
			f.fixture,
			"remote",
			func(c *repo.Config) { c.IncludesPattern = new(patterns) },
		)
	}

	set(includes)
	wantErrors(t, f.get("admin", prefix+"remote/v1/providers/hashicorp/time/versions"), 404,
		rejected("remote", ".terraform/providers/hashicorp/time/versions"))

	set(includes + ",.terraform/**")
	f.resolve("admin", "hashicorp/time", "0.9.1", timeArchive)
	f.resolve("admin", "confluentinc/confluent", "2.0.0", confluentArchive)
	f.cached(
		"terraform-provider-time/0.9.1/terraform-provider-time_0.9.1_linux_amd64.zip",
		"terraform-provider-time/0.9.1/terraform-provider-time_0.9.1_SHA256SUMS",
		"external/"+host.Host+"/confluentinc/terraform-provider-confluent/releases/download/v2.0.0/"+
			"terraform-provider-confluent_2.0.0_linux_amd64.zip",
		"external/"+host.Host+"/confluentinc/terraform-provider-confluent/releases/download/v2.0.0/"+
			"terraform-provider-confluent_2.0.0_SHA256SUMS.72D7468F.sig",
	)
}

// An exclude pattern on the files' 7.x folder refuses the package metadata
// with the pattern's 404, naming the first file read, which is the
// SHA256SUMS; the version stays listed (R3).
func TestAccRemoteProviderExcludedFilesRefuseThePackage(t *testing.T) {
	f := newProviderFixture(t)
	f.release(f.up, "hashicorp/null", "1.0.0", "/terraform-provider-null/1.0.0")
	f.grant("terraform-provider-null/**")
	patchRepo(t, f.fixture, "remote", func(c *repo.Config) {
		c.ExcludesPattern = new("**/terraform-provider-null/**")
	})
	wantListed(t, f.get("reader", remoteProviderBase+"versions"), "1.0.0")
	wantErrors(
		t,
		f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64"),
		404,
		rejected(
			"remote",
			"terraform-provider-null/1.0.0/terraform-provider-null_1.0.0_SHA256SUMS",
		),
	)
	f.uncached("terraform-provider-null/1.0.0/terraform-provider-null_1.0.0_SHA256SUMS")

	// A pattern on the registry identity's folder matches no 7.x path.
	patchRepo(t, f.fixture, "remote", func(c *repo.Config) {
		c.ExcludesPattern = new("hashicorp/null/**")
	})
	expect(t, f.get("reader", remoteProviderBase+"1.0.0/download/linux/amd64"), http.StatusOK)
}

// Read permission is checked at the files' cache paths: a caller who may
// read the provider's folder but not its files is refused as a file read
// refuses, 403, or 401 when anonymous.
func TestAccRemoteProviderUnreadableFilesRefuseThePackage(t *testing.T) {
	f := newProviderFixture(t)
	f.release(f.up, "hashicorp/null", "1.0.0", "/terraform-provider-null/1.0.0")
	wantListed(t, f.get("reader", remoteProviderBase+"versions"), "1.0.0")
	pkg := remoteProviderBase + "1.0.0/download/linux/amd64"
	wantErrors(t, f.get("reader", pkg), 403, "Forbidden")
	upstream, _ := url.Parse(f.up.URL)
	mirror := prefix + "remote/providers/" + upstream.Host + "/hashicorp/null/1.0.0.json"
	wantErrors(t, f.get("reader", mirror), 403, "Forbidden")
	f.uncached("terraform-provider-null/1.0.0/terraform-provider-null_1.0.0_SHA256SUMS")

	if err := f.db.Settings().Put(t.Context(), settings.SettingAnonymousAccess, true); err != nil {
		t.Fatal(err)
	}
	permission, _ := f.db.Permissions().Get(t.Context(), "remote-read")
	resource := permission.Resources[accessapi.ResourceArtifact]
	resource.Actions.Users[kernel.AnonymousUser] = accessapi.ActionRead
	permission.Resources[accessapi.ResourceArtifact] = resource
	if err := f.db.Permissions().Update(t.Context(), permission); err != nil {
		t.Fatal(err)
	}
	wantErrors(t, f.doPrincipal("GET", pkg, kernel.Anonymous), 401,
		"Authentication is required")
}

// An upstream network mirror's archive is cached at its URL's path below
// the Providers URL too, not under provider-mirrors/<host>/.
func TestAccRemoteProviderUpstreamMirrorCachesAtItsURLPath(t *testing.T) {
	f := newProviderFixture(t)
	f.configure(func(c *repo.Config) { c.TerraformRegistryURL = f.up.URL + "/smart" })
	f.grant("provider-mirrors/registry.terraform.io/hashicorp/null/**")
	upstream := "/smart/providers/registry.terraform.io/hashicorp/null/"
	f.json(
		upstream+"index.json",
		map[string]any{"versions": map[string]any{"1.0.0": map[string]any{}}},
	)
	f.json(upstream+"1.0.0.json", map[string]any{
		"archives": map[string]remoteMirrorArchive{
			"linux_amd64": {
				URL:    "/terraform-provider-null/1.0.0/terraform-provider-null_1.0.0_linux_amd64.zip",
				Hashes: []string{"zh:" + f.metadata["shasum"].(string)},
			},
		},
	})
	f.up.Set("/terraform-provider-null/1.0.0/terraform-provider-null_1.0.0_linux_amd64.zip",
		fakeupstream.Resource{Body: f.archive})
	f.grant("terraform-provider-null/**")
	got := f.get(
		"reader",
		prefix+"remote/providers/registry.terraform.io/hashicorp/null/1.0.0.json",
	)
	var doc struct {
		Archives map[string]remoteMirrorArchive `json:"archives"`
	}
	if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &doc) != nil {
		t.Fatalf("mirror archives %d %s", got.Code, got.Body)
	}
	path := "terraform-provider-null/1.0.0/terraform-provider-null_1.0.0_linux_amd64.zip"
	u, err := url.Parse(doc.Archives["linux_amd64"].URL)
	if err != nil || u.Path != prefix+"remote/providers/"+path {
		t.Fatalf("signed URL %q does not name the cache path", doc.Archives["linux_amd64"].URL)
	}
	expect(t, f.get("", doc.Archives["linux_amd64"].URL), http.StatusOK)
	f.cached(path)
	f.uncached("provider-mirrors/registry.terraform.io/hashicorp/null/1.0.0/" +
		"terraform-provider-null_1.0.0_linux_amd64.zip")
}

// A module's HTTP archive is cached at its URL's path below the remote's
// URL, where the remote's patterns and Read permission are checked, and
// its signed URL names that path.
func TestAccRemoteModuleArchiveCachesAtItsURLPath(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0")
	signed := f.signed("1.0.0", "reader")
	u, err := url.Parse(signed)
	if err != nil || u.Path != prefix+"remote/v1/modules/archives/1.0.0.zip" {
		t.Fatalf("signed URL %q does not name the cache path", signed)
	}
	expect(t, f.do("GET", signed, ""), http.StatusOK)
	f.cached("archives/1.0.0.zip")
	f.uncached("ns/name/aws/1.0.0.zip")
}

// A module's archive URL sets its cache path: an HTTP archive's path below
// the remote's URL, or external/<host>/<path>, and a GitHub source's VCS tag
// path. A location that names no file, or a repository name that is no
// path segment, has no cache path and is refused.
func TestUnitRemoteModuleCachePaths(t *testing.T) {
	c := repo.Config{
		URL:                  "https://storage.example/modules",
		TerraformRegistryURL: "https://registry.example",
	}
	base := "https://registry.example/v1/modules/ns/name/aws/1.0.0/download"
	for raw, want := range map[string]string{
		"https://storage.example/modules/ns/name/1.0.0.zip": "ns/name/1.0.0.zip",
		// A relative location resolves against the registry, another host.
		"/modules/a/1.0.0.tar.gz?archive=tgz": "external/registry.example/modules/a/1.0.0.tar.gz",
	} {
		if s, err := parseModuleSource(raw, base, c); err != nil || s.Path != want {
			t.Errorf("parseModuleSource(%q) = %q, %v, want %q", raw, s.Path, err, want)
		}
	}
	c.URL = "https://github.com"
	for _, raw := range []string{
		"https://github.com/archives/?archive=zip",
		"git::https://github.com/org/..git",
	} {
		if s, err := parseModuleSource(raw, base, c); err == nil {
			t.Errorf("parseModuleSource(%q) = %q, want an error", raw, s.Path)
		}
	}
}

func TestUnitRemoteFilePath(t *testing.T) {
	for _, tc := range []struct {
		base, target, want string
	}{
		{
			"https://releases.hashicorp.com", "https://releases.hashicorp.com/terraform-provider-null/3.2.1/a.zip",
			"terraform-provider-null/3.2.1/a.zip",
		},
		{
			"https://releases.hashicorp.com/", "https://Releases.HashiCorp.com/terraform-provider-null/a.zip",
			"terraform-provider-null/a.zip",
		},
		{
			"https://art.example/artifactory/api/terraform/r/providers",
			"https://art.example/artifactory/api/terraform/r/providers/terraform-provider-null/a.zip?sig=x",
			"terraform-provider-null/a.zip",
		},
		{
			"https://releases.hashicorp.com", "https://github.com/o/terraform-provider-x/releases/download/v1/a.zip",
			"external/github.com/o/terraform-provider-x/releases/download/v1/a.zip",
		},
		{
			"https://releases.hashicorp.com", "http://releases.hashicorp.com/a.zip",
			"external/releases.hashicorp.com/a.zip",
		},
		{
			"https://art.example/providers", "https://art.example/providersx/a.zip",
			"external/art.example/providersx/a.zip",
		},
		{"", "https://GitHub.com:8443/o/a.zip", "external/github.com:8443/o/a.zip"},
	} {
		if got, ok := remoteFilePath(tc.base, tc.target); !ok || got != tc.want {
			t.Errorf(
				"remoteFilePath(%q, %q) = %q, %t, want %q",
				tc.base,
				tc.target,
				got,
				ok,
				tc.want,
			)
		}
	}
	for _, target := range []string{
		"https://releases.hashicorp.com",
		"https://releases.hashicorp.com/",
		"https://releases.hashicorp.com/dir/",
		"https://releases.hashicorp.com//a.zip",
		"https://other.example/a//b.zip",
		"%zz",
	} {
		if got, ok := remoteFilePath("https://releases.hashicorp.com", target); ok {
			t.Errorf("remoteFilePath(%q) = %q, want no file", target, got)
		}
	}
}
