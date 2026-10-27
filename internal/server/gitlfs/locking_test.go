package gitlfs_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

func TestAccGitLFSLockCreateVerifyAndForce(t *testing.T) {
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

func TestAccGitLFSLockPermissionFilteringAndCursor(t *testing.T) {
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
	status, next := lockCall(
		t,
		f,
		"GET",
		"/locks?limit=2&cursor="+url.QueryEscape(page.NextCursor),
		"reader",
		"",
	)
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

func TestAccGitLFSLockRevocationPreservesState(t *testing.T) {
	f := lockFixture(t, repo.Local)
	status, created := lockCall(t, f, "POST", "/locks", "alice", `{"path":"assets/revoked.bin"}`)
	if status != http.StatusCreated {
		t.Fatalf("create before revocation: %d %+v", status, created)
	}
	if err := f.db.Permissions().Delete(t.Context(), "lfs-lock-access"); err != nil {
		t.Fatal(err)
	}
	status, listed := lockCall(t, f, "GET", "/locks", "alice", "")
	if status != http.StatusForbidden || len(listed.Locks) != 0 {
		t.Fatalf("revoked list: %d %+v", status, listed)
	}
	status, verified := lockCall(t, f, "POST", "/locks/verify", "alice", `{}`)
	if status != http.StatusForbidden || len(verified.Ours) != 0 || len(verified.Theirs) != 0 {
		t.Fatalf("revoked verify: %d %+v", status, verified)
	}
	status, removed := lockCall(t, f, "POST", "/locks/"+created.Lock.ID+"/unlock", "alice", `{}`)
	if status != http.StatusForbidden || removed.Lock.ID != "" {
		t.Fatalf("revoked unlock: %d %+v", status, removed)
	}
	if _, err := f.db.GitLFSLocks().Get(t.Context(), "lfs", created.Lock.ID); err != nil {
		t.Fatalf("revoked user removed durable lock: %v", err)
	}
}

func TestAccGitLFSLockWritePermissionAndOwnerUnlock(t *testing.T) {
	f := lockFixture(t, repo.Local)
	status, reply := lockCall(t, f, "POST", "/locks", "reader", `{"path":"assets/owned.bin"}`)
	if status != http.StatusForbidden || reply.Lock.ID != "" {
		t.Fatalf("read-only lock creation: status=%d reply=%+v", status, reply)
	}
	status, created := lockCall(t, f, "POST", "/locks", "alice", `{"path":"assets/owned.bin"}`)
	if status != http.StatusCreated || created.Lock.ID == "" {
		t.Fatalf("owner lock creation: status=%d reply=%+v", status, created)
	}
	status, reply = lockCall(t, f, "POST", "/locks/unknown/unlock", "alice", `{}`)
	if status != http.StatusNotFound || reply.Lock.ID != "" {
		t.Fatalf("unknown lock unlock: status=%d reply=%+v", status, reply)
	}
	status, reply = lockCall(t, f, "POST", "/locks/"+created.Lock.ID+"/unlock", "reader", `{}`)
	if status != http.StatusForbidden || reply.Lock.ID != "" {
		t.Fatalf("read-only lock unlock: status=%d reply=%+v", status, reply)
	}
	status, reply = lockCall(t, f, "POST", "/locks/"+created.Lock.ID+"/unlock", "alice", `{}`)
	if status != http.StatusOK || reply.Lock.ID != created.Lock.ID {
		t.Fatalf("owner lock unlock: status=%d reply=%+v", status, reply)
	}
	if _, err := f.db.GitLFSLocks().
		Get(t.Context(), "lfs", created.Lock.ID); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("owner unlock retained lock: %v", err)
	}
}

func TestAccGitLFSLockListingHidesRowsOutsidePathGrant(t *testing.T) {
	f := newFixture(t, repo.Local)
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "lfs-public-only", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{
					Users: map[string]types.Actions{"alice": types.ActionRead | types.ActionWrite},
				},
				Targets: map[string]types.Target{"lfs": {Include: []string{"assets/public.bin"}}},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Truncate(time.Second)
	for _, lock := range []types.GitLFSLock{
		{ID: "public", RepoKey: "lfs", Path: "assets/public.bin", Owner: "alice", LockedAt: stamp},
		{ID: "private", RepoKey: "lfs", Path: "assets/private.bin", Owner: "bob", LockedAt: stamp},
	} {
		if err := f.db.GitLFSLocks().Create(t.Context(), lock); err != nil {
			t.Fatal(err)
		}
	}
	status, listed := lockCall(t, f, "GET", "/locks", "alice", "")
	if status != http.StatusOK || len(listed.Locks) != 1 || listed.Locks[0].ID != "public" {
		t.Fatalf("listing disclosed private row: status=%d reply=%+v", status, listed)
	}
	status, verified := lockCall(t, f, "POST", "/locks/verify", "alice", `{}`)
	if status != http.StatusOK || len(verified.Ours) != 1 || verified.Ours[0].ID != "public" ||
		len(verified.Theirs) != 0 {
		t.Fatalf("verification disclosed private row: status=%d reply=%+v", status, verified)
	}
}

func TestAccGitLFSVirtualLocksUseDeploymentTarget(t *testing.T) {
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

func TestAccGitLFSVirtualLocksFailClosedWhenDeploymentTargetDisappears(t *testing.T) {
	f := lockFixture(t, repo.Virtual)
	status, created := lockCall(t, f, "POST", "/locks", "alice", `{"path":"assets/existing.bin"}`)
	if status != http.StatusCreated {
		t.Fatalf("seed lock: %d %+v", status, created)
	}
	c, err := f.db.Repos().Get(t.Context(), "lfs")
	if err != nil {
		t.Fatal(err)
	}
	c.DefaultDeploymentRepo = "missing"
	if err := f.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{name: "create", path: "/locks", body: `{"path":"assets/new.bin"}`, status: http.StatusMethodNotAllowed},
		{name: "verify", path: "/locks/verify", body: `{}`, status: http.StatusBadGateway},
		{name: "unlock", path: "/locks/" + created.Lock.ID + "/unlock", body: `{}`, status: http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reply := lockCall(t, f, "POST", tc.path, "alice", tc.body)
			if got != tc.status || reply.Lock.ID != "" || len(reply.Ours) != 0 {
				t.Fatalf("stale deployment target: status=%d reply=%+v", got, reply)
			}
		})
	}
	if _, err := f.db.GitLFSLocks().Get(t.Context(), "lfs-local", created.Lock.ID); err != nil {
		t.Fatalf("stale target removed durable lock: %v", err)
	}
}

func TestAccGitLFSVirtualCrossMemberConflictAndVerification(t *testing.T) {
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

func TestAccGitLFSVirtualLockPaginationKeepsFirstMemberPrecedence(t *testing.T) {
	f := lockFixture(t, repo.Virtual)
	stamp := time.Now().UTC().Truncate(time.Second)
	for _, lock := range []types.GitLFSLock{
		{ID: "first", RepoKey: "lfs-empty", Path: "assets/shared.bin", Owner: "alice", LockedAt: stamp},
		{ID: "shadowed", RepoKey: "lfs-local", Path: "assets/shared.bin", Owner: "bob", LockedAt: stamp},
		{ID: "later", RepoKey: "lfs-local", Path: "assets/other.bin", Owner: "bob", LockedAt: stamp},
	} {
		if err := f.db.GitLFSLocks().Create(t.Context(), lock); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for page := range 8 {
		suffix := "/locks?limit=1"
		if cursor != "" {
			suffix += "&cursor=" + url.QueryEscape(cursor)
		}
		status, reply := lockCall(t, f, "GET", suffix, "alice", "")
		if status != http.StatusOK || len(reply.Locks) > 1 {
			t.Fatalf("virtual lock page %d: status=%d reply=%+v", page, status, reply)
		}
		for _, lock := range reply.Locks {
			if seen[lock.ID] || lock.ID == "shadowed" {
				t.Fatalf("duplicate or shadowed lock disclosed: %+v", lock)
			}
			seen[lock.ID] = true
		}
		if reply.NextCursor == "" {
			break
		}
		cursor = reply.NextCursor
	}
	if len(seen) != 2 || !seen["first"] || !seen["later"] {
		t.Fatalf("virtual member precedence lost: %+v", seen)
	}
	status, _ := lockCall(t, f, "POST", "/locks/verify", "alice",
		`{"cursor":"`+cursor+`"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("list cursor accepted for verification: %d", status)
	}
}

func TestAccGitLFSVirtualVerifyFailsClosedOnHiddenMember(t *testing.T) {
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

func TestAccGitLFSVirtualVerifyFailsClosedOnHiddenPathWithinMember(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	for key, include := range map[string]string{
		"lfs-empty": "assets/public.bin", "lfs-local": "assets/**",
	} {
		if err := f.db.Permissions().Create(t.Context(), types.Permission{
			Name: "readwrite-" + key, Resources: map[types.ResourceType]types.Resource{
				types.ResourceArtifact: {
					Actions: types.Grants{
						Users: map[string]types.Actions{
							"alice": types.ActionRead | types.ActionWrite,
						},
					},
					Targets: map[string]types.Target{key: {Include: []string{include}}},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.GitLFSLocks().Create(t.Context(), types.GitLFSLock{
		ID: "hidden", RepoKey: "lfs-empty", Path: "assets/private.bin",
		Owner: "bob", LockedAt: time.Now().UTC().Truncate(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	status, reply := lockCall(t, f, "POST", "/locks/verify", "alice", `{}`)
	if status != http.StatusForbidden || len(reply.Ours) != 0 || len(reply.Theirs) != 0 {
		t.Fatalf("verification crossed path grant: status=%d reply=%+v", status, reply)
	}
}

func TestAccGitLFSVirtualVerifyHidesPathsOutsideDeploymentWriteGrant(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	for _, key := range []string{"lfs-empty", "lfs-local"} {
		if err := f.db.Permissions().Create(t.Context(), types.Permission{
			Name: "read-" + key, Resources: map[types.ResourceType]types.Resource{
				types.ResourceArtifact: {
					Actions: types.Grants{
						Users: map[string]types.Actions{"alice": types.ActionRead},
					},
					Targets: map[string]types.Target{key: {Include: []string{"assets/**"}}},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.Permissions().Create(t.Context(), types.Permission{
		Name: "write-public", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"alice": types.ActionWrite}},
				Targets: map[string]types.Target{
					"lfs-local": {Include: []string{"assets/public.bin"}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UTC().Truncate(time.Second)
	for _, lock := range []types.GitLFSLock{
		{ID: "public", RepoKey: "lfs-local", Path: "assets/public.bin", Owner: "alice", LockedAt: stamp},
		{ID: "private", RepoKey: "lfs-empty", Path: "assets/private.bin", Owner: "bob", LockedAt: stamp},
	} {
		if err := f.db.GitLFSLocks().Create(t.Context(), lock); err != nil {
			t.Fatal(err)
		}
	}
	status, reply := lockCall(t, f, "POST", "/locks/verify", "alice", `{}`)
	if status != http.StatusOK || len(reply.Ours) != 1 || reply.Ours[0].ID != "public" ||
		len(reply.Theirs) != 0 {
		t.Fatalf(
			"virtual verify disclosed path outside write grant: status=%d reply=%+v",
			status,
			reply,
		)
	}
}

func TestAccGitLFSVirtualPathFilterSkipsUnreadableMember(t *testing.T) {
	f := newFixture(t, repo.Virtual)
	for key, include := range map[string]string{
		"lfs-empty": "assets/public.bin", "lfs-local": "assets/private.bin",
	} {
		if err := f.db.Permissions().Create(t.Context(), types.Permission{
			Name: "read-" + key, Resources: map[types.ResourceType]types.Resource{
				types.ResourceArtifact: {
					Actions: types.Grants{
						Users: map[string]types.Actions{"alice": types.ActionRead},
					},
					Targets: map[string]types.Target{key: {Include: []string{include}}},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Now().UTC().Truncate(time.Second)
	for _, lock := range []types.GitLFSLock{
		{ID: "hidden", RepoKey: "lfs-empty", Path: "assets/private.bin", Owner: "bob", LockedAt: stamp},
		{ID: "visible", RepoKey: "lfs-local", Path: "assets/private.bin", Owner: "alice", LockedAt: stamp},
	} {
		if err := f.db.GitLFSLocks().Create(t.Context(), lock); err != nil {
			t.Fatal(err)
		}
	}
	status, listed := lockCall(t, f, "GET", "/locks?path=assets/private.bin", "alice", "")
	if status != http.StatusOK || len(listed.Locks) != 1 || listed.Locks[0].ID != "visible" {
		t.Fatalf("path filter disclosed unreadable member: status=%d reply=%+v", status, listed)
	}
}

func TestAccGitLFSLockInvalidRequestsLeaveStateUnchanged(t *testing.T) {
	f := lockFixture(t, repo.Local)
	for _, body := range []string{
		`{"path":"../escape.bin"}`, `{"path":"/absolute.bin"}`,
		`{"path":"assets/x.bin","ref":{"name":"main"}}`, `{"path":1}`,
		`{"path":"assets/x.bin","ref":{"name":"refs/"}}`,
		`{"path":"assets/x.bin","ref":{"name":"refs/heads/.private"}}`,
		`{"path":"assets/x.bin","ref":{"name":"refs/heads/a."}}`,
		`{"path":"assets/x.bin","ref":{"name":"refs/heads/a.lock/b"}}`,
		`null`,
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

func TestAccGitLFSLockNullVerifyAndUnlockCannotSucceed(t *testing.T) {
	f := lockFixture(t, repo.Local)
	status, created := lockCall(t, f, "POST", "/locks", "alice", `{"path":"assets/a.bin"}`)
	if status != http.StatusCreated {
		t.Fatalf("seed lock: %d %+v", status, created)
	}
	status, verified := lockCall(t, f, "POST", "/locks/verify", "alice", `null`)
	if status != http.StatusBadRequest || len(verified.Ours) != 0 {
		t.Fatalf("null verify: %d %+v", status, verified)
	}
	status, removed := lockCall(t, f, "POST", "/locks/"+created.Lock.ID+"/unlock", "alice", `null`)
	if status != http.StatusBadRequest || removed.Lock.ID != "" {
		t.Fatalf("null unlock: %d %+v", status, removed)
	}
	if _, err := f.db.GitLFSLocks().Get(t.Context(), "lfs", created.Lock.ID); err != nil {
		t.Fatalf("null request removed lock: %v", err)
	}
}

func TestAccGitLFSLockMalformedQueriesAndBodiesPreserveOwnership(t *testing.T) {
	f := lockFixture(t, repo.Local)
	status, created := lockCall(t, f, "POST", "/locks", "alice", `{"path":"assets/owned.bin"}`)
	if status != http.StatusCreated {
		t.Fatalf("seed lock: %d %+v", status, created)
	}
	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{method: "GET", path: "/locks?unknown=value"},
		{method: "GET", path: "/locks?limit=1&limit=2"},
		{method: "GET", path: "/locks?limit=0"},
		{method: "GET", path: "/locks?limit=invalid"},
		{method: "GET", path: "/locks?id=" + strings.Repeat("x", 257)},
		{method: "GET", path: "/locks?refspec=main"},
		{method: "GET", path: "/locks?cursor=broken"},
		{method: "POST", path: "/locks/verify", body: `{"limit":-1}`},
		{method: "POST", path: "/locks/verify", body: `{"ref":{"name":"main"}}`},
		{method: "POST", path: "/locks/verify", body: `{"extra":true}`},
		{method: "POST", path: "/locks/verify", body: `{"cursor":"broken"}`},
		{method: "POST", path: "/locks/" + created.Lock.ID + "/unlock", body: `{"ref":{"name":"main"}}`},
		{method: "POST", path: "/locks/" + created.Lock.ID + "/unlock", body: `{"force":false,"extra":true}`},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			status, reply := lockCall(t, f, tc.method, tc.path, "alice", tc.body)
			if status < http.StatusBadRequest || len(reply.Locks) != 0 || reply.Lock.ID != "" {
				t.Fatalf("untrusted request accepted: status=%d reply=%+v", status, reply)
			}
		})
	}
	if _, err := f.db.GitLFSLocks().Get(t.Context(), "lfs", created.Lock.ID); err != nil {
		t.Fatalf("malformed request changed lock ownership: %v", err)
	}
}

func TestAccGitLFSLockMediaTypeRejectionsPreserveState(t *testing.T) {
	f := lockFixture(t, repo.Local)
	for _, tc := range []struct {
		name     string
		method   string
		path     string
		body     string
		user     string
		accept   string
		content  string
		expected int
	}{
		{name: "create without accept", method: "POST", path: "/locks", body: `{"path":"a"}`, user: "alice", content: "application/vnd.git-lfs+json", expected: http.StatusUnsupportedMediaType},
		{name: "list without accept", method: "GET", path: "/locks", user: "alice", expected: http.StatusUnsupportedMediaType},
		{name: "verify wrong content type", method: "POST", path: "/locks/verify", body: `{}`, user: "alice", accept: "application/vnd.git-lfs+json", content: "text/plain", expected: http.StatusUnsupportedMediaType},
		{name: "unlock wrong content type", method: "POST", path: "/locks/unknown/unlock", body: `{}`, user: "alice", accept: "application/vnd.git-lfs+json", content: "text/plain", expected: http.StatusUnsupportedMediaType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(
				tc.method,
				f.server.URL+"/artifactory/api/lfs/lfs"+tc.path,
				strings.NewReader(tc.body),
			)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-User", tc.user)
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			if tc.content != "" {
				req.Header.Set("Content-Type", tc.content)
			}
			res, err := f.server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode != tc.expected {
				t.Fatalf("status=%d, want %d", res.StatusCode, tc.expected)
			}
		})
	}
	rows, _, err := f.db.GitLFSLocks().List(t.Context(),
		store.GitLFSLockFilter{RepoKey: "lfs"}, store.Page{})
	if err != nil || len(rows) != 0 {
		t.Fatalf("invalid media type changed locks: %+v, err=%v", rows, err)
	}
}
