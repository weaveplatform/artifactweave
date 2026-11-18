package nuget_test

import (
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

type v2VirtualFeed struct {
	Entries []struct {
		ID      string `xml:"id"`
		Content struct {
			Src string `xml:"src,attr"`
		} `xml:"content"`
		Properties struct {
			Items []struct {
				XMLName xml.Name
				Value   string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"properties"`
	} `xml:"entry"`
	Links []struct {
		Href string `xml:"href,attr"`
	} `xml:"link"`
}

func readVirtualV2Feed(t *testing.T, w *harness.RemoteWorld, path, user string) v2VirtualFeed {
	t.Helper()
	r := w.Request("GET", path, user, "")
	harness.WantStatus(t, r, 200)
	var feed v2VirtualFeed
	if err := xml.Unmarshal(r.Body.Bytes(), &feed); err != nil {
		t.Fatal(err, r.Body)
	}
	return feed
}

func propertyV2(t *testing.T, feed v2VirtualFeed, index int, name string) string {
	t.Helper()
	for _, p := range feed.Entries[index].Properties.Items {
		if p.XMLName.Local == name {
			return p.Value
		}
	}
	t.Fatalf("missing %s", name)
	return ""
}

func putV2Remote(t *testing.T, w *harness.RemoteWorld, versions []string) {
	t.Helper()
	var entries strings.Builder
	for _, v := range versions {
		body := nugetPackage("Acme.Mixed", v, "<description>remote owner</description>")
		sum := sha512.Sum512(body)
		target := w.Upstream.URL + "/custom/packages/Acme.Mixed/" + v
		w.Upstream.Set("/custom/packages/Acme.Mixed/"+v, fakeupstream.Resource{Body: body, Header: http.Header{"Content-Type": []string{"application/octet-stream"}}})
		fmt.Fprintf(
			&entries,
			`<entry xmlns="http://www.w3.org/2005/Atom" xmlns:m="http://schemas.microsoft.com/ado/2007/08/dataservices/metadata" xmlns:d="http://schemas.microsoft.com/ado/2007/08/dataservices"><title>Acme.Mixed</title><content src="%s" type="application/zip"/><m:properties><d:Id>Acme.Mixed</d:Id><d:Version>%s</d:Version><d:Description>remote owner</d:Description><d:Published>2026-10-22T00:00:00Z</d:Published><d:PackageHash>%s</d:PackageHash><d:PackageHashAlgorithm>SHA512</d:PackageHashAlgorithm><d:RichField>preserved</d:RichField></m:properties></entry>`,
			target,
			v,
			base64.StdEncoding.EncodeToString(sum[:]),
		)
	}
	body := `<feed xmlns="http://www.w3.org/2005/Atom">` + entries.String() + `</feed>`
	for _, endpoint := range []string{"Packages", "Packages()", "FindPackagesById()", "Search()"} {
		w.Upstream.Set("/custom/feed/"+endpoint, fakeupstream.Resource{Body: []byte(body), Header: http.Header{"Content-Type": []string{"application/atom+xml"}}})
	}
}

func TestAccNuGetVirtualV2BackendsAndPages(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			var mutate []func(*config.Config)
			if backend == "postgres" {
				dsn := platsqltest.ScratchDSN(t, platsqltest.PostgresDSN(t))
				mutate = append(mutate, func(c *config.Config) { c.Database.Driver = "postgres"; c.Database.DSN = dsn })
			}
			w := nugetVirtualWorld(t, mutate...)
			w.Repo.V3FeedURL = ""
			w.Repo.FeedContextPath = new("custom/feed")
			w.Repo.DownloadContextPath = new("custom/packages")
			w.Update()
			remoteVersions := []string{"1.0.0", "2.0.0-beta.1"}
			for i := range 150 {
				v := fmt.Sprintf("1.0.%d", i)
				if i%2 != 0 {
					remoteVersions = append(remoteVersions, v)
					continue
				}
				r := w.Request("PUT", "/artifactory/local/Acme.Mixed."+v+".nupkg", "admin", string(nugetPackage("Acme.Mixed", v, "<description>local owner</description>")))
				harness.WantStatus(t, r, 201)
			}
			putV2Remote(t, w, remoteVersions)
			if err := w.Runtime.Store.Users().Create(t.Context(), accesstest.User{Name: "v2-reader", PasswordHash: cheapHash}); err != nil {
				t.Fatal(err)
			}
			if err := w.Runtime.Store.Permissions().
				Create(t.Context(), accesstest.Permission{Name: "v2-partial", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"v2-reader": api.ActionRead | api.ActionWrite}}, Targets: map[string]accesstest.Target{"local": {Include: []string{"Acme.Mixed.1.*"}}, "remote": {Include: []string{"acme.mixed/1.*/*"}}}}}}); err != nil {
				t.Fatal(err)
			}
			root := "/artifactory/api/nuget/virtual/"
			for _, user := range []string{"admin", "v2-reader"} {
				for _, endpoint := range []string{"Packages()", "FindPackagesById()?id=%27Acme.Mixed%27", "Search()?searchTerm=%27Mixed%27&includePrerelease=true"} {
					sep := "?"
					if strings.Contains(endpoint, "?") {
						sep = "&"
					}
					feed := readVirtualV2Feed(t, w, root+endpoint+sep+"semVerLevel=2.0.0", user)
					if len(feed.Entries) != 100 || len(feed.Links) != 1 {
						t.Fatal(endpoint, len(feed.Entries), feed.Links)
					}
					for i, e := range feed.Entries {
						if !strings.Contains(e.ID, root+"Packages(") || !strings.Contains(e.Content.Src, root+"Download/") || propertyV2(t, feed, i, "IsLatestVersion") == "true" ||
							propertyV2(t, feed, i, "IsAbsoluteLatestVersion") == "true" {
							t.Fatal(e)
						}
					}
					link, err := url.Parse(feed.Links[0].Href)
					if err != nil {
						t.Fatal(err)
					}
					last := readVirtualV2Feed(t, w, link.RequestURI(), user)
					want := 51
					if user == "v2-reader" {
						want = 50
					}
					if len(last.Entries) != want || len(last.Links) != 0 || propertyV2(t, last, 49, "IsLatestVersion") != "true" {
						t.Fatal(len(last.Entries), want)
					}
				}
				r := w.Request("GET", root+"Packages()/$count?semVerLevel=2.0.0", user, "")
				harness.WantStatus(t, r, 200)
				want := "151"
				if user == "v2-reader" {
					want = "150"
				}
				if r.Body.String() != want {
					t.Fatal(r.Body)
				}
				for _, endpoint := range []string{"Packages()?$filter=IsLatestVersion", "Packages()?$filter=IsAbsoluteLatestVersion&semVerLevel=2.0.0", "Search()?searchTerm=%27Mixed%27&$filter=IsLatestVersion"} {
					feed := readVirtualV2Feed(t, w, root+endpoint, user)
					if len(feed.Entries) != 1 {
						t.Fatal(endpoint, feed)
					}
				}
			}
			r := w.Request("GET", root+"Download/Acme.Mixed/1.0.0", "v2-reader", "")
			harness.WantStatus(t, r, 200)
			if !bytes.Equal(r.Body.Bytes(), nugetPackage("Acme.Mixed", "1.0.0", "<description>local owner</description>")) {
				t.Fatal("wrong whole owner bytes")
			}
			before := len(w.Upstream.Requests())
			harness.WantStatus(t, w.Request("GET", root+"Download/Acme.Mixed/1.0.0", "v2-reader", ""), 200)
			if len(w.Upstream.Requests()) != before {
				t.Fatal("local binary reached upstream")
			}
			for _, endpoint := range []string{"Download/Acme.Mixed/2.0.0-beta.1", "Packages(Id='Acme.Mixed',Version='2.0.0-beta.1')"} {
				harness.WantStatus(t, w.Request("GET", root+endpoint, "v2-reader", ""), 404)
				harness.WantStatus(t, w.Request("GET", root+endpoint, "admin", ""), 200)
			}
			harness.WantStatus(t, w.Request("GET", root+"Packages()?$filter=IsPrerelease", "admin", ""), 400)
			w.Repo.PriorityResolution = repo.Flex(true)
			w.Update()
			r = w.Request("GET", root+"Packages()/$count?semVerLevel=2.0.0", "admin", "")
			harness.WantStatus(t, r, 200)
			if r.Body.String() != "77" {
				t.Fatal("priority shadow", r.Body)
			}
			for _, method := range []string{"PUT", "DELETE", "POST"} {
				harness.WantStatus(t, w.Request(method, root, "admin", "malformed"), 501)
			}
			harness.WantStatus(t, w.Request("GET", root+"Download/Missing/1.0.0", "denied", ""), 404)
		})
	}
}
