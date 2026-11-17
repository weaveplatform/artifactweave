package pypihttp_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	pypifmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pypi"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const virtualPublishBase = "/artifactory/api/pypi/pypi-virtual/"

func publishVirtual(t *testing.T, f *fixture, target string, hide bool, members ...string) {
	t.Helper()
	c := *repo.Defaults(&repo.Config{Key: "pypi-virtual", Class: repo.Virtual, PackageType: repo.PyPI, Repositories: members, DefaultDeploymentRepo: target, HideUnauthorizedResources: &hide})
	if err := f.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
}

type unreadPublishBody struct{ reads int }

func (b *unreadPublishBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("body must remain unread")
}
func (*unreadPublishBody) Close() error { return nil }

func TestUnitVirtualPublishChecksBeforeBody(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, user, target string
		hide                             bool
		members                          []string
		status                           int
		allow, message                   string
	}{
		{name: "anonymous", user: "anonymous", target: "pypi-local", members: []string{"pypi-local"}, status: 401},
		{name: "no principal", target: "pypi-local", members: []string{"pypi-local"}, status: 401},
		{name: "reader", user: "reader", target: "pypi-local", members: []string{"pypi-local"}, status: 403},
		{name: "hidden reader", user: "reader", hide: true, target: "pypi-local", members: []string{"pypi-local"}, status: 404, message: "Resource not found; Path: 'pypi-virtual:'"},
		{name: "hidden anonymous", user: "anonymous", hide: true, target: "pypi-local", members: []string{"pypi-local"}, status: 404, message: "Resource not found; Path: 'pypi-virtual:'"},
		{name: "simple precedes target", path: "simple", user: "reader", status: 415, message: "Unsupported Media Type"},
		{name: "simple slash", path: "simple/", user: "reader", status: 415, message: "Unsupported Media Type"},
		{name: "folder precedes target", path: "folder/", user: "reader", status: 405, message: "Twine is allowed to deploy packages only to the base level"},
		{name: "deeper path", path: "simple/demo/", user: "writer", status: 405, message: "Twine is allowed to deploy packages only to the base level"},
		{name: "no target", user: "writer", status: 405, allow: "GET", message: "No local repository was configured as local deployment repository for the (pypi-virtual) virtual repository."},
		{name: "missing target", user: "writer", target: "gone", members: []string{"gone"}, status: 405, allow: "GET"},
		{name: "unrelated target", user: "writer", target: "pypi-fed", members: []string{"pypi-local"}, status: 405, allow: "GET"},
		{name: "remote target", user: "admin", target: "pypi-remote", members: []string{"pypi-remote"}, status: 405, allow: "GET"},
		{name: "virtual target", user: "admin", target: "pypi-virtual", members: []string{"pypi-virtual"}, status: 405, allow: "GET"},
		{name: "wrong type", user: "admin", target: "libs-local", members: []string{"libs-local"}, status: 405, allow: "GET"},
		{name: "PUT anonymous", method: "PUT", path: "demo/1.0/demo.whl", user: "anonymous", target: "pypi-local", members: []string{"pypi-local"}, status: 401},
		{name: "PUT hidden anonymous", method: "PUT", path: "demo/1.0/demo.whl", user: "anonymous", hide: true, target: "pypi-local", members: []string{"pypi-local"}, status: 404, message: "Resource not found; Path: 'pypi-virtual:demo/1.0/demo.whl'"},
		{name: "PUT forbidden", method: "PUT", path: "demo/1.0/demo.whl", user: "reader", target: "pypi-local", members: []string{"pypi-local"}, status: 403},
		{name: "PUT hidden", method: "PUT", path: "demo/1.0/demo.whl", user: "reader", hide: true, target: "pypi-local", members: []string{"pypi-local"}, status: 404, message: "Resource not found; Path: 'pypi-virtual:demo/1.0/demo.whl'"},
		{name: "PUT no target", method: "PUT", path: "demo.whl", user: "writer", status: 405, allow: "GET"},
		{name: "PUT malformed", method: "PUT", path: "bad;=value", user: "writer", target: "pypi-local", members: []string{"pypi-local"}, status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, pypifmt.Settings{})
			publishVirtual(t, f, tc.target, tc.hide, tc.members...)
			method := tc.method
			if method == "" {
				method = "POST"
			}
			body := &unreadPublishBody{}
			r := httptest.NewRequest(method, virtualPublishBase+tc.path, nil)
			r.Body = body
			r.Header.Set("X-Test-User", tc.user)
			r.Header.Set("Content-Type", "multipart/form-data; boundary=unused")
			out := httptest.NewRecorder()
			f.h.ServeHTTP(out, r)
			wantStatus(t, out, tc.status)
			if body.reads != 0 || out.Header().Get("Allow") != tc.allow || !strings.Contains(out.Body.String(), tc.message) {
				t.Fatalf("reads=%d allow=%q body=%s", body.reads, out.Header().Get("Allow"), out.Body)
			}
			challenge, message := "", tc.message
			switch tc.status {
			case 401:
				challenge, message = apierror.BasicChallenge, kernel.ErrUnauthenticated.Error()
			case 403:
				message = kernel.ErrForbidden.Error()
			}
			if out.Header().Get("WWW-Authenticate") != challenge {
				t.Fatalf("challenge=%q want=%q", out.Header().Get("WWW-Authenticate"), challenge)
			}
			if message != "" {
				expected := fmt.Sprintf("{\"errors\":[{\"message\":%q,\"status\":%d}]}\n", message, tc.status)
				if out.Body.String() != expected {
					t.Fatalf("envelope=%q want=%q", out.Body.String(), expected)
				}
			}
		})
	}
}

func TestAccVirtualUploadUsesTargetAndPublishesImmediately(t *testing.T) {
	for _, target := range []string{"pypi-local", "pypi-fed"} {
		t.Run(target, func(t *testing.T) {
			f := newFixture(t, pypifmt.Settings{})
			publishVirtual(t, f, target, false, target)
			content := wheel(t, "demo", "1.0", metadata("demo", "1.0", ""))
			file := "demo-1.0-py3-none-any.whl"
			for _, base := range []string{strings.TrimSuffix(virtualPublishBase, "/"), virtualPublishBase} {
				out := f.upload(t, base, "writer", file, content, uploadFields(content))
				wantStatus(t, out, 200)
				if out.Body.Len() != 0 {
					t.Fatal(out.Body)
				}
			}
			it, err := f.db.Items().Get(t.Context(), target, false, "demo/1.0/"+file)
			if err != nil || it.Actual.SHA256 != sha256Hex(content) {
				t.Fatal(it, err)
			}
			wantStatus(t, f.do(http.MethodGet, virtualPublishBase+"simple/demo/", "reader"), 200)
		})
	}
	// A legacy nested target remains effectively reachable.
	f := newFixture(t, pypifmt.Settings{})
	if err := f.db.Repos().
		Create(t.Context(), *repo.Defaults(&repo.Config{Key: "inner", Class: repo.Virtual, PackageType: repo.PyPI, Repositories: []string{"pypi-local", "pypi-virtual"}})); err != nil {
		t.Fatal(err)
	}
	publishVirtual(t, f, "pypi-local", false, "inner")
	content := wheel(t, "demo", "1.0", metadata("demo", "1.0", ""))
	wantStatus(t, f.upload(t, virtualPublishBase, "writer", "demo-1.0-py3-none-any.whl", content, uploadFields(content)), 200)
}

func TestAccVirtualUploadKeepsCanonicalAuthorityAndPolicy(t *testing.T) {
	f := newFixture(t, pypifmt.Settings{})
	publishVirtual(t, f, "pypi-local", true, "pypi-local")
	if err := f.db.Permissions().
		Create(t.Context(), accesstest.Permission{Name: "narrow", Resources: map[accessapi.ResourceType]accesstest.Resource{accessapi.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"narrow": accessapi.ActionWrite}}, Targets: map[string]accesstest.Target{"pypi-local": {Include: []string{"else/**"}}}}}}); err != nil {
		t.Fatal(err)
	}
	content := wheel(t, "demo", "1.0", metadata("demo", "1.0", ""))
	out := f.upload(t, virtualPublishBase, "narrow", "demo-1.0-py3-none-any.whl", content, uploadFields(content))
	wantStatus(t, out, 404)
	if !strings.Contains(out.Body.String(), "Resource not found; Path: 'pypi-virtual:demo/1.0/demo-1.0-py3-none-any.whl'") {
		t.Fatal(out.Body)
	}
	if _, err := f.db.Items().Get(t.Context(), "pypi-local", false, "demo/1.0/demo-1.0-py3-none-any.whl"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal(err)
	}
	for _, name := range []string{"packages", "simple"} {
		body := wheel(t, name, "1.0", metadata(name, "1.0", ""))
		wantStatus(t, f.upload(t, virtualPublishBase, "writer", name+"-1.0-py3-none-any.whl", body, uploadFields(body)), 400)
	}
	strict := newFixture(t, pypifmt.Settings{EnforceNamingNormalization: true})
	publishVirtual(t, strict, "pypi-local", true, "pypi-local")
	body := wheel(t, "demo_pkg", "1.0", metadata("demo_pkg", "1.0", ""))
	wantStatus(t, strict.upload(t, virtualPublishBase, "writer", "demo-pkg-1.0-py3-none-any.whl", body, uploadFields(body)), 403)
	wantStatus(t, f.do(http.MethodPost, virtualPublishBase, "writer", "Content-Type", "text/xml"), 415)
	wantStatus(t, f.upload(t, virtualPublishBase, "writer", "demo-1.0-py3-none-any.whl", content, map[string]string{":action": "submit"}), 400)
}
