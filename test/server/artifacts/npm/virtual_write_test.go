package npm_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func virtualNPMDeploy(t *testing.T, w *harness.RemoteWorld, key string) {
	t.Helper()
	virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.DefaultDeploymentRepo = key })
}

func TestAccNPMVirtualWriteLifecycle(t *testing.T) {
	for _, tc := range []struct{ name, target string }{{"plain-local", "first"}, {"@acme/scoped-federated", "second"}} {
		t.Run(tc.name, func(t *testing.T) {
			w := virtualNPMWorld(t, npmfmt.Settings{})
			virtualNPMDeploy(t, w, tc.target)
			virtualNPMLocal(t, w, "first", "unrelated", "1.0.0", "untouched")
			other := "second"
			if tc.target == other {
				other = "first"
			}
			virtualNPMLocal(t, w, other, tc.name, "8.0.0", "other member")
			virtualNPMRemote(t, w, tc.name, map[string]string{"9.0.0": "remote"}, map[string]string{"latest": "9.0.0"})
			base := npmBase + "virtual/"
			local := npmBase + tc.target + "/"
			for _, v := range []string{"1.0.0", "2.0.0"} {
				harness.WantStatus(t, w.Request("PUT", base+tc.name, "admin", npmPublishBody(tc.name, v)), 201)
				harness.WantStatus(t, w.Request("GET", "/artifactory/"+tc.target+"/"+npmfmt.CanonicalPath(tc.name, v), "admin", ""), 200)
			}
			r := w.Request("GET", base+tc.name+"?write=true", "admin", "", "Accept", "application/vnd.npm.install-v1+json", "X-JFrog-Override-Base-Url", "https://public.example")
			harness.WantStatus(t, r, 200)
			doc := virtualNPMJSON(t, r.Body.Bytes())
			versions := doc["versions"].(map[string]any)
			if len(versions) != 2 || versions["8.0.0"] != nil || versions["9.0.0"] != nil || doc["_rev"] != "1-0" {
				t.Fatal(doc)
			}
			for _, v := range versions {
				url := v.(map[string]any)["dist"].(map[string]any)["tarball"].(string)
				if !strings.HasPrefix(url, "https://public.example/artifactory/api/npm/virtual/") {
					t.Fatal(url)
				}
			}
			harness.WantStatus(
				t,
				w.Request("GET", base+tc.name+"?write=true", "admin", "", "If-None-Match", r.Header().Get("ETag"), "X-JFrog-Override-Base-Url", "https://public.example"),
				304,
			)
			tags := base + "-/package/" + tc.name + "/dist-tags/next"
			harness.WantStatus(t, w.Request("PUT", tags, "admin", `"2.0.0"`), 200)
			harness.WantStatus(t, w.Request("DELETE", tags, "admin", ""), 200)
			harness.WantStatus(
				t,
				w.Request("PUT", base+tc.name, "admin", `{"versions":{"1.0.0":{"deprecated":"use two"},"8.0.0":{"deprecated":"wrong"},"9.0.0":{"deprecated":"wrong"}}}`),
				200,
			)
			r = w.Request("GET", local+tc.name+"/1.0.0", "admin", "")
			harness.WantStatus(t, r, 200)
			if virtualNPMJSON(t, r.Body.Bytes())["deprecated"] != "use two" {
				t.Fatal(r.Body.String())
			}
			r = w.Request("GET", npmBase+other+"/"+tc.name+"/8.0.0", "admin", "")
			harness.WantStatus(t, r, 200)
			if virtualNPMJSON(t, r.Body.Bytes())["deprecated"] != nil {
				t.Fatal("changed non-DDR", r.Body.String())
			}
			if len(w.Upstream.Requests()) != 0 {
				t.Fatal("write contacted a remote", w.Upstream.Requests())
			}
			harness.WantStatus(t, w.Request("PUT", base+tc.name+"/-rev/1-0", "admin", `{"versions":{"1.0.0":{}}}`), 200)
			harness.WantStatus(t, w.Request("DELETE", base+npmfmt.CanonicalPath(tc.name, "2.0.0")+"/-rev/1-0", "admin", ""), 404)
			harness.WantStatus(t, w.Request("DELETE", base+tc.name+"/-rev/1-0", "admin", ""), 200)
			harness.WantStatus(t, w.Request("GET", local+tc.name, "admin", ""), 404)
			harness.WantStatus(t, w.Request("GET", npmBase+other+"/"+tc.name, "admin", ""), 200)
			r = w.Request("DELETE", "/artifactory/virtual/"+npmfmt.CanonicalPath(tc.name, "8.0.0"), "admin", "")
			if r.Code != 404 || r.Body.Len() != 0 {
				t.Fatal("item DELETE changed", r.Code, r.Body.String())
			}
		})
	}
}

func TestAccNPMVirtualNoDeploymentTarget(t *testing.T) {
	for _, target := range []string{"", "missing", "remote", "unrelated", "wrong-type", "inner"} {
		t.Run("target-"+target, func(t *testing.T) {
			w := virtualNPMWorld(t, npmfmt.Settings{})
			for _, c := range []repo.Config{{Key: "unrelated", Class: repo.Local, PackageType: repo.NPM}, {Key: "wrong-type", Class: repo.Local, PackageType: repo.Generic}} {
				if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
					t.Fatal(err)
				}
			}
			virtualNPMDeploy(t, w, target)
			for _, tc := range []struct{ method, path string }{{"PUT", "pkg"}, {"PUT", "pkg/-rev/1-0"}, {"PUT", "-/package/pkg/dist-tags/next"}, {"DELETE", "-/package/pkg/dist-tags/next"}, {"DELETE", "pkg/-rev/1-0"}, {"DELETE", "pkg/-/pkg-1.0.0.tgz/-rev/1-0"}, {"GET", "pkg?write=true"}} {
				t.Run(tc.method+"-"+tc.path, func(t *testing.T) {
					r := w.Request(tc.method, npmBase+"virtual/"+tc.path, "admin", "not json")
					want := `{"error":"No local repository was configured as local deployment repository for the (virtual) virtual repository."}`
					if r.Code != 400 || strings.TrimSpace(r.Body.String()) != want {
						t.Fatal(r.Code, r.Body.String())
					}
				})
			}
			if len(w.Upstream.Requests()) != 0 {
				t.Fatal("no DDR contacted origin")
			}
		})
	}
}

func virtualNPMGrant(t *testing.T, w *harness.RemoteWorld, name, target string, actions api.Actions, includes ...string) {
	t.Helper()
	if err := w.Runtime.Store.Users().Create(t.Context(), accesstest.User{Name: name, PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := w.Runtime.Store.Permissions().
		Create(t.Context(), accesstest.Permission{Name: name, Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{name: actions}}, Targets: map[string]accesstest.Target{target: {Include: includes}}}}}); err != nil {
		t.Fatal(err)
	}
}

func TestAccNPMVirtualWritePermissions(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		t.Run(fmt.Sprint(hidden), func(t *testing.T) {
			w := virtualNPMWorld(t, npmfmt.Settings{})
			virtualNPMDeploy(t, w, "first")
			if err := w.Runtime.Store.Settings().Put(t.Context(), settings.SettingAnonymousAccess, true); err != nil {
				t.Fatal(err)
			}
			virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.HideUnauthorizedResources = new(hidden) })
			virtualNPMLocal(t, w, "first", "pkg", "1.0.0", "one")
			for _, user := range []string{"denied", ""} {
				for _, tc := range []struct{ method, path, body, permissionPath string }{{"PUT", "pkg", npmPublishBody("pkg", "2.0.0"), "pkg"}, {"PUT", "pkg", `{"versions":{"1.0.0":{"deprecated":"gone"}}}`, "pkg"}, {"PUT", "-/package/pkg/dist-tags/next", `"1.0.0"`, "pkg"}, {"DELETE", "-/package/pkg/dist-tags/next", "", "pkg"}, {"PUT", "pkg/-rev/1-0", `{"versions":{}}`, "pkg"}, {"DELETE", "pkg/-rev/1-0", "", "pkg"}, {"DELETE", npmfmt.CanonicalPath("pkg", "1.0.0") + "/-rev/1-0", "", npmfmt.CanonicalPath("pkg", "1.0.0")}} {
					r := w.Request(tc.method, npmBase+"virtual/"+tc.path, user, tc.body)
					status := 403
					if user == "" {
						status = 401
					}
					if hidden {
						status = 404
					}
					harness.WantStatus(t, r, status)
					if hidden && !strings.Contains(r.Body.String(), "Resource not found; Path: 'virtual:"+tc.permissionPath+"'") {
						t.Fatal(r.Body.String())
					}
				}
			}
			// Folder admission does not grant the actual version's path.
			virtualNPMGrant(t, w, "narrow", "first", api.ActionWrite, "pkg/-/pkg-9.0.0.tgz")
			r := w.Request("PUT", npmBase+"virtual/pkg", "narrow", npmPublishBody("pkg", "2.0.0"))
			status := 403
			if hidden {
				status = 404
			}
			harness.WantStatus(t, r, status)
			if hidden && !strings.Contains(r.Body.String(), "virtual:pkg/-/pkg-2.0.0.tgz") {
				t.Fatal(r.Body.String())
			}
			virtualNPMGrant(t, w, "publisher", "first", api.ActionWrite)
			harness.WantStatus(t, w.Request("PUT", npmBase+"virtual/pkg", "publisher", npmPublishBody("pkg", "2.0.0")), 201)
			harness.WantStatus(t, w.Request("PUT", npmBase+"virtual/-/package/pkg/dist-tags/next", "publisher", `"2.0.0"`), status)
			harness.WantStatus(t, w.Request("GET", npmBase+"first/pkg/1.0.0", "admin", ""), 200)
		})
	}
}

func TestAccNPMVirtualWriteReadTargetAndPolicy(t *testing.T) {
	w := virtualNPMWorld(t, npmfmt.Settings{})
	virtualNPMDeploy(t, w, "first")
	virtualNPMLocal(t, w, "first", "pkg", "1.0.0", "DDR")
	virtualNPMLocal(t, w, "second", "pkg", "2.0.0", "other")
	virtualNPMGrant(t, w, "other-reader", "second", api.ActionRead)
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/pkg", "other-reader", ""), 200)
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/pkg?write=true", "other-reader", ""), 403)
	virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.HideUnauthorizedResources = new(true) })
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/pkg?write=true", "other-reader", ""), 404)
	for _, key := range []string{"virtual", "first"} {
		t.Run(key, func(t *testing.T) {
			virtualNPMConfig(t, w, key, func(c *repo.Config) { c.ExcludesPattern = new(".npm/**") })
			harness.WantStatus(t, w.Request("GET", npmBase+"virtual/pkg?write=true", "admin", ""), 404)
			virtualNPMConfig(t, w, key, func(c *repo.Config) { c.ExcludesPattern = new("") })
		})
	}
}

func TestAccNPMVirtualWritesRetainMergedModel(t *testing.T) {
	w := virtualNPMWorld(t, npmfmt.Settings{})
	virtualNPMDeploy(t, w, "first")
	virtualNPMLocal(t, w, "first", "pkg", "1.0.0", "one")
	read := func() *bytes.Buffer {
		r := w.Request("GET", npmBase+"virtual/pkg", "admin", "")
		harness.WantStatus(t, r, 200)
		return r.Body
	}
	before := append([]byte(nil), read().Bytes()...)
	harness.WantStatus(t, w.Request("PUT", npmBase+"virtual/pkg", "admin", npmPublishBody("pkg", "2.0.0")), 201)
	harness.WantStatus(t, w.Request("PUT", npmBase+"virtual/-/package/pkg/dist-tags/next", "admin", `"2.0.0"`), 200)
	harness.WantStatus(t, w.Request("PUT", npmBase+"virtual/pkg", "admin", `{"versions":{"1.0.0":{"deprecated":"old"}}}`), 200)
	if !bytes.Equal(before, read().Bytes()) {
		t.Fatal("write invalidated merged model")
	}
	doc := w.Request("GET", npmBase+"virtual/pkg?write=true", "admin", "")
	harness.WantStatus(t, doc, 200)
	if !strings.Contains(doc.Body.String(), "2.0.0") || !strings.Contains(doc.Body.String(), "old") {
		t.Fatal(doc.Body.String())
	}
	if !bytes.Equal(before, read().Bytes()) {
		t.Fatal("write read changed merged model")
	}
	harness.WantStatus(t, w.Request("DELETE", npmBase+"virtual/pkg/-/pkg-2.0.0.tgz/-rev/1-0", "admin", ""), 200)
	if !bytes.Equal(before, read().Bytes()) {
		t.Fatal("delete invalidated merged model")
	}
	virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.VirtualRetrievalCachePeriodSecs = new(-1) })
	if bytes.Equal(before, read().Bytes()) {
		t.Fatal("disabled cache did not refresh")
	}
}

func TestAccNPMVirtualAccounts(t *testing.T) {
	w := virtualNPMWorld(t, npmfmt.Settings{}) // Accounts never need a deployment target.
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/-/ping", "reader", ""), 200)
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/-/whoami", "reader", ""), 200)
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/-/whoami", "", ""), 401)
	r := w.Request("PUT", npmBase+"virtual/-/user/org.couchdb.user:reader", "", npmLoginBody("reader", "pw"))
	harness.WantStatus(t, r, 201)
	var answer struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &answer); err != nil || answer.Token == "" {
		t.Fatal(r.Body.String(), err)
	}
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/-/whoami", "", "", "Authorization", "Bearer "+answer.Token), 200)
	harness.WantStatus(t, w.Request("DELETE", npmBase+"virtual/-/user/token/"+answer.Token, "", ""), 200)
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/-/whoami", "", "", "Authorization", "Bearer "+answer.Token), 401)
	harness.WantStatus(t, w.Request("PUT", npmBase+"virtual/-/user/org.couchdb.user:reader", "", "not json"), 400)
}

func TestAccNPMVirtualMutationRights(t *testing.T) {
	w := virtualNPMWorld(t, npmfmt.Settings{})
	virtualNPMDeploy(t, w, "first")
	virtualNPMLocal(t, w, "first", "pkg", "1.0.0", "one")
	virtualNPMLocal(t, w, "first", "pkg", "2.0.0", "two")
	virtualNPMGrant(t, w, "annotator", "first", api.ActionAnnotate)
	virtualNPMGrant(t, w, "deleter", "first", api.ActionDelete)
	virtualNPMGrant(t, w, "version-deleter", "first", api.ActionDelete, "pkg/-/pkg-1.0.0.tgz")
	virtualNPMGrant(t, w, "partial-annotator", "first", api.ActionAnnotate, "pkg/-/pkg-2.0.0.tgz")
	base := npmBase + "virtual/"
	harness.WantStatus(t, w.Request("PUT", base+"-/package/pkg/dist-tags/next", "annotator", `"1.0.0"`), 200)
	// Moving a tag needs Annotate on both the old and new holder. Refuse atomically.
	harness.WantStatus(t, w.Request("PUT", base+"-/package/pkg/dist-tags/next", "partial-annotator", `"2.0.0"`), 403)
	r := w.Request("GET", npmBase+"first/-/package/pkg/dist-tags", "admin", "")
	if virtualNPMJSON(t, r.Body.Bytes())["next"] != "1.0.0" {
		t.Fatal(r.Body.String())
	}
	harness.WantStatus(t, w.Request("PUT", base+"pkg", "annotator", `{"versions":{"1.0.0":{"deprecated":"old"}}}`), 200)
	harness.WantStatus(t, w.Request("DELETE", base+"-/package/pkg/dist-tags/next", "annotator", ""), 200)
	// A whole-package deletion checks every actual tarball before removing any.
	harness.WantStatus(t, w.Request("DELETE", base+"pkg/-rev/1-0", "version-deleter", ""), 403)
	harness.WantStatus(t, w.Request("GET", npmBase+"first/pkg/1.0.0", "admin", ""), 200)
	harness.WantStatus(t, w.Request("DELETE", base+npmfmt.CanonicalPath("pkg", "1.0.0")+"/-rev/1-0", "version-deleter", ""), 200)
	harness.WantStatus(t, w.Request("DELETE", base+"pkg/-rev/1-0", "deleter", ""), 200)
	harness.WantStatus(t, w.Request("GET", npmBase+"first/pkg", "admin", ""), 404)
}
