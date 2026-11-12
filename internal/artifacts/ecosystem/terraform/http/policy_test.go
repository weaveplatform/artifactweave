package terraformhttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// patchRepo changes the settings of the repository key in place.
func patchRepo(t *testing.T, f *fixture, key string, change func(*repo.Config)) {
	t.Helper()
	c, err := f.db.Repos().Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	change(&c)
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

func blackedOut(key, path string) string {
	return fmt.Sprintf("The repository '%s' is blacked out and cannot serve artifact '%s:%s'.",
		key, key, path)
}

func rejected(key, path string) string {
	return fmt.Sprintf("The repository '%s' rejected the artifact '%s:%s' due to its "+
		"include/exclude pattern settings.", key, key, path)
}

// wantErrors checks the registry's error envelope: status, and message as
// its only error.
func wantErrors(t *testing.T, w *httptest.ResponseRecorder, status int, message string) {
	t.Helper()
	var body struct {
		Errors []string `json:"errors"`
	}
	if w.Code != status || json.Unmarshal(w.Body.Bytes(), &body) != nil ||
		len(body.Errors) != 1 || body.Errors[0] != message {
		t.Fatalf("got %d %s, want %d %q", w.Code, w.Body, status, message)
	}
}

func wantListed(t *testing.T, w *httptest.ResponseRecorder, versions ...string) {
	t.Helper()
	expect(t, w, http.StatusOK)
	for _, v := range versions {
		if !strings.Contains(w.Body.String(), `"`+v+`"`) {
			t.Fatalf("%s is not listed: %s", v, w.Body)
		}
	}
}

// A local module repository follows the download policy: a blackout
// refuses the version list and every archive (R9, R2a), patterns
// refuse only the archive they reject, which stays listed (R3), and the
// 204 answers as the GET of its URL would. Read permission is checked
// first, for the signed URL's principal too (DV-057).
func TestAccLocalModuleRoutesFollowTheDownloadPolicy(t *testing.T) {
	f := newFixture(t)
	base := prefix + "v1/modules/mods__ns/name/aws/"
	const archive = "ns/name/aws/1.0.0.zip"
	location := f.do("GET", base+"1.0.0/download", "reader")
	expect(t, location, http.StatusNoContent)
	signed := location.Header().Get("X-Terraform-Get")

	patchRepo(t, f, "mods", func(c *repo.Config) { c.BlackedOut = new(true) })
	wantErrors(t, f.do("GET", base+"versions", "admin"), 404,
		blackedOut("mods", "v1/modules/ns/name/aws/versions"))
	wantErrors(t, f.do("GET", prefix+"mods/v1/modules/ns/name/aws/versions", "reader"), 404,
		blackedOut("mods", "v1/modules/ns/name/aws/versions"))
	download := f.do("GET", base+"1.0.0/download", "admin")
	wantErrors(t, download, 404, blackedOut("mods", archive))
	if download.Header().Get("X-Terraform-Get") != "" {
		t.Fatal("a refused archive was given a download URL")
	}
	wantErrors(t, f.do("GET", signed, ""), 404, blackedOut("mods", archive))
	expect(t, f.do("HEAD", signed, ""), 404)
	expect(t, f.do("GET", base+"versions", "nobody"), 403)
	expect(t, f.do("GET", base+"2.0.0/download", "reader"), 403)

	patchRepo(t, f, "mods", func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = new(false), new(archive)
	})
	wantListed(t, f.do("GET", base+"versions", "admin"), "1.0.0", "2.0.0")
	wantErrors(t, f.do("GET", base+"1.0.0/download", "admin"), 404, rejected("mods", archive))
	expect(t, f.do("GET", base+"2.0.0/download", "admin"), http.StatusNoContent)
	wantErrors(t, f.do("GET", signed, ""), 404, rejected("mods", archive))
	if err := f.db.Permissions().Delete(t.Context(), "read"); err != nil {
		t.Fatal(err)
	}
	wantErrors(t, f.do("GET", signed, ""), 403, "Invalid or expired Terraform download URL")

	patchRepo(t, f, "mods", func(c *repo.Config) { c.ExcludesPattern = new("") })
	expect(t, f.do("GET", base+"1.0.0/download", "admin"), http.StatusNoContent)
}

// A local provider repository's version list, package metadata and mirror
// documents are refused by a blackout at the request's path (R9,
// R2a), and each signed file by the download policy; patterns that reject
// one platform's archive leave the platform listed and its package
// metadata served (R3), and refuse only the archive's download.
func TestAccLocalProviderRoutesFollowTheDownloadPolicy(t *testing.T) {
	f, _, _ := providerFixture(t)
	const archive = releasePath + "terraform-provider-null_3.3.2_linux_amd64.zip"
	doc := downloadDoc(t, f)
	archiveURL, _ := doc["download_url"].(string)

	patchRepo(t, f, "provider", func(c *repo.Config) { c.BlackedOut = new(true) })
	for _, rest := range []string{
		"v1/providers/hashicorp/null/versions",
		"v1/providers/hashicorp/null/3.3.2/download/linux/amd64",
		"providers/registry.terraform.io/hashicorp/null/index.json",
		"providers/registry.terraform.io/hashicorp/null/3.3.2.json",
	} {
		route := prefix + "provider/" + rest
		wantErrors(t, f.do("GET", route, "admin"), 404, blackedOut("provider", rest))
	}
	wantErrors(t, f.do("GET", providerBase+"versions", "admin"), 404,
		blackedOut("provider", "v1/providers/hashicorp/null/versions"))
	expect(t, f.do("GET", providerBase+"versions", "reader"), 403)
	wantErrors(t, f.do("GET", archiveURL, ""), 404, blackedOut("provider", archive))

	patchRepo(t, f, "provider", func(c *repo.Config) {
		c.BlackedOut, c.ExcludesPattern = new(false), new("**/*_linux_amd64.zip")
	})
	versions := f.do("GET", providerBase+"versions", "admin")
	expect(t, versions, http.StatusOK)
	if !strings.Contains(versions.Body.String(), `{"arch":"amd64","os":"linux"}`) {
		t.Fatalf("the rejected platform is not listed: %s", versions.Body)
	}
	if mirror := f.do("GET", mirrorBase+"3.3.2.json", "admin"); mirror.Code != http.StatusOK ||
		!strings.Contains(mirror.Body.String(), `"linux_amd64"`) {
		t.Fatalf("the rejected platform left the mirror: %d %s", mirror.Code, mirror.Body)
	}
	doc = downloadDoc(t, f)
	archiveURL, _ = doc["download_url"].(string)
	sumsURL, _ := doc["shasums_url"].(string)
	wantErrors(t, f.do("GET", archiveURL, ""), 404, rejected("provider", archive))
	expect(t, f.do("GET", sumsURL, ""), http.StatusOK)
}

// A remote module repository checks its patterns at the metadata's 7.x
// cache path under .terraform/ (RTFACT-30802, RTFACT-31026), where the
// blackout's refusal names it too; the lookups on the way to a document or
// an archive are refused by the blackout alone (R4); and a version
// whose archive the patterns reject stays listed (R3), where it answered
// the whole list with an error.
func TestAccRemoteModuleRoutesFollowTheRemotePolicy(t *testing.T) {
	f := newRemoteFixture(t)
	f.modules("1.0.0", "2.0.0")
	// The fixture's archive URL is <upstream>/archives/1.0.0.zip, cached
	// at its path below the remote's URL.
	const archive = "archives/1.0.0.zip"
	signed := f.signed("1.0.0", "reader")
	set := func(change func(*repo.Config)) { patchRepo(t, f.fixture, "remote", change) }

	set(func(c *repo.Config) { c.ExcludesPattern = new(archive) })
	wantListed(t, f.do("GET", remoteModuleBase+"versions", "admin"), "1.0.0", "2.0.0")
	wantErrors(t, f.do("GET", remoteModuleBase+"1.0.0/download", "admin"), 404,
		rejected("remote", archive))
	expect(t, f.do("GET", remoteModuleBase+"2.0.0/download", "admin"), http.StatusNoContent)
	wantErrors(t, f.do("GET", signed, ""), 404, rejected("remote", archive))

	// RTFACT-30802: include patterns that leave out .terraform/** refuse
	// the metadata, and JFrog's fix is to add it.
	set(func(c *repo.Config) { c.ExcludesPattern, c.IncludesPattern = new(""), new("**/*.zip") })
	wantErrors(t, f.do("GET", remoteModuleBase+"versions", "admin"), 404,
		rejected("remote", ".terraform/modules/ns/name/aws/versions"))
	wantErrors(t, f.do("GET", remoteModuleBase+"1.0.0/download", "admin"), 404,
		rejected("remote", ".terraform/modules/ns/name/aws/1.0.0/download"))
	expect(t, f.do("GET", signed, ""), http.StatusOK)
	set(func(c *repo.Config) { c.IncludesPattern = new(".terraform/**,**/*.zip") })
	wantListed(t, f.do("GET", remoteModuleBase+"versions", "admin"), "1.0.0", "2.0.0")
	expect(t, f.do("GET", remoteModuleBase+"1.0.0/download", "admin"), http.StatusNoContent)

	set(func(c *repo.Config) { c.IncludesPattern, c.BlackedOut = new("**/*"), new(true) })
	wantErrors(t, f.do("GET", remoteModuleBase+"versions", "admin"), 404,
		blackedOut("remote", ".terraform/modules/ns/name/aws/versions"))
	wantErrors(t, f.do("GET", remoteModuleBase+"1.0.0/download", "reader"), 404,
		blackedOut("remote", ".terraform/modules/ns/name/aws/1.0.0/download"))
	wantErrors(t, f.do("GET", signed, ""), 404, blackedOut("remote", "ns/name/aws"))
	other := prefix + "v1/modules/remote__ns/other/aws/versions"
	expect(t, f.do("GET", other, "reader"), 403)

	set(func(c *repo.Config) { c.BlackedOut = new(false) })
	expect(t, f.do("GET", signed, ""), http.StatusOK)
}

// A remote provider repository checks its patterns at the registry
// documents' 7.x cache paths under .terraform/providers/ (RTFACT-31026,
// RTFACT-30802), where the blackout's refusal names them too, and each
// file at its cache path, its URL's path below the Providers URL. A
// platform whose archive the patterns reject stays listed (R3), and only
// what is built from the archive is refused.
func TestAccRemoteProviderRoutesFollowTheRemotePolicy(t *testing.T) {
	const (
		archive = "terraform-provider-null/1.0.0/terraform-provider-null_1.0.0_linux_amd64.zip"
		pkg     = remoteProviderBase + "1.0.0/download/linux/amd64"
	)
	f := newProviderFixture(t)
	f.release(f.up, "hashicorp/null", "1.0.0", "/terraform-provider-null/1.0.0")
	f.grant("terraform-provider-null/**")
	upstream, _ := url.Parse(f.up.URL)
	mirror := prefix + "remote/providers/" + upstream.Host + "/hashicorp/null/"
	set := func(change func(*repo.Config)) { patchRepo(t, f.fixture, "remote", change) }

	// RTFACT-30802: include patterns that leave out .terraform/** refuse
	// the metadata, and JFrog's fix is to add it.
	set(func(c *repo.Config) { c.IncludesPattern = new("terraform-provider-null/**") })
	wantErrors(t, f.get("reader", remoteProviderBase+"versions"), 404,
		rejected("remote", ".terraform/providers/hashicorp/null/versions"))
	wantErrors(t, f.get("reader", mirror+"index.json"), 404,
		rejected("remote", ".terraform/providers/hashicorp/null/versions"))
	wantErrors(t, f.get("reader", pkg), 404,
		rejected("remote", ".terraform/providers/hashicorp/null/1.0.0/download/linux/amd64"))

	const admitted = "terraform-provider-null/**,.terraform/**"
	set(func(c *repo.Config) { c.IncludesPattern = new(admitted) })
	wantListed(t, f.get("reader", remoteProviderBase+"versions"), "1.0.0")
	wantListed(t, f.get("reader", mirror+"index.json"), "1.0.0")
	response := f.get("reader", pkg)
	expect(t, response, http.StatusOK)
	var doc struct {
		Download string `json:"download_url"`
		Sums     string `json:"shasums_url"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	expect(t, f.get("", doc.Download), http.StatusOK)

	set(func(c *repo.Config) {
		c.IncludesPattern, c.ExcludesPattern = new("**/*"), new("**/*_linux_amd64.zip")
	})
	wantListed(t, f.get("reader", remoteProviderBase+"versions"), "1.0.0")
	wantListed(t, f.get("reader", mirror+"index.json"), "1.0.0")
	wantErrors(t, f.get("reader", pkg), 404, rejected("remote", archive))
	wantErrors(t, f.get("reader", mirror+"1.0.0.json"), 404,
		"Terraform module or operation not found")
	wantErrors(t, f.get("", doc.Sums), 404, rejected("remote", archive))

	set(func(c *repo.Config) { c.ExcludesPattern, c.BlackedOut = new(""), new(true) })
	wantErrors(t, f.get("reader", remoteProviderBase+"versions"), 404,
		blackedOut("remote", ".terraform/providers/hashicorp/null/versions"))
	wantErrors(t, f.get("reader", mirror+"1.0.0.json"), 404,
		blackedOut("remote", ".terraform/providers/hashicorp/null/versions"))
	wantErrors(t, f.get("reader", pkg), 404,
		blackedOut("remote", ".terraform/providers/hashicorp/null/1.0.0/download/linux/amd64"))
	wantErrors(t, f.get("", doc.Download), 404, blackedOut("remote", "hashicorp/null"))
	expect(t, f.get("reader", prefix+"remote/v1/providers/other/null/versions"), 403)

	set(func(c *repo.Config) { c.BlackedOut = new(false) })
	expect(t, f.get("reader", pkg), http.StatusOK)
}
