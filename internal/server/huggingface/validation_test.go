package huggingface_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitNativeEndpointsRejectMalformedAndUnsafeRequests(t *testing.T) {
	f := newFixture(t, repo.Local)
	model := "api/models/acme/demo/"
	header := `{"key":"header","value":{"summary":"publish"}}` + "\n"
	file := `{"key":"file","value":{"path":"config.json","content":"e30=","encoding":"base64"}}` + "\n"
	for _, tc := range []struct {
		name, method, path, media, body string
	}{
		{"invalid owner", "GET", "api/models/-bad/demo", "", ""},
		{"invalid revision", "GET", model + "revision/bad^ref", "", ""},
		{"invalid info query", "GET", model + "revision/main?leak=true", "", ""},
		{"absent revision", "GET", model + "revision/main", "", ""},
		{"absent named ref", "GET", model + "revision/v1", "", ""},
		{"invalid tree query", "GET", model + "tree/main?recursive=yes", "", ""},
		{"invalid tree revision", "GET", model + "tree/bad^ref", "", ""},
		{"invalid tree prefix", "GET", model + "tree/main/a%5Cb", "", ""},
		{"invalid resolve query", "GET", "acme/demo/resolve/main/config.json?raw=1", "", ""},
		{"invalid resolve revision", "GET", "acme/demo/resolve/bad^ref/config.json", "", ""},
		{"preupload revision", "POST", model + "preupload/bad^ref", "application/json", `{"files":[{"path":"x","sample":"eA==","size":1}]}`},
		{"preupload query", "POST", model + "preupload/main?unexpected=1", "application/json", `{"files":[{"path":"x","sample":"eA==","size":1}]}`},
		{"preupload media", "POST", model + "preupload/main", "text/plain", `{"files":[{"path":"x","sample":"eA==","size":1}]}`},
		{"preupload empty", "POST", model + "preupload/main", "application/json", `{"files":[]}`},
		{"preupload duplicate", "POST", model + "preupload/main", "application/json", `{"files":[{"path":"x","sample":"eA==","size":1},{"path":"x","sample":"eA==","size":1}]}`},
		{"preupload traversal", "POST", model + "preupload/main", "application/json", `{"files":[{"path":"../x","sample":"eA==","size":1}]}`},
		{"preupload sample", "POST", model + "preupload/main", "application/json", `{"files":[{"path":"x","sample":"?","size":1}]}`},
		{"preupload size", "POST", model + "preupload/main", "application/json", `{"files":[{"path":"x","sample":"eA==","size":-1}]}`},
		{"preupload excessive size", "POST", model + "preupload/main", "application/json", `{"files":[{"path":"x","sample":"eA==","size":10737418241}]}`},
		{"commit media", "POST", model + "commit/main", "application/json", header + file},
		{"commit empty", "POST", model + "commit/main", "application/x-ndjson", ""},
		{"commit revision", "POST", model + "commit/bad^ref", "application/x-ndjson", header + file},
		{"commit query", "POST", model + "commit/main?unexpected=1", "application/x-ndjson", header + file},
		{"commit header", "POST", model + "commit/main", "application/x-ndjson", `{"key":"wrong","value":{}}` + "\n" + file},
		{"commit operation", "POST", model + "commit/main", "application/x-ndjson", header + `{"key":"delete","value":{"path":"x"}}` + "\n"},
		{"commit invalid base64", "POST", model + "commit/main", "application/x-ndjson", header + `{"key":"file","value":{"path":"x","content":"?","encoding":"base64"}}` + "\n"},
		{"commit invalid encoding", "POST", model + "commit/main", "application/x-ndjson", header + `{"key":"file","value":{"path":"x","content":"eA==","encoding":"utf8"}}` + "\n"},
		{"commit missing LFS", "POST", model + "commit/main", "application/x-ndjson", header + fmt.Sprintf(`{"key":"lfsFile","value":{"path":"weights.bin","algo":"sha256","oid":"%064d","size":1048577}}`, 1) + "\n"},
		{"commit invalid LFS algorithm", "POST", model + "commit/main", "application/x-ndjson", header + fmt.Sprintf(`{"key":"lfsFile","value":{"path":"weights.bin","algo":"sha1","oid":"%064d","size":1048577}}`, 1) + "\n"},
		{"batch query", "POST", "acme/demo.git/info/lfs/objects/batch?unexpected=1", "application/json", `{"operation":"upload","objects":[{"oid":"x","size":1}]}`},
		{"batch operation", "POST", "acme/demo.git/info/lfs/objects/batch", "application/json", `{"operation":"download","objects":[{"oid":"x","size":1}]}`},
		{"batch empty", "POST", "acme/demo.git/info/lfs/objects/batch", "application/json", `{"operation":"upload","objects":[]}`},
		{"batch digest", "POST", "acme/demo.git/info/lfs/objects/batch", "application/json", `{"operation":"upload","objects":[{"oid":"bad","size":1048577}]}`},
		{"batch small size", "POST", "acme/demo.git/info/lfs/objects/batch", "application/json", fmt.Sprintf(`{"operation":"upload","objects":[{"oid":"%064d","size":1}]}`, 1)},
		{"batch unknown field", "POST", "acme/demo.git/info/lfs/objects/batch", "application/json", `{"operation":"upload","objects":[],"unexpected":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := f.request(tc.method, tc.path, tc.media, []byte(tc.body))
			body := readResponse(t, r)
			if r.StatusCode < 400 || r.StatusCode >= 500 {
				t.Fatalf(
					"unsafe request accepted or caused server error: %d %s",
					r.StatusCode,
					body,
				)
			}
		})
	}
	for _, path := range []string{model + "xet-read-token/main", model + "xet-write-token/main"} {
		r := f.request(http.MethodGet, path, "", nil)
		body := readResponse(t, r)
		if r.StatusCode != http.StatusNotImplemented ||
			bytes.Contains(body, []byte("accessToken")) {
			t.Fatalf(
				"Xet CAS authority advertised without implementation: %d %s",
				r.StatusCode,
				body,
			)
		}
	}
}

func TestUnitNativeTreeAndMutableRefStayWithinPublishedRevision(t *testing.T) {
	f := newFixture(t, repo.Local)
	base := "api/models/acme/demo/"
	commit := func(body string) string {
		t.Helper()
		r := f.request("POST", base+"commit/main", "application/x-ndjson", []byte(body))
		b := readResponse(t, r)
		if r.StatusCode != 200 {
			t.Fatalf("publish revision: %d %s", r.StatusCode, b)
		}
		var doc struct {
			CommitOID string `json:"commitOid"`
		}
		if err := json.Unmarshal(b, &doc); err != nil || len(doc.CommitOID) != 40 {
			t.Fatalf("commit response: %s %v", b, err)
		}
		return doc.CommitOID
	}
	header := `{"key":"header","value":{"summary":"publish"}}` + "\n"
	first := commit(
		header + `{"key":"file","value":{"path":"nested/one.txt","content":"MQ==","encoding":"base64"}}` + "\n",
	)
	second := commit(header +
		`{"key":"file","value":{"path":"nested/deeper/two.txt","content":"Mg==","encoding":"base64"}}` + "\n" +
		`{"key":"file","value":{"path":"nested/deeper/three.txt","content":"Mw==","encoding":"base64"}}` + "\n" +
		`{"key":"file","value":{"path":"config.json","content":"e30=","encoding":"base64"}}` + "\n")
	if first == second {
		t.Fatal("distinct content published under one immutable revision")
	}
	for _, tc := range []struct {
		path               string
		want               int
		contains, excludes string
	}{
		{base + "revision/main", 200, second, "one.txt"},
		{base + "revision/" + first, 200, "one.txt", "two.txt"},
		{base + "tree/main", 200, `"type":"directory"`, "two.txt"},
		{base + "tree/main?recursive=true", 200, "nested/deeper/two.txt", "nested/one.txt"},
		{base + "tree/main/nested", 200, `"path":"nested/deeper"`, "two.txt"},
		{base + "tree/main/nested?recursive=true", 200, "nested/deeper/two.txt", "nested/one.txt"},
		{base + "tree/main/elsewhere", 200, "[]", "two.txt"},
		{base + "tree/missing", 404, "", ""},
		{"acme/demo/resolve/main/nested/deeper/two.txt", 200, "2", "1"},
		{"acme/demo/resolve/main/nested/one.txt", 404, "", ""},
	} {
		r := f.request("GET", tc.path, "", nil)
		b := readResponse(t, r)
		if r.StatusCode != tc.want ||
			tc.contains != "" && !bytes.Contains(b, []byte(tc.contains)) ||
			tc.excludes != "" && bytes.Contains(b, []byte(tc.excludes)) {
			t.Errorf("revision/tree %q: %d %s", tc.path, r.StatusCode, b)
		}
	}
}

func TestUnitVirtualDeploymentRequiresAuthorizedLocalMember(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		memberClass  repo.Class
		include      bool
		virtualOnly  bool
		user         string
		want         int
	}{
		{"no deployment member", "", repo.Local, true, false, "", 405},
		{"target outside members", "member", repo.Local, false, false, "", 405},
		{"remote deployment member", "member", repo.Remote, true, false, "", 400},
		{"denied model write", "member", repo.Local, true, false, "intruder", 403},
		{"denied member write", "member", repo.Local, true, true, "virtual-only", 403},
		{"local deployment member", "member", repo.Local, true, false, "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, repo.Virtual, func(f *fixture) *source.Service {
				member := *repo.Defaults(&repo.Config{
					Key: "member", Class: tc.memberClass, PackageType: repo.HuggingFaceML,
				})
				if err := f.db.Repos().Create(t.Context(), member); err != nil {
					t.Fatal(err)
				}
				virtual, err := f.db.Repos().Get(t.Context(), "hf")
				if err != nil {
					t.Fatal(err)
				}
				virtual.DefaultDeploymentRepo = tc.target
				if tc.include {
					virtual.Repositories = []string{"member"}
				}
				if err := f.db.Repos().Update(t.Context(), virtual); err != nil {
					t.Fatal(err)
				}
				if tc.virtualOnly {
					if err := f.db.Permissions().Create(t.Context(), types.Permission{
						Name: "virtual-only-write",
						Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
							Actions: types.Grants{
								Users: map[string]types.Actions{"virtual-only": types.ActionWrite},
							},
							Targets: map[string]types.Target{"hf": {Include: []string{"**"}}},
						}},
					}); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			})
			pre := []byte(`{"files":[{"path":"config.json","sample":"e30=","size":2}]}`)
			r := f.requestAs(
				"POST",
				"api/models/acme/demo/preupload/main",
				"application/json",
				pre,
				tc.user,
			)
			b := readResponse(t, r)
			if r.StatusCode != tc.want {
				t.Fatalf("virtual preupload: %d %s", r.StatusCode, b)
			}
			if tc.want != 200 {
				return
			}
			body := `{"key":"header","value":{"summary":"publish"}}` + "\n" +
				`{"key":"file","value":{"path":"config.json","content":"e30=","encoding":"base64"}}` + "\n"
			r = f.request(
				"POST",
				"api/models/acme/demo/commit/main",
				"application/x-ndjson",
				[]byte(body),
			)
			b = readResponse(t, r)
			if r.StatusCode != 200 {
				t.Fatalf("virtual commit: %d %s", r.StatusCode, b)
			}
			r = f.request("GET", "api/models/acme/demo/revision/main", "", nil)
			b = readResponse(t, r)
			if r.StatusCode != 200 || !bytes.Contains(b, []byte(`"rfilename":"config.json"`)) {
				t.Fatalf("virtual member read: %d %s", r.StatusCode, b)
			}
		})
	}
}

func TestUnitRevisionRefRejectsCorruptionAndAbsentSnapshot(t *testing.T) {
	f := newFixture(t, repo.Local)
	c, err := f.db.Repos().Get(t.Context(), "hf")
	if err != nil {
		t.Fatal(err)
	}
	path := "models--acme--demo/refs/main"
	for _, ref := range []struct {
		name, content string
		want          int
	}{
		{"malformed ref", "not-a-revision", 500},
		{"absent snapshot", strings.Repeat("f", 40), 404},
	} {
		t.Run(ref.name, func(t *testing.T) {
			_, err := f.items.Deploy(t.Context(), item.Deploy{
				Target: item.Target{Repo: c, Path: path},
				Body:   strings.NewReader(ref.content), By: "admin",
			})
			if err != nil {
				t.Fatal(err)
			}
			r := f.request("GET", "api/models/acme/demo/revision/main", "", nil)
			b := readResponse(t, r)
			if r.StatusCode != ref.want {
				t.Fatalf("corrupt ref disclosed: %d %s", r.StatusCode, b)
			}
		})
	}
	marker, err := f.db.Items().Get(t.Context(), "hf", false, path)
	if err != nil {
		t.Fatal(err)
	}
	marker.Actual.SHA256 = strings.Repeat("f", 64)
	if _, err := f.db.Items().Put(t.Context(), marker); err != nil {
		t.Fatal(err)
	}
	r := f.request("GET", "api/models/acme/demo/revision/main", "", nil)
	if b := readResponse(t, r); r.StatusCode == 200 {
		t.Fatalf("missing ref blob accepted: %s", b)
	}
	marker.Folder = true
	if _, err := f.db.Items().Put(t.Context(), marker); err != nil {
		t.Fatal(err)
	}
	r = f.request("GET", "api/models/acme/demo/revision/main", "", nil)
	if b := readResponse(t, r); r.StatusCode == 200 {
		t.Fatalf("folder masqueraded as revision ref: %s", b)
	}
}

func TestUnitLocalInfoRejectsCorruptStoredSnapshot(t *testing.T) {
	for _, corruption := range []string{"missing blob", "wrong size"} {
		t.Run(corruption, func(t *testing.T) {
			f := newFixture(t, repo.Local)
			body := `{"key":"header","value":{"summary":"publish"}}` + "\n" +
				`{"key":"file","value":{"path":"config.json","content":"e30=","encoding":"base64"}}` + "\n"
			r := f.request(
				"POST",
				"api/models/acme/demo/commit/"+sha,
				"application/x-ndjson",
				[]byte(body),
			)
			if b := readResponse(t, r); r.StatusCode != 200 {
				t.Fatalf("publish fixture: %d %s", r.StatusCode, b)
			}
			path := "models--acme--demo/snapshots/" + sha + "/config.json"
			stored, err := f.db.Items().Get(t.Context(), "hf", false, path)
			if err != nil {
				t.Fatal(err)
			}
			if corruption == "missing blob" {
				stored.Actual.SHA256 = strings.Repeat("f", 64)
			} else {
				stored.Size++
			}
			if _, err := f.db.Items().Put(t.Context(), stored); err != nil {
				t.Fatal(err)
			}
			r = f.request("GET", "api/models/acme/demo/revision/"+sha, "", nil)
			b := readResponse(t, r)
			if r.StatusCode == 200 || bytes.Contains(b, []byte(`"rfilename"`)) {
				t.Fatalf("corrupt snapshot disclosed: %d %s", r.StatusCode, b)
			}
		})
	}
}

func TestUnitPathScopedWritesCannotBypassSnapshotOrRefGrants(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, grant := range []struct {
		user, include, revision string
	}{
		{"refs-only", "models--acme--demo/refs/**", sha},
		{"snapshots-only", "models--acme--demo/snapshots/**", "main"},
	} {
		if err := f.db.Permissions().Create(t.Context(), types.Permission{
			Name: "hf-" + grant.user,
			Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
				Actions: types.Grants{
					Users: map[string]types.Actions{grant.user: types.ActionWrite},
				},
				Targets: map[string]types.Target{"hf": {Include: []string{grant.include}}},
			}},
		}); err != nil {
			t.Fatal(err)
		}
		body := `{"key":"header","value":{"summary":"publish"}}` + "\n" +
			`{"key":"file","value":{"path":"config.json","content":"e30=","encoding":"base64"}}` + "\n"
		r := f.requestAs("POST", "api/models/acme/demo/commit/"+grant.revision,
			"application/x-ndjson", []byte(body), grant.user)
		b := readResponse(t, r)
		if r.StatusCode != http.StatusForbidden {
			t.Fatalf("%s escaped path grant: %d %s", grant.user, r.StatusCode, b)
		}
	}
}

func TestUnitMainFallsBackToLatestCurrentLayoutSnapshotWithoutRef(t *testing.T) {
	f := newFixture(t, repo.Local)
	now := time.Date(2026, time.October, 25, 5, 0, 0, 0, time.UTC)
	f.items.Now = func() time.Time { return now }
	header := `{"key":"header","value":{"summary":"publish"}}` + "\n"
	for _, tc := range []struct {
		revision, content string
	}{
		{sha, "MQ=="},
		{strings.Repeat("a", 40), "Mg=="},
	} {
		body := header + `{"key":"file","value":{"path":"config.json","content":"` + tc.content + `","encoding":"base64"}}` + "\n"
		r := f.request("POST", "api/models/acme/demo/commit/"+tc.revision,
			"application/x-ndjson", []byte(body))
		if b := readResponse(t, r); r.StatusCode != 200 {
			t.Fatalf("immutable snapshot: %d %s", r.StatusCode, b)
		}
		now = now.Add(time.Minute)
	}
	r := f.request("GET", "api/models/acme/demo/revision/main", "", nil)
	b := readResponse(t, r)
	if r.StatusCode != 200 || !bytes.Contains(b, []byte(strings.Repeat("a", 40))) {
		t.Fatalf("main did not resolve latest old snapshot: %d %s", r.StatusCode, b)
	}
}
