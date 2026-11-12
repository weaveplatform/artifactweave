package puppethttp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// wantPolicy fails unless r is the 404 Artifactory's envelope gives the
// download policy's refusal, with message as its only error.
func wantPolicy(t *testing.T, r *httptest.ResponseRecorder, message string) {
	t.Helper()
	var doc struct {
		Errors []struct {
			Status  int    `json:"status"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if r.Code != 404 || json.Unmarshal(r.Body.Bytes(), &doc) != nil || len(doc.Errors) != 1 ||
		doc.Errors[0].Status != 404 || doc.Errors[0].Message != message {
		t.Fatalf("want 404 %q, got %d %s", message, r.Code, r.Body.String())
	}
}

// releaseVersions returns the versions a release list answers, in order,
// with each one's advertised archive checksum.
func releaseVersions(t *testing.T, r *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var doc struct {
		Results []struct {
			Version    string `json:"version"`
			FileSHA256 string `json:"file_sha256"`
		} `json:"results"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &doc) != nil {
		t.Fatalf("releases: %d %s", r.Code, r.Body.String())
	}
	out := map[string]string{}
	for _, v := range doc.Results {
		out[v.Version] = v.FileSHA256
	}
	return out
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func (f *fixture) deployVersion(c repo.Config, version string, body []byte) {
	f.t.Helper()
	if _, err := f.items.Deploy(f.t.Context(), item.Deploy{
		Target: item.Target{Repo: c, Path: "acme-widget/acme-widget-" + version + ".tar.gz"},
		Body:   bytes.NewReader(body),
		By:     "admin",
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) save(c repo.Config) {
	f.t.Helper()
	if err := f.db.Repos().Update(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) create(c repo.Config) repo.Config {
	f.t.Helper()
	c = *repo.Defaults(&c)
	if err := f.db.Repos().Create(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
	return c
}

// emptySearch is the module search's answer when it finds nothing.
const emptySearch = `{"pagination":{"limit":1000,"offset":0,"total":0,"next":null},"results":[]}`

// searchVersions returns the release versions the module search's one
// result lists, or none when it finds nothing.
func searchVersions(t *testing.T, r *httptest.ResponseRecorder) []string {
	t.Helper()
	var doc struct {
		Results []struct {
			Releases []struct {
				Version string `json:"version"`
			} `json:"releases"`
		} `json:"results"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &doc) != nil || len(doc.Results) > 1 {
		t.Fatalf("search: %d %s", r.Code, r.Body.String())
	}
	var out []string
	for _, result := range doc.Results {
		for _, rel := range result.Releases {
			out = append(out, rel.Version)
		}
	}
	return out
}

// A blacked-out local or federated repository refuses every Puppet route
// but the module search with the blackout 404, administrators included,
// after the caller's read permission (R2a, R9, R10). The computed listings
// name the request's own path, since 7.x is not shown to store them at
// one; the archive names its own path, whether the release exists or not.
// The search, a native search route, finds nothing instead (R2b).
func TestUnitHostedBlackoutRefusesEveryRouteButTheSearch(t *testing.T) {
	const blackedOut = "The repository 'puppet-local' is blacked out and cannot serve artifact "
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			f.deploy(f.c, fixtureArchive(t))
			f.c.BlackedOut = new(true)
			f.save(f.c)
			const query = "/v3/modules?query=acme-widget"
			if r := f.do("GET", base+query, "admin"); r.Code != 200 ||
				strings.TrimSpace(r.Body.String()) != emptySearch {
				t.Fatalf("search: %d %s", r.Code, r.Body.String())
			}
			if r := f.do("HEAD", base+query, "admin"); r.Code != 200 || r.Body.Len() != 0 {
				t.Fatalf("HEAD search: %d %s", r.Code, r.Body.String())
			}
			if r := f.do("GET", base+query, "denied"); r.Code != 403 {
				t.Fatalf("denied search: %d %s", r.Code, r.Body.String())
			}
			for route, path := range map[string]string{
				"/v3/releases?module=acme-widget":    "v3/releases",
				"/v3/releases/acme-widget-1.0.0":     "v3/releases/acme-widget-1.0.0",
				"/v3/modules/acme-widget":            "v3/modules/acme-widget",
				"/v3/files/acme-widget-1.0.0.tar.gz": "acme-widget/acme-widget-1.0.0.tar.gz",
				"/v3/files/acme-widget-9.9.9.tar.gz": "acme-widget/acme-widget-9.9.9.tar.gz",
			} {
				wantPolicy(t, f.do("GET", base+route, "admin"),
					blackedOut+"'puppet-local:"+path+"'.")
				if r := f.do("HEAD", base+route, "admin"); r.Code != 404 {
					t.Fatalf("HEAD %s: %d", route, r.Code)
				}
				if r := f.do("GET", base+route, "denied"); r.Code != 403 {
					t.Fatalf("denied %s: %d %s", route, r.Code, r.Body.String())
				}
			}
			f.c.BlackedOut = new(false)
			f.save(f.c)
			if got := releaseVersions(t, f.do("GET", base+"/v3/releases?module=acme-widget",
				"admin")); len(got) != 1 {
				t.Fatalf("restored: %v", got)
			}
			if got := searchVersions(t, f.do("GET", base+query, "admin")); len(got) != 1 {
				t.Fatalf("restored search: %v", got)
			}
			if r := f.do("GET", base+"/v3/files/acme-widget-1.0.0.tar.gz", "admin"); r.Code != 200 {
				t.Fatalf("restored archive: %d", r.Code)
			}
		})
	}
}

// Patterns that reject one release's archive leave the release in every
// listing, and refuse only its archive, with the patterns' 404 checked
// before the release is looked up (R3; decision 0032).
func TestUnitPatternsRefuseOnlyTheArchive(t *testing.T) {
	f := newFixture(t, repo.Local)
	older, newer := variantArchive(t, "1.0.0", "older"), variantArchive(t, "2.0.0", "newer")
	f.deployVersion(f.c, "1.0.0", older)
	f.deployVersion(f.c, "2.0.0", newer)
	f.c.ExcludesPattern = new("**/acme-widget-2.0.0.tar.gz")
	f.save(f.c)
	got := releaseVersions(t, f.do("GET", base+"/v3/releases?module=acme-widget", "admin"))
	if len(got) != 2 || got["2.0.0"] != sum(newer) || got["1.0.0"] != sum(older) {
		t.Fatalf("listing: %v", got)
	}
	if r := f.do("GET", base+"/v3/releases/acme-widget-2.0.0", "admin"); r.Code != 200 {
		t.Fatalf("release: %d %s", r.Code, r.Body.String())
	}
	for _, route := range []string{"/v3/modules/acme-widget", "/v3/modules?query=acme-widget"} {
		r := f.do("GET", base+route, "admin")
		if r.Code != 200 || !strings.Contains(r.Body.String(),
			`"current_release":{"uri":"/v3/releases/acme-widget-2.0.0"`) {
			t.Fatalf("%s: %d %s", route, r.Code, r.Body.String())
		}
	}
	const rejected = "The repository 'puppet-local' rejected the artifact 'puppet-local:%s' " +
		"due to its include/exclude pattern settings."
	wantPolicy(t, f.do("GET", base+"/v3/files/acme-widget-2.0.0.tar.gz", "admin"),
		strings.Replace(rejected, "%s", "acme-widget/acme-widget-2.0.0.tar.gz", 1))
	if r := f.do("GET", base+"/v3/files/acme-widget-1.0.0.tar.gz", "admin"); r.Code != 200 ||
		!bytes.Equal(r.Body.Bytes(), older) {
		t.Fatalf("accepted archive: %d", r.Code)
	}
	f.c.ExcludesPattern = new("acme-widget/**")
	f.save(f.c)
	if got := releaseVersions(t, f.do("GET", base+"/v3/releases?module=acme-widget",
		"admin")); len(got) != 2 {
		t.Fatalf("module folder excluded: %v", got)
	}
	if got := searchVersions(t, f.do("GET", base+"/v3/modules?query=acme-widget",
		"admin")); len(got) != 2 {
		t.Fatalf("module folder excluded, search: %v", got)
	}
	wantPolicy(t, f.do("GET", base+"/v3/files/acme-widget-9.9.9.tar.gz", "admin"),
		strings.Replace(rejected, "%s", "acme-widget/acme-widget-9.9.9.tar.gz", 1))
}

// A virtual repository leaves a blacked-out member out, keeps a member
// that refuses only a release's archive, and serves each archive from the
// first member that holds the version and accepts it, whose checksum the
// listing gives; a version no member serves stays listed and answers
// "Puppet release not found" (R5).
func TestUnitVirtualMembersRefusingTheArchive(t *testing.T) {
	first, second := variantArchive(t, "1.0.0", "first"), variantArchive(t, "1.0.0", "second")
	for _, tc := range []struct {
		name   string
		tune   func(*repo.Config)
		second bool
		want   []byte
	}{
		{"blacked out", func(c *repo.Config) { c.BlackedOut = new(true) }, true, second},
		{"archive excluded", func(c *repo.Config) {
			c.ExcludesPattern = new("**/acme-widget-1.0.0.tar.gz")
		}, true, second},
		{"archive excluded, no other member", func(c *repo.Config) {
			c.ExcludesPattern = new("**/acme-widget-1.0.0.tar.gz")
		}, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, repo.Local)
			f.deployVersion(f.c, "1.0.0", first)
			members := []string{f.c.Key}
			if tc.second {
				other := f.create(
					repo.Config{Key: "second", Class: repo.Local, PackageType: repo.Puppet},
				)
				f.deployVersion(other, "1.0.0", second)
				members = append(members, other.Key)
			}
			tc.tune(&f.c)
			f.save(f.c)
			f.create(repo.Config{
				Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet,
				Repositories: members,
			})
			const v = "/artifactory/api/puppet/virtual"
			got := releaseVersions(t, f.do("GET", v+"/v3/releases?module=acme-widget", "admin"))
			listed := sum(first)
			if tc.want != nil {
				listed = sum(tc.want)
			}
			if len(got) != 1 || got["1.0.0"] != listed {
				t.Fatalf("listing: %v", got)
			}
			if found := searchVersions(t, f.do("GET", v+"/v3/modules?query=acme-widget",
				"admin")); len(found) != 1 || found[0] != "1.0.0" {
				t.Fatalf("search: %v", found)
			}
			r := f.do("GET", v+"/v3/files/acme-widget-1.0.0.tar.gz", "admin")
			if tc.want == nil {
				if r.Code != 404 || !strings.Contains(r.Body.String(), "Puppet release not found") {
					t.Fatalf("no member serves: %d %s", r.Code, r.Body.String())
				}
				return
			}
			if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), tc.want) {
				t.Fatalf("archive: %d", r.Code)
			}
		})
	}
}

// The virtual repository's own patterns refuse a release's archive with
// its "No repository found" 404 and leave the release listed (R3,
// R5).
func TestUnitVirtualPatternsRefuseOnlyTheArchive(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.deploy(f.c, fixtureArchive(t))
	f.create(repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet,
		Repositories: []string{f.c.Key}, ExcludesPattern: new("**/acme-widget-1.0.0.tar.gz"),
	})
	const v = "/artifactory/api/puppet/virtual"
	if got := releaseVersions(t, f.do("GET", v+"/v3/releases?module=acme-widget",
		"admin")); len(got) != 1 {
		t.Fatalf("listing: %v", got)
	}
	if r := f.do("GET", v+"/v3/releases/acme-widget-1.0.0", "admin"); r.Code != 200 {
		t.Fatalf("release: %d %s", r.Code, r.Body.String())
	}
	wantPolicy(t, f.do("GET", v+"/v3/files/acme-widget-1.0.0.tar.gz", "admin"),
		"No repository found to serve the request for virtual:acme-widget/acme-widget-1.0.0.tar.gz")
}

// A remote's release pages are read with the policy at 7.x's cache path,
// .puppet/<m>/releases.json, for a listing, and with the blackout alone to
// find an archive; a release whose archive the remote's patterns reject
// stays listed, and the archive is refused before the source is read
// (R3, R4).
func TestUnitRemoteReleasePagesAndRejectedArchive(t *testing.T) {
	data := fixtureArchive(t)
	var reads []source.Resource
	src := metadataSource{
		bodyAt: func(res source.Resource) string {
			reads = append(reads, res)
			return upstreamBody(t, data)
		},
		check: func(context.Context, string) error { return nil },
	}
	f := newFixtureWith(t, repo.Remote,
		func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src })
	f.c.ExcludesPattern = new("**/*.tar.gz")
	f.save(f.c)
	if got := releaseVersions(t, f.do("GET", base+"/v3/releases?module=acme-widget",
		"admin")); len(got) != 1 {
		t.Fatalf("listing: %v", got)
	}
	if len(reads) != 1 || reads[0].PolicyPath != ".puppet/acme-widget/releases.json" ||
		reads[0].BlackoutOnly {
		t.Fatalf("listing read: %+v", reads)
	}
	wantPolicy(t, f.do("GET", base+"/v3/files/acme-widget-1.0.0.tar.gz", "admin"),
		"The repository 'puppet-local' rejected the artifact "+
			"'puppet-local:acme-widget/acme-widget-1.0.0.tar.gz' due to its include/exclude "+
			"pattern settings.")
	if len(reads) != 1 {
		t.Fatalf("refused archive read the source: %d", len(reads))
	}
	f.c.ExcludesPattern = nil
	f.save(f.c)
	r := f.do("GET", base+"/v3/files/acme-widget-1.0.0.tar.gz", "admin")
	if len(reads) != 2 || !reads[1].BlackoutOnly ||
		reads[1].PolicyPath != ".puppet/acme-widget/releases.json" {
		t.Fatalf("archive lookup: %d %+v", r.Code, reads)
	}
	// The search, a native search route, reads the pages with the blackout
	// alone, checked at the module folder (R2b).
	if got := searchVersions(t, f.do("GET", base+"/v3/modules?query=acme-widget",
		"admin")); len(got) != 1 {
		t.Fatalf("search: %v", got)
	}
	if len(reads) != 3 || !reads[2].BlackoutOnly || reads[2].PolicyPath != "" ||
		reads[2].PermissionPath != "acme-widget" {
		t.Fatalf("search read: %+v", reads)
	}
}

// A virtual repository whose only member is blacked out finds nothing in
// its module search, an empty result rather than a refusal (R2b, R5).
func TestUnitVirtualSearchOverABlackedOutMember(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.deploy(f.c, fixtureArchive(t))
	f.c.BlackedOut = new(true)
	f.save(f.c)
	f.create(repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet,
		Repositories: []string{f.c.Key},
	})
	r := f.do("GET", "/artifactory/api/puppet/virtual/v3/modules?query=acme-widget", "admin")
	if r.Code != 200 || strings.TrimSpace(r.Body.String()) != emptySearch {
		t.Fatalf("search: %d %s", r.Code, r.Body.String())
	}
}

// grant gives viewer Read on each repository in targets, within its
// target's include and exclude patterns.
func (f *fixture) grant(targets map[string]accesstest.Target) {
	f.t.Helper()
	if err := f.db.Permissions().Create(f.t.Context(), accesstest.Permission{
		Name: "viewer-read",
		Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{Users: map[string]api.Actions{"viewer": api.ActionRead}},
				Targets: targets,
			},
		},
	}); err != nil {
		f.t.Fatal(err)
	}
}

// Read permission, unlike the patterns, still leaves out a release whose
// archive the caller may not read, from a hosted, virtual and remote
// listing alike, so the client resolves behind the repository's
// permissions.
func TestUnitReadPermissionStillDecidesTheListing(t *testing.T) {
	all := accesstest.Target{Include: []string{"**"}}
	newer := accesstest.Target{
		Include: []string{"**"}, Exclude: []string{"**/acme-widget-2.0.0.tar.gz"},
	}
	hosted := func(t *testing.T) *fixture {
		t.Helper()
		f := newFixture(t, repo.Local)
		f.deployVersion(f.c, "1.0.0", variantArchive(t, "1.0.0", "older"))
		f.deployVersion(f.c, "2.0.0", variantArchive(t, "2.0.0", "newer"))
		f.create(repo.Config{
			Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet,
			Repositories: []string{f.c.Key},
		})
		return f
	}
	t.Run("hosted", func(t *testing.T) {
		f := hosted(t)
		f.grant(map[string]accesstest.Target{f.c.Key: newer})
		if got := releaseVersions(t, f.do("GET", base+"/v3/releases?module=acme-widget",
			"viewer")); len(got) != 1 || got["1.0.0"] == "" {
			t.Fatalf("hosted: %v", got)
		}
		if r := f.do("GET", base+"/v3/files/acme-widget-2.0.0.tar.gz", "viewer"); r.Code != 403 {
			t.Fatalf("unreadable archive: %d", r.Code)
		}
	})
	t.Run("virtual", func(t *testing.T) {
		f := hosted(t)
		f.grant(map[string]accesstest.Target{f.c.Key: all, "virtual": newer})
		if got := releaseVersions(t, f.do("GET",
			"/artifactory/api/puppet/virtual/v3/releases?module=acme-widget", "viewer",
		)); len(got) != 1 || got["1.0.0"] == "" {
			t.Fatalf("virtual: %v", got)
		}
	})
	t.Run("remote", func(t *testing.T) {
		src := metadataSource{
			body:  upstreamBody(t, fixtureArchive(t)),
			check: func(context.Context, string) error { return nil },
		}
		f := newFixtureWith(t, repo.Remote,
			func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src })
		f.grant(map[string]accesstest.Target{f.c.Key: {
			Include: []string{"**"}, Exclude: []string{"**/acme-widget-1.0.0.tar.gz"},
		}})
		if got := releaseVersions(t, f.do("GET", base+"/v3/releases?module=acme-widget",
			"viewer")); len(got) != 0 {
			t.Fatalf("remote: %v", got)
		}
	})
}
