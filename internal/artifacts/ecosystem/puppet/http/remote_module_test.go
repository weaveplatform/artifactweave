package puppethttp_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// remotePageBody returns an upstream's one page of acme-widget's releases.
func remotePageBody(t *testing.T, releases ...map[string]any) string {
	t.Helper()
	results := make([]any, 0, len(releases))
	for _, rel := range releases {
		results = append(results, rel)
	}
	page, err := json.Marshal(map[string]any{
		"results":    results,
		"pagination": map[string]any{"offset": 0, "total": len(releases), "next": nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(page)
}

// remoteRecordJSON is the record a remote answers for an upstream release
// of acme-widget at version that gives only what upstreamRelease gives and
// tags.
func remoteRecordJSON(version, metadata, tags string) string {
	return `{"uri":"/v3/releases/acme-widget-` + version + `","slug":"acme-widget-` + version + `",` +
		`"module":{"uri":"/v3/modules/acme-widget","slug":"acme-widget","name":"widget",` +
		`"owner":` + hostedOwner + `,"deprecated_at":null},"version":"` + version + `",` +
		`"metadata":` + metadata + `,"tags":` + tags + `,"supported":false,` +
		`"file_uri":"/v3/files/acme-widget-` + version + `.tar.gz","file_size":100,` +
		`"file_md5":null,"file_sha256":"` + strings.Repeat(
		"a",
		64,
	) + `","downloads":0,` +
		`"readme":null,"changelog":null,"license":null,` +
		`"created_at":null,"updated_at":null,"deleted_at":null}`
}

// remoteFixture returns a remote whose upstream answers page for every
// release page and tune's changes.
func remoteFixture(t *testing.T, page string, tune func(*metadataSource)) *fixture {
	t.Helper()
	src := metadataSource{body: page, check: func(context.Context, string) error { return nil }}
	if tune != nil {
		tune(&src)
	}
	return newFixtureWith(
		t,
		repo.Remote,
		func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src },
	)
}

// A remote's release record answers the upstream release's whole metadata
// in 7.x's order (#1325) [I] and the upstream's tags, which may name tags
// metadata.json does not [I]; tags of another type answer none. A
// metadata.json above the most an archive's may be is invalid upstream
// metadata, since no archive could match it.
func TestUnitRemoteReleaseMetadataAndTags(t *testing.T) {
	manifest := strings.ReplaceAll(strings.ReplaceAll(fullManifest, "acme-full", "acme-widget"),
		"acme/full", "acme/widget")
	metadata := strings.ReplaceAll(strings.ReplaceAll(fullMetadata, "acme-full", "acme-widget"),
		"acme/full", "acme/widget")
	newer := upstreamRelease(t, "2.5.0", map[string]any{
		"metadata": json.RawMessage(manifest), "tags": []string{"sql", "windows", "database"},
	})
	older := upstreamRelease(t, "1.0.0", map[string]any{"tags": "sql"})
	f := remoteFixture(t, remotePageBody(t, newer, older), nil)
	release := remoteRecordJSON("2.5.0", metadata, `["sql","windows","database"]`)
	wantBody(t, f.do("GET", base+"/v3/releases/acme-widget-2.5.0", "admin"), release+"\n")
	wantBody(t, f.do("GET", base+"/v3/releases?module=acme-widget", "admin"),
		listJSON("/v3/releases?module=acme-widget", 20, 0, 2, release,
			remoteRecordJSON("1.0.0", linkedMetadata("1.0.0"), `[]`)))

	large := upstreamRelease(t, "1.0.0", map[string]any{"metadata": json.RawMessage(
		`{"name":"acme-widget","version":"1.0.0","dependencies":[],"description":"` +
			strings.Repeat("x", 1<<20) + `"}`)})
	f = remoteFixture(t, remotePageBody(t, large), nil)
	wantError(t, f.do("GET", base+"/v3/releases?module=acme-widget", "admin"), 502,
		"invalid upstream Puppet metadata")
}

// A remote's release list leaves out the fields exclude_fields names,
// separated by spaces as in the Forge's v3 API description, and keeps the
// parameter in its links (#1327) [I].
func TestUnitRemoteReleaseListExcludeFields(t *testing.T) {
	f := remoteFixture(t, remotePageBody(t, upstreamRelease(t, "1.0.0", nil)), nil)
	record := strings.Replace(remoteRecordJSON("1.0.0", linkedMetadata("1.0.0"), `[]`),
		`"readme":null,"changelog":null,"license":null,`, "", 1)
	wantBody(
		t,
		f.do(
			"GET",
			base+"/v3/releases?module=acme-widget&exclude_fields=readme%20changelog%20license",
			"admin",
		),
		listJSON(
			"/v3/releases?module=acme-widget&exclude_fields=readme+changelog+license",
			20,
			0,
			1,
			record,
		),
	)
}

// When the upstream answers 404 for the module's record, the remote's
// v3/modules/<m> answers 404 and its search finds nothing, while the
// module's releases stay listed (#1326). RTFACT-19130 reports a 6.8.6
// remote answering 404 for a module the Forge served; here the 404 is the
// upstream's own.
func TestUnitRemoteModuleTheUpstreamDoesNotServe(t *testing.T) {
	// The source boundary reports the upstream's 404 as kernel.ErrNotFound,
	// and a remote source answers it as a 404.
	for name, tune := range map[string]func(*metadataSource){
		"not found": func(s *metadataSource) {
			s.moduleAt = func(source.Resource) (string, bool) { return "", true }
		},
		"a 404 answer": func(s *metadataSource) { s.moduleReadError = apierror.New(404, "Not Found") },
	} {
		t.Run(name, func(t *testing.T) {
			f := remoteFixture(t, remotePageBody(t, upstreamRelease(t, "1.0.0", nil)), tune)
			wantError(t, f.do("GET", base+"/v3/modules/acme-widget", "admin"), 404,
				"Puppet module not found")
			wantBody(t, f.do("GET", base+"/v3/modules?query=acme-widget", "admin"),
				listJSON("/v3/modules?query=acme-widget", 20, 0, 0))
			wantBody(t, f.do("GET", base+"/v3/releases?module=acme-widget", "admin"),
				listJSON("/v3/releases?module=acme-widget", 20, 0, 1,
					remoteRecordJSON("1.0.0", linkedMetadata("1.0.0"), `[]`)))
		})
	}
}

// A remote asks its upstream for 20 releases a page without the fields no
// record answers (#1300), and for the module's record without its current
// release and release list, under the release pages' authority: Read on
// the module folder, the download policy at .puppet/<m>/releases.json for
// v3/modules/<m>, and the blackout alone for the search. The release list
// reads no module record.
func TestUnitRemoteAsksTheUpstream(t *testing.T) {
	var pages, modules []source.Resource
	f := remoteFixture(t, "", func(s *metadataSource) {
		page := remotePageBody(t, upstreamRelease(t, "1.0.0", nil))
		s.bodyAt = func(r source.Resource) string { pages = append(pages, r); return page }
		s.moduleAt = func(r source.Resource) (string, bool) {
			modules = append(modules, r)
			return `{"slug":"acme-widget"}`, false
		}
	})
	if r := f.do(
		"GET",
		base+"/v3/releases?module=acme-widget",
		"admin",
	); r.Code != 200 ||
		len(modules) != 0 {
		t.Fatalf("release list: %d, %d module reads", r.Code, len(modules))
	}
	const wantPage = "https://example.com/puppet/v3/releases?exclude_fields=reference+tasks+plans+pdk+" +
		"validation_score+malware_scan+deleted_for&limit=20&module=acme-widget&offset=0&sort_by=version"
	if len(pages) != 1 || pages[0].ExternalURL != wantPage {
		t.Fatalf("release pages asked: %+v", pages)
	}
	for _, route := range []string{"/v3/modules/acme-widget", "/v3/modules?query=acme-widget"} {
		if r := f.do("GET", base+route, "admin"); r.Code != 200 {
			t.Fatalf("%s: %d %s", route, r.Code, r.Body.String())
		}
	}
	if len(modules) != 2 {
		t.Fatalf("module reads: %d", len(modules))
	}
	for i, want := range []struct {
		policy  string
		blacked bool
	}{{".puppet/acme-widget/releases.json", false}, {"", true}} {
		got := modules[i]
		fields := []any{
			got.ExternalURL, got.Path, got.Variant, got.Kind, got.PermissionFolder,
			got.PermissionPath, got.NoCrossOriginAuth, got.PolicyPath, got.BlackoutOnly,
		}
		wantFields := []any{
			"https://example.com/puppet/v3/modules/acme-widget?exclude_fields=current_release+releases",
			"v3/modules/acme-widget",
			"puppet-forge-v3-module-v1",
			source.ResourceKindMetadata,
			true,
			"acme-widget",
			true,
			want.policy,
			want.blacked,
		}
		if !slices.Equal(fields, wantFields) {
			t.Fatalf("module read %d: %+v", i, got)
		}
	}
}

// An upstream module record that names another module, is not one JSON
// object, is larger than any metadata a remote reads, or was read from
// anywhere but the module's record fails as invalid upstream metadata does,
// and the read's own failures answer as a release page's do.
func TestUnitRemoteModuleRecordFailures(t *testing.T) {
	record := func(body string) func(*metadataSource) {
		return func(s *metadataSource) {
			s.moduleAt = func(source.Resource) (string, bool) { return body, false }
		}
	}
	for _, tc := range []struct {
		name    string
		tune    func(*metadataSource)
		status  int
		message string
	}{
		{"another module", record(`{"slug":"acme-other"}`), 502, "invalid upstream Puppet metadata"},
		{"no slug", record(`{"name":"widget"}`), 502, "invalid upstream Puppet metadata"},
		{"not an object", record(`[1]`), 502, "invalid upstream Puppet metadata"},
		{"empty", record(``), 502, "invalid upstream Puppet metadata"},
		{
			"too large", record(`{"slug":"acme-widget","x":"` + strings.Repeat("x", 16<<20) + `"}`),
			502, "invalid upstream Puppet metadata",
		},
		{
			"another record", func(s *metadataSource) {
				s.moduleBase = "https://example.com/puppet/v3/modules/acme-other"
			}, 502, "invalid upstream Puppet metadata",
		},
		{
			"outside the registry", func(s *metadataSource) {
				s.moduleBase = "https://elsewhere.example/puppet/v3/modules/acme-widget"
			}, 502, "invalid upstream Puppet metadata",
		},
		{
			"outside the registry, unvalidated", func(s *metadataSource) {
				s.skipMetadataValidation = true
				s.moduleBase = "https://elsewhere.example/puppet/v3/modules/acme-widget"
			}, 502, "invalid upstream Puppet metadata",
		},
		{
			"another module, unvalidated", func(s *metadataSource) {
				record(`{"slug":"acme-other"}`)(s)
				s.skipMetadataValidation = true
			}, 502, "invalid upstream Puppet metadata",
		},
		{
			"upstream 503", func(s *metadataSource) {
				s.moduleReadError = &source.HTTPError{Status: 503}
			}, 503, "Service Unavailable",
		},
		{
			"no revalidation", func(s *metadataSource) { s.moduleNoRevalidate = true },
			502, "Bad Gateway",
		},
		{
			"stale", func(s *metadataSource) { s.moduleRevalidateError = source.ErrSuperseded },
			503, "Service Unavailable",
		},
		{
			"no body", func(s *metadataSource) { s.moduleNoStream = true },
			502, "invalid upstream Puppet metadata",
		},
		{
			// The download policy's 404 is the remote's own refusal, not
			// the upstream's missing record.
			"refused", func(s *metadataSource) {
				s.moduleReadError = format.Rejectf(404, "refused")
			}, 404, "refused",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := remoteFixture(t, remotePageBody(t, upstreamRelease(t, "1.0.0", nil)), tc.tune)
			for _, route := range []string{"/v3/modules/acme-widget", "/v3/modules?query=acme-widget"} {
				wantError(t, f.do("GET", base+route, "admin"), tc.status, tc.message)
			}
		})
	}
}
