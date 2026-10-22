package nuget_test

import (
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	remoteV2     = "/artifactory/api/nuget/nuget-remote/"
	remoteAtomNS = "http://www.w3.org/2005/Atom"
)

func newLegacyFixture(t *testing.T) *remoteFixture {
	t.Helper()
	f := newRemoteFixture(t)
	c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
	c.FeedContextPath = new("custom/feed")
	c.DownloadContextPath = new("custom/packages")
	c.V3FeedURL = ""
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.setXML(
		"/custom/feed",
		`<service xmlns="http://www.w3.org/2007/app"><workspace><collection href="Packages"/></workspace></service>`,
	)
	f.setXML(
		"/custom/feed/$metadata",
		`<edmx:Edmx xmlns:edmx="http://schemas.microsoft.com/ado/2007/06/edmx" Version="1.0"/>`,
	)
	return f
}

func (f *remoteFixture) setXML(path, body string) {
	f.up.Set(
		path,
		fakeupstream.Resource{
			Body:   []byte(body),
			Header: http.Header{"Content-Type": {"application/atom+xml"}},
		},
	)
}

func xmlEsc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func legacyEntryXML(id, ver, target string, body []byte) string {
	sum := sha512.Sum512(body)
	return `<entry xmlns="` + remoteAtomNS + `" xmlns:m="http://schemas.microsoft.com/ado/2007/08/dataservices/metadata" xmlns:d="http://schemas.microsoft.com/ado/2007/08/dataservices"><title>` + xmlEsc(
		id,
	) + `</title><content type="application/zip" src="` + xmlEsc(
		target,
	) + `"/><m:properties><d:Id>` + xmlEsc(
		id,
	) + `</d:Id><d:Version>` + ver + `</d:Version><d:Description>Remote package</d:Description><d:Published>2026-10-22T00:00:00Z</d:Published><d:PackageHash>` + base64.StdEncoding.EncodeToString(
		sum[:],
	) + `</d:PackageHash><d:PackageHashAlgorithm>SHA512</d:PackageHashAlgorithm><d:DownloadCount>9999</d:DownloadCount></m:properties></entry>`
}

func legacyFeedXML(entries, next string) string {
	if next != "" {
		next = `<link rel="next" href="` + xmlEsc(next) + `"/>`
	}
	return `<feed xmlns="` + remoteAtomNS + `">` + entries + next + `</feed>`
}

func (f *remoteFixture) legacyPackage(id, ver string) string {
	body := nupkg(id, ver, "")
	p := "/custom/packages/" + id + "/" + ver
	f.up.Set(p, fakeupstream.Resource{Redirect: "/binary/" + id + "-" + ver + ".zip"})
	f.up.Set(
		"/binary/"+id+"-"+ver+".zip",
		fakeupstream.Resource{
			Body:   body,
			Header: http.Header{"Content-Type": {"application/octet-stream"}},
		},
	)
	return legacyEntryXML(id, ver, f.up.URL+p, body)
}

func TestRemoteV2PagingQueriesDownloadAndOffline(t *testing.T) {
	f := newLegacyFixture(t)
	one, two := f.legacyPackage("Acme.Mixed", "1.0.0"), f.legacyPackage("Acme.Mixed", "2.0.0")
	f.setXML("/custom/feed/FindPackagesById()", legacyFeedXML(one, "pages/second.xml"))
	f.setXML("/custom/feed/pages/second.xml", legacyFeedXML(two, ""))
	f.setXML("/custom/feed/Packages", legacyFeedXML(one+two, ""))
	f.setXML("/custom/feed/Search()", legacyFeedXML(one+two, ""))
	for _, p := range []string{"", "$metadata"} {
		wantStatus(t, f.do("GET", remoteV2+p, "reader"), 200)
	}
	request := remoteV2 + "FindPackagesById()?id=%27ACME.MIXED%27&semVerLevel=2.0.0"
	rec := f.do("GET", request, "partial")
	wantStatus(t, rec, 200)
	if strings.Contains(rec.Body.String(), "2.0.0") ||
		strings.Contains(rec.Body.String(), "9999") ||
		strings.Contains(rec.Body.String(), f.up.URL) {
		t.Fatalf("leaked metadata: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), base+remoteV2+"Download/Acme.Mixed/1.0.0") {
		t.Fatal("download not rewritten", rec.Body.String())
	}
	count := f.do("GET", remoteV2+"Packages/$count", "partial")
	wantStatus(t, count, 200)
	if count.Body.String() != "1" {
		t.Fatal(count.Body.String())
	}
	query := url.Values{"$filter": {"IsLatestVersion"}, "searchTerm": {"'Mixed'"}}
	rec = f.do("GET", remoteV2+"Search()?"+query.Encode(), "partial")
	wantStatus(t, rec, 200)
	if !strings.Contains(rec.Body.String(), "1.0.0") ||
		strings.Contains(rec.Body.String(), "2.0.0") {
		t.Fatal(rec.Body.String())
	}
	rec = f.do("GET", remoteV2+"Packages(Id='ACME.MIXED',Version='01.0.0.0')", "partial")
	wantStatus(t, rec, 200)
	for _, user := range []string{"reader", "partial"} {
		rec = f.do("GET", remoteV2+"Download/aCmE.MiXeD/01.0.0.0", user)
		wantStatus(t, rec, 200)
		if !bytes.Equal(rec.Body.Bytes(), nupkg("Acme.Mixed", "1.0.0", "")) {
			t.Fatal("package changed")
		}
	}
	if f.up.Count("GET", "/custom/packages/Acme.Mixed/1.0.0") != 1 {
		t.Fatal("configured download context unused", f.up.Requests())
	}
	wantStatus(t, f.do("GET", remoteV2+"Download/Acme.Mixed/2.0.0", "partial"), 403)
	wantStatus(t, f.do("POST", remoteV2, "reader"), 405)
	f.up.Close()
	f.clock.Advance(6 * time.Second)
	wantStatus(t, f.do("GET", request, "reader"), 200)
	wantStatus(t, f.do("GET", remoteV2+"Download/Acme.Mixed/1.0.0", "reader"), 200)
}

func TestRemoteV2LocalPaginationAfterPermission(t *testing.T) {
	f := newLegacyFixture(t)
	var entries strings.Builder
	for i := range 103 {
		v := fmt.Sprintf("1.0.%d", i)
		entries.WriteString(f.legacyPackage("Acme.Mixed", v))
	}
	f.setXML("/custom/feed/Packages", legacyFeedXML(entries.String(), ""))
	rec := f.do("GET", remoteV2+"Packages?$top=102", "partial")
	wantStatus(t, rec, 200)
	var feed struct {
		Entries []struct{} `xml:"entry"`
		Links   []struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if len(feed.Entries) != 100 || len(feed.Links) != 1 {
		t.Fatal("first page", len(feed.Entries), feed.Links)
	}
	next := relative(t, feed.Links[0].Href)
	if !strings.Contains(next, "%24top=2") {
		t.Fatal(next)
	}
	rec = f.do("GET", next, "partial")
	wantStatus(t, rec, 200)
	feed.Entries = nil
	feed.Links = nil
	if err := xml.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if len(feed.Entries) != 2 || len(feed.Links) != 0 {
		t.Fatal("continuation allowance", len(feed.Entries), feed.Links)
	}
}

func TestRemoteNuGetSnapshotCacheRetention(t *testing.T) {
	f := newRemoteFixture(t)
	c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
	c.MaxUniqueSnapshots = new(2)
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	f.packageDocs("Acme.Mixed", "1.0.0-a", "1.0.0-b", "1.0.0-c", "1.0.0")
	for _, version := range []string{"1.0.0-a", "1.0.0-b", "1.0.0-c", "1.0.0"} {
		wantStatus(
			t,
			f.do(
				"GET",
				remoteV3+"flatcontainer/acme.mixed/"+version+"/acme.mixed."+version+".nupkg",
				"reader",
			),
			200,
		)
		f.clock.Advance(time.Second)
	}
	if err := (nugetfmt.Handler{}).Work(
		t.Context(),
		f.items.Env(),
		types.WorkItem{Kind: nugetfmt.KindSnapshots, Key: "nuget-remote/acme.mixed"},
	); err != nil {
		t.Fatal(err)
	}
	old := "acme.mixed/1.0.0-a/acme.mixed.1.0.0-a.nupkg"
	if _, err := f.db.Items().
		Get(t.Context(), c.Key, true, old); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("old cache item retained", err)
	}
	// Upstream version metadata stays authoritative and removed content can be fetched anew.
	rec := f.do("GET", remoteV3+"flatcontainer/acme.mixed/index.json", "reader")
	wantStatus(t, rec, 200)
	if !strings.Contains(rec.Body.String(), "1.0.0-a") {
		t.Fatal("retention rewrote upstream metadata")
	}
	wantStatus(t, f.do("GET", remoteV3+"flatcontainer/"+old, "reader"), 200)
}

func TestRemoteV2EmptyContextsAndUnsafeContext(t *testing.T) {
	f := newLegacyFixture(t)
	c, _ := f.db.Repos().Get(t.Context(), "nuget-remote")
	c.FeedContextPath = new("")
	c.DownloadContextPath = new("")
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	body := nupkg("Acme.Mixed", "1.0.0", "")
	f.setXML(
		"/FindPackagesById()",
		legacyFeedXML(
			legacyEntryXML("Acme.Mixed", "1.0.0", f.up.URL+"/Acme.Mixed/1.0.0", body),
			"",
		),
	)
	f.up.Set("/Acme.Mixed/1.0.0", fakeupstream.Resource{Body: body})
	wantStatus(t, f.do("GET", remoteV2+"Download/Acme.Mixed/1.0.0", "reader"), 200)
	if f.up.Count("GET", "/Acme.Mixed/1.0.0") != 1 {
		t.Fatal("empty download context not used")
	}
	for _, path := range []string{"../feed", "feed/../escape", "feed?token=x", "feed\\escape", "https://host"} {
		c.FeedContextPath = &path
		if err := f.db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, f.do("GET", remoteV2+"FindPackagesById()?id=%27Acme.Mixed%27", "reader"), 502)
	}
}
