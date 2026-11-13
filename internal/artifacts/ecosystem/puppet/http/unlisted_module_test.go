package puppethttp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
)

// peRecord is an upstream's record of acme-widget, a Puppet Enterprise
// module, that lists releases, each a version and its deleted_at.
func peRecord(t *testing.T, releases ...[2]string) string {
	t.Helper()
	list := make([]map[string]any, 0, len(releases))
	for _, rel := range releases {
		var deleted any
		if rel[1] != "" {
			deleted = rel[1]
		}
		list = append(list, map[string]any{
			"uri": "/v3/releases/acme-widget-" + rel[0], "slug": "acme-widget-" + rel[0],
			"version": rel[0], "supported": false, "deleted_at": deleted,
			"file_uri": "/v3/files/acme-widget-" + rel[0] + ".tar.gz", "file_size": 100,
		})
	}
	data, err := json.Marshal(map[string]any{
		"slug": "acme-widget", "module_group": "pe_only", "endorsement": "supported",
		"downloads": 91585, "releases": list,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// unlistedFixture returns a remote whose upstream's release pages list no
// release of acme-widget, whose module record is module, and whose record
// of each release is releaseBody's, recording each release record read in
// reads.
func unlistedFixture(t *testing.T, module string, reads *[]source.Resource) *fixture {
	t.Helper()
	return remoteFixture(t, remotePageBody(t), func(s *metadataSource) {
		s.moduleAt = func(source.Resource) (string, bool) { return module, false }
		s.release.at = func(r source.Resource) (string, bool) {
			if reads != nil {
				*reads = append(*reads, r)
			}
			version := strings.TrimPrefix(r.Path, "v3/releases/acme-widget-")
			return releaseBody(t, version, nil), false
		}
	})
}

// peModuleJSON is the module record a remote answers for peRecord's module
// whose current release is version and whose releases are versions.
func peModuleJSON(current string, versions ...string) string {
	links := make([]string, 0, len(versions))
	for _, v := range versions {
		links = append(links, `{"uri":"/v3/releases/acme-widget-`+v+`","version":"`+v+`"}`)
	}
	return `{"uri":"/v3/modules/acme-widget","slug":"acme-widget","name":"widget",` +
		`"downloads":91585,"created_at":null,"updated_at":null,"supported":false,` +
		`"endorsement":"supported","module_group":"pe_only","owner":` + hostedOwner + `,` +
		`"current_release":` + remoteRecordJSON(current, linkedMetadata(current), `[]`) + `,` +
		`"releases":[` + strings.Join(links, ",") + `],"homepage_url":null,"issues_url":null}` + "\n"
}

// A remote answers v3/modules/<m> for a module its upstream's release
// pages leave out, as a Puppet Enterprise module is left out of the Forge's
// lists, from the upstream's own record of the module, as r10k needs to
// install it (#1370) [I]: its releases are those the record lists, newest
// first, without the ones it marks deleted, and its current release the
// newest one's own record, read under the release pages' authority. Its
// release list and search still answer from the pages.
func TestUnitRemoteModuleTheListLeavesOut(t *testing.T) {
	var reads []source.Resource
	f := unlistedFixture(t, peRecord(t, [2]string{"1.0.0", ""},
		[2]string{"2.1.0", "2017-11-16 16:16:15 -0800"}, [2]string{"2.0.0", ""}), &reads)
	wantBody(t, f.do("GET", base+"/v3/modules/acme-widget", "admin"),
		peModuleJSON("2.0.0", "2.0.0", "1.0.0"))
	if r := f.do("HEAD", base+"/v3/modules/acme-widget", "admin"); r.Code != 200 ||
		r.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %s", r.Code, r.Body.String())
	}
	if len(reads) != 2 || reads[0].Path != "v3/releases/acme-widget-2.0.0" ||
		reads[0].PolicyPath != ".puppet/acme-widget/releases.json" || reads[0].BlackoutOnly {
		t.Fatalf("release record reads: %+v", reads)
	}
	wantBody(t, f.do("GET", base+"/v3/releases?module=acme-widget", "admin"),
		listJSON("/v3/releases?module=acme-widget", 20, 0, 0))
	wantBody(t, f.do("GET", base+"/v3/modules?query=acme-widget", "admin"),
		listJSON("/v3/modules?query=acme-widget", 20, 0, 0))
	wantError(t, f.do("GET", base+"/v3/modules/acme-widget", "denied"), 403, "Forbidden")
}

// Read permission decides which of the record's releases are listed and
// which is current, as it decides a release page's: a release the caller
// may not read, or that the source denies, is left out.
func TestUnitRemoteUnlistedModuleReadPermission(t *testing.T) {
	module := peRecord(t, [2]string{"1.0.0", ""}, [2]string{"1.5.0", ""}, [2]string{"2.0.0", ""})
	f := unlistedFixture(t, module, nil)
	f.grant(map[string]accesstest.Target{f.c.Key: {
		Include: []string{"**"}, Exclude: []string{"**/acme-widget-2.0.0.tar.gz"},
	}})
	wantBody(t, f.do("GET", base+"/v3/modules/acme-widget", "viewer"),
		peModuleJSON("1.5.0", "1.5.0", "1.0.0"))

	f = remoteFixture(t, remotePageBody(t), func(s *metadataSource) {
		s.moduleAt = func(source.Resource) (string, bool) { return module, false }
		served(releaseBody(t, "1.0.0", nil), nil)(s)
		s.check = func(_ context.Context, path string) error {
			if path != "acme-widget/acme-widget-1.0.0.tar.gz" {
				return source.ErrDenied
			}
			return nil
		}
		s.release.check = s.check
	})
	wantBody(t, f.do("GET", base+"/v3/modules/acme-widget", "admin"),
		peModuleJSON("1.0.0", "1.0.0"))
}

// A record the upstream does not hold, or that lists no release the caller
// may read, answers 404 as before; a record whose release list is not one
// of valid, distinct versions, or whose current release has no record of
// its own, is invalid upstream metadata (502); and the reads' failures
// answer as a release page's do.
func TestUnitRemoteUnlistedModuleFailures(t *testing.T) {
	const invalid = "invalid upstream Puppet metadata"
	one := peRecord(t, [2]string{"1.0.0", ""})
	record := func(body string) func(*metadataSource) {
		return func(s *metadataSource) {
			s.moduleAt = func(source.Resource) (string, bool) { return body, false }
			served(releaseBody(t, "1.0.0", nil), nil)(s)
		}
	}
	with := func(tune func(*metadataSource)) func(*metadataSource) {
		return func(s *metadataSource) {
			record(one)(s)
			tune(s)
		}
	}
	for _, tc := range []struct {
		name    string
		tune    func(*metadataSource)
		status  int
		message string
	}{
		{"no record", func(s *metadataSource) {
			s.moduleAt = func(source.Resource) (string, bool) { return "", true }
		}, 404, "Puppet module not found"},
		{"no releases", record(`{"slug":"acme-widget"}`), 404, "Puppet module not found"},
		{
			"null releases", record(`{"slug":"acme-widget","releases":null}`), 404,
			"Puppet module not found",
		},
		{
			"every release deleted", record(peRecord(t, [2]string{"1.0.0", "2020-01-01 00:00:00 +0000"})),
			404, "Puppet module not found",
		},
		{"no readable release", with(func(s *metadataSource) {
			s.check = func(context.Context, string) error { return source.ErrDenied }
		}), 404, "Puppet module not found"},
		{"releases not a list", record(`{"slug":"acme-widget","releases":{}}`), 502, invalid},
		{"invalid version", record(peRecord(t, [2]string{"1.x", ""})), 502, invalid},
		{
			"repeated version", record(peRecord(t, [2]string{"1.0.0", ""}, [2]string{"1.0.0", ""})),
			502, invalid,
		},
		{"too many releases", record(`{"slug":"acme-widget","releases":[` +
			strings.TrimSuffix(strings.Repeat(`{"version":"1.0.0"},`, 4097), ",") + `]}`), 502, invalid},
		{"no current record", with(func(s *metadataSource) { s.release.at = nil }), 502, invalid},
		{"invalid current record", with(func(s *metadataSource) {
			served(releaseBody(t, "1.0.1", nil), nil)(s)
		}), 502, invalid},
		{
			"no authority", with(func(s *metadataSource) { s.moduleNoAuthority = true }), 502,
			"Bad Gateway",
		},
		{"authority failure", with(func(s *metadataSource) {
			s.check = func(context.Context, string) error { return source.ErrUnavailable }
		}), 502, "Bad Gateway"},
		{"module refused", with(func(s *metadataSource) {
			s.moduleReadError = format.Rejectf(404, "refused")
		}), 404, "refused"},
		{"module upstream 503", with(func(s *metadataSource) {
			s.moduleReadError = &source.HTTPError{Status: 503}
		}), 503, "Service Unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := remoteFixture(t, remotePageBody(t), tc.tune)
			wantError(t, f.do("GET", base+"/v3/modules/acme-widget", "admin"), tc.status,
				tc.message)
		})
	}
}

// A module the release pages list never reads the module record's release
// list: its releases are the pages', whatever the record lists, and a
// release list the record gets wrong changes nothing.
func TestUnitRemoteListedModuleIgnoresTheRecordReleases(t *testing.T) {
	var reads []source.Resource
	f := remoteFixture(
		t,
		remotePageBody(t, upstreamRelease(t, "1.0.0", nil)),
		func(s *metadataSource) {
			s.moduleAt = func(source.Resource) (string, bool) {
				return `{"slug":"acme-widget","releases":[{"version":"x"},{"version":"9.0.0"}]}`, false
			}
			served(releaseBody(t, "9.0.0", nil), &reads)(s)
		},
	)
	r := f.do("GET", base+"/v3/modules/acme-widget", "admin")
	if r.Code != 200 || !strings.Contains(r.Body.String(),
		`"releases":[{"uri":"/v3/releases/acme-widget-1.0.0","version":"1.0.0"}]`) {
		t.Fatalf("module: %d %s", r.Code, r.Body.String())
	}
	if len(reads) != 0 {
		t.Fatalf("a listed module read a release record: %+v", reads)
	}
}
