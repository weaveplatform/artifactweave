package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	condafmt "github.com/weaveplatform/artifactweave/internal/format/conda"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func condaFixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "format", "conda", "testdata",
		"artifactweave-conda-fixture-1.0.0-0.tar.bz2"))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestUnitCondaChannelRejectsUnsafeRequestsAndVirtualCycles(t *testing.T) {
	w := newItemWorld(t, false)
	excluded := "**/*.tar.bz2"
	for _, c := range []repo.Config{
		{Key: "conda-empty", Class: repo.Local, PackageType: repo.Conda},
		{Key: "conda-empty-virtual", Class: repo.Virtual, PackageType: repo.Conda, Repositories: []string{"conda-empty"}},
		{Key: "conda-excluded-virtual", Class: repo.Virtual, PackageType: repo.Conda, Repositories: []string{"conda-empty"}, ExcludesPattern: &excluded},
		{Key: "conda-cycle-a", Class: repo.Virtual, PackageType: repo.Conda, Repositories: []string{"conda-cycle-b"}},
		{Key: "conda-cycle-b", Class: repo.Virtual, PackageType: repo.Conda, Repositories: []string{"conda-cycle-a"}},
		{Key: "conda-excluded", Class: repo.Local, PackageType: repo.Conda, ExcludesPattern: &excluded},
		{Key: "conda-duplicate", Class: repo.Virtual, PackageType: repo.Conda, Repositories: []string{"conda-empty", "conda-empty"}},
		{Key: "conda-cycle", Class: repo.Virtual, PackageType: repo.Conda, Repositories: []string{"conda-cycle"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	base := "/artifactory/api/conda/conda-empty/noarch/"
	for _, tc := range []struct {
		path string
		want int
	}{
		{base + "notices.json", http.StatusOK},
		{base + "repodata.json.bz2", http.StatusNotFound},
		{base + "unrelated.json", http.StatusNotFound},
		{base + "repodata.json?unsafe=1", http.StatusBadRequest},
		{base + "missing-1.0-0.tar.bz2", http.StatusNotFound},
		{"/artifactory/api/conda/conda-empty-virtual/noarch/missing-1.0-0.tar.bz2", http.StatusNotFound},
		{"/artifactory/api/conda/conda-excluded-virtual/noarch/missing-1.0-0.tar.bz2", http.StatusNotFound},
		{"/artifactory/api/conda/conda-cycle-a/noarch/repodata.json", http.StatusBadRequest},
		{base + "repodata.json", http.StatusOK},
		{"/artifactory/api/conda/conda-excluded/noarch/missing-1.0-0.tar.bz2", http.StatusNotFound},
		{"/artifactory/api/conda/conda-duplicate/noarch/repodata.json", http.StatusBadRequest},
		{"/artifactory/api/conda/conda-cycle/noarch/repodata.json", http.StatusBadRequest},
	} {
		wantStatus(t, w.do(http.MethodGet, tc.path, "admin", ""), tc.want)
	}
	wantStatus(t, w.do(http.MethodGet, base+"repodata.json", "user", ""), http.StatusForbidden)
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "conda-virtual-only",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
			Targets: map[string]types.Target{"conda-empty-virtual": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	indexOnly := w.do(
		http.MethodGet,
		"/artifactory/api/conda/conda-empty-virtual/noarch/repodata.json",
		"user",
		"",
	)
	wantStatus(t, indexOnly, http.StatusOK)
	if got := w.do(
		http.MethodHead,
		base+"repodata.json.zst",
		"admin",
		"",
	); got.Code != http.StatusOK ||
		got.Body.Len() != 0 {
		t.Fatalf("HEAD returned %d with %d body bytes", got.Code, got.Body.Len())
	}
}

func TestUnitCondaRemoteModernPackageIsFilteredByCurrentSourceAuthority(t *testing.T) {
	const name = "ca-certificates-2026.7.22-hbd8a1cb_0.conda"
	body, err := os.ReadFile(filepath.Join("..", "format", "conda", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := condafmt.ParsePackagePath("noarch/" + name)
	rec, err := condafmt.ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)), id)
	if err != nil {
		t.Fatal(err)
	}
	rec.Size, rec.SHA256 = int64(len(body)), fmt.Sprintf("%x", sha256.Sum256(body))
	index := condafmt.EmptyRepodata("noarch")
	index.PackagesConda[name] = rec
	encoded, err := condafmt.EncodeRepodata(index, "", "noarch")
	if err != nil {
		t.Fatal(err)
	}
	denied := false
	sources := &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(ctx context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			var data []byte
			switch q.Resource.Path {
			case "noarch/repodata.json":
				data = encoded["repodata.json"]
			case "noarch/" + name:
				if q.Resource.Checksums.SHA256 != rec.SHA256 || q.Resource.Validate == nil {
					t.Error("modern archive was not bound to metadata and parser")
				}
				if err := q.Resource.Validate(
					ctx,
					bytes.NewReader(body),
					int64(len(body)),
				); err != nil {
					t.Errorf("genuine modern archive rejected: %v", err)
				}
				data = body
			default:
				t.Errorf("unexpected modern source path %s", q.Resource.Path)
				return source.Result{}, source.ErrInvalidRequest
			}
			return source.Result{Stream: &source.Stream{
				Body: io.NopCloser(bytes.NewReader(data)), Status: http.StatusOK,
				Representation: source.Representation{Size: int64(len(data))},
			}}, nil
		},
	)}
	sources.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
		if denied && q.Resource.Path == "noarch/"+name {
			return source.ErrDenied
		}
		return nil
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = sources })
	c := *repo.Defaults(&repo.Config{Key: "conda-modern-remote", Class: repo.Remote, PackageType: repo.Conda, URL: "https://up.example"})
	if err := w.db.Repos().Create(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	base := "/artifactory/api/conda/" + c.Key + "/noarch/"
	wantStatus(t, w.do(http.MethodGet, base+name, "admin", ""), http.StatusOK)
	denied = true
	response := w.do(http.MethodGet, base+"repodata.json", "admin", "")
	wantStatus(t, response, http.StatusOK)
	metadataPath, _ := condafmt.ParseMetadataPath("noarch/repodata.json")
	filtered, err := condafmt.DecodeRepodata(response.Body.Bytes(), metadataPath)
	if err != nil || len(filtered.PackagesConda) != 0 {
		t.Fatalf("revoked modern package remained in index: %+v %v", filtered, err)
	}
	if got := w.do(http.MethodGet, base+name, "admin", ""); got.Code == http.StatusOK {
		t.Fatal("revoked modern archive served")
	}
}

func TestUnitCondaLocalIndexRejectsCorruptionAndIncompatibleMember(t *testing.T) {
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{Key: "conda-broken", Class: repo.Local, PackageType: repo.Conda},
		{Key: "conda-missing-blob", Class: repo.Local, PackageType: repo.Conda},
		{Key: "conda-folder-index", Class: repo.Local, PackageType: repo.Conda},
		{Key: "conda-wrong-member", Class: repo.Virtual, PackageType: repo.Conda, Repositories: []string{"other-local"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	bad := `{"info":{"subdir":"noarch"},"packages":{"../evil-1-0.tar.bz2":{}}}`
	wantStatus(
		t,
		w.do(http.MethodPut, "/artifactory/other-local/bad-conda-index", "admin", bad),
		http.StatusCreated,
	)
	_, err := w.db.Items().Put(t.Context(), types.Item{
		RepoKey: "conda-broken", Path: "noarch", Name: "repodata.json", Size: int64(len(bad)),
		Actual: types.Checksums{SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(bad)))},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.do(
		http.MethodGet,
		"/artifactory/api/conda/conda-broken/noarch/repodata.json",
		"admin",
		"",
	); got.Code == http.StatusOK {
		t.Fatal("corrupt stored channel metadata was served")
	}
	if _, err := w.db.Items().Put(t.Context(), types.Item{
		RepoKey: "conda-missing-blob", Name: "noarch/repodata.json", Size: 1,
		Actual: types.Checksums{SHA256: fmt.Sprintf("%064x", 0)},
	}); err != nil {
		t.Fatal(err)
	}
	if got := w.do(
		http.MethodGet,
		"/artifactory/api/conda/conda-missing-blob/noarch/repodata.json",
		"admin",
		"",
	); got.Code == http.StatusOK {
		t.Fatal("missing index blob was silently replaced with an empty channel")
	}
	if _, err := w.db.Items().
		Put(t.Context(), types.Item{RepoKey: "conda-folder-index", Name: "noarch/repodata.json", Folder: true}); err != nil {
		t.Fatal(err)
	}
	wantStatus(
		t,
		w.do(
			http.MethodGet,
			"/artifactory/api/conda/conda-folder-index/noarch/repodata.json",
			"admin",
			"",
		),
		http.StatusNotFound,
	)
	wantStatus(t, w.do(http.MethodGet,
		"/artifactory/api/conda/conda-wrong-member/noarch/repodata.json", "admin", ""),
		http.StatusBadRequest)
}

func TestUnitCondaRemoteIndexAndArchiveTrustBoundary(t *testing.T) {
	const name = "artifactweave-conda-fixture-1.0.0-0.tar.bz2"
	body := condaFixture(t)
	id, _ := condafmt.ParsePackagePath("noarch/" + name)
	rec, err := condafmt.ReadPackage(t.Context(), bytes.NewReader(body), int64(len(body)), id)
	if err != nil {
		t.Fatal(err)
	}
	rec.SHA256, rec.Size = fmt.Sprintf("%x", sha256.Sum256(body)), int64(len(body))
	index := condafmt.EmptyRepodata("noarch")
	index.Packages[name] = rec
	encoded, err := condafmt.EncodeRepodata(index, "", "noarch")
	if err != nil {
		t.Fatal(err)
	}
	mode := "good"
	cachedSHA := ""
	metadataReads, packageReads := 0, 0
	sources := &source.Service{Resolve: sourceIdentity, HTTP: sourceBackend(
		func(ctx context.Context, q source.Request, _ source.Permit) (source.Result, error) {
			switch q.Resource.Path {
			case "noarch/repodata.json":
				metadataReads++
				if mode == "outage" {
					return source.Result{}, source.ErrUnavailable
				}
				if mode == "empty-result" {
					return source.Result{}, nil
				}
				if mode == "cached-index" || mode == "missing-cached-index" {
					sha := cachedSHA
					if mode == "missing-cached-index" {
						sha = fmt.Sprintf("%064x", 0)
					}
					return source.Result{Item: &types.Item{
						RepoKey: "conda-remote",
						Path:    "noarch",
						Name:    "repodata.json",
						Cached:  true,
						Actual:  types.Checksums{SHA256: sha},
					}}, nil
				}
				if mode == "broken-stream" {
					return source.Result{
						Stream: &source.Stream{
							Body:   io.NopCloser(opkgReadError{}),
							Status: http.StatusOK,
						},
					}, nil
				}
				payload := encoded["repodata.json"]
				if mode == "bad-index" {
					payload = []byte(`{"packages":{"../evil-1-0.tar.bz2":{}}}`)
				}
				if q.Resource.ValidateMetadata == nil {
					t.Error("upstream metadata validator missing")
				} else if err := q.Resource.ValidateMetadata(ctx, bytes.NewReader(payload), int64(len(payload)), source.MetadataResponse{}); (err == nil) == (mode == "bad-index") {
					t.Errorf("upstream index validation: mode=%s error=%v", mode, err)
				}
				result := source.Result{
					Stream: &source.Stream{
						Body:           io.NopCloser(bytes.NewReader(payload)),
						Status:         http.StatusOK,
						Representation: source.Representation{Size: int64(len(payload))},
					},
				}
				if mode == "revoked-index" {
					result.Revalidate = func(context.Context) error { return source.ErrDenied }
				}
				return result, nil
			case "noarch/" + name:
				packageReads++
				if q.Resource.Checksums.SHA256 != rec.SHA256 || q.Resource.Validate == nil {
					t.Errorf(
						"remote package not bound to index digest and archive validator: %+v",
						q.Resource,
					)
				}
				if mode == "bad-package" {
					if err := q.Resource.Validate(
						ctx,
						bytes.NewReader([]byte("bad")),
						3,
					); err == nil {
						t.Error("invalid package passed source validator")
					}
					return source.Result{}, source.ErrInvalidResult
				}
				if err := q.Resource.Validate(
					ctx,
					bytes.NewReader(body),
					int64(len(body))+1,
				); err == nil {
					t.Error("package whose size differs from signed index was accepted")
				}
				if err := q.Resource.Validate(
					ctx,
					bytes.NewReader(body),
					int64(len(body)),
				); err != nil {
					t.Errorf("valid package failed source validator: %v", err)
				}
				return source.Result{
					Stream: &source.Stream{
						Body:           io.NopCloser(bytes.NewReader(body)),
						Status:         http.StatusOK,
						Representation: source.Representation{Size: int64(len(body))},
					},
				}, nil
			default:
				t.Errorf("unexpected Conda source path %q", q.Resource.Path)
				return source.Result{}, source.ErrInvalidRequest
			}
		},
	)}
	sources.Revalidate = func(_ context.Context, q source.Request, _ source.Permit) error {
		if q.Resource.Path == "noarch/"+name {
			switch mode {
			case "denied-package":
				return source.ErrDenied
			case "failed-check":
				return source.ErrUnavailable
			}
		}
		return nil
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = sources })
	cachedSHA = fmt.Sprintf("%x", sha256.Sum256(encoded["repodata.json"]))
	wantStatus(
		t,
		w.do(
			http.MethodPut,
			"/artifactory/other-local/conda-cached-index",
			"admin",
			string(encoded["repodata.json"]),
		),
		http.StatusCreated,
	)
	remote := *repo.Defaults(&repo.Config{Key: "conda-remote", Class: repo.Remote, PackageType: repo.Conda, URL: "https://up.example"})
	if err := w.db.Repos().Create(t.Context(), remote); err != nil {
		t.Fatal(err)
	}
	copy := *repo.Defaults(&repo.Config{Key: "conda-remote-copy", Class: repo.Remote, PackageType: repo.Conda, URL: "https://up.example"})
	if err := w.db.Repos().Create(t.Context(), copy); err != nil {
		t.Fatal(err)
	}
	virtual := *repo.Defaults(&repo.Config{Key: "conda-remote-virtual", Class: repo.Virtual, PackageType: repo.Conda, Repositories: []string{"missing", remote.Key, copy.Key}})
	if err := w.db.Repos().Create(t.Context(), virtual); err != nil {
		t.Fatal(err)
	}
	base := "/artifactory/api/conda/conda-remote/noarch/"
	wantStatus(t, w.do(http.MethodGet, base+"repodata.json", "admin", ""), http.StatusOK)
	got := w.do(http.MethodGet, base+name, "admin", "")
	wantStatus(t, got, http.StatusOK)
	if !bytes.Equal(got.Body.Bytes(), body) || metadataReads < 2 || packageReads != 1 {
		t.Fatalf(
			"remote metadata/package mismatch: index=%d package=%d",
			metadataReads,
			packageReads,
		)
	}
	virtualBase := "/artifactory/api/conda/" + virtual.Key + "/noarch/"
	virtualIndex := w.do(http.MethodGet, virtualBase+"repodata.json", "admin", "")
	wantStatus(t, virtualIndex, http.StatusOK)
	virtualPath, _ := condafmt.ParseMetadataPath("noarch/repodata.json")
	combined, err := condafmt.DecodeRepodata(virtualIndex.Body.Bytes(), virtualPath)
	if err != nil || len(combined.Packages) != 1 {
		t.Fatalf("duplicate upstream package was not combined once: %+v %v", combined, err)
	}
	wantStatus(t, w.do(http.MethodGet, virtualBase+name, "admin", ""), http.StatusOK)
	mode = "outage"
	if got := w.do(http.MethodGet, base+"repodata.json", "admin", ""); got.Code == http.StatusOK {
		t.Fatal("upstream outage served as healthy index")
	}
	mode = "bad-index"
	if got := w.do(http.MethodGet, base+"repodata.json", "admin", ""); got.Code == http.StatusOK {
		t.Fatal("malicious upstream index served")
	}
	mode = "bad-package"
	if got := w.do(http.MethodGet, base+name, "admin", ""); got.Code == http.StatusOK {
		t.Fatal("corrupt upstream package served")
	}
	mode = "cached-index"
	wantStatus(t, w.do(http.MethodGet, base+"repodata.json", "admin", ""), http.StatusOK)
	mode = "missing-cached-index"
	if got := w.do(http.MethodGet, base+"repodata.json", "admin", ""); got.Code == http.StatusOK {
		t.Fatal("cached index with missing blob served")
	}
	mode = "denied-package"
	filtered := w.do(http.MethodGet, base+"repodata.json", "admin", "")
	wantStatus(t, filtered, http.StatusOK)
	metadataPath, _ := condafmt.ParseMetadataPath("noarch/repodata.json")
	filteredIndex, err := condafmt.DecodeRepodata(filtered.Body.Bytes(), metadataPath)
	if err != nil || len(filteredIndex.Packages) != 0 {
		t.Fatalf("revoked source package leaked through index: %+v %v", filteredIndex, err)
	}
	if got := w.do(http.MethodGet, base+name, "admin", ""); got.Code == http.StatusOK {
		t.Fatal("revoked source package served")
	}
	for _, modeUnderTest := range []string{"failed-check", "revoked-index", "empty-result", "broken-stream"} {
		mode = modeUnderTest
		if got := w.do(
			http.MethodGet,
			base+"repodata.json",
			"admin",
			"",
		); got.Code == http.StatusOK {
			t.Errorf("%s served a trusted index", modeUnderTest)
		}
	}
	mode = "revoked-index"
	if got := w.do(http.MethodGet, base+name, "admin", ""); got.Code == http.StatusOK {
		t.Fatal("package served after metadata source authority was revoked")
	}
	if got := w.do(
		http.MethodGet,
		virtualBase+"repodata.json",
		"admin",
		"",
	); got.Code == http.StatusOK {
		t.Fatal("virtual channel served an index after source authority was revoked")
	}
	if got := w.do(http.MethodGet, virtualBase+name, "admin", ""); got.Code == http.StatusOK {
		t.Fatal("virtual package served after source authority was revoked")
	}
}

func TestUnitCondaLocalAndVirtualNativeChannel(t *testing.T) {
	var q *work.Queue
	w := newItemWorld(t, false, func(cfg *Config) {
		var err error
		q, err = work.New(cfg.Store, work.Options{})
		if err != nil {
			t.Fatal(err)
		}
		svc := &item.Service{Store: cfg.Store, Blobs: cfg.Blobs, Work: q}
		if err := svc.RegisterWork(q); err != nil {
			t.Fatal(err)
		}
		cfg.Work = q
	})
	for _, c := range []repo.Config{
		{Key: "conda-local", Class: repo.Local, PackageType: repo.Conda},
		{Key: "conda-fed", Class: repo.Federated, PackageType: repo.Conda},
		{
			Key: "conda-virtual", Class: repo.Virtual, PackageType: repo.Conda,
			Repositories: []string{"conda-local", "conda-fed"},
		},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	const filename = "artifactweave-conda-fixture-1.0.0-0.tar.bz2"
	body := condaFixture(t)
	put := w.do(http.MethodPut, "/artifactory/conda-local/noarch/"+filename,
		"admin", string(body))
	wantStatus(t, put, http.StatusCreated)
	wantStatus(t, w.do(http.MethodPost,
		"/artifactory/api/conda/conda-local/reindex?async=true", "admin", ""), http.StatusOK)
	if err := q.EnsureFresh(t.Context(), condafmt.KindIndex, "conda-local"); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "conda-index-only",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
			Targets: map[string]types.Target{
				"conda-local":   {Include: []string{"**"}, Exclude: []string{"**/*.tar.bz2"}},
				"conda-virtual": {Include: []string{"**"}, Exclude: []string{"**/*.tar.bz2"}},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	hidden := w.do(http.MethodGet,
		"/artifactory/api/conda/conda-virtual/noarch/repodata.json", "user", "")
	wantStatus(t, hidden, http.StatusOK)
	id, _ := condafmt.ParseMetadataPath("noarch/repodata.json")
	hiddenIndex, err := condafmt.DecodeRepodata(hidden.Body.Bytes(), id)
	if err != nil || len(hiddenIndex.Packages) != 0 {
		t.Fatalf("unreadable package leaked through virtual index: %+v %v", hiddenIndex, err)
	}
	wantStatus(t, w.do(http.MethodGet,
		"/artifactory/api/conda/conda-virtual/noarch/"+filename, "user", ""),
		http.StatusForbidden)
	for _, key := range []string{"conda-local", "conda-virtual"} {
		for _, metadata := range []string{"repodata.json", "repodata.json.zst", "current_repodata.json"} {
			rec := w.do(http.MethodGet, "/artifactory/api/conda/"+key+"/noarch/"+metadata,
				"reader", "")
			wantStatus(t, rec, http.StatusOK)
			id, _ := condafmt.ParseMetadataPath("noarch/" + metadata)
			index, err := condafmt.DecodeRepodata(rec.Body.Bytes(), id)
			if err != nil || index.Packages[filename].Name != "artifactweave-conda-fixture" {
				t.Fatalf("%s %s: %+v, %v", key, metadata, index, err)
			}
		}
		rec := w.do(http.MethodGet, "/artifactory/api/conda/"+key+"/noarch/"+filename,
			"reader", "")
		wantStatus(t, rec, http.StatusOK)
		if !bytes.Equal(rec.Body.Bytes(), body) {
			t.Fatalf("%s package bytes changed", key)
		}
	}
	missing := w.do(http.MethodGet, "/artifactory/api/conda/conda-local/linux-64/repodata.json",
		"reader", "")
	wantStatus(t, missing, http.StatusOK)
	var empty condafmt.Repodata
	if err := json.Unmarshal(missing.Body.Bytes(), &empty); err != nil ||
		len(empty.Packages) != 0 || empty.Info.Subdir != "linux-64" {
		t.Fatalf("empty platform index: %+v %v", empty, err)
	}
	deleteRec := w.do(http.MethodDelete, "/artifactory/conda-local/noarch/"+filename,
		"admin", "")
	wantStatus(t, deleteRec, http.StatusNoContent)
	after := w.do(http.MethodGet,
		"/artifactory/api/conda/conda-local/noarch/repodata.json", "reader", "")
	wantStatus(t, after, http.StatusOK)
	id, _ = condafmt.ParseMetadataPath("noarch/repodata.json")
	index, err := condafmt.DecodeRepodata(after.Body.Bytes(), id)
	if err != nil || len(index.Packages) != 0 {
		t.Fatalf("deleted package remains indexed: %+v %v", index, err)
	}
}
