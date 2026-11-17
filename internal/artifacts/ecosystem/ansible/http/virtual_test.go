package ansiblehttp_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	ansiblefmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/ansible"
)

const (
	virtualVersions = "/artifactory/api/ansible/all/api/v3/collections/weave/sample/versions/"
	virtualArchive  = "/artifactory/api/ansible/all/api/v3/plugin/ansible/content/published/collections/artifacts/weave-sample-"
)

// createRepo creates the Ansible repository key of class with members.
func (f *fixture) createRepo(key string, class repo.Class, members ...string) repo.Config {
	f.t.Helper()
	c := *repo.Defaults(&repo.Config{
		Key: key, Class: class, PackageType: repo.Ansible, Repositories: members,
	})
	if err := f.db.Repos().Create(f.t.Context(), c); err != nil {
		f.t.Fatal(err)
	}
	return c
}

// deployVersion stores version of the sample collection in c.
func (f *fixture) deployVersion(c repo.Config, version string) {
	f.t.Helper()
	if _, err := f.items.Deploy(f.t.Context(), item.Deploy{
		Target: item.Target{Repo: c, Path: ansiblefmt.Path("weave", "sample", version)},
		Body:   bytes.NewReader(versionArchive(f.t, f.data, version)),
		By:     "admin",
	}); err != nil {
		f.t.Fatal(err)
	}
}

// grantRead lets user read the repositories keys and nothing else: no
// "ANY" target, so the virtual repository is never named.
func (f *fixture) grantRead(name, user string, actions api.Actions, keys ...string) {
	f.t.Helper()
	targets := map[string]accesstest.Target{}
	for _, key := range keys {
		targets[key] = accesstest.Target{Include: []string{"**"}}
	}
	if err := f.db.Permissions().Create(f.t.Context(), accesstest.Permission{
		Name: name,
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{user: actions}},
			Targets: targets,
		}},
	}); err != nil {
		f.t.Fatal(err)
	}
}

// versionsOf is the version list key answers user, which must be 200.
func versionsOf(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	requireStatus(t, w, http.StatusOK)
	var doc struct {
		Data []struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, d := range doc.Data {
		out = append(out, d.Version)
	}
	return out
}

// A virtual repository walks nested virtual members (decision 0008, R2):
// each real member once, a cycle back to itself or to the outer virtual
// repository skipped, and a member reached twice kept at its first place.
// main answered 400 "invalid virtual repository member".
func TestAccGalaxyVirtualWalksNestedMembersOnce(t *testing.T) {
	f := newFixture(t)
	f.deployVersion(f.repo, "1.2.3")
	other := f.createRepo("other", repo.Local)
	f.deployVersion(other, "2.0.0")
	f.createRepo("inner", repo.Virtual, "other", "all", "inner")
	f.createRepo("all", repo.Virtual, "inner", "all", "ansible-local", "inner", "other")
	if got := versionsOf(t, f.do("GET", virtualVersions, nil, "", "")); strings.Join(got, ",") != "2.0.0,1.2.3" {
		t.Fatalf("versions through nested members = %v", got)
	}
	for _, v := range []string{"1.2.3", "2.0.0"} {
		w := f.do("GET", virtualArchive+v+".tar.gz", nil, "", "")
		requireStatus(t, w, http.StatusOK)
		if !bytes.Equal(w.Body.Bytes(), versionArchive(t, f.data, v)) {
			t.Fatalf("archive %s through nested members changed", v)
		}
	}
}

// A virtual repository passes over a remote member whose upstream answers
// 404, one whose upstream fails, with a warning naming it [I], and one the
// caller may not read (decision 0008, R3), and lists the others. A caller
// who may read the members alone, with no "ANY" target, resolves through
// the virtual repository, and one who may read none gets the protocol's
// not-found rather than 403 (R3).
func TestAccGalaxyVirtualPassesOverMissingFailingAndUnreadableMembers(t *testing.T) {
	setup := func(t *testing.T) (*fixture, *fakeupstream.Server) {
		f, up, _, _ := remoteFixture(t)
		f.deployVersion(f.repo, "2.0.0")
		f.createRepo("all", repo.Virtual, "ansible-remote", "ansible-local")
		return f, up
	}
	f, _ := setup(t)
	f.grantRead("members", "reader", api.ActionRead, "ansible-local")
	f.grantRead("nothing", "stranger", api.ActionRead, "elsewhere")
	if got := versionsOf(t, f.do("GET", virtualVersions, nil, "", "reader")); strings.Join(got, ",") != "2.0.0" {
		t.Fatalf("a member-only reader's versions = %v", got)
	}
	requireStatus(t, f.do("GET", virtualArchive+"2.0.0.tar.gz", nil, "", "reader"), http.StatusOK)
	requireStatus(t, f.do("GET", virtualVersions+"2.0.0/", nil, "", "reader"), http.StatusOK)
	for _, path := range []string{virtualVersions + "2.0.0/", virtualArchive + "2.0.0.tar.gz"} {
		w := f.do("GET", path, nil, "", "stranger")
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "Could not find resource") {
			t.Fatalf("%s for a caller who may read no member: %d %s", path, w.Code, w.Body)
		}
	}
	if got := versionsOf(t, f.do("GET", virtualVersions, nil, "", "")); strings.Join(got, ",") != "2.0.0,1.2.3" {
		t.Fatalf("versions = %v", got)
	}
	for _, tc := range []struct {
		name   string
		status int
		warned bool
	}{{"miss", http.StatusNotFound, false}, {"fault", http.StatusInternalServerError, true}} {
		t.Run(tc.name, func(t *testing.T) {
			f, up := setup(t)
			up.Set(remoteList, fakeupstream.Resource{Status: tc.status})
			if got := versionsOf(t, f.do("GET", virtualVersions, nil, "", "")); strings.Join(got, ",") != "2.0.0" {
				t.Fatalf("versions = %v", got)
			}
			logged := strings.Contains(f.logs.String(), "virtual repository passed over a failing member") &&
				strings.Contains(f.logs.String(), "member=ansible-remote") &&
				strings.Contains(f.logs.String(), "virtual=all")
			if logged != tc.warned {
				t.Fatalf("warning logged %v, want %v: %s", logged, tc.warned, f.logs)
			}
		})
	}
}

// publishTo posts the sample collection to the Ansible repository key as
// user, as ansible-galaxy collection publish does.
func (f *fixture) publishTo(key, user string) *httptest.ResponseRecorder {
	f.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	sum := sha256.Sum256(f.data)
	_ = mw.WriteField("sha256", hex.EncodeToString(sum[:]))
	file, err := mw.CreateFormFile("file", "weave-sample-1.2.3.tar.gz")
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := file.Write(f.data); err != nil {
		f.t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		f.t.Fatal(err)
	}
	return f.do(http.MethodPost, "/artifactory/api/ansible/"+key+"/api/v3/artifacts/collections/",
		body.Bytes(), mw.FormDataContentType(), user)
}

// ansible-galaxy collection publish -s .../api/ansible/<virtual> lands in
// the virtual repository's default deployment repository, with Write asked
// there alone, and its import task completes through the virtual repository
// (JFrog's Ansible Repositories page; decision 0008, R6). Without one the
// publish answers R6's 405 with Allow: GET before any permission is asked,
// and no task is found.
func TestAccGalaxyPublishesThroughAVirtualToItsDeploymentRepository(t *testing.T) {
	f := newFixture(t)
	v := f.createRepo("all", repo.Virtual, "ansible-local")
	v.DefaultDeploymentRepo = "ansible-local"
	if err := f.db.Repos().Update(t.Context(), v); err != nil {
		t.Fatal(err)
	}
	f.createRepo("bare", repo.Virtual, "ansible-local")
	f.grantRead("writer", "writer", api.ActionRead|api.ActionWrite, "ansible-local")
	f.grantRead("reader", "reader", api.ActionRead, "ansible-local")
	requireStatus(t, f.publishTo("all", "reader"), http.StatusForbidden)
	w := f.publishTo("all", "writer")
	requireStatus(t, w, http.StatusCreated)
	var task map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil ||
		!strings.Contains(task["task"], "/artifactory/api/ansible/all/api/v3/imports/collections/") {
		t.Fatalf("task: %s %v", w.Body, err)
	}
	w = f.do(http.MethodGet, task["task"], nil, "", "writer")
	requireStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"state":"completed"`) {
		t.Fatalf("task through the virtual repository: %s", w.Body)
	}
	if _, err := f.db.Items().Get(t.Context(), "ansible-local", false,
		ansiblefmt.Path("weave", "sample", "1.2.3")); err != nil {
		t.Fatalf("the collection did not land in the deployment repository: %v", err)
	}
	bareTask := strings.Replace(task["task"], "/api/ansible/all/", "/api/ansible/bare/", 1)
	requireStatus(t, f.do(http.MethodGet, bareTask, nil, "", ""), http.StatusNotFound)
	w = f.publishTo("bare", "stranger")
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodGet ||
		!strings.Contains(w.Body.String(),
			"No local repository was configured as local deployment repository for the (bare) virtual repository.") {
		t.Fatalf("publish without a deployment repository: %d %v %s", w.Code, w.Header(), w.Body)
	}
}
