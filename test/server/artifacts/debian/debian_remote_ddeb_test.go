package debian_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
)

// TestAccDebianRemoteCachesADebugSymbolPackage (#1352): a remote Debian
// repository serves and caches a .ddeb from its upstream, where it asked
// the upstream and then answered 502 "Bad Gateway" because the cache write
// was refused as a debug package. ddebSupported is "Enable indexing with
// debug symbols (.ddeb)" in JFrog's Debian guide: it governs indexing,
// which a remote does not do, and no source says 7.x refuses a remote file
// by type [I]. A reader asks the upstream once, and an administrator, the
// cache itself and a virtual over the remote are served the cached bytes;
// a user without read permission is refused 403 and an anonymous caller
// 401. A local or federated repository with ddebSupported unset still
// refuses the .ddeb with 400, and one with it on stores it.
func TestAccDebianRemoteCachesADebugSymbolPackage(t *testing.T) {
	const ddeb = "pool/main/a/alpha/alpha-dbgsym_1.0_amd64.ddeb"
	body := repopath.Read(t, "internal/artifacts/ecosystem/debian/testdata/alpha_1.0_amd64_0.deb")
	w, up := queryRemote(t, map[string][]byte{ddeb: body})
	on := true
	for _, c := range []repo.Config{
		{Key: "deb-local", Class: repo.Local},
		{Key: "deb-fed", Class: repo.Federated},
		{Key: "deb-ddeb", Class: repo.Local, DdebSupported: &on},
	} {
		c.PackageType = repo.Debian
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		key, caller string
		asked       []string
	}{
		{"remote", "denied", nil},
		{"remote", "", nil},
		{"remote", "reader", []string{"/" + ddeb}},
		{"remote", "admin", nil},
		{"remote-cache", "admin", nil},
		{"deb-virtual", "reader", nil},
	} {
		r := w.Request(http.MethodGet, "/artifactory/"+tc.key+"/"+ddeb, tc.caller, "")
		switch tc.caller {
		case "denied":
			queryRefusal(t, r, http.StatusForbidden, "Forbidden")
		case "":
			queryRefusal(t, r, http.StatusUnauthorized, "Authentication is required")
		default:
			if r.Code != http.StatusOK || r.Body.String() != string(body) {
				t.Errorf("%s as %s answered %d %q, want 200 and the upstream's bytes",
					tc.key, tc.caller, r.Code, strings.TrimSpace(r.Body.String()))
			}
		}
		if got := up.take(); !slices.Equal(got, tc.asked) {
			t.Errorf(
				"%s as %q asked the upstream for %q, want %q",
				tc.key,
				tc.caller,
				got,
				tc.asked,
			)
		}
	}
	for _, key := range []string{"deb-local", "deb-fed"} {
		queryRefusal(
			t,
			w.Request(http.MethodPut, "/artifactory/"+key+"/"+ddeb, "admin", string(body)),
			http.StatusBadRequest,
			"Debian debug packages are disabled",
		)
	}
	if r := w.Request(
		http.MethodPut,
		"/artifactory/deb-ddeb/"+ddeb,
		"admin",
		string(body),
	); r.Code != http.StatusCreated {
		t.Errorf("deploy with ddebSupported on answered %d %s, want 201", r.Code, r.Body)
	}
}
