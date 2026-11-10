package server

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestUnitP2SourceAdmission(t *testing.T) {
	for _, name := range []string{"p2.index", "content.jar", "artifacts.jar", "content.xml.xz", "artifacts.xml.xz"} {
		q, err := itemkit.P2SourceRequest(
			source.Request{Resource: source.Resource{Path: name, Method: http.MethodGet}},
		)
		if err != nil || q.Resource.Kind != source.ResourceKindMetadata ||
			q.Resource.ValidateMetadata == nil {
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
		expected                 error
	}{
		{"../content.jar", "", "", store.ErrNotFound},
		{"content.jar", "download=1", "", source.ErrInvalidRequest},
		{"plugins/org.example_1.0.jar", "", "bytes=0-10", source.ErrInvalidRequest},
	} {
		_, err := itemkit.P2SourceRequest(
			source.Request{
				Resource: source.Resource{Path: tc.path, Query: tc.query, Range: tc.rangeHeader},
			},
		)
		if !errors.Is(err, tc.expected) {
			t.Errorf("%+v: %v, want %v", tc, err, tc.expected)
		}
	}
	plugin := "org.eclipse.glsp.layout.source_2.7.0.jar"
	q, err := itemkit.P2SourceRequest(source.Request{
		Resource: source.Resource{
			Kind:   source.ResourceKindArtifact,
			Path:   "plugins/" + plugin,
			Method: http.MethodGet,
		},
	})
	if err != nil || q.Resource.Kind != source.ResourceKindArtifact || q.Resource.Validate == nil {
		t.Fatalf("plugin source request: %+v %v", q.Resource, err)
	}
	body := p2Fixture(t, plugin)
	if err := q.Resource.Validate(
		t.Context(),
		bytes.NewReader(body),
		int64(len(body)),
	); err != nil {
		t.Errorf("published plugin rejected: %v", err)
	}
	if err := q.Resource.Validate(t.Context(), bytes.NewReader([]byte("bad")), 3); err == nil {
		t.Error("invalid plugin admitted to source cache")
	}
}

func TestUnitP2ArchiveURLAcceptsOnlyHTTPZipRoot(t *testing.T) {
	for _, tc := range []struct {
		url, expected string
	}{
		{"https://eclipse.example/site.zip!/", "https://eclipse.example/site.zip"},
		{"https://eclipse.example/site.ZIP!/", "https://eclipse.example/site.ZIP"},
		{"https://eclipse.example/site.zip", ""},
		{"https://eclipse.example/site.zip!/nested", ""},
		{"https://user:password@eclipse.example/site.zip!/", ""},
		{"https://eclipse.example/site.zip#fragment!/", ""},
		{"file:///tmp/site.zip!/", ""},
		{"https://eclipse.example/site.jar!/", ""},
		{"https://[invalid/site.zip!/", ""},
	} {
		got, ok := p2ArchiveURL(tc.url)
		if got != tc.expected || ok != (tc.expected != "") {
			t.Errorf("archive URL %q: %q, %v; want %q", tc.url, got, ok, tc.expected)
		}
	}
}

func TestUnitP2VirtualFailsClosedWhenMemberStoreFails(t *testing.T) {
	w := newItemWorld(t, false)
	admin := accessapi.Subject{Name: "admin", Admin: true}
	d, err := accesstest.Over(t, w.db).Decide(t.Context(), admin, false)
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("repository store unavailable")
	st := p2FaultStore{Store: w.db, err: fault}
	c := repo.Config{
		Key:          "p2-parent",
		Class:        repo.Virtual,
		PackageType:  repo.P2,
		Repositories: []string{"p2-child"},
	}
	if _, err := p2Members(
		t.Context(),
		st,
		c,
		itemkit.Access{d},
		map[string]bool{},
	); !errors.Is(
		err,
		fault,
	) {
		t.Errorf("member store failure was hidden: %v", err)
	}
}

func p2Fixture(t *testing.T, name string) []byte {
	t.Helper()
	return repopath.Read(t, "migration/artifacts/ecosystem/p2/testdata/"+name)
}

type p2FaultStore struct {
	store.Store
	err error
}

func (s p2FaultStore) Repos() store.RepoStore { return p2FaultRepos{s.Store.Repos(), s.err} }

type p2FaultRepos struct {
	store.RepoStore
	err error
}

func (s p2FaultRepos) Get(
	context.Context,
	string,
) (repo.Config, error) {
	return repo.Config{}, s.err
}
