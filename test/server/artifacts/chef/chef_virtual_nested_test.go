package chef_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// cookbookArchive is a cookbook tarball of name at version.
func cookbookArchive(t *testing.T, name, version string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	meta := fmt.Sprintf(`{"name":%q,"version":%q,"dependencies":{},"platforms":{}}`, name, version)
	if err := tw.WriteHeader(&tar.Header{Name: name + "/metadata.json", Mode: 0o644, Size: int64(len(meta))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(meta)); err != nil {
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

// A virtual repository over a remote and a nested virtual repository over a
// local walks each real member once (decision 0008, R2): cvirtual lists the
// remote, inner, itself and inner again, and inner lists the local and
// itself. Its cookbook record lists the versions of both members, with no
// 400 for the cycle or the duplicate, as main answered.
func TestAccChefVirtualOverARemoteAndANestedVirtual(t *testing.T) {
	up := newCappedSupermarket(t, 1, 100, false)
	w := chefRemote(t, up)
	for _, c := range []repo.Config{
		{Key: "clocal", Class: repo.Local, PackageType: repo.Chef},
		{Key: "inner", Class: repo.Virtual, PackageType: repo.Chef, Repositories: []string{"clocal", "inner"}},
		{Key: "cvirtual", Class: repo.Virtual, PackageType: repo.Chef, Repositories: []string{w.Repo.Key, "inner", "cvirtual", "inner"}},
	} {
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	name := up.names[0]
	if r := w.Request(http.MethodPut, "/artifactory/clocal/"+name+"/"+name+"-2.0.0.tar.gz", "admin",
		string(cookbookArchive(t, name, "2.0.0"))); r.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", r.Code, r.Body)
	}
	r := w.Request(http.MethodGet, "/artifactory/api/chef/cvirtual/api/v1/cookbooks/"+name, "admin", "")
	var doc struct {
		Versions []string `json:"versions"`
	}
	if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &doc) != nil {
		t.Fatalf("cookbook: %d %s", r.Code, r.Body)
	}
	want := []string{
		"http://example.com/artifactory/api/chef/cvirtual/api/v1/cookbooks/" + name + "/versions/2_0_0",
		"http://example.com/artifactory/api/chef/cvirtual/api/v1/cookbooks/" + name + "/versions/1_0_0",
	}
	if !slices.Equal(doc.Versions, want) {
		t.Fatalf("versions %v, want %v", doc.Versions, want)
	}
	for _, version := range []string{"2.0.0", "1.0.0"} {
		if r := w.Request(http.MethodGet, "/artifactory/api/chef/cvirtual/api/v1/cookbooks/"+name+
			"/versions/"+version, "admin", ""); r.Code != http.StatusOK {
			t.Fatalf("version %s: %d %s", version, r.Code, r.Body)
		}
	}
}
