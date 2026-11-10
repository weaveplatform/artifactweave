package p2_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	accmodel "github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	artmodel "github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/p2"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
)

func TestAccP2ArchiveSuffixIsBoundedAndPermissionChecked(t *testing.T) {
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	f, err := zw.Create("p2.index")
	if err != nil {
		t.Fatal(err)
	}
	index := p2Fixture(t, "p2.index")
	if _, err := f.Write(index); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var reads int
	var delivered int
	backend := sourceBackend(
		func(_ context.Context, q source.Request, permit source.Permit) (source.Result, error) {
			reads++
			if q.Resource.ExternalURL != "https://eclipse.example/site.zip" ||
				q.Resource.Validate == nil {
				t.Fatalf("archive source request: %+v", q.Resource)
			}
			if q.Resource.Path == "content.jar" {
				if err := q.Resource.Validate(
					t.Context(),
					bytes.NewReader(zipped.Bytes()),
					int64(zipped.Len()),
				); err == nil {
					t.Fatal("missing content.jar was admitted to the cache")
				}
				return source.Result{}, source.ErrInvalidResult
			}
			if q.Resource.Path != "p2.index" {
				t.Fatalf("unexpected archive path: %s", q.Resource.Path)
			}
			if permit.Intent != source.RetrievalIntentMayFetch &&
				permit.Intent != source.RetrievalIntentCacheOnly {
				t.Fatalf("archive source intent: %+v", permit)
			}
			if err := q.Resource.Validate(
				t.Context(),
				bytes.NewReader(zipped.Bytes()),
				int64(zipped.Len()),
			); err != nil {
				t.Fatalf("valid update-site archive refused: %v", err)
			}
			return source.Result{Used: func(context.Context) error {
				delivered++
				return nil
			}, Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(zipped.Bytes())), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(zipped.Len())},
			}}, nil
		},
	)
	sources := &source.Service{Resolve: sourceIdentity, HTTP: backend, Cache: backend}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = sources })
	c := *repo.Defaults(&repo.Config{
		Key: "p2-zipped", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://eclipse.example/site.zip!/",
	})
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/artifactory/p2-zipped/p2.index",
		"/artifactory/p2-zipped-cache/p2.index",
	} {
		res := w.do(http.MethodGet, path, "admin", "")
		if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), index) {
			t.Errorf("archive %s: %d %s", path, res.Code, res.Body)
		}
	}
	head := w.do(http.MethodHead, "/artifactory/p2-zipped/p2.index", "admin", "")
	if head.Code != http.StatusOK || head.Body.Len() != 0 || delivered != 2 {
		t.Errorf(
			"HEAD must not record a body delivery: %d %s, used %d",
			head.Code,
			head.Body,
			delivered,
		)
	}
	partial := w.do(
		http.MethodGet,
		"/artifactory/p2-zipped/p2.index",
		"admin",
		"",
		"Range",
		"bytes=0-9",
	)
	if partial.Code != http.StatusPartialContent ||
		!bytes.Equal(partial.Body.Bytes(), index[:10]) ||
		delivered != 3 {
		t.Errorf("archive range delivery: %d %s, used %d", partial.Code, partial.Body, delivered)
	}
	if res := w.do(
		http.MethodGet,
		"/artifactory/p2-zipped/p2.index",
		"user",
		"",
	); res.Code != http.StatusForbidden {
		t.Errorf("unreadable archive entry: %d", res.Code)
	}
	if reads != 4 {
		t.Errorf("unauthorized request fetched archive: %d reads", reads)
	}
	if delivered != 3 {
		t.Errorf("successful deliveries were not recorded: %d", delivered)
	}
	if res := w.do(
		http.MethodGet,
		"/artifactory/p2-zipped/content.jar",
		"admin",
		"",
	); res.Code == http.StatusOK {
		t.Fatal("missing archive metadata returned success")
	}
}

func p2Fixture(t *testing.T, name string) []byte {
	t.Helper()
	return repopath.Read(t, "migration/artifacts/ecosystem/p2/testdata/"+name)
}

func TestAccP2RemoteAndVirtualWirePaths(t *testing.T) {
	index := p2Fixture(t, "p2.index")
	content := p2Fixture(t, "content.jar")
	var calls []string
	sources := &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(_ context.Context, q source.Request, permit source.Permit) (source.Result, error) {
			calls = append(calls, q.Resource.Path)
			if permit.Intent != source.RetrievalIntentMayFetch || !permit.UpstreamNetworkAllowed {
				t.Fatalf("wrong P2 retrieval intent: %+v", permit)
			}
			var body []byte
			switch q.Resource.Path {
			case "p2.index":
				body = index
			case "content.jar":
				body = content
			default:
				return source.Result{}, kernel.ErrNotFound
			}
			if err := q.Resource.ValidateMetadata(
				t.Context(),
				bytes.NewReader(body),
				int64(len(body)),
				source.MetadataResponse{},
			); err != nil {
				t.Fatalf("source request rejected published Eclipse metadata: %v", err)
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(body)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(body))},
			}}, nil
		},
	)}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = sources })
	for _, c := range []repo.Config{
		{Key: "p2-remote", Class: repo.Remote, PackageType: repo.P2, URL: "https://download.eclipse.org/glsp/server/p2/releases/2.7.0/content.jar"},
		{Key: "p2-virtual", Class: repo.Virtual, PackageType: repo.P2, Repositories: []string{"p2-local", "p2-remote"}},
		{Key: "p2-local", Class: repo.Local, PackageType: repo.Generic},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"p2.index", "content.jar"} {
		body := p2Fixture(t, name)
		res := w.do(http.MethodPut, "/artifactory/p2-local/"+name, "admin", string(body))
		if res.Code != http.StatusCreated {
			t.Fatalf("local P2 member publish %s: %d %s", name, res.Code, res.Body)
		}
	}
	for _, tc := range []struct {
		path string
		body []byte
	}{
		{"/artifactory/p2-remote/content.jar", content},
		{"/artifactory/p2-virtual/__p2/0/content.jar", content},
		{"/artifactory/p2-virtual/__p2/1/p2.index", index},
	} {
		res := w.do(http.MethodGet, tc.path, "admin", "")
		if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), tc.body) {
			t.Errorf("%s: %d, body size %d", tc.path, res.Code, res.Body.Len())
		}
	}
	for _, name := range []string{"p2.index", "compositeContent.xml", "compositeArtifacts.xml"} {
		res := w.do(http.MethodGet, "/artifactory/p2-virtual/"+name, "admin", "")
		if res.Code != http.StatusOK {
			t.Errorf("virtual %s: %d %s", name, res.Code, res.Body)
		}
		if name != "p2.index" &&
			(!strings.Contains(res.Body.String(), "__p2/0") || !strings.Contains(res.Body.String(), "__p2/1")) {
			t.Errorf("virtual %s omitted members: %s", name, res.Body)
		}
	}
	for _, tc := range []struct {
		method, path, user string
		expected           int
	}{
		{http.MethodGet, "/artifactory/p2-remote/arbitrary.txt", "admin", 404},
		{http.MethodGet, "/artifactory/p2-remote/content.jar?url=https://attacker.example", "admin", 404},
		{http.MethodGet, "/artifactory/p2-remote/content.jar", "user", 403},
		{http.MethodGet, "/artifactory/p2-virtual/__p2/2/content.jar", "admin", 404},
		{http.MethodGet, "/artifactory/p2-virtual/__p2/0/../content.jar", "admin", 400},
		{http.MethodPut, "/artifactory/p2-virtual/content.jar", "admin", 405},
	} {
		res := w.do(tc.method, tc.path, tc.user, "bad")
		if res.Code != tc.expected {
			t.Errorf("%s %s as %s: %d, want %d", tc.method, tc.path, tc.user, res.Code, tc.expected)
		}
	}
	if len(calls) != 2 || calls[0] != "content.jar" || calls[1] != "p2.index" {
		t.Errorf("unexpected remote reads: %v", calls)
	}
	if _, err := w.db.Repos().Get(t.Context(), "p2-remote"); err != nil {
		t.Fatal(err)
	}
	if p2.Classify("plugins/org.example_1.0.jar") != p2.KindArtifact {
		t.Fatal("plugin path not recognized")
	}
}

func TestAccP2ArchiveRefusesInvalidCachedRepresentations(t *testing.T) {
	for _, tc := range []struct {
		name     string
		archive  []byte
		declared int64
		validate bool
	}{
		{name: "zero length", archive: []byte("not a zip"), declared: 0, validate: true},
		{name: "truncated source", archive: []byte("not a zip"), declared: 100, validate: true},
		{name: "malformed source", archive: []byte("not a zip"), declared: 9, validate: true},
		{name: "malformed delivery", archive: []byte("not a zip"), declared: 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := sourceBackend(
				func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
					if q.Resource.Validate == nil {
						t.Fatal("archive admission validation was omitted")
					}
					if tc.validate {
						if err := q.Resource.Validate(
							t.Context(),
							bytes.NewReader(tc.archive),
							tc.declared,
						); err == nil {
							t.Fatal("invalid archive admitted to the source cache")
						}
						return source.Result{}, source.ErrInvalidResult
					}
					return source.Result{Stream: &source.Stream{
						Body: io.NopCloser(bytes.NewReader(tc.archive)), Status: http.StatusOK,
						Representation: source.Representation{Size: int64(len(tc.archive))},
					}}, nil
				},
			)
			sources := &source.Service{Resolve: sourceIdentity, HTTP: backend, Cache: backend}
			w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = sources })
			c := *repo.Defaults(&repo.Config{
				Key: "p2-archive-fault", Class: repo.Remote, PackageType: repo.P2,
				URL: "https://eclipse.example/site.zip!/",
			})
			if err := w.db.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			res := w.do(http.MethodGet, "/artifactory/p2-archive-fault/p2.index", "admin", "")
			if res.Code == http.StatusOK || bytes.Contains(res.Body.Bytes(), []byte("version=1")) {
				t.Errorf("invalid source delivered as a P2 site: %d %s", res.Code, res.Body)
			}
		})
	}
}

func TestAccP2ArchiveRefusesUnavailableOrChangedSources(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(*testing.T) *source.Service
	}{
		{name: "no source service"},
		{name: "source changes before delivery", make: func(t *testing.T) *source.Service {
			var checks int
			backend := sourceBackend(func(_ context.Context, _ source.Request, _ source.Permit) (source.Result, error) {
				return source.Result{
					Stream: &source.Stream{
						Body: io.NopCloser(strings.NewReader("not a zip")), Status: http.StatusOK,
						Representation: source.Representation{Size: 9},
					},
					Revalidate: func(context.Context) error {
						checks++
						if checks > 1 {
							return source.ErrSuperseded
						}
						return nil
					},
				}, nil
			})
			return &source.Service{Resolve: sourceIdentity, HTTP: backend, Cache: backend}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sources *source.Service
			if tc.make != nil {
				sources = tc.make(t)
			}
			w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = sources })
			c := *repo.Defaults(&repo.Config{
				Key: "p2-archive-unavailable", Class: repo.Remote, PackageType: repo.P2,
				URL: "https://eclipse.example/site.zip!/",
			})
			if err := w.db.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			res := w.do(http.MethodGet, "/artifactory/p2-archive-unavailable/p2.index", "admin", "")
			if res.Code == http.StatusOK {
				t.Fatalf("unavailable source delivered P2 metadata: %s", res.Body)
			}
		})
	}
}

func TestAccP2ArchiveReadsCachedBlobAndRejectsMissingBlob(t *testing.T) {
	index := p2Fixture(t, "p2.index")
	var archive bytes.Buffer
	zw := zip.NewWriter(&archive)
	f, err := zw.Create("p2.index")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(index); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var cached artmodel.Item
	var used int
	backend := sourceBackend(
		func(_ context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			if q.Resource.Path != "p2.index" {
				t.Fatalf("unexpected cached archive request: %s", q.Resource.Path)
			}
			return source.Result{Item: &cached, Used: func(context.Context) error {
				used++
				return nil
			}}, nil
		},
	)
	sources := &source.Service{Resolve: sourceIdentity, HTTP: backend, Cache: backend}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = sources })
	res := w.do(http.MethodPut, "/artifactory/libs-local/site.zip", "admin", archive.String())
	if res.Code != http.StatusCreated {
		t.Fatalf("seed archive: %d %s", res.Code, res.Body)
	}
	cached = w.item("libs-local", false, "site.zip")
	cached.RepoKey, cached.Cached, cached.Path, cached.Name = "p2-store", true, "", "p2.index"
	c := *repo.Defaults(&repo.Config{
		Key: "p2-store", Class: repo.Remote, PackageType: repo.P2,
		URL: "https://eclipse.example/site.zip!/",
	})
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	res = w.do(http.MethodGet, "/artifactory/p2-store/p2.index", "admin", "")
	if res.Code != http.StatusOK || !bytes.Equal(res.Body.Bytes(), index) || used != 1 {
		t.Fatalf("cached archive: %d %s, used %d", res.Code, res.Body, used)
	}
	cached.Actual.SHA256 = strings.Repeat("0", 64)
	res = w.do(http.MethodGet, "/artifactory/p2-store/p2.index", "admin", "")
	if res.Code == http.StatusOK || used != 1 {
		t.Errorf("missing cached blob delivered: %d %s, used %d", res.Code, res.Body, used)
	}
}

func TestAccP2VirtualFiltersMembersAndDeniesUnpermittedChild(t *testing.T) {
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{Key: "p2-child", Class: repo.Local, PackageType: repo.Generic},
		{Key: "p2-wrong", Class: repo.Remote, PackageType: repo.Generic, URL: "https://example.test"},
		{Key: "p2-nested", Class: repo.Virtual, PackageType: repo.P2, Repositories: []string{"p2-child"}},
		{Key: "p2-parent", Class: repo.Virtual, PackageType: repo.P2, Repositories: []string{"missing", "p2-wrong", "p2-child", "p2-nested"}},
		{Key: "p2-cycle", Class: repo.Virtual, PackageType: repo.P2, Repositories: []string{"p2-cycle"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.db.Permissions().Create(t.Context(), accmodel.Permission{
		Name: "p2-child-path-limited",
		Resources: map[api.ResourceType]accmodel.Resource{api.ResourceArtifact: {
			Actions: accmodel.Grants{Users: map[string]api.Actions{"user": api.ActionRead}},
			Targets: map[string]accmodel.Target{
				"p2-parent": {Include: []string{"**"}},
				"p2-child":  {Include: []string{"plugins/**"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	res := w.do(http.MethodGet, "/artifactory/p2-parent/compositeContent.xml", "admin", "")
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `children size="2"`) ||
		strings.Contains(res.Body.String(), "__p2/2") {
		t.Fatalf("virtual member filtering: %d %s", res.Code, res.Body)
	}
	for _, tc := range []struct {
		path, user string
		expected   int
	}{
		{"/artifactory/p2-parent/__p2/0/p2.index", "user", http.StatusForbidden},
		{"/artifactory/p2-parent/__p2/zero/p2.index", "admin", http.StatusNotFound},
		{"/artifactory/p2-parent/__p2/00/p2.index", "admin", http.StatusNotFound},
		{"/artifactory/p2-parent/__p2/0/unknown.txt", "admin", http.StatusNotFound},
		{"/artifactory/p2-parent/__p2/0", "admin", http.StatusNotFound},
		{"/artifactory/p2-parent/unknown.txt", "admin", http.StatusNotFound},
		{"/artifactory/p2-parent/p2.index", "deployer", http.StatusForbidden},
	} {
		res := w.do(http.MethodGet, tc.path, tc.user, "")
		if res.Code != tc.expected {
			t.Errorf("%s as %s: %d, want %d: %s", tc.path, tc.user, res.Code, tc.expected, res.Body)
		}
	}
	res = w.do(http.MethodGet, "/artifactory/p2-cycle/p2.index", "admin", "")
	if res.Code == http.StatusOK {
		t.Fatalf("cyclic update site returned metadata: %s", res.Body)
	}
}
