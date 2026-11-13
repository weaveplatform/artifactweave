package puppet_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// A remote serves the archive of a release its Forge's release list leaves
// out from the address the Forge's own record of that release names, the
// record it answers for v3/releases/<slug> (#1344), so r10k, which reads
// that record and then fetches its file_uri, installs the release (#1369)
// [I]. The archive is refused with 502 when it does not match the record,
// and Read is checked before the Forge is asked.
func TestAccPuppetRemoteArchiveOfAnUpstreamOnlyRelease(t *testing.T) {
	const archive = "/artifactory/api/puppet/remote/v3/files/acme-pe-1.0.0.tar.gz"
	up := newGroupedForge(t)
	w := groupedRemote(t, up, nil)
	r := w.request("GET", archive, "reader", "")
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), up.archives["acme-pe-1.0.0"]) {
		t.Fatalf("archive: %d %s", r.Code, r.Body.String())
	}
	if asked := up.requests(); !slices.Contains(asked, "/v3/files/acme-pe-1.0.0.tar.gz") {
		t.Fatalf("the Forge was asked %q", asked)
	}
	r = w.request("HEAD", archive, "reader", "")
	if r.Code != 200 || r.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %s", r.Code, r.Body.String())
	}
	remoteStatus(t, w.request("GET", archive, "denied", ""), 403)
	if asked := up.requests(); len(asked) != 0 {
		t.Fatalf("a denied caller reached the Forge: %q", asked)
	}

	other := newGroupedForge(t)
	other.files["acme-pe-1.0.0"] = other.archives["acme-widget-1.0.0"]
	w = groupedRemote(t, other, nil)
	r = w.request("GET", archive, "reader", "")
	if want := `{"errors":[{"message":"Bad Gateway","status":502}]}` + "\n"; r.Code != 502 ||
		r.Body.String() != want {
		t.Fatalf("mismatched archive: %d %s\nwant 502 %s", r.Code, r.Body.String(), want)
	}
}

// A virtual repository over that remote answers v3/releases/<slug> and the
// archive of the release from the remote member's Forge record, as the
// remote answers them, once every member's release list leaves it out
// (#1369) [I]; a local member's release of the same version is listed, and
// so is the one answered.
func TestAccPuppetVirtualReleaseOnlyAnUpstreamRecordHolds(t *testing.T) {
	const v = "/artifactory/api/puppet/pvirtual"
	up := newGroupedForge(t)
	w := groupedRemote(t, up, nil)
	virtual := *repo.Defaults(&repo.Config{
		Key: "pvirtual", Class: repo.Virtual, PackageType: repo.Puppet,
		Repositories: []string{"plocal", "remote"},
	})
	if err := w.rt.Store.Repos().Create(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	want := encodeAnswer(t, up.want("acme-pe-1.0.0"))
	r := w.request("GET", v+"/v3/releases/acme-pe-1.0.0", "admin", "")
	if r.Code != 200 || r.Body.String() != want {
		t.Fatalf("release: %d %s\nwant 200 %s", r.Code, r.Body.String(), want)
	}
	r = w.request("GET", v+"/v3/files/acme-pe-1.0.0.tar.gz", "admin", "")
	if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), up.archives["acme-pe-1.0.0"]) {
		t.Fatalf("archive: %d %s", r.Code, r.Body.String())
	}
	remoteStatus(t, w.request("GET", v+"/v3/releases/acme-pe-9.9.9", "admin", ""), 404)

	local := groupArchive(t, "acme-pe", "1.0.0")
	remoteStatus(t, w.request("PUT", "/artifactory/plocal/acme-pe/acme-pe-1.0.0.tar.gz", "admin",
		string(local)), 201)
	r = w.request("GET", v+"/v3/releases/acme-pe-1.0.0", "admin", "")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"readme":null`) ||
		!strings.Contains(r.Body.String(), `"slug":"acme-pe-1.0.0"`) {
		t.Fatalf("local member's release: %d %s", r.Code, r.Body.String())
	}
}

// A remote answers v3/modules/<m> for a module its Forge's release list
// leaves out, a Puppet Enterprise module's here, from the Forge's own
// record of the module, with the releases that record lists and the newest
// one's own record as its current release, as r10k reads it before it
// installs (#1370) [I]. RTFACT-19130 names a 6.8.6 remote's 404 here as
// the fault. The release list and the search still answer from the Forge's
// release list, and Read is checked before the Forge is asked.
func TestAccPuppetRemoteModuleTheForgeListLeavesOut(t *testing.T) {
	const remote = "/artifactory/api/puppet/remote"
	up := newGroupedForge(t)
	w := groupedRemote(t, up, nil)
	current := strings.TrimSuffix(encodeAnswer(t, up.want("acme-pe-1.0.0")), "\n")
	want := `{"uri":"/v3/modules/acme-pe","slug":"acme-pe","name":"pe","downloads":0,` +
		`"created_at":null,"updated_at":null,"supported":false,"endorsement":null,` +
		`"module_group":"pe_only","owner":{"uri":"/v3/users/acme","slug":"acme",` +
		`"username":"acme","gravatar_id":null},"current_release":` + current + `,` +
		`"releases":[{"uri":"/v3/releases/acme-pe-1.0.0","version":"1.0.0"}],` +
		`"homepage_url":null,"issues_url":null}` + "\n"
	r := w.request("GET", remote+"/v3/modules/acme-pe", "reader", "")
	if r.Code != 200 || r.Body.String() != want {
		t.Fatalf("module: %d %s\nwant 200 %s", r.Code, r.Body.String(), want)
	}
	asked := up.requests()
	for _, path := range []string{
		"/v3/modules/acme-pe?exclude_fields=current_release",
		"/v3/releases/acme-pe-1.0.0?exclude_fields=reference+tasks+plans+pdk+validation_score+" +
			"malware_scan+deleted_for",
	} {
		if !slices.Contains(asked, path) {
			t.Fatalf("the Forge was not asked %s: %q", path, asked)
		}
	}
	r = w.request("GET", remote+"/v3/modules?query=acme-pe", "reader", "")
	if empty := encodeAnswer(t, struct {
		Pagination pageObject `json:"pagination"`
		Results    []any      `json:"results"`
	}{newPageObject("/v3/modules", "query=acme-pe", 20, 0, 0), []any{}}); r.Code != 200 ||
		r.Body.String() != empty {
		t.Fatalf("search: %d %s\nwant 200 %s", r.Code, r.Body.String(), empty)
	}
	up.requests()
	remoteStatus(t, w.request("GET", remote+"/v3/modules/acme-pe", "denied", ""), 403)
	if asked := up.requests(); len(asked) != 0 {
		t.Fatalf("a denied caller reached the Forge: %q", asked)
	}
	remoteStatus(t, w.request("GET", remote+"/v3/modules/acme-none", "reader", ""), 404)
}
