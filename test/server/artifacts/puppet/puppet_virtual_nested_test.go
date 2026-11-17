package puppet_test

import (
	"bytes"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// A virtual repository over a remote and a nested virtual repository over a
// local walks each real member once (decision 0008, R2): pvirtual lists
// the remote, inner, itself and inner again, and inner lists the local and
// itself. It lists the releases of both, serves each archive from the
// member that holds it, and answers no 400 for the cycle or the duplicate,
// as main did.
func TestAccPuppetVirtualOverARemoteAndANestedVirtual(t *testing.T) {
	const v = "/artifactory/api/puppet/pvirtual"
	w := newRemoteWorld(t)
	up := newForge(t, "remote", "1.0.0", "1.1.0")
	w.c.PackageType = repo.Puppet
	w.c.URL = up.URL
	w.update()
	for _, c := range []repo.Config{
		{Key: "plocal", Class: repo.Local, PackageType: repo.Puppet},
		{Key: "inner", Class: repo.Virtual, PackageType: repo.Puppet, Repositories: []string{"plocal", "inner"}},
	} {
		if err := w.rt.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	local, _ := puppetArchive(t, "2.0.0", "local")
	remoteStatus(t, w.request("PUT", "/artifactory/plocal/acme-widget/acme-widget-2.0.0.tar.gz",
		"admin", string(local)), 201)
	puppetVirtual(t, w, w.c.Key, "inner", "pvirtual", "inner")
	got := listed(t, w.request("GET", v+"/v3/releases?module=acme-widget", "admin", ""))
	want := map[string]string{
		"2.0.0": checksum(local), "1.1.0": checksum(up.archives["1.1.0"]),
		"1.0.0": checksum(up.archives["1.0.0"]),
	}
	if len(got) != len(want) {
		t.Fatalf("listing %v, want %v", got, want)
	}
	for version, sum := range want {
		if got[version] != sum {
			t.Fatalf("listing %v, want %v", got, want)
		}
	}
	for version, body := range map[string][]byte{"2.0.0": local, "1.0.0": up.archives["1.0.0"]} {
		r := w.request("GET", v+"/v3/files/acme-widget-"+version+".tar.gz", "admin", "")
		if r.Code != 200 || !bytes.Equal(r.Body.Bytes(), body) {
			t.Fatalf("%s: %d %s", version, r.Code, r.Body.String())
		}
	}
	remoteStatus(t, w.request("GET", v+"/v3/modules/acme-widget", "admin", ""), 200)
}
