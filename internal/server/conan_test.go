package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestAccConanNativeRevisionLifecycle(t *testing.T) {
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{Key: "conan-local", Class: repo.Local, PackageType: repo.Conan},
		{Key: "conan-fed", Class: repo.Federated, PackageType: repo.Conan},
		{Key: "conan-virt", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-local"}, DefaultDeploymentRepo: "conan-local"},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	base := "/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/abc/files/"
	if rec := w.do(
		"GET",
		"/artifactory/api/conan/conan-local/v1/ping",
		"admin",
		"",
	); rec.Code != 200 {
		t.Fatalf("ping: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("PUT", base+"conanfile.py", "admin", "recipe"); rec.Code != 201 {
		t.Fatalf("file upload: %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{base + "conanfile.py", base + "conanmanifest.txt"} {
		if rec := w.do("GET", path, "admin", ""); rec.Code != 404 {
			t.Fatalf("uncommitted %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	wrong := fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex("changed"))
	if rec := w.do("PUT", base+"conanmanifest.txt", "admin", wrong); rec.Code != 422 {
		t.Fatalf("incorrect manifest checksum accepted: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"GET",
		"/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/latest",
		"admin",
		"",
	); rec.Code != 404 {
		t.Fatalf("invalid revision became visible: %d %s", rec.Code, rec.Body)
	}
	manifest := fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex("recipe"))
	if rec := w.do("PUT", base+"conanmanifest.txt", "admin", manifest); rec.Code != 201 {
		t.Fatalf("manifest upload: %d %s", rec.Code, rec.Body)
	}
	if packages, _, err := w.db.Packages().
		List(t.Context(), "conan-local", store.Page{}); err != nil ||
		len(packages) != 1 {
		t.Fatalf("committed recipe index: %d %v", len(packages), err)
	}
	for _, key := range []string{"conan-local", "conan-virt"} {
		prefix := "/artifactory/api/conan/" + key + "/v2/conans/hello/1.0/_/_"
		if rec := w.do("GET", prefix+"/latest", "admin", ""); rec.Code != 200 {
			t.Fatalf("%s latest: %d %s", key, rec.Code, rec.Body)
		}
		if rec := w.do(
			"GET",
			prefix+"/revisions/abc/files/conanfile.py",
			"admin",
			"",
		); rec.Code != 200 ||
			rec.Body.String() != "recipe" {
			t.Fatalf("%s read: %d %s", key, rec.Code, rec.Body)
		}
		if rec := w.do("GET", prefix+"/revisions/abc/files", "admin", ""); rec.Code != 200 {
			t.Fatalf("%s files: %d %s", key, rec.Code, rec.Body)
		} else {
			var v map[string]any
			if json.Unmarshal(rec.Body.Bytes(), &v) != nil {
				t.Fatal(rec.Body)
			}
		}
	}
	for _, tc := range []struct {
		as   string
		want int
	}{{"user", 403}, {"", 401}} {
		if rec := w.do("GET", base+"conanfile.py", tc.as, ""); rec.Code != tc.want {
			t.Fatalf("read as %q: %d %s", tc.as, rec.Code, rec.Body)
		}
		if rec := w.do(
			"GET",
			"/artifactory/api/conan/conan-local/v2/conans/search?q=*",
			tc.as,
			"",
		); rec.Code != tc.want {
			t.Fatalf("search as %q: %d %s", tc.as, rec.Code, rec.Body)
		}
	}
	if rec := w.do(
		"PUT",
		"/artifactory/conan-local/_/hello/1.0/_/abc/export/conanfile.py",
		"admin",
		"changed",
	); rec.Code != 409 {
		t.Fatalf("generic bypass: %d %s", rec.Code, rec.Body)
	}
	setTrashcan(t, w.db, true)
	if rec := w.do(
		"DELETE",
		"/artifactory/api/conan/conan-local/v2/conans/hello/1.0/_/_/revisions/abc",
		"admin",
		"",
	); rec.Code != 204 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do("GET", base+"conanfile.py", "admin", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted read: %d %s", rec.Code, rec.Body)
	}
	if packages, _, err := w.db.Packages().
		List(t.Context(), "conan-local", store.Page{}); err != nil ||
		len(packages) != 0 {
		t.Fatalf("deleted recipe index: %d %v", len(packages), err)
	}
	if rec := w.do(
		"POST",
		"/artifactory/api/trash/restore/conan-local/_/hello/1.0/_/abc?to=conan-local",
		"admin",
		"",
	); rec.Code != 202 {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(
		"GET",
		base+"conanfile.py",
		"admin",
		"",
	); rec.Code != 200 ||
		rec.Body.String() != "recipe" {
		t.Fatalf("restored recipe: %d %s", rec.Code, rec.Body)
	}
	if packages, _, err := w.db.Packages().
		List(t.Context(), "conan-local", store.Page{}); err != nil ||
		len(packages) != 1 {
		t.Fatalf("restored recipe index: %d %v", len(packages), err)
	}
}

func TestAccConanRemoteAndVirtualSource(t *testing.T) {
	manifest := fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex("remote recipe"))
	var calls []string
	corrupt := false
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
				calls = append(calls, q.Resource.UpstreamPath)
				var body string
				switch q.Resource.UpstreamPath {
				case "v2/conans/hello/1.0/_/_/revisions":
					body = `{"revisions":[{"revision":"abc","time":"2026-10-24T00:00:00Z"}]}`
				case "v2/conans/hello/1.0/_/_/revisions/abc/files":
					body = `{"files":{"conanfile.py":{},"conanmanifest.txt":{}}}`
				case "v2/conans/hello/1.0/_/_/revisions/abc/files/conanmanifest.txt":
					body = manifest
				case "v2/conans/hello/1.0/_/_/revisions/abc/files/conanfile.py":
					body = "remote recipe"
					if corrupt {
						body = "tampered recipe"
					}
				default:
					t.Fatalf("unexpected upstream %q", q.Resource.UpstreamPath)
				}
				if q.Resource.Validate != nil {
					if err := q.Resource.Validate(
						t.Context(),
						bytes.NewReader([]byte(body)),
						int64(len(body)),
					); err != nil {
						return source.Result{}, &source.HTTPError{
							Status: 502,
							Kind:   source.IntegrityFailure,
						}
					}
				}
				if q.Resource.Checksums.MD5 != "" && q.Resource.Checksums.MD5 != md5Hex(body) {
					return source.Result{}, &source.HTTPError{
						Status: 502,
						Kind:   source.IntegrityFailure,
					}
				}
				return source.Result{
					Stream: &source.Stream{
						Body:   io.NopCloser(strings.NewReader(body)),
						Status: 200,
						Representation: source.Representation{
							Size:      int64(len(body)),
							MediaType: "application/json",
						},
					},
				}, nil
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = svc })
	for _, c := range []repo.Config{
		{Key: "conan-remote", Class: repo.Remote, PackageType: repo.Conan, URL: "https://up.example/artifactory/api/conan/up"},
		{Key: "conan-virtual", Class: repo.Virtual, PackageType: repo.Conan, Repositories: []string{"conan-remote"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"conan-remote", "conan-virtual"} {
		base := "/artifactory/api/conan/" + key + "/v2/conans/hello/1.0/_/_"
		for _, tc := range []struct{ path, body string }{
			{"/latest", "\"revision\":\"abc\""},
			{"/revisions/abc/files", "conanfile.py"},
			{"/revisions/abc/files/conanfile.py", "remote recipe"},
		} {
			rec := w.do("GET", base+tc.path, "admin", "")
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), tc.body) {
				t.Fatalf("%s%s: %d %s", key, tc.path, rec.Code, rec.Body)
			}
		}
	}
	if len(calls) < 6 {
		t.Fatalf("missing upstream requests: %v", calls)
	}
	corrupt = true
	if rec := w.do(
		"GET",
		"/artifactory/api/conan/conan-remote/v2/conans/hello/1.0/_/_/revisions/abc/files/conanfile.py",
		"admin",
		"",
	); rec.Code != 502 {
		t.Fatalf("upstream integrity failure: %d %s", rec.Code, rec.Body)
	}
}
