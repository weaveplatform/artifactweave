package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	condafmt "github.com/weaveplatform/artifactweave/internal/format/conda"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
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
