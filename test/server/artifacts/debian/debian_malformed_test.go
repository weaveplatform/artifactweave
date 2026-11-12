package debian_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccDebianStoresAMalformedPackageUnindexed (#1355): a .deb that is
// not a readable package deploys with 201 to a local or a federated
// repository, where it answered 400 "Invalid Debian package: ..." and
// stored nothing, and carries the reason in deb.index.status beside the
// coordinates it was deployed with. JFrog's Debian guide ("Use Debian
// Metadata Validation") has 7.x leave such a package out of the index,
// set deb.index.status to the reason and find it with a property search or
// AQL; RTFACT-16378 shows an unreadable control archive stored unindexed.
// The reason's wording is artifactweave's own [I]. The rebuilt Packages
// index lists only the readable package and Release lists that index's
// digest, an AQL query on the property finds every malformed package, and
// a readable package that replaces one is indexed and loses the property.
func TestAccDebianStoresAMalformedPackageUnindexed(t *testing.T) {
	const coords = ";deb.distribution=stable;deb.component=main;deb.architecture=amd64"
	const good = "pool/main/a/alpha/alpha_1.0_amd64.deb"
	deb := repopath.Read(t, "internal/artifacts/ecosystem/debian/testdata/alpha_1.0_amd64_0.deb")
	malformed := []struct {
		path   string
		body   []byte
		reason string
	}{
		{"pool/main/g/garbage/garbage_1.0_amd64.deb", []byte("not a debian package"), "ar magic"},
		{
			"pool/main/a/ar-only/ar-only_1.0_amd64.deb",
			[]byte("!<arch>\n"),
			"required ar members or order",
		},
		{
			"pool/main/n/no-fields/no-fields_1.0_amd64.deb",
			debOf(
				t,
				xzOf(
					t,
					tarOf(t, "./control", "Package: beta\nVersion: 1.0\nArchitecture: amd64\n"),
				),
				xzOf(t, tarOf(t, "./usr/bin/beta", "beta")),
			),
			"mandatory control fields",
		},
	}
	w := harness.NewRemoteWorld(t)
	keys := []string{"deb-local", "deb-fed"}
	for _, c := range []repo.Config{
		{Key: "deb-local", Class: repo.Local},
		{Key: "deb-fed", Class: repo.Federated},
	} {
		c.PackageType = repo.Debian
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	items := &item.Service{Store: w.Runtime.Store, Blobs: w.Runtime.Blobs, Formats: ecosystem.For}
	rebuild := func(key string) {
		t.Helper()
		if err := (debian.Handler{}).Work(t.Context(), items.Env(),
			work.WorkItem{Kind: debian.KindIndex, Key: key}); err != nil {
			t.Fatal(err)
		}
	}
	properties := func(key, path string) string {
		t.Helper()
		r := w.Request(
			http.MethodGet,
			"/artifactory/api/storage/"+key+"/"+path+"?properties",
			"admin",
			"",
		)
		if r.Code != http.StatusOK {
			t.Fatalf("properties of %s/%s answered %d %s", key, path, r.Code, r.Body)
		}
		return strings.TrimSpace(r.Body.String())
	}
	propertiesWith := func(key, path, extra string) string {
		return `{"properties":{"deb.architecture":["amd64"],"deb.component":["main"],` +
			`"deb.distribution":["stable"],` + extra + `},"uri":"http://example.com/artifactory/` +
			`api/storage/` + key + `/` + path + `"}`
	}
	// packages is the rebuilt Packages index of key, after checking that
	// Release lists its digest.
	packages := func(key string) string {
		t.Helper()
		rebuild(key)
		r := w.Request(
			http.MethodGet,
			"/artifactory/"+key+"/dists/stable/main/binary-amd64/Packages",
			"admin",
			"",
		)
		if r.Code != http.StatusOK {
			t.Fatalf("%s Packages answered %d %s", key, r.Code, r.Body)
		}
		index := r.Body.String()
		r = w.Request(http.MethodGet, "/artifactory/"+key+"/dists/stable/Release", "admin", "")
		digest := fmt.Sprintf(
			" %x %16d main/binary-amd64/Packages\n",
			sha256.Sum256([]byte(index)),
			len(index),
		)
		if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), digest) {
			t.Errorf("%s Release answered %d %q, want the digest%s", key, r.Code, r.Body, digest)
		}
		return index
	}
	for _, key := range keys {
		harness.WantStatus(t,
			w.Request(http.MethodPut, "/artifactory/"+key+"/"+good+coords, "admin", string(deb)),
			http.StatusCreated)
		for _, m := range malformed {
			r := w.Request(
				http.MethodPut,
				"/artifactory/"+key+"/"+m.path+coords,
				"admin",
				string(m.body),
			)
			if r.Code != http.StatusCreated ||
				!strings.Contains(r.Body.String(), `"path":"/`+m.path+`"`) {
				t.Errorf("%s deploy of %s answered %d %s, want 201", key, m.path, r.Code, r.Body)
			}
			want := propertiesWith(key, m.path,
				`"deb.index.status":["debian: invalid binary package: `+m.reason+`"]`)
			if got := properties(key, m.path); got != want {
				t.Errorf("%s properties of %s are %s, want %s", key, m.path, got, want)
			}
		}
		index := packages(key)
		if strings.Count(index, "Package: ") != 1 ||
			!strings.Contains(index, "Filename: "+good+"\n") {
			t.Errorf("%s Packages is %q, want the readable package alone", key, index)
		}
	}
	r := w.Request(http.MethodPost, "/artifactory/api/search/aql", "admin",
		`items.find({"@deb.index.status":{"$match":"*"}}).include("repo","path","name")`,
		"Content-Type", "text/plain")
	var found struct {
		Results []struct{ Repo, Path, Name string } `json:"results"`
	}
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &found) != nil {
		t.Fatalf("AQL answered %d %s", r.Code, r.Body)
	}
	var got, want []string
	for _, f := range found.Results {
		got = append(got, f.Repo+"/"+f.Path+"/"+f.Name)
	}
	for _, key := range keys {
		for _, m := range malformed {
			want = append(want, key+"/"+m.path)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("AQL on deb.index.status found %q, want %q", got, want)
	}
	for _, key := range keys {
		replaced := malformed[0].path
		harness.WantStatus(
			t,
			w.Request(
				http.MethodPut,
				"/artifactory/"+key+"/"+replaced+coords,
				"admin",
				string(deb),
			),
			http.StatusCreated,
		)
		want := propertiesWith(key, replaced, `"deb.name":["alpha"],"deb.version":["1.0"]`)
		if got := properties(key, replaced); got != want {
			t.Errorf("%s properties of the replacement are %s, want %s", key, got, want)
		}
		index := packages(key)
		if strings.Count(index, "Package: ") != 2 ||
			!strings.Contains(index, "Filename: "+replaced+"\n") {
			t.Errorf("%s Packages is %q, want the replacement indexed", key, index)
		}
	}
}
