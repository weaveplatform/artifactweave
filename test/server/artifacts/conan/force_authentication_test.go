package conan_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// With anonymous access on, a local, remote or virtual Conan repository
// with forceConanAuthentication answers a request without credentials 401,
// with the Basic challenge, before it routes it, so the Conan client learns
// to send the credentials it holds ([D] conan-repositories, "Force
// Authentication"). Signed in, the same requests keep their answers, and so
// do the anonymous ones once the flag is off (#1440).
func TestAccConanForceAuthentication(t *testing.T) {
	const revisions = `{"revisions":[{"revision":"abc","time":"2026-10-24T00:00:00Z"}]}`
	svc := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
				if q.Resource.UpstreamPath != "v2/conans/hello/1.0/_/_/revisions" {
					t.Errorf("unexpected upstream %q", q.Resource.UpstreamPath)
					return source.Result{}, &source.HTTPError{Status: http.StatusNotFound}
				}
				return source.Result{Stream: &source.Stream{
					Body:   io.NopCloser(strings.NewReader(revisions)),
					Status: http.StatusOK,
					Representation: source.Representation{
						Size: int64(len(revisions)), MediaType: "application/json",
					},
				}}, nil
			},
		),
	}
	w := newItemWorld(t, true, func(c *Config) { c.RemoteSources = svc })
	keys := []string{"conan-local", "conan-remote", "conan-virtual"}
	for _, c := range []repo.Config{
		{Key: keys[0], Class: repo.Local, PackageType: repo.Conan},
		{
			Key: keys[1], Class: repo.Remote, PackageType: repo.Conan,
			URL: "https://up.example/artifactory/api/conan/up",
		},
		{Key: keys[2], Class: repo.Virtual, PackageType: repo.Conan, Repositories: keys[:1]},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	// The anonymous user may read all three, so only the flag stands between
	// it and an answer.
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "anonymous-conan",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{
				kernel.AnonymousUser: api.ActionRead,
			}},
			Targets: map[string]accesstest.Target{
				keys[0]: {Include: []string{"**"}}, keys[2]: {Include: []string{"**"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	const recipe = "/v2/conans/hello/1.0/_/_/revisions"
	files := "/artifactory/api/conan/conan-local" + recipe + "/abc/files/"
	for _, f := range []struct{ name, body string }{
		{"conanfile.py", "local recipe"},
		{"conanmanifest.txt", fmt.Sprintf("1\nconanfile.py: %s\n", md5Hex("local recipe"))},
	} {
		rec := w.do(http.MethodPut, files+f.name, "admin", f.body)
		if rec.Code != http.StatusCreated {
			t.Fatalf("PUT %s: %d %s", f.name, rec.Code, rec.Body)
		}
	}
	const required = `{"errors":[{"message":"Authentication is required","status":401}]}` + "\n"
	served := func(key, path, as string, flag bool) {
		t.Helper()
		rec := w.do(http.MethodGet, "/artifactory/api/conan/"+key+path, as, "")
		want := "\"abc\""
		if strings.HasSuffix(path, "ping") {
			want = ""
		}
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s%s as %q, flag %v: %d %s, want 200",
				key, path, as, flag, rec.Code, rec.Body)
		}
	}
	force := func(key string, on bool) {
		t.Helper()
		set := func(c *repo.Config) { c.ForceConanAuthentication = new(on) }
		patchConan(t, w.db.Repos(), key, set)
	}
	for _, key := range keys {
		force(key, true)
		for _, path := range []string{"/v1/ping", recipe} {
			rec := w.do(http.MethodGet, "/artifactory/api/conan/"+key+path, "", "")
			if rec.Code != http.StatusUnauthorized || rec.Body.String() != required ||
				rec.Header().Get("WWW-Authenticate") != apierror.BasicChallenge {
				t.Errorf("%s%s anonymously: %d %q %q, want 401 %q with the Basic challenge",
					key, path, rec.Code, rec.Body, rec.Header().Get("WWW-Authenticate"), required)
			}
			served(key, path, "admin", true)
		}
		force(key, false)
		for _, path := range []string{"/v1/ping", recipe} {
			served(key, path, "", false)
			served(key, path, "admin", false)
		}
	}
}
