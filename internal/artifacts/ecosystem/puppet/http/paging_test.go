package puppethttp_test

import (
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// deployReleases deploys n releases of acme-alpha, 1.0.0 to 1.<n-1>.0, to
// c, and returns them newest first, the order the release list answers.
func (f *fixture) deployReleases(c repo.Config, n int) []listRelease {
	f.t.Helper()
	out := make([]listRelease, n)
	for i := range n {
		v := fmt.Sprintf("1.%d.0", i)
		out[n-1-i] = f.deployRelease(c, "acme-alpha", v, moduleArchive(f.t, "acme-alpha", v))
	}
	return out
}

// releasesJSON returns the records of rels.
func releasesJSON(rels []listRelease) []string {
	out := make([]string, 0, len(rels))
	for _, rel := range rels {
		out = append(out, releaseJSON(rel))
	}
	return out
}

// One module's release list pages 20 releases unless limit asks for up to
// 100, under the Forge's whole pagination object, whose links keep module,
// limit and offset and write "&" as it is, as 7.x answers a release list
// (RTFACT-14557) and as the lists of every release do (#1287). A limit
// above 100 is served as 100 [I]; the bounds' 400s stay.
func TestUnitOneModuleReleaseListPages(t *testing.T) {
	f := newFixture(t, repo.Local)
	rels := f.deployReleases(f.c, 45)
	const route = "/v3/releases?module=acme-alpha"
	for _, tc := range []struct {
		query                 string
		limit, offset, served int
	}{
		{"", 20, 0, 20},
		{"&offset=20", 20, 20, 20},
		{"&offset=40", 20, 40, 5},
		{"&limit=7&offset=14", 7, 14, 7},
		{"&limit=100", 100, 0, 45},
		{"&limit=1001", 100, 0, 45},
		{"&offset=4096", 20, 4096, 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			end := min(tc.offset+tc.served, len(rels))
			start := min(tc.offset, end)
			wantBody(t, f.do("GET", base+route+tc.query, "admin"),
				listJSON(route, tc.limit, tc.offset, 45, releasesJSON(rels[start:end])...))
			if r := f.do("HEAD", base+route+tc.query, "admin"); r.Code != 200 || r.Body.Len() != 0 {
				t.Fatalf("HEAD: %d %s", r.Code, r.Body.String())
			}
		})
	}
	for query, message := range map[string]string{
		"&limit=0":      "invalid Puppet limit",
		"&limit=x":      "invalid Puppet limit",
		"&offset=-1":    "invalid Puppet offset",
		"&offset=x":     "invalid Puppet offset",
		"&offset=4097":  "invalid Puppet offset",
		"&limit=0&x=1":  "unsupported Puppet query",
		"&sort_by=name": "invalid Puppet module",
	} {
		wantError(t, f.do("GET", base+route+query, "admin"), 400, message)
	}
	// sort_by and exclude_fields are accepted and leave the order and the
	// fields as they are, and the links name only module, limit and offset
	// [I].
	wantBody(t, f.do("GET", base+route+"&sort_by=version&exclude_fields=readme&limit=2", "admin"),
		listJSON(route, 2, 0, 45, releasesJSON(rels[:2])...))
}

// The module search pages as the lists do, under the Forge's pagination
// object, whose links keep query, limit and offset: 7.x's own virtual
// repository asks a member's upstream for "query=...&limit=100"
// (RTFACT-13687), and the Forge pages its search so. It answers at most the
// one module named (#1287).
func TestUnitModuleSearchPages(t *testing.T) {
	f := newFixture(t, repo.Local)
	rels := f.deployReleases(f.c, 2)
	const route = "/v3/modules?query=acme-alpha"
	module := moduleJSON(rels...)
	for _, tc := range []struct {
		query         string
		limit, offset int
		results       []string
	}{
		{"", 20, 0, []string{module}},
		{"&limit=1", 1, 0, []string{module}},
		{"&limit=1&offset=1", 1, 1, nil},
		{"&offset=3&limit=2", 2, 3, nil},
		{"&limit=500", 100, 0, []string{module}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			wantBody(t, f.do("GET", base+route+tc.query, "admin"),
				listJSON(route, tc.limit, tc.offset, 1, tc.results...))
		})
	}
	wantBody(t, f.do("GET", base+"/v3/modules?query=acme-beta", "admin"),
		listJSON("/v3/modules?query=acme-beta", 20, 0, 0))
	for query, message := range map[string]string{
		"&limit":      "invalid Puppet limit",
		"&limit=0":    "invalid Puppet limit",
		"&offset=-1":  "invalid Puppet offset",
		"&x=1":        "search requires an exact Puppet module name",
		"&query=acme": "search requires an exact Puppet module name",
	} {
		wantError(t, f.do("GET", base+route+query, "admin"), 400, message)
	}
	wantError(t, f.do("GET", base+route, "denied"), 403, "Forbidden")
}
