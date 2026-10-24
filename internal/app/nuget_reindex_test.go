package app

import (
	"archive/zip"
	"bytes"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/format"
	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// This is API evidence against the production router/runtime and SQLite queue.
// HTTP 200 schedules the rebuild. The runtime worker may lease or finish it
// before this test observes the queue, so completion and repaired metadata are
// the stable assertions with the upstream off.
func TestAccNuGetRemoteReindexAPI(t *testing.T) {
	w := newRemoteWorld(t)
	w.c = *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.NuGet, URL: w.up.URL, V3FeedURL: w.up.URL + "/v3/index.json"})
	w.update()
	body := reindexNupkg(t)
	seedReindexFeed(w, body)
	const v3 = "/artifactory/api/nuget/v3/remote/"
	const v2 = "/artifactory/api/nuget/remote/"
	const packagePath = "reindex.demo/1.0.0/reindex.demo.1.0.0.nupkg"
	for _, route := range []string{v3 + "flatcontainer/" + packagePath, v2 + "Download/Reindex.Demo/1.0.0"} {
		got := w.request("GET", route, "reader", "")
		remoteStatus(t, got, http.StatusOK)
		if !bytes.Equal(got.Body.Bytes(), body) {
			t.Fatal("cached package bytes changed")
		}
	}
	assertReindexFeeds(t, w, "remote")
	st := w.rt.store
	it, err := st.Items().Get(t.Context(), w.c.Key, true, packagePath)
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.Packages().Find(t.Context(), w.c.Key, "reindex.demo")
	if err != nil {
		t.Fatal(err)
	}
	if err = st.Packages().Delete(t.Context(), p.ID); err != nil {
		t.Fatal(err)
	}
	// Simulate damaged derived metadata through the same store that persists it.
	// The upstream package and its raw protocol representations remain intact.
	if err = st.Items().
		SetProperties(t.Context(), it.ID, types.Properties{nugetfmt.PropID: {"incorrect"}, "custom": {"keep"}}, it.Updated); err != nil {
		t.Fatal(err)
	}
	assertReindexSearch(t, w, 0)
	rawBefore, _, err := st.RemoteCache().
		List(t.Context(), w.c.Key, "", store.Page{Limit: store.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	w.c.Offline = new(true)
	w.update()
	calls := len(w.up.Requests())
	for _, key := range []string{"remote", "remote-cache"} {
		route := "/artifactory/api/nuget/" + key + "/reindex"
		remoteStatus(t, w.request("POST", route, "reader", ""), http.StatusForbidden)
		remoteStatus(t, w.request("POST", route, "", ""), http.StatusUnauthorized)
		accepted := w.request("POST", route, "admin", "")
		remoteStatus(t, accepted, http.StatusOK)
		if accepted.Body.String() != fmt.Sprintf(
			"NuGet reindex calculation for repository '%s' accepted.",
			key,
		) {
			t.Fatal(accepted.Body.String())
		}
	}
	queued, err := st.WorkQueue().Get(t.Context(), format.KindReindex, w.c.Key)
	if err == nil {
		if queued.State != types.WorkPending && queued.State != types.WorkLeased {
			t.Fatal("unexpected worker state after HTTP response", queued)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		t.Fatal("read scheduled worker", err)
	}
	if _, err = st.WorkQueue().
		Get(t.Context(), format.KindReindex, "remote-cache"); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("cache alias created independent work", err)
	}
	if err = w.rt.work.EnsureFresh(t.Context(), format.KindReindex, w.c.Key); err != nil {
		t.Fatal(err)
	}
	if _, err = st.WorkQueue().
		Get(t.Context(), format.KindReindex, w.c.Key); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("worker still pending", err)
	}
	rawAfter, _, err := st.RemoteCache().
		List(t.Context(), w.c.Key, "", store.Page{Limit: store.MaxLimit})
	if err != nil || !reflect.DeepEqual(rawBefore, rawAfter) {
		t.Fatal("reindex changed raw upstream metadata/cache", err)
	}
	p, err = st.Packages().Find(t.Context(), w.c.Key, "reindex.demo")
	if err != nil {
		t.Fatal(err)
	}
	versions, err := st.Packages().Versions(t.Context(), p.ID)
	if err != nil || len(versions) != 1 || versions[0].Version != "1.0.0" {
		t.Fatal("uncached upstream version invented", versions, err)
	}
	metadata, err := nugetfmt.DecodeMetadata(versions[0].Metadata)
	sum := sha512.Sum512(body)
	if err != nil || metadata.ID != "Reindex.Demo" ||
		metadata.SHA512 != base64.StdEncoding.EncodeToString(sum[:]) ||
		metadata.Path != packagePath {
		t.Fatal("rebuilt metadata does not describe bytes", metadata, err)
	}
	props, err := st.Items().Properties(t.Context(), it.ID)
	if err != nil || props[nugetfmt.PropID][0] != "Reindex.Demo" || props["custom"][0] != "keep" {
		t.Fatal(props, err)
	}
	assertReindexSearch(t, w, 1)
	assertReindexFeeds(t, w, "remote")
	cached := w.request("GET", "/artifactory/remote-cache/"+packagePath, "reader", "")
	remoteStatus(t, cached, http.StatusOK)
	if !bytes.Equal(cached.Body.Bytes(), body) {
		t.Fatal("cached artifact changed after rebuild")
	}
	if len(w.up.Requests()) != calls {
		t.Fatalf(
			"offline reindex/API reads fetched upstream: before %d after %d",
			calls,
			len(w.up.Requests()),
		)
	}
	t.Logf(
		"accepted both aliases; worker completed; repaired one cached version; preserved two upstream versions and exact SHA512; upstream requests during scheduling/rebuild/offline reads: 0",
	)
}

func reindexNupkg(t *testing.T) []byte {
	t.Helper()
	var body bytes.Buffer
	z := zip.NewWriter(&body)
	f, err := z.Create("Reindex.Demo.nuspec")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(
		[]byte(
			`<package><metadata><id>Reindex.Demo</id><version>1.0.0</version><authors>Fixture</authors><description>Reindex fixture</description></metadata></package>`,
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func seedReindexFeed(w *remoteWorld, body []byte) {
	set := func(path, media string, b []byte) {
		w.up.Set(path, fakeupstream.Resource{Body: b, Header: http.Header{"Content-Type": {media}}})
	}
	set(
		"/v3/index.json",
		"application/json",
		fmt.Appendf(
			nil,
			`{"version":"3.0.0","resources":[{"@id":%q,"@type":"PackageBaseAddress/3.0.0"}]}`,
			w.up.URL+"/flat/",
		),
	)
	set(
		"/flat/reindex.demo/index.json",
		"application/json",
		[]byte(`{"versions":["1.0.0","2.0.0"]}`),
	)
	set("/flat/reindex.demo/1.0.0/reindex.demo.1.0.0.nupkg", "application/octet-stream", body)
	set("/api/v2/package/Reindex.Demo/1.0.0", "application/octet-stream", body)
	sum := sha512.Sum512(body)
	var entries strings.Builder
	for _, version := range []string{"1.0.0", "2.0.0"} {
		fmt.Fprintf(
			&entries,
			`<entry xmlns:m="http://schemas.microsoft.com/ado/2007/08/dataservices/metadata" xmlns:d="http://schemas.microsoft.com/ado/2007/08/dataservices"><title>Reindex.Demo</title><content type="application/zip" src="%s/api/v2/package/Reindex.Demo/%s"/><m:properties><d:Id>Reindex.Demo</d:Id><d:Version>%s</d:Version><d:Description>Fixture</d:Description><d:PackageHash>%s</d:PackageHash><d:PackageHashAlgorithm>SHA512</d:PackageHashAlgorithm></m:properties></entry>`,
			w.up.URL,
			version,
			version,
			base64.StdEncoding.EncodeToString(sum[:]),
		)
	}
	set(
		"/api/v2/FindPackagesById()",
		"application/atom+xml",
		[]byte(`<feed xmlns="http://www.w3.org/2005/Atom">`+entries.String()+`</feed>`),
	)
}

func assertReindexFeeds(t *testing.T, w *remoteWorld, key string) {
	t.Helper()
	rec := w.request(
		"GET",
		"/artifactory/api/nuget/v3/"+key+"/flatcontainer/reindex.demo/index.json",
		"reader",
		"",
	)
	remoteStatus(t, rec, http.StatusOK)
	var versions struct {
		Versions []string `json:"versions"`
	}
	if err := json.Unmarshal(
		rec.Body.Bytes(),
		&versions,
	); err != nil ||
		!slices.Equal(versions.Versions, []string{"1.0.0", "2.0.0"}) {
		t.Fatal("upstream versions lost", rec.Body, err)
	}
	rec = w.request(
		"GET",
		"/artifactory/api/nuget/"+key+"/FindPackagesById()?id='Reindex.Demo'",
		"reader",
		"",
	)
	remoteStatus(t, rec, http.StatusOK)
	var feed struct {
		Entries []struct {
			Properties struct {
				ID      string `xml:"Id"`
				Version string `xml:"Version"`
			} `xml:"properties"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &feed); err != nil || len(feed.Entries) != 2 {
		t.Fatal("v2 feed", rec.Body, err)
	}
	for i, version := range []string{"1.0.0", "2.0.0"} {
		if feed.Entries[i].Properties.ID != "Reindex.Demo" ||
			feed.Entries[i].Properties.Version != version {
			t.Fatal("wrong v2 identity", feed)
		}
	}
}

func assertReindexSearch(t *testing.T, w *remoteWorld, count int) {
	t.Helper()
	rec := w.request(
		"GET",
		"/artifactory/api/search/prop?nuget.id=Reindex.Demo&repos=remote-cache",
		"reader",
		"",
	)
	remoteStatus(t, rec, http.StatusOK)
	var response struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(
		rec.Body.Bytes(),
		&response,
	); err != nil ||
		len(response.Results) != count {
		t.Fatal("property search", rec.Body, err)
	}
}
