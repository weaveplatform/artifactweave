package rpm_test

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func rpmPolicyWorld(t *testing.T, backend string) *harness.RemoteWorld {
	t.Helper()
	var mutate []func(*config.Config)
	if backend == "postgres" {
		dsn := sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
		mutate = append(mutate, func(c *config.Config) { c.Database = config.Database{Driver: "postgres", DSN: dsn} })
	}
	w := harness.NewRemoteWorld(t, mutate...)
	w.Repo.PackageType = repo.RPM
	w.Update()
	return w
}

func TestAccRPMRemoteMetadataPolicyNamesRequestedFile(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, root := range []string{"", "nested/repository/"} {
			for _, policy := range []string{"blackout", "exclude"} {
				t.Run(backend+"/"+root+"/"+policy, func(t *testing.T) {
					w := rpmPolicyWorld(t, backend)
					if policy == "blackout" {
						w.Repo.BlackedOut = new(true)
					} else {
						w.Repo.ExcludesPattern = new("**/repomd.xml,**/*-primary.xml.gz")
					}
					w.Update()
					for _, name := range []string{root + "repodata/repomd.xml", root + "repodata/" + strings.Repeat("a", 64) + "-primary.xml.gz"} {
						for _, key := range []string{"remote", "remote-cache"} {
							for _, method := range []string{http.MethodGet, http.MethodHead} {
								rec := w.Request(method, "/artifactory/"+key+"/"+name, "admin", "")
								reason := fmt.Sprintf("The repository '%s' is blacked out and cannot serve artifact '%s:%s'.", key, key, name)
								if policy == "exclude" {
									reason = fmt.Sprintf("The repository '%s' rejected the artifact '%s:%s' due to its include/exclude pattern settings.", key, key, name)
								}
								wantRefusal(t, rec, http.StatusNotFound, reason+"; Path: '"+key+":"+name+"'")
							}
						}
					}
					if got := w.Upstream.Requests(); len(got) != 0 {
						t.Fatalf("refused metadata reached upstream: %+v", got)
					}
					if policy == "exclude" {
						rpmPolicyAcceptedPackage(t, w)
					}
				})
			}
		}
	}
}

func TestAccRPMExpiredMetadataRetainsRepositoryFile(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := rpmPolicyWorld(t, backend)
			w.Repo.RetrievalCachePeriodSecs = new(0)
			w.Update()
			const name = "repodata/repomd.xml"
			body, err := os.ReadFile(repopath.Path(t, "test/acceptance/testdata/rpm-upstream/repodata/repomd.xml"))
			if err != nil {
				t.Fatal(err)
			}
			w.Upstream.Set("/"+name, fakeupstream.Resource{Body: body})
			before := w.Request(http.MethodGet, "/artifactory/remote/"+name, "admin", "")
			if before.Code != http.StatusOK {
				t.Fatal(before.Code, before.Body.String())
			}
			w.Upstream.Set("/"+name, fakeupstream.Resource{Status: http.StatusNotFound})
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				after := w.Request(method, "/artifactory/remote/"+name, "admin", "")
				if after.Code != http.StatusOK || (method == http.MethodGet && !bytes.Equal(after.Body.Bytes(), body)) {
					t.Fatal("physical RPM metadata adopted API miss semantics", method, after.Code, after.Body.String())
				}
			}
		})
	}
}

func rpmPolicyAcceptedPackage(t *testing.T, w *harness.RemoteWorld) {
	t.Helper()
	const name = "pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm"
	body, err := os.ReadFile(repopath.Path(t, "internal/artifacts/ecosystem/rpm/testdata/artifactweave-alpha-1.0-1.fc42.noarch.rpm"))
	if err != nil {
		t.Fatal(err)
	}
	w.Upstream.Set("/"+name, fakeupstream.Resource{Body: body})
	for _, key := range []string{"remote", "remote-cache"} {
		rec := w.Request(http.MethodGet, "/artifactory/"+key+"/"+name, "admin", "")
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), body) {
			t.Fatalf("allowed RPM on %s: %d %s", key, rec.Code, rec.Body)
		}
	}
	if got := w.Upstream.Count(http.MethodGet, "/"+name); got != 1 {
		t.Fatalf("package upstream requests=%d,want1", got)
	}
}

func TestAccRPMRemoteMetadataRetainsNarrowReaderRefusal(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := rpmPolicyWorld(t, backend)
			permission := accesstest.Permission{Name: "remote-read", Resources: map[api.ResourceType]accesstest.Resource{
				api.ResourceArtifact: {
					Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead | api.ActionWrite}},
					Targets: map[string]accesstest.Target{"remote": {Include: []string{"pool/**"}}},
				},
			}}
			if err := w.Runtime.Store.Permissions().Update(t.Context(), permission); err != nil {
				t.Fatal(err)
			}
			const name = "repodata/repomd.xml"
			body, err := os.ReadFile(repopath.Path(t, "test/acceptance/testdata/rpm-upstream/repodata/repomd.xml"))
			if err != nil {
				t.Fatal(err)
			}
			w.Upstream.Set("/"+name, fakeupstream.Resource{Body: body})
			if rec := w.Request(http.MethodGet, "/artifactory/remote/"+name, "admin", ""); rec.Code != http.StatusOK {
				t.Fatalf("warm metadata: %d %s", rec.Code, rec.Body)
			}
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				wantRefusal(t, w.Request(method, "/artifactory/remote/"+name, "reader", ""), http.StatusForbidden,
					"Download request for repo:path 'remote-cache:repodata/repomd.xml' is forbidden for user: 'reader'.; Path: 'remote:repodata/repomd.xml'")
			}
			before := len(w.Upstream.Requests())
			w.Repo.ExcludesPattern = new("**/repomd.xml")
			w.Update()
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				denied := w.Request(method, "/artifactory/remote/"+name, "denied", "")
				if denied.Code != http.StatusForbidden || strings.Contains(denied.Body.String(), "pattern settings") {
					t.Fatalf("Read must precede policy: %d %s", denied.Code, denied.Body)
				}
			}
			if len(w.Upstream.Requests()) != before {
				t.Fatal("refusal reached upstream")
			}
		})
	}
}

func TestAccRPMVirtualPackageDiscoveryKeepsMetadataPatternsSeparate(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		for _, store := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/store=%t", backend, store), func(t *testing.T) {
				w := rpmPolicyWorld(t, backend)
				w.Repo.ExcludesPattern = new("repodata/**")
				w.Repo.StoreArtifactsLocally = new(store)
				w.Update()
				c := *repo.Defaults(&repo.Config{Key: "rpm-virtual", Class: repo.Virtual, PackageType: repo.RPM, Repositories: []string{"remote"}})
				if err := w.Runtime.Store.Repos().Create(t.Context(), c); err != nil {
					t.Fatal(err)
				}
				dir := repopath.Path(t, "test/acceptance/testdata/rpm-upstream/repodata")
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
					if err != nil {
						t.Fatal(err)
					}
					w.Upstream.Set("/repodata/"+entry.Name(), fakeupstream.Resource{Body: data})
				}
				const name = "pool/artifactweave-alpha-1.0-1.fc42.noarch.rpm"
				body, err := os.ReadFile(repopath.Path(t, "internal/artifacts/ecosystem/rpm/testdata/artifactweave-alpha-1.0-1.fc42.noarch.rpm"))
				if err != nil {
					t.Fatal(err)
				}
				w.Upstream.Set("/"+name, fakeupstream.Resource{Body: body})
				route := "/artifactory/rpm-virtual/" + name
				rec := w.Request(http.MethodGet, route, "admin", "")
				if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), body) {
					t.Fatalf("package discovery under metadata exclusion: %d %s", rec.Code, rec.Body)
				}
				if w.Upstream.Count(http.MethodGet, "/repodata/repomd.xml") != 1 {
					t.Fatal("package did not use the source metadata lookup")
				}
				before := len(w.Upstream.Requests())
				for _, key := range []string{"remote", "remote-cache"} {
					reason := fmt.Sprintf(
						"The repository '%s' rejected the artifact '%s:repodata/repomd.xml' due to its include/exclude pattern settings.; Path: '%s:repodata/repomd.xml'",
						key,
						key,
						key,
					)
					wantRefusal(t, w.Request(http.MethodGet, "/artifactory/"+key+"/repodata/repomd.xml", "admin", ""), http.StatusNotFound, reason)
				}
				w.Repo.BlackedOut = new(true)
				w.Update()
				if rec := w.Request(http.MethodGet, route, "admin", ""); rec.Code != http.StatusNotFound {
					t.Fatalf("blackout package=%d %s", rec.Code, rec.Body)
				}
				w.Repo.BlackedOut = new(false)
				w.Repo.ExcludesPattern = new("repodata/**,pool/**")
				w.Update()
				if rec := w.Request(http.MethodGet, route, "admin", ""); rec.Code != http.StatusNotFound {
					t.Fatalf("excluded package=%d %s", rec.Code, rec.Body)
				}
				if len(w.Upstream.Requests()) != before {
					t.Fatal("refusal performed an additional upstream exchange")
				}
			})
		}
	}
}
