package puppethttp_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

const virtualBase = "/artifactory/api/puppet/virtual"

// upstreamHost is the host of the upstream address r reads.
func upstreamHost(t *testing.T, r source.Resource) string {
	t.Helper()
	u, err := url.Parse(r.ExternalURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

// unlistedMembers returns a fixture whose virtual repository, "virtual",
// has the members keys, in order: "puppet-local" is a remote over
// https://example.com/puppet/, "other" a remote over
// https://other.example/puppet/, and "hosted" a local repository. Neither
// remote's release pages list a release of acme-widget. Each remote's
// upstream answers its module record with modules[its host], or 404 when
// that is empty, and the record of any release with that release tagged
// with its host. reads, when set, records each module and release record
// read. tune changes the remotes' source.
func unlistedMembers(
	t *testing.T,
	modules map[string]string,
	reads *[]source.Resource,
	tune func(*metadataSource),
	keys ...string,
) *fixture {
	t.Helper()
	src := unlistedSource(t, modules, reads)
	if tune != nil {
		tune(&src)
	}
	return membersOver(t, src, keys...)
}

// unlistedSource is the remotes' source of unlistedMembers.
func unlistedSource(
	t *testing.T,
	modules map[string]string,
	reads *[]source.Resource,
) metadataSource {
	t.Helper()
	src := metadataSource{
		body: remotePageBody(t), pagesAtURL: true,
		check: func(context.Context, string) error { return nil },
	}
	src.moduleAt = func(r source.Resource) (string, bool) {
		if reads != nil {
			*reads = append(*reads, r)
		}
		body := modules[upstreamHost(t, r)]
		return body, body == ""
	}
	src.release.at = func(r source.Resource) (string, bool) {
		if reads != nil {
			*reads = append(*reads, r)
		}
		version := strings.TrimPrefix(r.Path, "v3/releases/acme-widget-")
		return releaseBody(t, version, map[string]any{"tags": []string{upstreamHost(t, r)}}), false
	}
	return src
}

// membersOver is unlistedMembers with src as the remotes' source.
func membersOver(t *testing.T, src pkgroute.Source, keys ...string) *fixture {
	t.Helper()
	f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src })
	f.create(repo.Config{
		Key: "other", Class: repo.Remote, PackageType: repo.Puppet,
		URL: "https://other.example/puppet/",
	})
	f.create(repo.Config{Key: "hosted", Class: repo.Local, PackageType: repo.Puppet})
	f.create(repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet, Repositories: keys,
	})
	return f
}

// memberModuleJSON is peModuleJSON as the remote over host answers it,
// whose releases unlistedMembers tags with host.
func memberModuleJSON(host, current string, versions ...string) string {
	return strings.Replace(peModuleJSON(current, versions...), `"tags":[]`,
		`"tags":["`+host+`"]`, 1)
}

// A virtual repository answers v3/modules/<m> for a module every member's
// release pages leave out, as the Forge's lists leave out a Puppet
// Enterprise module, with the record its first remote member, in member
// order, answers from its upstream's own record of the module (#1422) [I]:
// releases newest first without the deleted ones, the newest one's own
// record as its current release, and the upstream's module-wide fields, as
// the remote itself answers (#1370). Its links are relative, so they lead
// back to the virtual repository, which answers the current release's
// record from the same member (#1369). The record is read under the
// member's authority, the release pages' policy path included.
func TestUnitVirtualModuleOnlyAnUpstreamRecordHolds(t *testing.T) {
	var reads []source.Resource
	f := unlistedMembers(t, map[string]string{
		"example.com": peRecord(t, [2]string{"1.0.0", ""},
			[2]string{"2.1.0", "2017-11-16 16:16:15 -0800"}, [2]string{"2.0.0", ""}),
		"other.example": peRecord(t, [2]string{"3.0.0", ""}),
	}, &reads, nil, "hosted", "puppet-local", "other")
	want := memberModuleJSON("example.com", "2.0.0", "2.0.0", "1.0.0")
	wantBody(t, f.do("GET", virtualBase+"/v3/modules/acme-widget", "admin"), want)
	// The remote member answers the same record.
	wantBody(t, f.do("GET", base+"/v3/modules/acme-widget", "admin"), want)
	if len(reads) != 4 {
		t.Fatalf("reads: %+v", reads)
	}
	module, current := reads[0], reads[1]
	if module.Path != "v3/modules/acme-widget" ||
		upstreamHost(t, module) != "example.com" || module.PermissionPath != "acme-widget" ||
		module.PolicyPath != ".puppet/acme-widget/releases.json" || module.BlackoutOnly {
		t.Fatalf("module record read: %+v", module)
	}
	if current.Path != "v3/releases/acme-widget-2.0.0" ||
		upstreamHost(t, current) != "example.com" || current.BlackoutOnly {
		t.Fatalf("current release read: %+v", current)
	}
	if r := f.do("HEAD", virtualBase+"/v3/modules/acme-widget", "admin"); r.Code != 200 ||
		r.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %s", r.Code, r.Body.String())
	}
	if r := f.do("GET", virtualBase+"/v3/releases/acme-widget-2.0.0", "admin"); r.Code != 200 ||
		!strings.Contains(r.Body.String(), `"tags":["example.com"]`) {
		t.Fatalf("current release through the virtual repository: %d %s", r.Code, r.Body.String())
	}
	// The release list and the search still answer from the pages.
	wantBody(t, f.do("GET", virtualBase+"/v3/releases?module=acme-widget", "admin"),
		listJSON("/v3/releases?module=acme-widget", 20, 0, 0))
	wantBody(t, f.do("GET", virtualBase+"/v3/modules?query=acme-widget", "admin"),
		listJSON("/v3/modules?query=acme-widget", 20, 0, 0))
}

// A remote member whose upstream holds no record of the module, or whose
// record lists no release the caller may read, is passed over for the next
// in member order; when none holds one, the answer is 404 as before.
func TestUnitVirtualModuleMemberOrder(t *testing.T) {
	second := peRecord(t, [2]string{"3.0.0", ""})
	for _, tc := range []struct {
		name  string
		first string
	}{
		{"no record", ""},
		{"every release deleted", peRecord(t, [2]string{"2.0.0", "2020-01-01 00:00:00 +0000"})},
		{"no releases", `{"slug":"acme-widget","releases":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := unlistedMembers(t,
				map[string]string{"example.com": tc.first, "other.example": second}, nil, nil,
				"puppet-local", "hosted", "other")
			wantBody(t, f.do("GET", virtualBase+"/v3/modules/acme-widget", "admin"),
				memberModuleJSON("other.example", "3.0.0", "3.0.0"))
		})
	}
	f := unlistedMembers(t, map[string]string{"example.com": "", "other.example": ""}, nil, nil,
		"puppet-local", "other", "hosted", "missing")
	wantError(t, f.do("GET", virtualBase+"/v3/modules/acme-widget", "admin"), 404,
		"Puppet module not found")
	f = unlistedMembers(t, map[string]string{"example.com": second}, nil, nil, "hosted")
	wantError(t, f.do("GET", virtualBase+"/v3/modules/acme-widget", "admin"), 404,
		"Puppet module not found")
}

// Read permission is checked on the virtual repository first: a caller who
// may not read the module folder there is refused before any member is
// asked, as on main. Each member is then checked as the merge of the
// release pages checks it: a member whose module folder the caller may not
// read is passed over, and a release the caller may not read in the virtual
// repository or in the member is left out of the record, so the newest
// readable one is current.
func TestUnitVirtualModuleReadPermission(t *testing.T) {
	modules := map[string]string{
		"example.com":   peRecord(t, [2]string{"1.0.0", ""}, [2]string{"2.0.0", ""}),
		"other.example": peRecord(t, [2]string{"3.0.0", ""}),
	}
	all := accesstest.Target{Include: []string{"**"}}
	notNewest := accesstest.Target{
		Include: []string{"**"}, Exclude: []string{"**/acme-widget-2.0.0.tar.gz"},
	}
	t.Run("virtual module folder", func(t *testing.T) {
		var reads []source.Resource
		f := unlistedMembers(t, modules, &reads, nil, "puppet-local", "other")
		f.grant(map[string]accesstest.Target{"puppet-local": all, "other": all})
		wantError(t, f.do("GET", virtualBase+"/v3/modules/acme-widget", "viewer"), 403,
			"Forbidden")
		wantError(t, f.do("GET", virtualBase+"/v3/modules/acme-widget", "denied"), 403,
			"Forbidden")
		if len(reads) != 0 {
			t.Fatalf("a refused caller reached an upstream: %+v", reads)
		}
	})
	t.Run("member module folder", func(t *testing.T) {
		f := unlistedMembers(t, modules, nil, nil, "puppet-local", "other")
		f.grant(map[string]accesstest.Target{"virtual": all, "other": all})
		wantBody(t, f.do("GET", virtualBase+"/v3/modules/acme-widget", "viewer"),
			memberModuleJSON("other.example", "3.0.0", "3.0.0"))
	})
	for _, tc := range []struct {
		name    string
		targets map[string]accesstest.Target
	}{
		{"release in the virtual repository", map[string]accesstest.Target{
			"virtual": notNewest, "puppet-local": all,
		}},
		{"release in the member", map[string]accesstest.Target{
			"virtual": all, "puppet-local": notNewest,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := unlistedMembers(t, modules, nil, nil, "puppet-local", "other")
			f.grant(tc.targets)
			wantBody(t, f.do("GET", virtualBase+"/v3/modules/acme-widget", "viewer"),
				memberModuleJSON("example.com", "1.0.0", "1.0.0"))
		})
	}
	t.Run("no readable release in the first member", func(t *testing.T) {
		f := unlistedMembers(t, modules, nil, nil, "puppet-local", "other")
		f.grant(map[string]accesstest.Target{
			"virtual": {Include: []string{"**"}, Exclude: []string{
				"**/acme-widget-1.0.0.tar.gz", "**/acme-widget-2.0.0.tar.gz",
			}},
			"puppet-local": all, "other": all,
		})
		wantBody(t, f.do("GET", virtualBase+"/v3/modules/acme-widget", "viewer"),
			memberModuleJSON("other.example", "3.0.0", "3.0.0"))
	})
}

// A member whose record read answers 404, as a blacked-out member's does or
// one whose patterns reject the release pages' policy path, is passed over
// (R5), as virtualRecord passes it over; any other failure of the first
// member that holds a record fails the answer.
func TestUnitVirtualModuleMemberFailures(t *testing.T) {
	const invalid = "invalid upstream Puppet metadata"
	one := peRecord(t, [2]string{"1.0.0", ""})
	for _, tc := range []struct {
		name    string
		tune    func(*metadataSource)
		status  int
		message string
	}{
		{"refused", func(s *metadataSource) {
			s.moduleReadError = format.Rejectf(404, "refused")
		}, 404, "Puppet module not found"},
		{"upstream 503", func(s *metadataSource) {
			s.moduleReadError = &source.HTTPError{Status: 503}
		}, 503, "Service Unavailable"},
		{"invalid record", func(s *metadataSource) {
			s.moduleAt = func(source.Resource) (string, bool) {
				return `{"slug":"acme-widget","releases":{}}`, false
			}
		}, 502, invalid},
		{"no current record", func(s *metadataSource) { s.release.at = nil }, 502, invalid},
		{
			"no authority", func(s *metadataSource) { s.moduleNoAuthority = true }, 502,
			"Bad Gateway",
		},
		{"authority failure", func(s *metadataSource) {
			s.check = func(context.Context, string) error { return source.ErrUnavailable }
		}, 502, "Bad Gateway"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := unlistedMembers(t, map[string]string{"example.com": one, "other.example": one},
				nil, tc.tune, "puppet-local", "other")
			wantError(t, f.do("GET", virtualBase+"/v3/modules/acme-widget", "admin"), tc.status,
				tc.message)
		})
	}
}

// moduleFault is a remote source whose module record reads from host fail
// with err.
type moduleFault struct {
	metadataSource
	t    *testing.T
	host string
	err  error
}

func (s moduleFault) Read(
	r *http.Request,
	c repo.Config,
	resource source.Resource,
) (source.Result, error) {
	if strings.HasPrefix(resource.Path, "v3/modules/") && upstreamHost(s.t, resource) == s.host {
		return source.Result{}, s.err
	}
	return s.metadataSource.Read(r, c, resource)
}

// A member whose module record read fails with a 404 of its own, rather
// than the upstream's missing record, is passed over too, as virtualRecord
// passes it over.
func TestUnitVirtualModuleMemberAnswering404(t *testing.T) {
	modules := map[string]string{
		"example.com":   peRecord(t, [2]string{"1.0.0", ""}),
		"other.example": peRecord(t, [2]string{"3.0.0", ""}),
	}
	f := membersOver(t, moduleFault{
		metadataSource: unlistedSource(t, modules, nil), t: t, host: "example.com",
		err: &source.HTTPError{Status: 404},
	}, "puppet-local", "other")
	wantBody(t, f.do("GET", virtualBase+"/v3/modules/acme-widget", "admin"),
		memberModuleJSON("other.example", "3.0.0", "3.0.0"))
}

// laterRead is a store whose reads of key after the first answer then's
// result instead, as when a member's configuration changes, or the store
// fails, between the merge of the release pages and the module record.
type laterRead struct {
	pkgroute.Store
	key   string
	reads int
	then  func(repo.Config) (repo.Config, error)
}

func (s *laterRead) Repos() port.RepoStore { return laterReadRepos{s.Store.Repos(), s} }

type laterReadRepos struct {
	port.RepoStore
	s *laterRead
}

func (r laterReadRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	c, err := r.RepoStore.Get(ctx, key)
	if err != nil || key != r.s.key {
		return c, err
	}
	if r.s.reads++; r.s.reads < 2 {
		return c, nil
	}
	return r.s.then(c)
}

// A member the store fails to read fails the answer, and so does one that
// is no longer a Puppet repository, as they fail the merge.
func TestUnitVirtualModuleMemberLookupFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		then   func(repo.Config) (repo.Config, error)
		status int
	}{
		{"store failure", func(repo.Config) (repo.Config, error) {
			return repo.Config{}, context.Canceled
		}, 500},
		{"another type", func(c repo.Config) (repo.Config, error) {
			c.PackageType = repo.Generic
			return c, nil
		}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := metadataSource{
				body: remotePageBody(t), check: func(context.Context, string) error { return nil },
			}
			f := newFixtureStore(t, repo.Remote,
				func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src },
				func(s pkgroute.Store) pkgroute.Store {
					return &laterRead{Store: s, key: "puppet-local", then: tc.then}
				})
			f.create(repo.Config{
				Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet,
				Repositories: []string{"puppet-local"},
			})
			if r := f.do(
				"GET",
				virtualBase+"/v3/modules/acme-widget",
				"admin",
			); r.Code != tc.status {
				t.Fatalf("%d %s", r.Code, r.Body.String())
			}
		})
	}
}

// A module some member lists keeps the answer it had before #1422, the
// record merged from the members' releases: no member's upstream module
// record is read, whatever it lists.
func TestUnitVirtualListedModuleKeepsItsAnswer(t *testing.T) {
	var reads []source.Resource
	f := unlistedMembers(t, map[string]string{
		"example.com": peRecord(t, [2]string{"9.0.0", ""}),
	}, &reads, nil, "puppet-local", "hosted")
	hosted, err := f.db.Repos().Get(t.Context(), "hosted")
	if err != nil {
		t.Fatal(err)
	}
	rel := f.deployRelease(hosted, "acme-widget", "1.0.0", moduleArchive(t, "acme-widget", "1.0.0"))
	wantBody(t, f.do("GET", virtualBase+"/v3/modules/acme-widget", "admin"), moduleJSON(rel)+"\n")
	if len(reads) != 0 {
		t.Fatalf("a listed module read an upstream record: %+v", reads)
	}
}
