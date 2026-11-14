package debian_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// TestAccDebianRemoteIndexHeadAnswersItsLength: a HEAD of a remote Debian
// repository's Packages index and of its gzipped form answers 200 with the
// Content-Length, Content-Type and Cache-Control its GET answers, and no
// body, cold, before any GET, and warm, once the GET was served, on a
// remote that stores artifacts locally and on one that stores nothing,
// whose HEAD asks the upstream for a HEAD only. 7.161.26's
// RequestResponseHelper.sendHeadResponse sets Content-Length to the
// resource's size; the index route answered a HEAD Content-Length 0
// (#1478).
func TestAccDebianRemoteIndexHeadAnswersItsLength(t *testing.T) {
	files, _ := queryArchive(t)
	paths := []string{
		"dists/stable/main/binary-amd64/Packages", "dists/stable/main/binary-amd64/Packages.gz",
	}
	for _, store := range []bool{true, false} {
		t.Run("store="+strconv.FormatBool(store), func(t *testing.T) {
			w := harness.NewRemoteWorld(t)
			w.Repo.PackageType = repo.Debian
			w.Repo.StoreArtifactsLocally = new(store)
			w.Update()
			for _, p := range paths {
				w.Upstream.Set("/"+p, fakeupstream.Resource{
					Body:   files[p],
					Header: http.Header{"Content-Type": {"application/octet-stream"}},
				})
			}
			for _, p := range paths {
				target := "/artifactory/remote/" + p
				cold := w.Request(http.MethodHead, target, "reader", "")
				get := w.Request(http.MethodGet, target, "reader", "")
				warm := w.Request(http.MethodHead, target, "reader", "")
				if get.Code != http.StatusOK || get.Body.String() != string(files[p]) ||
					get.Header().Get("Content-Length") != strconv.Itoa(len(files[p])) {
					t.Fatalf("%s: GET answered %d with %q bytes", p, get.Code,
						get.Header().Get("Content-Length"))
				}
				for pass, head := range map[string]*httptest.ResponseRecorder{
					"cold": cold, "warm": warm,
				} {
					for _, name := range []string{"Content-Length", "Content-Type", "Cache-Control"} {
						if got, want := head.Header().
							Get(name),
							get.Header().
								Get(name); got != want {
							t.Errorf("%s %s HEAD: %s %q, the GET's %q", p, pass, name, got, want)
						}
					}
					if head.Code != http.StatusOK || head.Body.Len() != 0 {
						t.Errorf("%s %s HEAD: %d with %d bytes, want 200 and none", p, pass,
							head.Code, head.Body.Len())
					}
				}
				if got := w.Upstream.Count(http.MethodGet, "/"+p); got != 1 {
					t.Errorf("%s: the upstream was asked for %d GETs, want 1: %v", p, got,
						w.Upstream.Requests())
				}
			}
		})
	}
}

// TestAccDebianRemoteIndexHeadReadsByItsOwnPath: a caller who may read a
// remote Debian repository's Packages index by its path, but not the
// package it lists, is refused the index's GET, 403, which checks every
// listed package; its HEAD, cold and once cached, answers 200 with the
// index's length and no body. The HEAD is answered from the index's info,
// as 7.x reads a remote index by its own path's permission, and sends no
// package list (#1478).
func TestAccDebianRemoteIndexHeadReadsByItsOwnPath(t *testing.T) {
	files, _ := queryArchive(t)
	const p = "dists/stable/main/binary-amd64/Packages"
	for _, store := range []bool{true, false} {
		t.Run("store="+strconv.FormatBool(store), func(t *testing.T) {
			w := harness.NewRemoteWorld(t)
			w.Repo.PackageType = repo.Debian
			w.Repo.StoreArtifactsLocally = new(store)
			w.Update()
			w.Upstream.Set("/"+p, fakeupstream.Resource{Body: files[p]})
			if err := w.Runtime.Store.Users().Create(t.Context(), accesstest.User{
				Name: "indexonly", PasswordHash: support.CheapHash,
			}); err != nil {
				t.Fatal(err)
			}
			if err := w.Runtime.Store.Permissions().Create(t.Context(), accesstest.Permission{
				Name: "index-only", Resources: map[api.ResourceType]accesstest.Resource{
					api.ResourceArtifact: {
						Actions: accesstest.Grants{
							Users: map[string]api.Actions{"indexonly": api.ActionRead},
						},
						Targets: map[string]accesstest.Target{"remote": {
							Include: []string{"**"}, Exclude: []string{"pool/**"},
						}},
					},
				},
			}); err != nil {
				t.Fatal(err)
			}
			target := "/artifactory/remote/" + p
			cold := w.Request(http.MethodHead, target, "indexonly", "")
			harness.WantStatus(t, w.Request(http.MethodGet, target, "reader", ""), http.StatusOK)
			warm := w.Request(http.MethodHead, target, "indexonly", "")
			for pass, head := range map[string]*httptest.ResponseRecorder{"cold": cold, "warm": warm} {
				if head.Code != http.StatusOK || head.Body.Len() != 0 ||
					head.Header().Get("Content-Length") != strconv.Itoa(len(files[p])) {
					t.Errorf(
						"%s HEAD: %d, Content-Length %q, %d bytes; want 200, %d and none",
						pass,
						head.Code,
						head.Header().Get("Content-Length"),
						head.Body.Len(),
						len(files[p]),
					)
				}
			}
			queryRefusal(t, w.Request(http.MethodGet, target, "indexonly", ""),
				http.StatusForbidden, "Forbidden")
		})
	}
}
