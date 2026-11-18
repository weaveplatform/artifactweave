package dockerhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/registry"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	layout "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestAccVirtualImportsRevalidateBeforePublication(t *testing.T) {
	for _, revoked := range []error{source.ErrDenied, source.ErrSuperseded} {
		t.Run(revoked.Error(), func(t *testing.T) {
			w := newBlobWorld(t)
			g := New(Options{Store: w.db, Blobs: w.blobs})
			c, err := w.db.Repos().Get(t.Context(), "docker-local")
			if err != nil {
				t.Fatal(err)
			}
			from := w.deploy(c.Key, layout.UploadPath("source", strings.TrimPrefix(digestOf("blob"), "sha256:")), "blob")
			q := Request{Repo: c, Name: "destination", virtualRepo: &repo.Config{Key: "virtual", Class: repo.Virtual}}
			r := httptest.NewRequest("PUT", "/", nil)
			read := virtualRead{remote: true, result: source.Result{Item: &from, Revalidate: func(context.Context) error { return revoked }}}
			if copied, err := g.copyVirtualBlob(r, q, read, from.Actual.SHA256); copied || !errors.Is(err, revoked) {
				t.Fatal("revoked source blob imported", copied, err)
			}
			if _, err := layout.FindBlob(t.Context(), w.db.Items(), c.Key, q.Name, from.Actual.SHA256); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal(err)
			}
			body := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`
			hex := strings.TrimPrefix(digestOf(body), "sha256:")
			read.result.Item = nil
			read.result.Stream = &source.Stream{Body: io.NopCloser(strings.NewReader(body)), Representation: source.Representation{MediaType: layout.MediaTypeOCIIndex}}
			if err := g.importManifest(r, q, read, hex, map[string]bool{}, 1); !errors.Is(err, revoked) {
				t.Fatal("revoked child imported", err)
			}
			if _, err := layout.FindManifest(t.Context(), w.db.Items(), c.Key, q.Name, hex); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal(err)
			}
		})
	}
}

type virtualFaultRepo struct{ port.RepoStore }

type virtualReadFailure struct{}

func (virtualReadFailure) Read([]byte) (int, error) { return 0, errBoom }

func TestAccVirtualImportsRejectInvalidChildren(t *testing.T) {
	for _, name := range []string{"wrong digest", "invalid manifest", "read failure", "local open failure", "cache open failure"} {
		t.Run(name, func(t *testing.T) {
			w := newBlobWorld(t)
			g := New(Options{Store: w.db, Blobs: w.blobs})
			c, err := w.db.Repos().Get(t.Context(), "docker-local")
			if err != nil {
				t.Fatal(err)
			}
			q := Request{Repo: c, Name: "destination", virtualRepo: &repo.Config{Key: "virtual", Class: repo.Virtual}}
			body := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`
			if name == "invalid manifest" {
				body = `not a manifest`
			}
			hex := strings.TrimPrefix(digestOf(body), "sha256:")
			read := virtualRead{
				remote: true,
				result: source.Result{
					Stream: &source.Stream{Body: io.NopCloser(strings.NewReader(body)), Representation: source.Representation{MediaType: layout.MediaTypeOCIIndex}},
				},
			}
			switch name {
			case "wrong digest":
				hex = strings.Repeat("b", 64)
			case "read failure":
				read.result.Stream.Body = io.NopCloser(virtualReadFailure{})
			case "local open failure", "cache open failure":
				from := w.deploy(c.Key, "source/v1/list.manifest.json", body)
				g.blobs = failingBlobs{w.blobs}
				read.item, read.result.Item, read.result.Stream = from, &from, nil
				read.remote = name == "cache open failure"
			}
			err = g.importManifest(httptest.NewRequest("PUT", "/", nil), q, read, hex, map[string]bool{}, 1)
			if err == nil {
				t.Fatal("invalid/unavailable child imported")
			}
			if _, err := layout.FindManifest(t.Context(), w.db.Items(), c.Key, q.Name, hex); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal(err)
			}
		})
	}
}

func (s virtualFaultRepo) Get(ctx context.Context, key string) (repo.Config, error) {
	if key == "broken" {
		return repo.Config{}, errBoom
	}
	return s.RepoStore.Get(ctx, key) //nolint:wrapcheck // test double
}

func TestAccVirtualHeadPreservesLookupFaults(t *testing.T) {
	w := newTokenWorld(t)
	c, err := w.db.Repos().Get(t.Context(), "docker-virtual")
	if err != nil {
		t.Fatal(err)
	}
	c.Repositories, c.DefaultDeploymentRepo = []string{"broken"}, "docker-local"
	st := faultyStore{Store: w.db, repos: faultyRepos{RepoStore: virtualFaultRepo{w.db.Repos()}}}
	g := New(Options{Store: st})
	q := Request{Kind: KindManifest, Repo: c, RepoKey: c.Key, Name: "app", Reference: "v1", base: "/v2/" + c.Key}
	r := httptest.NewRequest("HEAD", "/v2/docker-virtual/app/manifests/v1", nil)
	r = r.WithContext(w.as(r.Context(), kernel.Principal{Name: "writer"}))
	if !g.virtualPushProbe(r, q) {
		t.Fatal("fixture must authorize DDR push")
	}
	rec := httptest.NewRecorder()
	g.serveVirtual(rec, r, q)
	if rec.Code != 500 {
		t.Fatal("lookup fault became a missing HEAD", rec.Code, rec.Body.String())
	}
}

func TestUnitVirtualImportBudget(t *testing.T) {
	for _, count := range []int{255, 256, 257} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			g := New(Options{Store: fixture(t)})
			q := Request{Repo: repo.Config{Key: "docker-local", Class: repo.Local}, Name: "app", virtualRepo: &repo.Config{Key: "empty", Class: repo.Virtual}}
			m := layout.Manifest{Digest: strings.Repeat("a", 64)}
			for i := range count {
				m.Layers = append(m.Layers, layout.Descriptor{Digest: fmt.Sprintf("sha256:%064x", i)})
			}
			seen := map[string]bool{}
			err := g.syncVirtual(httptest.NewRequest("PUT", "/", nil), q, m, seen, 0)
			if len(seen) != min(count, 256) {
				t.Fatal("import budget", len(seen))
			}
			if count <= 256 && err != nil {
				t.Fatal(err)
			}
			if count == 257 {
				var unknown *registry.BlobsUnknownError
				if !errors.As(err, &unknown) || len(unknown.Digests) != 1 || unknown.Digests[0] != m.Layers[256].Digest {
					t.Fatal(err)
				}
			}
			if err := g.syncVirtual(httptest.NewRequest("PUT", "/", nil), q, m, map[string]bool{}, 8); err == nil {
				t.Fatal("depth limit ignored")
			}
		})
	}
}

func TestAccVirtualPushAddressForms(t *testing.T) {
	for _, tc := range []struct {
		name, base, host string
	}{
		{"repository path", "/v2/docker-virtual/app", ""},
		{"Artifactory prefix", "/artifactory/api/docker/docker-virtual/v2/app", ""},
		{"subdomain", "/v2/app", "docker-virtual.reg.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newBlobWorld(t)
			if r := w.do("POST", "/v2/docker-virtual/Upper/referrers/sha256:"+strings.Repeat("a", 64), "", nil); r.Code != 405 {
				t.Fatal("unsupported referrers method changed its validation precedence", r.Code, r.Body.String())
			}
			c, err := w.db.Repos().Get(t.Context(), "docker-virtual")
			if err != nil {
				t.Fatal(err)
			}
			c.DefaultDeploymentRepo = "docker-local"
			if err = w.db.Repos().Update(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			headers := map[string]string{"Host": tc.host}
			if tc.host == "" {
				delete(headers, "Host")
			}
			r := w.do("POST", tc.base+"/blobs/uploads/?digest="+digestOf("blob"), "blob", headers)
			if r.Code != 201 || !strings.Contains(r.Header().Get("Location"), tc.base+"/blobs/") || strings.Contains(r.Header().Get("Location"), "docker-local") {
				t.Fatal(r.Code, r.Header(), r.Body.String())
			}
			r = w.do("POST", tc.base+"/blobs/uploads/", "", headers)
			if r.Code != 202 || !strings.Contains(r.Header().Get("Location"), tc.base+"/blobs/uploads/") {
				t.Fatal(r.Code, r.Header(), r.Body.String())
			}
			path := tc.base + "/blobs/uploads/" + r.Header().Get(uploadUUIDHeader)
			if r = w.do("PATCH", path, "blob", headers); r.Code != 202 {
				t.Fatal(r.Code, r.Body.String())
			}
			if r = w.do("PUT", path+"?digest="+digestOf("blob"), "", headers); r.Code != 201 {
				t.Fatal(r.Code, r.Body.String())
			}
		})
	}
}
