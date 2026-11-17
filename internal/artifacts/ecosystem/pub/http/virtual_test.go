package pubhttp_test

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// wantNoDeployTarget checks rec is R6's answer to a publish to the virtual
// repository pub-local without a local or federated deployment repository.
func wantNoDeployTarget(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	const want = "No local repository was configured as local deployment repository " +
		"for the (pub-local) virtual repository."
	if status, message := pubError(t, rec); status != http.StatusMethodNotAllowed || message != want {
		t.Fatalf("no deployment repository: %d %q", status, message)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("no deployment repository: Allow %q", allow)
	}
}

// publishAs runs the three steps of dart pub publish to repository key as
// user and returns the answer of the last step taken, with the URLs each
// step handed the client.
func (f *fixture) publishAs(key, user string, data []byte) (*httptest.ResponseRecorder, []string) {
	f.t.Helper()
	at := "/artifactory/api/pub/" + key
	rec := f.do(http.MethodGet, at+"/api/packages/versions/new", user, "", nil)
	if rec.Code != http.StatusOK {
		return rec, nil
	}
	urls := []string{rec.Body.String()}
	body, contentType := upload(f.t, data)
	rec = f.do(http.MethodPost, at+"/api/packages/versions/new", user, contentType, body)
	if rec.Code != http.StatusNoContent {
		return rec, urls
	}
	urls = append(urls, rec.Header().Get("Location"))
	return f.do(http.MethodGet, rec.Header().Get("Location"), user, "", nil), urls
}

// TestUnitVirtualPublishLandsInTheDeploymentRepository: dart pub publish to
// a virtual repository stores the archive in its default deployment
// repository, local or federated, asking Write of that repository alone,
// while each step hands the client the virtual repository's URLs
// (RTFACT-31048; decision 0008, R6). Without one every step answers R6's
// 405 with Allow: GET.
func TestUnitVirtualPublishLandsInTheDeploymentRepository(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	f.createRepo(repo.Config{Key: "pub-a", Class: repo.Local, PackageType: repo.Pub})
	f.createRepo(repo.Config{Key: "pub-fed", Class: repo.Federated, PackageType: repo.Pub})
	f.c.DefaultDeploymentRepo = "pub-a"
	f.setMembers("pub-a", "pub-fed")
	f.grant("deployer", accessapi.ActionRead|accessapi.ActionWrite,
		map[string][]string{"pub-a": {"**"}})
	f.grant("virtual-writer", accessapi.ActionRead|accessapi.ActionWrite,
		map[string][]string{f.c.Key: {"**"}, "pub-fed": {"**"}})

	rec, urls := f.publishAs(f.c.Key, "deployer", archive(t, "hello_pub", "1.2.3"))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"success"`) {
		t.Fatalf("publish through the virtual repository: %d %s", rec.Code, rec.Body)
	}
	for _, u := range urls {
		if !strings.Contains(u, "/api/pub/pub-local/api/packages/versions/new") {
			t.Fatalf("a step's URL leaves the virtual repository: %s", u)
		}
	}
	if _, err := f.db.Items().Get(t.Context(), "pub-a", false,
		"hello_pub/hello_pub-1.2.3.tar.gz"); err != nil {
		t.Fatalf("archive in the deployment repository: %v", err)
	}
	if _, listed := f.versionsOf(f.c.Key, "hello_pub", "deployer"); !slices.Equal(
		listed, []string{"1.2.3"},
	) {
		t.Fatalf("published version through the virtual repository: %v", listed)
	}
	rec, _ = f.publishAs(f.c.Key, "virtual-writer", archive(t, "hello_pub", "1.3.0"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Write on the virtual repository alone: %d %s", rec.Code, rec.Body)
	}

	f.c.DefaultDeploymentRepo = "pub-fed"
	f.setMembers("pub-a", "pub-fed")
	rec, _ = f.publishAs(f.c.Key, "virtual-writer", archive(t, "hello_pub", "1.3.0"))
	if rec.Code != http.StatusOK {
		t.Fatalf("publish to a federated deployment repository: %d %s", rec.Code, rec.Body)
	}
	if _, err := f.db.Items().Get(t.Context(), "pub-fed", false,
		"hello_pub/hello_pub-1.3.0.tar.gz"); err != nil {
		t.Fatalf("archive in the federated deployment repository: %v", err)
	}

	f.c.DefaultDeploymentRepo = ""
	f.setMembers("pub-a", "pub-fed")
	for _, step := range []struct{ method, path string }{
		{http.MethodGet, base + "/api/packages/versions/new"},
		{http.MethodPost, base + "/api/packages/versions/new"},
		{http.MethodGet, base + "/api/packages/versions/new/complete"},
	} {
		wantNoDeployTarget(t, f.do(step.method, step.path, "deployer", "", nil))
	}
	if _, err := f.db.Items().Get(t.Context(), f.c.Key, false,
		"hello_pub/hello_pub-1.2.3.tar.gz"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("archive stored in the virtual repository: %v", err)
	}
}

// TestUnitVirtualResolvesForMemberReaders: a virtual repository over
// [local, nested virtual -> local] walks the nested one, skipping it where
// it lists the outer one back or itself (R2). A caller with Read on the
// members alone resolves through it (R3); one who may read one member sees
// that member's versions only, the other's archive not found; and one who
// may read no member finds no package.
func TestUnitVirtualResolvesForMemberReaders(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	f.createRepo(repo.Config{Key: "pub-a", Class: repo.Local, PackageType: repo.Pub})
	f.createRepo(repo.Config{Key: "pub-b", Class: repo.Local, PackageType: repo.Pub})
	f.createRepo(repo.Config{
		Key: "pub-inner", Class: repo.Virtual, PackageType: repo.Pub,
		Repositories: []string{"pub-b", "pub-local", "pub-inner"},
	})
	fromB := archive(t, "hello_pub", "2.0.0")
	f.publish("pub-a", archive(t, "hello_pub", "1.0.0"))
	f.publish("pub-b", fromB)
	f.setMembers("pub-local", "pub-a", "pub-inner")
	f.grant("members", accessapi.ActionRead, map[string][]string{"pub-a": {"**"}, "pub-b": {"**"}})
	f.grant("only-a", accessapi.ActionRead, map[string][]string{"pub-a": {"**"}})
	for _, user := range []string{"admin", "members"} {
		if _, listed := f.versionsOf(f.c.Key, "hello_pub", user); !slices.Equal(
			listed, []string{"2.0.0", "1.0.0"},
		) {
			t.Fatalf("versions as %s: %v", user, listed)
		}
		rec := f.do(http.MethodGet, base+"/packages/hello_pub/versions/2.0.0.tar.gz", user, "", nil)
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), fromB) {
			t.Fatalf("nested member's archive as %s: %d %s", user, rec.Code, rec.Body)
		}
		rec = f.do(http.MethodGet, base+"/api/packages/hello_pub/versions/2.0.0", user, "", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("nested member's version as %s: %d %s", user, rec.Code, rec.Body)
		}
	}
	if _, listed := f.versionsOf("pub-inner", "hello_pub", "members"); !slices.Equal(
		listed, []string{"2.0.0", "1.0.0"},
	) {
		t.Fatalf("versions through the nested repository: %v", listed)
	}
	if _, listed := f.versionsOf(f.c.Key, "hello_pub", "only-a"); !slices.Equal(
		listed, []string{"1.0.0"},
	) {
		t.Fatalf("versions without the second member: %v", listed)
	}
	rec := f.do(http.MethodGet, base+"/packages/hello_pub/versions/2.0.0.tar.gz", "only-a", "", nil)
	if status, message := pubError(t, rec); status != http.StatusNotFound ||
		message != "version not found" {
		t.Fatalf("archive of an unreadable member: %d %q", status, message)
	}
	for path, want := range map[string]string{
		"/api/packages/hello_pub":                   "package not found",
		"/api/packages/hello_pub/versions/1.0.0":    "version not found",
		"/packages/hello_pub/versions/1.0.0.tar.gz": "version not found",
	} {
		rec = f.do(http.MethodGet, base+path, "nobody", "", nil)
		if status, message := pubError(t, rec); status != http.StatusNotFound || message != want {
			t.Fatalf("%s for a caller who reads no member: %d %q", path, status, message)
		}
	}
}
