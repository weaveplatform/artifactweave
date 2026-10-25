package server

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/p2"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
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
			if permit.Intent != source.MayFetch && permit.Intent != source.CacheOnly {
				t.Fatalf("archive source intent: %+v", permit)
			}
			if err := q.Resource.Validate(
				t.Context(),
				bytes.NewReader(zipped.Bytes()),
				int64(zipped.Len()),
			); err != nil {
				t.Fatalf("valid update-site archive refused: %v", err)
			}
			return source.Result{Stream: &source.Stream{
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
	if res := w.do(
		http.MethodGet,
		"/artifactory/p2-zipped/p2.index",
		"user",
		"",
	); res.Code != http.StatusForbidden {
		t.Errorf("unreadable archive entry: %d", res.Code)
	}
	if reads != 2 {
		t.Errorf("unauthorized request fetched archive: %d reads", reads)
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
	data, err := os.ReadFile(filepath.Join("..", "format", "p2", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestUnitP2SourceAdmission(t *testing.T) {
	for _, name := range []string{"p2.index", "content.jar", "artifacts.jar", "content.xml.xz", "artifacts.xml.xz"} {
		q, err := p2SourceRequest(
			source.Request{Resource: source.Resource{Path: name, Method: http.MethodGet}},
		)
		if err != nil || q.Resource.Kind != source.Metadata || q.Resource.ValidateMetadata == nil {
			t.Fatalf("%s metadata request: %+v %v", name, q.Resource, err)
		}
		body := p2Fixture(t, name)
		if err := q.Resource.ValidateMetadata(
			t.Context(),
			bytes.NewReader(body),
			int64(len(body)),
			source.MetadataResponse{},
		); err != nil {
			t.Errorf("published %s rejected: %v", name, err)
		}
		if err := q.Resource.ValidateMetadata(
			t.Context(),
			bytes.NewReader([]byte("bad")),
			3,
			source.MetadataResponse{},
		); err == nil {
			t.Errorf("bad %s accepted", name)
		}
	}
	for _, tc := range []struct {
		path, query, rangeHeader string
		want                     error
	}{
		{"../content.jar", "", "", store.ErrNotFound},
		{"content.jar", "download=1", "", source.ErrInvalidRequest},
		{"plugins/org.example_1.0.jar", "", "bytes=0-10", source.ErrInvalidRequest},
	} {
		_, err := p2SourceRequest(
			source.Request{
				Resource: source.Resource{Path: tc.path, Query: tc.query, Range: tc.rangeHeader},
			},
		)
		if !errors.Is(err, tc.want) {
			t.Errorf("%+v: %v, want %v", tc, err, tc.want)
		}
	}
}

func TestAccP2RemoteAndVirtualWirePaths(t *testing.T) {
	index := p2Fixture(t, "p2.index")
	content := p2Fixture(t, "content.jar")
	var calls []string
	sources := &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(_ context.Context, q source.Request, permit source.Permit) (source.Result, error) {
			calls = append(calls, q.Resource.Path)
			if permit.Intent != source.MayFetch || !permit.UpstreamNetworkAllowed {
				t.Fatalf("wrong P2 retrieval intent: %+v", permit)
			}
			var body []byte
			switch q.Resource.Path {
			case "p2.index":
				body = index
			case "content.jar":
				body = content
			default:
				return source.Result{}, store.ErrNotFound
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
		want               int
	}{
		{http.MethodGet, "/artifactory/p2-remote/arbitrary.txt", "admin", 404},
		{http.MethodGet, "/artifactory/p2-remote/content.jar?url=https://attacker.example", "admin", 404},
		{http.MethodGet, "/artifactory/p2-remote/content.jar", "user", 403},
		{http.MethodGet, "/artifactory/p2-virtual/__p2/2/content.jar", "admin", 404},
		{http.MethodGet, "/artifactory/p2-virtual/__p2/0/../content.jar", "admin", 400},
		{http.MethodPut, "/artifactory/p2-virtual/content.jar", "admin", 405},
	} {
		res := w.do(tc.method, tc.path, tc.user, "bad")
		if res.Code != tc.want {
			t.Errorf("%s %s as %s: %d, want %d", tc.method, tc.path, tc.user, res.Code, tc.want)
		}
	}
	if len(calls) != 2 || calls[0] != "content.jar" || calls[1] != "p2.index" {
		t.Errorf("unexpected remote reads: %v", calls)
	}
	if _, err := w.db.Repos().Get(t.Context(), "p2-remote"); err != nil {
		t.Fatal(err)
	}
	if p2.Classify("plugins/org.example_1.0.jar") != p2.Artifact {
		t.Fatal("plugin path not recognized")
	}
}
