package debian_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccDebianRepositoryConfigurationReadsDdebSupported (#1372): a Debian
// remote repository takes ddebSupported, JFrog's "Enable indexing with
// debug symbols (.ddeb)", which the Remote Repositories page lists among a
// Debian remote's additional settings, on create and update, and Get
// Repository Configuration returns it as set. A Debian repository of any
// class that never set ddebSupported or debianTrivialLayout reads them as
// false in the v1 read, where it left them out: 7.161.26's
// RepositoryConfigurationBase defaults both to false, RTFACT-30304 shows
// "ddebSupported" : false in a Debian remote's configuration, and the
// Terraform provider's sample read of a Debian remote gives both false. The
// v2 read keeps ddebSupported for every Debian class and debianTrivialLayout
// for a local or federated one only, as that model's @IncludeTypeSpecific
// marks them, so a remote's or virtual's v2 read leaves debianTrivialLayout
// out, set or not. A remote of another package type reads both as false in
// the v1 read whatever it was sent, as only a Debian repository's
// configuration sets them in 7.161.26
// (LocalRepositoryConfigurationImpl.setDebianConfigIfNeeded) and the
// provider's sample read of an RPM remote (samples/repository_remote/rpm.json)
// gives both false, and leaves both out of the v2 read (#1479).
func TestAccDebianRepositoryConfigurationReadsDdebSupported(t *testing.T) {
	w := harness.NewRemoteWorld(t)
	read := func(path string) map[string]any {
		t.Helper()
		r := w.Request(http.MethodGet, path, "admin", "")
		var got map[string]any
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &got) != nil {
			t.Fatalf("GET %s answered %d %s", path, r.Code, r.Body)
		}
		return got
	}
	// want checks key's v1 and v2 reads: a nil value is a field left out.
	want := func(key string, v1, v2 map[string]any) {
		t.Helper()
		for path, fields := range map[string]map[string]any{
			"/artifactory/api/repositories/" + key:    v1,
			"/artifactory/api/v2/repositories/" + key: v2,
		} {
			got := read(path)
			for field, value := range fields {
				v, ok := got[field]
				switch {
				case value == nil && ok:
					t.Errorf("%s gives %s %v, want it left out", path, field, v)
				case value != nil && v != value:
					t.Errorf("%s gives %s %v (present %t), want %v", path, field, v, ok, value)
				}
			}
		}
	}
	write := func(method, key, body string) {
		t.Helper()
		r := w.Request(method, "/artifactory/api/repositories/"+key, "admin", body,
			"Content-Type", "application/json")
		if r.Code != http.StatusOK {
			t.Fatalf("%s %s answered %d %s", method, key, r.Code, r.Body)
		}
	}
	unset := map[string]any{"ddebSupported": false, "debianTrivialLayout": false}
	unsetV2 := map[string]any{"ddebSupported": false, "debianTrivialLayout": nil}
	for _, tc := range []struct {
		key, body string
		v2        map[string]any
	}{
		{"deb-local", `{"rclass":"local","packageType":"debian"}`, unset},
		{"deb-fed", `{"rclass":"federated","packageType":"debian"}`, unset},
		{"deb-remote", `{"rclass":"remote","packageType":"debian","url":"https://deb.example"}`, unsetV2},
		{"deb-virtual", `{"rclass":"virtual","packageType":"debian","repositories":["deb-remote"]}`, unsetV2},
	} {
		write(http.MethodPut, tc.key, tc.body)
		want(tc.key, unset, tc.v2)
	}
	write(http.MethodPost, "deb-remote", `{"ddebSupported":true}`)
	want("deb-remote",
		map[string]any{"ddebSupported": true, "debianTrivialLayout": false},
		map[string]any{"ddebSupported": true, "debianTrivialLayout": nil})
	write(http.MethodPost, "deb-remote", `{"ddebSupported":false}`)
	want("deb-remote", unset, unsetV2)
	write(http.MethodPut, "deb-remote-on", `{"rclass":"remote","packageType":"debian",`+
		`"url":"https://deb.example","ddebSupported":true,"debianTrivialLayout":true}`)
	want("deb-remote-on",
		map[string]any{"ddebSupported": true, "debianTrivialLayout": true},
		map[string]any{"ddebSupported": true, "debianTrivialLayout": nil})

	left := map[string]any{"ddebSupported": nil, "debianTrivialLayout": nil}
	write(
		http.MethodPut,
		"rpm-remote",
		`{"rclass":"remote","packageType":"rpm","url":"https://rpm.example"}`,
	)
	want("rpm-remote", unset, left)
	write(http.MethodPut, "rpm-remote-on", `{"rclass":"remote","packageType":"rpm",`+
		`"url":"https://rpm.example","ddebSupported":true,"debianTrivialLayout":true}`)
	want("rpm-remote-on", unset, left)
}
