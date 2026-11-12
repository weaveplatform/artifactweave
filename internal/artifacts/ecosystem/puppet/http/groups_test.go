package puppethttp_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// clientExclusions is the Puppet client's MODULE_RELEASE_EXCLUSIONS as
// forge.rb's fetch sends them, its commas percent-encoded.
const clientExclusions = "readme%2Cchangelog%2Clicense%2Curi%2Cmodule%2Ctags%2Csupported%2C" +
	"file_size%2Cdownloads%2Ccreated_at%2Cupdated_at%2Cdeleted_at"

// moduleGroupValues are module_groups as a Puppet Enterprise node sends it
// (base%20pe_only, PUP-1890), as older clients sent the setting unchanged
// (base+pe_only), each group alone, empty, and a group the Forge does not
// name.
var moduleGroupValues = []string{"base%20pe_only", "base+pe_only", "base", "pe_only", "", "other"}

// wantSameAnswer fails unless route with module_groups=value answers what
// route answers without it, and that answer is want.
func wantSameAnswer(t *testing.T, f *fixture, prefix, route, rest, want string) {
	t.Helper()
	wantBody(t, f.do("GET", prefix+route+rest, "admin"), want)
	for _, value := range moduleGroupValues {
		wantBody(t, f.do("GET", prefix+route+"&module_groups="+value+rest, "admin"), want)
		if r := f.do("HEAD", prefix+route+"&module_groups="+value+rest, "admin"); r.Code != 200 ||
			r.Body.Len() != 0 {
			t.Fatalf("HEAD %s: %d %s", value, r.Code, r.Body.String())
		}
	}
}

// One module's release list and the module search take module_groups, as
// the Puppet client sends it on Puppet Enterprise with every fetch and
// search (lib/puppet/forge.rb), and answer as they do without it, on a
// hosted repository and a virtual one: no value narrows the answer and the
// links leave it out (#1343) [I].
func TestUnitModuleGroupsLeaveTheAnswerAsItIs(t *testing.T) {
	f := newFixture(t, repo.Local)
	rels := f.deployReleases(f.c, 25)
	f.create(repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet,
		Repositories: []string{f.c.Key},
	})
	const releases = "/v3/releases?module=acme-alpha"
	const search = "/v3/modules?query=acme-alpha"
	for _, prefix := range []string{base, "/artifactory/api/puppet/virtual"} {
		t.Run(prefix, func(t *testing.T) {
			wantSameAnswer(t, f, prefix, releases, "",
				listJSON(releases, 20, 0, 25, releasesJSON(rels[:20])...))
			wantSameAnswer(t, f, prefix, releases, "&limit=10&offset=20",
				listJSON(releases, 10, 20, 25, releasesJSON(rels[20:])...))
			// The Puppet client's own fetch, as forge.rb sends it on Puppet
			// Enterprise: sort_by and exclude_fields stay in the links,
			// module_groups does not.
			trimmed := make([]string, 0, 20)
			for _, rel := range rels[:20] {
				trimmed = append(trimmed, releaseExclusionRecord(rel))
			}
			fetch := "&sort_by=version&exclude_fields=" + clientExclusions
			wantBody(t, f.do("GET", prefix+releases+fetch+"&module_groups=base%20pe_only", "admin"),
				listJSON(releases+fetch, 20, 0, 25, trimmed...))
			wantSameAnswer(t, f, prefix, search, "",
				listJSON(search, 20, 0, 1, moduleJSON(rels...)))
			wantSameAnswer(t, f, prefix, search, "&limit=1&offset=1", listJSON(search, 1, 1, 1))
		})
	}
}

// releaseExclusionRecord is hosted release rel's record without the fields
// the Puppet client's exclude_fields names.
func releaseExclusionRecord(rel listRelease) string {
	slug := rel.name + "-" + rel.version
	return `{"slug":"` + slug + `","version":"` + rel.version + `","metadata":` +
		metadataJSON(rel.name, rel.version, rel.name+" "+rel.version, "[]", "") +
		`,"file_uri":"/v3/files/` + slug + `.tar.gz","file_md5":"` + rel.item.Actual.MD5 +
		`","file_sha256":"` + sum(rel.data) + `"}`
}

// A remote takes module_groups as a hosted repository does, and asks its
// upstream for the same release pages and module record with or without
// it: it does not pass the parameter on, so the upstream's own lists
// decide what it lists (#1343) [I].
func TestUnitRemoteModuleGroupsAreNotPassedOn(t *testing.T) {
	var asked []string
	f := remoteFixture(t, "", func(s *metadataSource) {
		page := remotePageBody(t, upstreamRelease(t, "1.0.0", nil))
		s.bodyAt = func(r source.Resource) string { asked = append(asked, r.ExternalURL); return page }
		s.moduleAt = func(r source.Resource) (string, bool) {
			asked = append(asked, r.ExternalURL)
			return `{"slug":"acme-widget"}`, false
		}
	})
	record := remoteRecordJSON("1.0.0", linkedMetadata("1.0.0"), `[]`)
	const releases = "/v3/releases?module=acme-widget"
	wantSameAnswer(t, f, base, releases, "", listJSON(releases, 20, 0, 1, record))
	const search = "/v3/modules?query=acme-widget"
	r := f.do("GET", base+search, "admin")
	wantSameAnswer(t, f, base, search, "", r.Body.String())
	if !strings.Contains(r.Body.String(), `"slug":"acme-widget-1.0.0"`) {
		t.Fatalf("search: %s", r.Body.String())
	}
	for _, at := range asked {
		u, err := url.Parse(at)
		if err != nil || u.Query().Has("module_groups") {
			t.Fatalf("asked the upstream %s", at)
		}
	}
	if len(asked) == 0 {
		t.Fatal("the upstream was not asked")
	}
}

// module_groups changes no other answer: the whole-repository lists, which
// a page alone asks for, the release and module records, and a repeated
// parameter keep their 400s, on every repository class.
func TestUnitModuleGroupsKeepTheOtherRefusals(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Remote, repo.Virtual} {
		t.Run(string(class), func(t *testing.T) {
			f := newFixture(t, class)
			if class == repo.Remote {
				f = remoteFixture(t, remotePageBody(t, upstreamRelease(t, "1.0.0", nil)), nil)
			}
			for route, message := range map[string]string{
				"/v3/releases?module_groups=base%20pe_only":                                "unsupported Puppet query",
				"/v3/releases?module_groups=base%20pe_only&limit=1":                        "unsupported Puppet query",
				"/v3/modules?module_groups=base%20pe_only":                                 "search requires an exact Puppet module name",
				"/v3/modules?module_groups=base%20pe_only&limit=1":                         "search requires an exact Puppet module name",
				"/v3/releases?module=acme-widget&module_groups=base&module_groups=pe_only": "unsupported Puppet query",
				"/v3/modules?query=acme-widget&module_groups=base&module_groups=pe_only":   "search requires an exact Puppet module name",
				"/v3/releases/acme-widget-1.0.0?module_groups=base%20pe_only":              "invalid Puppet release",
				"/v3/modules/acme-widget?module_groups=base%20pe_only":                     "invalid Puppet module",
			} {
				wantError(t, f.do("GET", base+route, "admin"), 400, message)
			}
		})
	}
}
