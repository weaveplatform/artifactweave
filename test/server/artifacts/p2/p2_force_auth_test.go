package p2_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// forcedBody is the whole body of the 401 a P2 virtual repository with
// forceP2Authentication on answers an anonymous request: 7.x's
// force-authentication filter's ErrorResponse(401, "Unauthorized")
// (OssForceAuthenticationFilter, 7.161.26) in the Artifactory envelope.
const forcedBody = `{"errors":[{"message":"Unauthorized","status":401}]}` + "\n"

// wantForced fails the test unless rec is the forced-authentication 401:
// the status, the Basic challenge and the exact body.
func wantForced(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized ||
		rec.Header().Get("WWW-Authenticate") != apierror.BasicChallenge ||
		rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("%s: %d %v, want 401 with the Basic challenge", what, rec.Code, rec.Header())
	}
	if what != "HEAD" && rec.Body.String() != forcedBody {
		t.Errorf("%s: body %q, want %q", what, rec.Body, forcedBody)
	}
}

// p2IndexSource is a remote source over an update site that serves the
// published Eclipse p2.index and content.jar and records each path it is
// asked for.
func p2IndexSource(t *testing.T, calls *[]string) *source.Service {
	t.Helper()
	files := map[string][]byte{
		"p2.index":    p2Fixture(t, "p2.index"),
		"content.jar": p2Fixture(t, "content.jar"),
	}
	return &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			*calls = append(*calls, q.Resource.Path)
			body, ok := files[q.Resource.Path]
			if !ok {
				return source.Result{}, kernel.ErrNotFound
			}
			if err := q.Resource.ValidateMetadata(t.Context(), bytes.NewReader(body),
				int64(len(body)), source.MetadataResponse{}); err != nil {
				t.Fatalf("source request rejected published Eclipse metadata: %v", err)
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		},
	)}
}

// grantAnonymous lets the anonymous user read every path of each key.
func grantAnonymous(t *testing.T, w *itemWorld, keys ...string) {
	t.Helper()
	targets := map[string]accesstest.Target{}
	for _, k := range keys {
		targets[k] = accesstest.Target{Include: []string{"**"}}
	}
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "p2-anonymous",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{
				kernel.AnonymousUser: api.ActionRead,
			}},
			Targets: targets,
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestAccP2ForceAuthenticationOnAVirtual: with anonymous access on, a P2
// virtual repository with forceP2Authentication on answers each anonymous
// read 401 with the Basic challenge, though anonymous may read the virtual,
// a local member and every remote: its p2.index, GET and HEAD, its
// composites, a folder and each member's files under <member key>/, before
// the remote member's upstream is asked. The same virtual with the flag off
// answers anonymous a composite of the members it may read and serves their
// files, and a signed-in caller gets what it got before (#1280).
func TestAccP2ForceAuthenticationOnAVirtual(t *testing.T) {
	var calls []string
	w := newItemWorld(t, true, func(c *Config) { c.RemoteSources = p2IndexSource(t, &calls) })
	on := true
	members := []string{"p2-public", "p2-private", "p2-remote"}
	for _, c := range []repo.Config{
		{Key: "p2-public", Class: repo.Local, PackageType: repo.Generic},
		{Key: "p2-private", Class: repo.Local, PackageType: repo.Generic},
		{
			Key: "p2-remote", Class: repo.Remote, PackageType: repo.P2,
			URL: "https://download.eclipse.org/glsp/server/p2/releases/2.7.0/",
		},
		{
			Key: "p2-forced-virtual", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: members, ForceP2Authentication: &on,
		},
		{
			Key: "p2-open-virtual", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: members,
		},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	index := p2Fixture(t, "p2.index")
	for _, key := range []string{"p2-public", "p2-private"} {
		res := w.do(http.MethodPut, "/artifactory/"+key+"/p2.index", "admin", string(index))
		if res.Code != http.StatusCreated {
			t.Fatalf("publish %s: %d %s", key, res.Code, res.Body)
		}
	}
	grantAnonymous(t, w, "p2-forced-virtual", "p2-open-virtual", "p2-public")
	for _, tc := range []struct{ method, name string }{
		{http.MethodGet, "p2.index"},
		{http.MethodHead, "p2.index"},
		{http.MethodGet, "compositeContent.xml"},
		{http.MethodGet, "compositeArtifacts.xml"},
		{http.MethodGet, "plugins/"},
		{http.MethodGet, "p2-public/p2.index"},
		{http.MethodGet, "p2-private/p2.index"},
		{http.MethodGet, "p2-remote/p2.index"},
	} {
		what := tc.method + " " + tc.name
		if tc.method == http.MethodHead {
			what = "HEAD"
		}
		wantForced(t, what, w.do(tc.method, "/artifactory/p2-forced-virtual/"+tc.name, "", ""))
	}
	if len(calls) != 0 {
		t.Fatalf("a refused anonymous read reached the upstream: %v", calls)
	}
	for _, tc := range []struct {
		path, as string
		code     int
		children string
	}{
		{
			"/artifactory/p2-open-virtual/compositeContent.xml", "", http.StatusOK,
			"<children size=\"2\">\n<child location=\"p2-public\"/>\n<child location=\"p2-remote\"/>\n</children>",
		},
		{"/artifactory/p2-open-virtual/p2-public/p2.index", "", http.StatusOK, ""},
		{"/artifactory/p2-open-virtual/p2-remote/p2.index", "", http.StatusOK, ""},
		{
			"/artifactory/p2-forced-virtual/compositeContent.xml", "admin", http.StatusOK,
			"<children size=\"3\">\n<child location=\"p2-public\"/>\n<child location=\"p2-private\"/>\n" +
				"<child location=\"p2-remote\"/>\n</children>",
		},
		{"/artifactory/p2-forced-virtual/p2-private/p2.index", "admin", http.StatusOK, ""},
		// A signed-in caller who may not read keeps its 403.
		{"/artifactory/p2-forced-virtual/p2.index", "user", http.StatusForbidden, ""},
	} {
		res := w.do(http.MethodGet, tc.path, tc.as, "")
		ok := res.Code == tc.code
		switch {
		case tc.children != "":
			ok = ok && strings.Contains(res.Body.String(), tc.children)
		case tc.code == http.StatusOK:
			ok = ok && bytes.Equal(res.Body.Bytes(), index)
		}
		if !ok {
			t.Errorf("%s as %q: %d %s, want %d", tc.path, tc.as, res.Code, res.Body, tc.code)
		}
	}
}

// TestAccP2ForceAuthenticationIsTheNamedVirtualRepositorys: only the flag
// of the virtual repository the request names counts. A nested P2 virtual
// repository with the flag on answers anonymous 401 when it is read
// directly, and serves it, its composite and its member's files, under an
// outer virtual repository whose own flag is off, as 7.x's filter reads the
// key from the request's path [I]. A P2 remote's flag changes no answer:
// JFrog offers Force Authentication on P2 virtual repositories only
// (virtual-repositories and remote-repositories), and 7.x's filter reads it
// from a virtual repository alone (#1280).
func TestAccP2ForceAuthenticationIsTheNamedVirtualRepositorys(t *testing.T) {
	var calls []string
	w := newItemWorld(t, true, func(c *Config) { c.RemoteSources = p2IndexSource(t, &calls) })
	on := true
	for _, c := range []repo.Config{
		{Key: "p2-public", Class: repo.Local, PackageType: repo.Generic},
		{
			Key: "p2-flagged-remote", Class: repo.Remote, PackageType: repo.P2,
			URL: "https://download.eclipse.org/glsp/server/p2/releases/2.7.0/", ForceP2Authentication: &on,
		},
		{
			Key: "p2-forced-nested", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{"p2-public"}, ForceP2Authentication: &on,
		},
		{
			Key: "p2-outer", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{"p2-forced-nested", "p2-flagged-remote"},
		},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	index := p2Fixture(t, "p2.index")
	res := w.do(http.MethodPut, "/artifactory/p2-public/p2.index", "admin", string(index))
	if res.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", res.Code, res.Body)
	}
	grantAnonymous(t, w, "p2-outer", "p2-forced-nested", "p2-public")
	for _, path := range []string{
		"/artifactory/p2-forced-nested/compositeContent.xml",
		"/artifactory/p2-forced-nested/p2-public/p2.index",
	} {
		wantForced(t, path, w.do(http.MethodGet, path, "", ""))
	}
	for _, tc := range []struct{ path, children string }{
		{
			"/artifactory/p2-outer/compositeContent.xml",
			"<child location=\"p2-forced-nested\"/>\n<child location=\"p2-flagged-remote\"/>\n",
		},
		{"/artifactory/p2-outer/p2-forced-nested/compositeContent.xml", "<child location=\"p2-public\"/>\n"},
		{"/artifactory/p2-outer/p2-forced-nested/p2-public/p2.index", ""},
		{"/artifactory/p2-outer/p2-flagged-remote/p2.index", ""},
		{"/artifactory/p2-flagged-remote/p2.index", ""},
	} {
		res := w.do(http.MethodGet, tc.path, "", "")
		ok := res.Code == http.StatusOK
		if tc.children != "" {
			ok = ok && strings.Contains(res.Body.String(), tc.children)
		} else {
			ok = ok && bytes.Equal(res.Body.Bytes(), index)
		}
		if !ok {
			t.Errorf("anonymous %s: %d %s", tc.path, res.Code, res.Body)
		}
	}
}
