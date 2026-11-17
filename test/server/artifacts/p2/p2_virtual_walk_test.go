package p2_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// TestAccP2VirtualWalksNestedMembersOnce (#1552): a P2 virtual repository
// over [remote, nested virtual -> local], whose nested member lists the
// outer one again, lists the remote and the nested virtual as its two
// children, where the cycle answered 500, and the nested composite read
// through the outer one leaves the outer one out, as the walk's visited
// set does (decision 0008, R2) [I]. A file of the nested member's local is
// served through both. A caller who may read the members, and no virtual
// repository, reads the composites and the files through them, where it
// was refused 403 (R3); one who may read nothing is still refused.
func TestAccP2VirtualWalksNestedMembersOnce(t *testing.T) {
	content := p2Fixture(t, "content.jar")
	backend := sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			if q.Resource.Path != "content.jar" {
				return source.Result{}, kernel.ErrNotFound
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(content)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(content))},
			}}, nil
		},
	)
	w := newItemWorld(t, false, func(c *Config) {
		c.RemoteSources = &source.Service{Resolve: sourceIdentity, HTTP: backend, Cache: backend}
	})
	for _, c := range []repo.Config{
		{Key: "p2-remote", Class: repo.Remote, PackageType: repo.P2, URL: "https://root.example/site/"},
		{Key: "p2-local", Class: repo.Local, PackageType: repo.Generic},
		{
			Key: "p2-inner", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{"p2-outer", "p2-local", "p2-inner"},
		},
		{
			Key: "p2-outer", Class: repo.Virtual, PackageType: repo.P2,
			Repositories: []string{"p2-remote", "p2-inner"},
		},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	remote, err := w.db.Repos().Get(t.Context(), "p2-remote")
	if err != nil {
		t.Fatal(err)
	}
	// Priority resolution keeps the remote before the local member, so the
	// configured order is the walk's (R1).
	remote.PriorityResolution = repo.Flex(true)
	if err := w.db.Repos().Update(t.Context(), remote); err != nil {
		t.Fatal(err)
	}
	if res := w.do(http.MethodPut, "/artifactory/p2-local/content.jar", "admin", string(content)); res.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", res.Code, res.Body)
	}
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "p2-members-read",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{"user": api.ActionRead}},
			Targets: map[string]accesstest.Target{
				"p2-remote": {Include: []string{"**"}}, "p2-local": {Include: []string{"**"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, children string }{
		{"p2-outer/compositeContent.xml", "<child location=\"p2-remote\"/>\n<child location=\"p2-inner\"/>\n"},
		{"p2-outer/p2-inner/compositeContent.xml", "<children size=\"1\">\n<child location=\"p2-local\"/>\n</children>"},
		{"p2-inner/compositeContent.xml", "<child location=\"p2-outer\"/>\n<child location=\"p2-local\"/>\n</children>"},
	} {
		for _, as := range []string{"admin", "user"} {
			res := w.do(http.MethodGet, "/artifactory/"+tc.path, as, "")
			if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), tc.children) {
				t.Errorf("%s as %s: %d %s, want the children %q", tc.path, as, res.Code, res.Body, tc.children)
			}
		}
	}
	for _, path := range []string{
		"p2-outer/p2-remote/content.jar", "p2-outer/p2-inner/p2-local/content.jar",
	} {
		for _, as := range []string{"admin", "user"} {
			res := w.do(http.MethodGet, "/artifactory/"+path, as, "")
			if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), content) {
				t.Errorf("%s as %s: %d %.200s, want the member's file", path, as, res.Code, res.Body)
			}
		}
	}
	for _, path := range []string{"p2-outer/compositeContent.xml", "p2-outer/p2-remote/content.jar"} {
		if res := w.do(http.MethodGet, "/artifactory/"+path, "deployer", ""); res.Code != http.StatusForbidden {
			t.Errorf("%s as deployer: %d %s, want 403", path, res.Code, res.Body)
		}
	}
}
