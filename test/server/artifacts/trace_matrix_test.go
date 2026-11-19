package artifacts_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// Matrix selection keeps its ordinary response; only the trace gains the
// getInfo step before an exact, missing-key or conflicting result (#1636).
func TestAccTraceMatrixQueryProperties(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := traceMatrixSQLWorld(t, backend)
			const includes = "Request includes query properties"
			const conflict = "Request query properties conflict with those that annotate the artifact - returning unfound resource"
			const lacks = "Request query properties don't match those that annotate the artifact"
			for _, c := range []struct {
				name, path, body string
				status, count    int
				order            []string
			}{
				{
					"direct-exact", "trace-exact/d/a.txt;team=core", "exact", 200, 1,
					[]string{includes, "Returning found resource from trace-exact:d/a.txt"},
				},
				{
					"direct-lacks-key", "trace-exact/d/a.txt;unknown=value", "exact", 200, 1,
					[]string{includes, lacks, "Returning found resource from trace-exact:d/a.txt"},
				},
				{
					"direct-conflict", "trace-exact/d/a.txt;team=other", "", 404, 1,
					[]string{includes, conflict, "Responding with unfound resource"},
				},
				{
					"mandatory-missing-key", "trace-exact/d/a.txt;unknown+=value", "", 404, 1,
					[]string{includes, conflict, "Responding with unfound resource"},
				},
				{"nested-exact", "trace-outer/d/a.txt;team=core", "exact", 200, 3, []string{
					"Searching for the resource within trace-miss",
					"Unable to find resource in trace-miss:d/a.txt",
					"Searching for the resource within trace-conflict", includes, conflict,
					"Searching for the resource within trace-lacks", includes, lacks,
					"Resource is not an exact match - keeping as closest match",
					"Searching for the resource within trace-exact", includes,
					"Returning found resource from trace-exact:d/a.txt",
				}},
				{"nested-closest", "trace-outer/d/a.txt;team=unknown", "closest", 200, 3, []string{
					"Searching for the resource within trace-conflict", includes, conflict,
					"Searching for the resource within trace-lacks", includes, lacks,
					"Searching for the resource within trace-exact", includes, conflict,
					"Unable to find an exact matching resource - returning closest match",
					"Returning found resource from trace-lacks:d/a.txt",
				}},
				{"no-selectors", "trace-exact/d/a.txt", "exact", 200, 0, nil},
				{
					"empty-selector", "trace-exact/d/a.txt;team=", "", 404, 1,
					[]string{includes, conflict, "Responding with unfound resource"},
				},
				{"missing-direct", "trace-exact/d/none.txt;team=core", "", 404, 0, nil},
				{"missing-nested", "trace-outer/d/none.txt;team=core", "", 404, 0, nil},
			} {
				t.Run(c.name, func(t *testing.T) {
					path := "/artifactory/" + c.path
					_, steps := traceResponse(t, w.Request(http.MethodGet, path+"?trace", "admin", ""), path)
					var count int
					for _, step := range steps {
						if step == includes {
							count++
						}
					}
					if count != c.count {
						t.Fatalf("includes-query steps = %d, want %d; steps: %v", count, c.count, steps)
					}
					remaining := steps
					for _, want := range c.order {
						i := slices.Index(remaining, want)
						if i < 0 {
							t.Fatalf("missing ordered step %q after %v; all steps: %v", want, remaining, steps)
						}
						remaining = remaining[i+1:]
					}
					for _, method := range []string{http.MethodGet, http.MethodHead} {
						r := w.Request(method, path, "admin", "")
						wantStatus(t, r, c.status)
						if method == http.MethodGet && c.status == http.StatusOK && r.Body.String() != c.body {
							t.Fatalf("download body = %q, want %q", r.Body, c.body)
						}
						if method == http.MethodHead && c.status == http.StatusOK && r.Body.Len() != 0 {
							t.Fatalf("HEAD returned a body: %s", r.Body)
						}
					}
					head := w.Request(http.MethodHead, path+"?trace", "admin", "")
					wantStatus(t, head, http.StatusOK)
					if head.Body.Len() != 0 {
						t.Fatalf("traced HEAD returned a body: %s", head.Body)
					}
				})
			}
			t.Run("folder", func(t *testing.T) {
				const path = "/artifactory/trace-exact/d;unknown=value?trace"
				_, steps := traceResponse(t, w.Request(http.MethodGet, path, "admin", ""), path)
				if slices.Contains(steps, includes) || !slices.Contains(steps, "Identified requested resource as a folder") {
					t.Fatalf("folder trace changed: %v", steps)
				}
			})
			t.Run("denied", func(t *testing.T) {
				const path = "/artifactory/trace-exact/d/a.txt;team=core?trace"
				_, steps := traceResponse(t, w.Request(http.MethodGet, path, "denied", ""), path)
				if slices.Contains(steps, includes) || !strings.Contains(strings.Join(steps, "\n"), "status '403'") {
					t.Fatalf("denied trace disclosed properties or changed refusal: %v", steps)
				}
			})
		})
	}
}

func TestAccTraceCachedRemoteQueryProperties(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := newVirtualRemoteWorld(t, backend)
			const file = "d/a.bin"
			w.upA.Set("/"+file, fakeupstream.Resource{Body: []byte("cached")})
			wantFetched(t, "warm copy", w.do(http.MethodGet,
				"/artifactory/remote-a/"+file, "reader", ""), 200, "cached")
			it, err := w.rt.Store.Items().Get(t.Context(), "remote-a", true, file)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.rt.Store.Items().SetProperties(t.Context(), it.ID,
				model.Properties{"team": {"core"}}, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			for _, c := range []struct {
				name, key, params string
				count, status     int
			}{
				{"direct-exact", "remote-a", ";team=core", 1, 200},
				{"direct-lacks-key", "remote-a", ";unknown=value", 1, 200},
				{"direct-conflict", "remote-a", ";team=other", 1, 404},
				{"direct-mandatory", "remote-a", ";unknown+=value", 1, 404},
				{"cache-alias-exact", "remote-a-cache", ";team=core", 1, 200},
				{"cache-alias-conflict", "remote-a-cache", ";team=other", 1, 404},
				{"virtual-exact", "pair", ";team=core", 1, 200},
				{"virtual-conflict-cache-and-upstream", "pair", ";team=other", 2, 404},
				{"no-selectors", "remote-a", "", 0, 200},
			} {
				t.Run(c.name, func(t *testing.T) {
					path := "/artifactory/" + c.key + "/" + file + c.params
					before := len(w.upA.Requests())
					_, steps := traceResponse(t, w.do(http.MethodGet, path+"?trace", "reader", ""), path)
					wantTraceQueryCount(t, steps, c.count)
					if c.status == 404 {
						at := slices.Index(steps, "Request includes query properties")
						verdict := slices.IndexFunc(steps, func(s string) bool {
							return strings.Contains(s, "property doesn't match") || strings.Contains(s, "properties conflict")
						})
						if at < 0 || verdict <= at {
							t.Fatalf("prefix must precede conflict: %v", steps)
						}
					}
					got := w.do(http.MethodGet, path, "reader", "")
					wantStatus(t, got, c.status)
					if c.status == 200 && got.Body.String() != "cached" {
						t.Fatalf("download changed: %s", got.Body)
					}
					if len(w.upA.Requests()) != before {
						t.Fatalf("matched cache copy contacted its upstream: %v", w.upA.Requests()[before:])
					}
				})
			}
			permission, err := w.rt.Store.Permissions().Get(t.Context(), "viewer-a")
			if err != nil {
				t.Fatal(err)
			}
			resource := permission.Resources[accessapi.ResourceArtifact]
			target := resource.Targets["remote-a"]
			target.Exclude = []string{file}
			resource.Targets["remote-a"] = target
			permission.Resources[accessapi.ResourceArtifact] = resource
			if err := w.rt.Store.Permissions().Update(t.Context(), permission); err != nil {
				t.Fatal(err)
			}
			path := "/artifactory/remote-a/" + file + ";unknown=value"
			_, steps := traceResponse(t, w.do(http.MethodGet, path+"?trace", "viewer", ""), path)
			wantTraceQueryCount(t, steps, 0)
			if !strings.Contains(strings.Join(steps, "\n"), "status '403'") {
				t.Fatalf("excluded-file trace changed: %v", steps)
			}
			wantStatus(t, w.do(http.MethodGet, path, "viewer", ""), http.StatusForbidden)
			// Descendant-only access admits folder visibility at this path,
			// while granting no Read of the file actually held there.
			target.Include, target.Exclude = []string{file + "/visible/**"}, nil
			resource.Targets["remote-a"] = target
			permission.Resources[accessapi.ResourceArtifact] = resource
			if err := w.rt.Store.Permissions().Update(t.Context(), permission); err != nil {
				t.Fatal(err)
			}
			_, steps = traceResponse(t, w.do(http.MethodGet, path+"?trace", "viewer", ""), path)
			wantTraceQueryCount(t, steps, 0)
			if !slices.Contains(steps, "Retrieving info from remote repository 'remote-a' type generic") ||
				!strings.Contains(strings.Join(steps, "\n"), "status '403'") {
				t.Fatalf("folder-visible/file-denied trace changed: %v", steps)
			}
			wantStatus(t, w.do(http.MethodGet, path, "viewer", ""), http.StatusForbidden)
			// Existing internal zero-TTL fixture: the virtual cache lookup and
			// remote getInfo each evaluate the expired copy. Delivery must not
			// add a third event (RemoteRepoBase.internalGetInfo, 7.161.26).
			w.update(repo.Config{
				Key: "maven-remote", Class: repo.Remote, PackageType: repo.Maven, URL: w.upMaven.URL,
				RetrievalCachePeriodSecs: new(0),
			})
			const snapshot = "org/acme/a/1.0-SNAPSHOT/a-1.0-SNAPSHOT.jar"
			w.upMaven.Set("/"+snapshot, fakeupstream.Resource{Body: []byte("snapshot")})
			w.upMaven.Set("/"+snapshot+";unknown=value", fakeupstream.Resource{Body: []byte("snapshot")})
			wantFetched(t, "warm mutable copy", w.do(http.MethodGet,
				"/artifactory/maven-remote/"+snapshot, "reader", ""), 200, "snapshot")
			w.update(repo.Config{
				Key: "pair", Class: repo.Virtual, PackageType: repo.Generic,
				Repositories: []string{"maven-remote"},
			})
			path = "/artifactory/pair/" + snapshot + ";unknown=value"
			_, steps = traceResponse(t, w.do(http.MethodGet, path+"?trace", "reader", ""), path)
			wantTraceQueryCount(t, steps, 2)
			if !slices.Contains(steps, "Returning resource as expired") ||
				!slices.Contains(steps, "Request succeeded") {
				t.Fatalf("expired copy was not revalidated successfully: %v", steps)
			}
			// A cold upstream answer is accepted without re-matching the
			// newly cached item; its parameters were sent to the upstream.
			w.upA.Set("/cold.bin;unknown=value", fakeupstream.Resource{Body: []byte("cold")})
			path = "/artifactory/remote-a/cold.bin;unknown=value"
			_, steps = traceResponse(t, w.do(http.MethodGet, path+"?trace", "reader", ""), path)
			wantTraceQueryCount(t, steps, 0)
			if !slices.Contains(steps, "Request succeeded") {
				t.Fatalf("cold source answer changed: %v", steps)
			}
		})
	}
}

func wantTraceQueryCount(t *testing.T, steps []string, want int) {
	t.Helper()
	var count int
	for _, step := range steps {
		if step == "Request includes query properties" {
			count++
		}
	}
	if count != want {
		t.Fatalf("query-property step count = %d, want %d: %v", count, want, steps)
	}
}

func traceMatrixSQLWorld(t *testing.T, backend string) *harness.RemoteWorld {
	t.Helper()
	w := harness.NewRemoteWorld(t, func(c *config.Config) {
		c.Database.Driver = backend
		if backend == "postgres" {
			c.Database.DSN = sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
		}
	})
	for _, c := range []repo.Config{
		{Key: "trace-miss", Class: repo.Local, PackageType: repo.Generic},
		{Key: "trace-conflict", Class: repo.Local, PackageType: repo.Generic},
		{Key: "trace-lacks", Class: repo.Local, PackageType: repo.Generic},
		{Key: "trace-exact", Class: repo.Local, PackageType: repo.Generic},
		{
			Key: "trace-inner", Class: repo.Virtual, PackageType: repo.Generic,
			Repositories: []string{"trace-miss", "trace-conflict", "trace-lacks", "trace-exact"},
		},
		{
			Key: "trace-outer", Class: repo.Virtual, PackageType: repo.Generic,
			Repositories: []string{"trace-inner", "trace-inner"},
		},
	} {
		body, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		r := w.Request(http.MethodPut, "/artifactory/api/repositories/"+c.Key, "admin", string(body), "Content-Type", "application/json")
		wantStatus(t, r, http.StatusOK)
	}
	for _, c := range []struct{ path, body string }{
		{"trace-conflict/d/a.txt;team=other", "conflict"},
		{"trace-lacks/d/a.txt", "closest"},
		{"trace-exact/d/a.txt;team=core", "exact"},
	} {
		wantStatus(t, w.Request(http.MethodPut, "/artifactory/"+c.path, "admin", c.body), http.StatusCreated)
	}
	return w
}
