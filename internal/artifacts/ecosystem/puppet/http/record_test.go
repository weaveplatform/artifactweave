package puppethttp_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

// linkedManifest is the metadata.json of acme-widget at version that
// linkedArchive holds: a dependency requirement with "<" and ">", and the
// project_page and issues_url a module record answers.
func linkedManifest(version string) string {
	return fmt.Sprintf(`{"name":"acme-widget","version":"%s","summary":"widget %s",`+
		`"dependencies":[{"name":"acme/helper","version_requirement":">= 1.0.0 < 2.0.0"}],`+
		`"project_page":"https://example.invalid/widget?tab=a&b","issues_url":"https://example.invalid/issues"}`,
		version, version)
}

// linkedArchive returns an archive of acme-widget at version that holds
// only linkedManifest.
func linkedArchive(t *testing.T, version string) []byte {
	t.Helper()
	manifest := linkedManifest(version)
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "acme-widget-" + version + "/metadata.json", Mode: 0o644,
		Size: int64(len(manifest)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(manifest)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// linkedMetadata is the metadata a release record answers for
// linkedManifest: the manifest's fields, which leave its links out.
func linkedMetadata(version string) string {
	return fmt.Sprintf(`{"name":"acme-widget","version":"%s","summary":"widget %s",`+
		`"dependencies":[{"name":"acme/helper","version_requirement":">= 1.0.0 < 2.0.0"}]}`,
		version, version)
}

// A hosted release answers every field of JFrog's reference and of 7.x's
// answers, on every route that answers it, and so does its module: the
// archive's MD5 (RTFACT-26143), the downloads counted, the item's dates in
// the Forge's form, and the owner's links (JFrog's sample answer for Get
// Puppet Modules). "<", ">" and "&" are written as they are (#1288).
func TestUnitHostedRecordsOnEveryRoute(t *testing.T) {
	f := newFixture(t, repo.Local)
	older := f.deployRelease(f.c, "acme-widget", "1.0.0", linkedArchive(t, "1.0.0"))
	newer := f.deployRelease(f.c, "acme-widget", "1.1.0", linkedArchive(t, "1.1.0"))
	for rel, count := range map[*listRelease]int64{&older: 3, &newer: 4} {
		if err := f.db.Items().PutStats(t.Context(), rel.item.ID,
			artmodel.ItemStats{DownloadCount: count}); err != nil {
			t.Fatal(err)
		}
	}
	release := recordJSON(newer, linkedMetadata("1.1.0"), 4)
	oldRelease := recordJSON(older, linkedMetadata("1.0.0"), 3)
	created := min(forgeDate(older.item.Created), forgeDate(newer.item.Created))
	updated := max(forgeDate(older.item.Updated), forgeDate(newer.item.Updated))
	module := fmt.Sprintf(`{"uri":"/v3/modules/acme-widget","slug":"acme-widget","name":"widget",`+
		`"downloads":7,"created_at":"%s","updated_at":"%s","supported":false,`+
		`"endorsement":null,"module_group":"base","owner":`+hostedOwner+`,`+
		`"current_release":%s,"releases":[`+
		`{"uri":"/v3/releases/acme-widget-1.1.0","version":"1.1.0"},`+
		`{"uri":"/v3/releases/acme-widget-1.0.0","version":"1.0.0"}],`+
		`"homepage_url":"https://example.invalid/widget?tab=a&b",`+
		`"issues_url":"https://example.invalid/issues"}`, created, updated, release)
	f.create(repo.Config{
		Key: "virtual", Class: repo.Virtual, PackageType: repo.Puppet,
		Repositories: []string{f.c.Key},
	})
	for _, prefix := range []string{base, "/artifactory/api/puppet/virtual"} {
		wantBody(t, f.do("GET", prefix+"/v3/releases/acme-widget-1.1.0", "admin"), release+"\n")
		wantBody(t, f.do("GET", prefix+"/v3/modules/acme-widget", "admin"), module+"\n")
		wantBody(t, f.do("GET", prefix+"/v3/releases?module=acme-widget", "admin"),
			listJSON("/v3/releases?module=acme-widget", 20, 0, 2, release, oldRelease))
		wantBody(t, f.do("GET", prefix+"/v3/modules?query=acme-widget", "admin"),
			listJSON("/v3/modules?query=acme-widget", 20, 0, 1, module))
	}
	wantBody(t, f.do("GET", base+"/v3/releases", "admin"),
		listJSON("/v3/releases", 20, 0, 2, release, oldRelease))
	wantBody(t, f.do("GET", base+"/v3/modules", "admin"), listJSON("/v3/modules", 20, 0, 1, module))
}

// A release whose statistics cannot be read fails rather than answering a
// count it does not have.
func TestUnitHostedRecordStatisticsFailure(t *testing.T) {
	var s *statsStore
	f := newFixtureStore(t, repo.Local, nil,
		func(st pkgroute.Store) pkgroute.Store { s = &statsStore{Store: st}; return s })
	f.deployRelease(f.c, "acme-widget", "1.0.0", linkedArchive(t, "1.0.0"))
	s.fail = true
	wantError(t, f.do("GET", base+"/v3/releases/acme-widget-1.0.0", "admin"), 500, "internal error")
}

// statsStore fails every statistics read once fail is set.
type statsStore struct {
	pkgroute.Store
	fail bool
}

func (s *statsStore) Items() port.ItemStore { return statsItems{s.Store.Items(), s} }

type statsItems struct {
	port.ItemStore
	s *statsStore
}

func (i statsItems) Stats(ctx context.Context, id int64) (artmodel.ItemStats, error) {
	if i.s.fail {
		return artmodel.ItemStats{}, context.Canceled
	}
	return i.ItemStore.Stats(ctx, id)
}

// upstreamRelease returns a Forge release record of acme-widget at version
// whose metadata is linkedManifest's, with extra's fields beside it.
func upstreamRelease(t *testing.T, version string, extra map[string]any) map[string]any {
	t.Helper()
	out := map[string]any{
		"version":     version,
		"file_uri":    "/puppet/v3/files/acme-widget-" + version + ".tar.gz",
		"file_sha256": strings.Repeat("a", 64),
		"file_size":   100,
		"metadata":    json.RawMessage(linkedManifest(version)),
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// A remote relays the upstream's values of a release record's fields, as
// 7.x relays the Forge's (RTFACT-19130); a value of another type answers
// as a hosted release's would, and the page stays valid. Its module record
// takes the module-wide fields from the releases [I] (#1288).
func TestUnitRemoteRecordsRelayTheUpstream(t *testing.T) {
	newer := upstreamRelease(t, "1.1.0", map[string]any{
		"uri": "/v3/releases/acme-widget-1.1.0", "slug": "acme-widget-1.1.0",
		"module": map[string]any{
			"uri": "/v3/modules/acme-widget", "slug": "acme-widget", "name": "widget",
			"deprecated_at": "2026-01-02 03:04:05 -0700",
			"owner":         map[string]any{"username": "acme", "gravatar_id": "fdd009b7"},
		},
		"supported": true, "file_md5": "0acb03237d306753cad5c68352c7488c", "downloads": 42,
		"readme": "# Widget", "changelog": "## 1.1.0", "license": "Apache License",
		"created_at": "2026-10-27 04:20:14 -0700", "updated_at": "2026-10-28 04:20:14 -0700",
		"deleted_at": nil, "pdk": true,
	})
	older := upstreamRelease(t, "1.0.0", map[string]any{
		"module": "acme-widget", "supported": "yes", "file_md5": "NOT-MD5", "downloads": -5,
		"readme": 5, "created_at": "2011-05-24 18:34:58 -0700", "updated_at": "yesterday",
		"deleted_at": "2012-01-01 00:00:00 +0000",
	})
	page, err := json.Marshal(map[string]any{
		"results":    []any{newer, older},
		"pagination": map[string]any{"limit": 20, "offset": 0, "total": 2, "next": nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	src := metadataSource{
		body:  string(page),
		check: func(context.Context, string) error { return nil },
	}
	f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src })
	const remoteOwner = `{"uri":"/v3/users/acme","slug":"acme","username":"acme","gravatar_id":"fdd009b7"}`
	relayed := `{"uri":"/v3/releases/acme-widget-1.1.0","slug":"acme-widget-1.1.0",` +
		`"module":{"uri":"/v3/modules/acme-widget","slug":"acme-widget","name":"widget",` +
		`"owner":` + remoteOwner + `,"deprecated_at":"2026-01-02 03:04:05 -0700"},` +
		`"version":"1.1.0","metadata":` + linkedMetadata("1.1.0") + `,"tags":[],"supported":true,` +
		`"file_uri":"/v3/files/acme-widget-1.1.0.tar.gz","file_size":100,` +
		`"file_md5":"0acb03237d306753cad5c68352c7488c","file_sha256":"` + strings.Repeat("a", 64) +
		`","downloads":42,"readme":"# Widget","changelog":"## 1.1.0","license":"Apache License",` +
		`"created_at":"2026-10-27 04:20:14 -0700","updated_at":"2026-10-28 04:20:14 -0700",` +
		`"deleted_at":null}`
	defaulted := `{"uri":"/v3/releases/acme-widget-1.0.0","slug":"acme-widget-1.0.0",` +
		`"module":{"uri":"/v3/modules/acme-widget","slug":"acme-widget","name":"widget",` +
		`"owner":` + hostedOwner + `,"deprecated_at":null},` +
		`"version":"1.0.0","metadata":` + linkedMetadata("1.0.0") + `,"tags":[],"supported":false,` +
		`"file_uri":"/v3/files/acme-widget-1.0.0.tar.gz","file_size":100,` +
		`"file_md5":null,"file_sha256":"` + strings.Repeat("a", 64) +
		`","downloads":0,"readme":null,"changelog":null,"license":null,` +
		`"created_at":"2011-05-24 18:34:58 -0700","updated_at":"yesterday",` +
		`"deleted_at":"2012-01-01 00:00:00 +0000"}`
	module := `{"uri":"/v3/modules/acme-widget","slug":"acme-widget","name":"widget",` +
		`"downloads":42,"created_at":"2011-05-24 18:34:58 -0700",` +
		`"updated_at":"2026-10-28 04:20:14 -0700","supported":true,"endorsement":null,` +
		`"module_group":"base","owner":` + remoteOwner + `,"current_release":` + relayed + `,` +
		`"releases":[{"uri":"/v3/releases/acme-widget-1.1.0","version":"1.1.0"},` +
		`{"uri":"/v3/releases/acme-widget-1.0.0","version":"1.0.0"}],` +
		`"homepage_url":"https://example.invalid/widget?tab=a&b",` +
		`"issues_url":"https://example.invalid/issues"}`
	wantBody(t, f.do("GET", base+"/v3/releases/acme-widget-1.1.0", "admin"), relayed+"\n")
	wantBody(t, f.do("GET", base+"/v3/releases/acme-widget-1.0.0", "admin"), defaulted+"\n")
	wantBody(t, f.do("GET", base+"/v3/releases?module=acme-widget", "admin"),
		listJSON("/v3/releases?module=acme-widget", 20, 0, 2, relayed, defaulted))
	wantBody(t, f.do("GET", base+"/v3/modules/acme-widget", "admin"), module+"\n")
	wantBody(t, f.do("GET", base+"/v3/modules?query=acme-widget", "admin"),
		listJSON("/v3/modules?query=acme-widget", 20, 0, 1, module))
}

// A module none of whose releases gives a date in the Forge's form answers
// null for its own dates.
func TestUnitRemoteModuleWithoutDates(t *testing.T) {
	page, err := json.Marshal(map[string]any{
		"results":    []any{upstreamRelease(t, "1.0.0", map[string]any{"created_at": 1})},
		"pagination": map[string]any{"offset": 0, "total": 1, "next": nil},
	})
	if err != nil {
		t.Fatal(err)
	}
	src := metadataSource{
		body:  string(page),
		check: func(context.Context, string) error { return nil },
	}
	f := newFixtureWith(t, repo.Remote, func(p *pkgroute.Protocol) { p.Sources[repo.Remote] = src })
	r := f.do("GET", base+"/v3/modules/acme-widget", "admin")
	if r.Code != 200 || !strings.Contains(r.Body.String(),
		`"downloads":0,"created_at":null,"updated_at":null,`) {
		t.Fatalf("module dates: %d %s", r.Code, r.Body.String())
	}
}
