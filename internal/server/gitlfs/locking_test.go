package gitlfs_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type wireLock struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Owner struct {
		Name string `json:"name"`
	} `json:"owner"`
}

type lockReply struct {
	Lock       wireLock   `json:"lock"`
	Locks      []wireLock `json:"locks"`
	Ours       []wireLock `json:"ours"`
	Theirs     []wireLock `json:"theirs"`
	NextCursor string     `json:"next_cursor"`
	Message    string     `json:"message"`
}

func lockFixture(t *testing.T, class repo.Class) *fixture {
	t.Helper()
	f := newFixture(t, class)
	targets := map[string]types.Target{}
	keys := []string{"lfs"}
	if class == repo.Virtual {
		keys = []string{"lfs-empty", "lfs-local"}
	}
	for _, key := range keys {
		targets[key] = types.Target{Include: []string{"**"}}
	}
	err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "lfs-lock-access", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{
					"alice":   types.ActionRead | types.ActionWrite,
					"bob":     types.ActionRead | types.ActionWrite,
					"manager": types.ActionRead | types.ActionWrite | types.ActionDelete,
					"reader":  types.ActionRead,
				}}, Targets: targets,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func lockCall(t *testing.T, f *fixture, method, suffix, user, body string) (int, lockReply) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, f.server.URL+"/artifactory/api/lfs/lfs"+suffix, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/vnd.git-lfs+json")
	req.Header.Set("Content-Type", "application/vnd.git-lfs+json; charset=utf-8")
	if user != "" {
		req.Header.Set("X-User", user)
	}
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("Content-Type"); got != "application/vnd.git-lfs+json" {
		t.Fatalf("content type = %q", got)
	}
	var reply lockReply
	if err := json.NewDecoder(res.Body).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, reply
}

func TestUnitGitLFSLockCreateVerifyAndForce(t *testing.T) {
	f := lockFixture(t, repo.Local)
	status, created := lockCall(t, f, "POST", "/locks", "alice", `{"path":"models/a.bin"}`)
	if status != 201 || created.Lock.ID == "" || created.Lock.Path != "models/a.bin" ||
		created.Lock.Owner.Name != "alice" {
		t.Fatalf("create: %d %+v", status, created)
	}
	status, duplicate := lockCall(t, f, "POST", "/locks", "bob", `{"path":"models/a.bin"}`)
	if status != 409 || duplicate.Lock.ID != created.Lock.ID {
		t.Fatalf("duplicate: %d %+v", status, duplicate)
	}
	status, listed := lockCall(t, f, "GET", "/locks", "reader", "")
	if status != 200 || len(listed.Locks) != 1 || listed.Locks[0].ID != created.Lock.ID {
		t.Fatalf("list: %d %+v", status, listed)
	}
	status, verified := lockCall(t, f, "POST", "/locks/verify", "bob", `{}`)
	if status != 200 || len(verified.Ours) != 0 || len(verified.Theirs) != 1 ||
		verified.Theirs[0].ID != created.Lock.ID {
		t.Fatalf("verify: %d %+v", status, verified)
	}
	path := "/locks/" + url.PathEscape(created.Lock.ID) + "/unlock"
	status, _ = lockCall(t, f, "POST", path, "bob", `{}`)
	if status != 403 {
		t.Fatalf("other owner's unlock = %d", status)
	}
	status, _ = lockCall(t, f, "POST", path, "bob", `{"force":true}`)
	if status != 403 {
		t.Fatalf("force without Delete = %d", status)
	}
	status, removed := lockCall(t, f, "POST", path, "manager", `{"force":true}`)
	if status != 200 || removed.Lock.ID != created.Lock.ID {
		t.Fatalf("managed force: %d %+v", status, removed)
	}
	status, listed = lockCall(t, f, "GET", "/locks", "reader", "")
	if status != 200 || len(listed.Locks) != 0 {
		t.Fatalf("after removal: %d %+v", status, listed)
	}
}

func TestUnitGitLFSLockPermissionFilteringAndCursor(t *testing.T) {
	f := lockFixture(t, repo.Local)
	for i := range 5 {
		path := fmt.Sprintf("assets/%d.bin", i)
		status, _ := lockCall(t, f, "POST", "/locks", "alice", `{"path":"`+path+`"}`)
		if status != 201 {
			t.Fatalf("create %q = %d", path, status)
		}
	}
	status, page := lockCall(t, f, "GET", "/locks?limit=2", "reader", "")
	if status != 200 || len(page.Locks) != 2 || page.NextCursor == "" {
		t.Fatalf("first page: %d %+v", status, page)
	}
	status, next := lockCall(t, f, "GET", "/locks?limit=2&cursor="+url.QueryEscape(page.NextCursor), "reader", "")
	if status != 200 || len(next.Locks) != 2 || next.Locks[0].Path != "assets/2.bin" {
		t.Fatalf("second page: %d %+v", status, next)
	}
	status, _ = lockCall(t, f, "GET", "/locks?limit=2&path=assets/0.bin&cursor="+
		url.QueryEscape(page.NextCursor), "reader", "")
	if status != 400 {
		t.Fatalf("cross-filter cursor = %d", status)
	}
	status, _ = lockCall(t, f, "GET", "/locks?path=assets%ZZ0.bin", "reader", "")
	if status != http.StatusBadRequest {
		t.Fatalf("malformed query escape = %d", status)
	}
	status, _ = lockCall(t, f, "POST", "/locks/verify", "reader", `{}`)
	if status != 403 {
		t.Fatalf("read-only verify = %d", status)
	}
	status, _ = lockCall(t, f, "GET", "/locks", "intruder", "")
	if status != 403 {
		t.Fatalf("intruder listing = %d", status)
	}
}

func TestUnitGitLFSVirtualLocksUseDeploymentTarget(t *testing.T) {
	f := lockFixture(t, repo.Virtual)
	status, created := lockCall(t, f, "POST", "/locks", "alice", `{"path":"assets/shared.bin"}`)
	if status != 201 {
		t.Fatalf("virtual create: %d %+v", status, created)
	}
	if _, err := f.db.GitLFSLocks().Get(t.Context(), "lfs-local", created.Lock.ID); err != nil {
		t.Fatal(err)
	}
	status, verified := lockCall(t, f, "POST", "/locks/verify", "bob", `{}`)
	if status != 200 || len(verified.Theirs) != 1 {
		t.Fatalf("virtual verify: %d %+v", status, verified)
	}
}

func TestUnitGitLFSVirtualCrossMemberConflictAndVerification(t *testing.T) {
	f := lockFixture(t, repo.Virtual)
	upstream := types.GitLFSLock{
		ID: "member-lock", RepoKey: "lfs-empty", Path: "assets/shared.bin",
		Owner: "bob", LockedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := f.db.GitLFSLocks().Create(t.Context(), upstream); err != nil {
		t.Fatal(err)
	}
	status, conflict := lockCall(t, f, "POST", "/locks", "alice", `{"path":"assets/shared.bin"}`)
	if status != 409 || conflict.Lock.ID != upstream.ID {
		t.Fatalf("cross-member create: %d %+v", status, conflict)
	}
	status, verified := lockCall(t, f, "POST", "/locks/verify", "alice", `{}`)
	if status != 200 || len(verified.Theirs) != 1 || verified.Theirs[0].ID != upstream.ID {
		t.Fatalf("cross-member verify: %d %+v", status, verified)
	}
	rows, _, err := f.db.GitLFSLocks().List(t.Context(),
		store.GitLFSLockFilter{RepoKey: "lfs-local"}, store.Page{})
	if err != nil || len(rows) != 0 {
		t.Fatalf("target mutated on conflict: %+v %v", rows, err)
	}
}

func TestUnitGitLFSVirtualVerifyFailsClosedOnHiddenMember(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	for _, key := range []string{"lfs-local"} {
		err := f.db.Permissions().Create(t.Context(), types.Permission{
			Name: "lock-" + key,
			Resources: map[types.ResourceType]types.Resource{
				types.ResourceArtifact: {
					Actions: types.Grants{Users: map[string]types.Actions{
						"alice": types.ActionRead | types.ActionWrite,
					}},
					Targets: map[string]types.Target{key: {Include: []string{"**"}}},
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.GitLFSLocks().Create(t.Context(), types.GitLFSLock{
		ID: "hidden", RepoKey: "lfs-empty", Path: "assets/private.bin",
		Owner: "bob", LockedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.db.GitLFSLocks().Create(t.Context(), types.GitLFSLock{
		ID: "visible", RepoKey: "lfs-local", Path: "assets/public.bin",
		Owner: "bob", LockedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	status, listed := lockCall(t, f, "GET", "/locks", "alice", "")
	if status != 200 || len(listed.Locks) != 1 || listed.Locks[0].ID != "visible" {
		t.Fatalf("virtual listing skipped readable later member: %d %+v", status, listed)
	}
	status, reply := lockCall(t, f, "POST", "/locks/verify", "alice", `{}`)
	if status != 403 || len(reply.Theirs) != 0 {
		t.Fatalf("hidden member verification: %d %+v", status, reply)
	}
}

func TestUnitGitLFSLockInvalidRequestsLeaveStateUnchanged(t *testing.T) {
	f := lockFixture(t, repo.Local)
	for _, body := range []string{
		`{"path":"../escape.bin"}`, `{"path":"/absolute.bin"}`,
		`{"path":"assets/x.bin","ref":{"name":"main"}}`, `{"path":1}`,
		`{"path":"assets/x.bin"}{"path":"assets/y.bin"}`,
	} {
		status, _ := lockCall(t, f, "POST", "/locks", "alice", body)
		if status < 400 {
			t.Fatalf("invalid body accepted: %q (%d)", body, status)
		}
	}
	rows, _, err := f.db.GitLFSLocks().List(t.Context(),
		store.GitLFSLockFilter{RepoKey: "lfs"}, store.Page{})
	if err != nil || len(rows) != 0 {
		t.Fatalf("invalid requests mutated locks: %+v %v", rows, err)
	}
}
