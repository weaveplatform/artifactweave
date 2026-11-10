package server

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

func TestUnitCRANSourceValidatesPublishedBytes(t *testing.T) {
	base := repopath.Path(t, "migration/artifacts/ecosystem/cran/testdata")
	index, err := os.ReadFile(filepath.Join(base, "assertthat-PACKAGES"))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(filepath.Join(base, "assertthat_0.2.1.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	q, err := itemkit.CranSourceRequest(source.Request{Resource: source.Resource{
		Path: "src/contrib/PACKAGES", Method: http.MethodGet,
	}})
	if err != nil || q.Resource.ValidateMetadata == nil || !q.Resource.PermissionFolder ||
		q.Resource.PermissionPath != "src/contrib" {
		t.Fatalf("CRAN metadata contract = %+v, err = %v", q.Resource, err)
	}
	for _, tc := range []struct {
		name string
		body []byte
		size int64
		bad  bool
	}{
		{"published", index, int64(len(index)), false},
		{"mismatched length", index, int64(len(index) + 1), true},
		{"unsafe name", []byte("Package: ../private\nVersion: 1.0\n\n"), 35, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := q.Resource.ValidateMetadata(t.Context(), bytes.NewReader(tc.body), tc.size,
				source.MetadataResponse{})
			if tc.bad != (err != nil) {
				t.Fatalf("metadata error = %v", err)
			}
		})
	}
	q.Resource.Query = "malformed=1"
	if _, err := itemkit.CranSourceRequest(q); !errors.Is(err, source.ErrInvalidRequest) {
		t.Fatalf("index query admitted: %v", err)
	}
	pkg, err := itemkit.CranSourceRequest(source.Request{Resource: source.Resource{
		Path: "src/contrib/assertthat_0.2.1.tar.gz", Method: http.MethodGet,
	}})
	if err != nil || pkg.Resource.Validate == nil || pkg.Resource.Variant != "cran-package-v1" {
		t.Fatalf("CRAN archive contract = %+v, err = %v", pkg.Resource, err)
	}
	if err := pkg.Resource.Validate(
		t.Context(),
		bytes.NewReader(archive),
		int64(len(archive)),
	); err != nil {
		t.Fatalf("published CRAN archive rejected: %v", err)
	}
	corrupt := bytes.Clone(archive)
	corrupt[len(corrupt)-1] ^= 0xff
	if err := pkg.Resource.Validate(
		t.Context(),
		bytes.NewReader(corrupt),
		int64(len(corrupt)),
	); err == nil {
		t.Fatal("corrupt archive admitted")
	}
	for _, p := range []string{
		"src/contrib/PACKAGES.rds", "src/contrib/../secret.tar.gz",
		"bin/windows/contrib/latest/demo_1.0.zip",
	} {
		if _, err := itemkit.CranSourceRequest(
			source.Request{Resource: source.Resource{Path: p}},
		); !errors.Is(
			err,
			store.ErrNotFound,
		) {
			t.Errorf("unsafe or unsupported path %q admitted: %v", p, err)
		}
	}
}

func TestUnitCRANVirtualRejectsCyclesAndWrongMembers(t *testing.T) {
	w := newItemWorld(t, false)
	for _, c := range []repo.Config{
		{Key: "cran-wrong", Class: repo.Local, PackageType: repo.Maven},
		{Key: "cran-local", Class: repo.Local, PackageType: repo.Cran},
		{Key: "cran-empty", Class: repo.Virtual, PackageType: repo.Cran, Repositories: []string{"absent"}},
		{Key: "cran-cycle", Class: repo.Virtual, PackageType: repo.Cran, Repositories: []string{"cran-cycle"}},
		{Key: "cran-mixed", Class: repo.Virtual, PackageType: repo.Cran, Repositories: []string{"cran-wrong"}},
	} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	p := kernel.Principal{Name: "admin", Admin: true}
	acc, err := itemkit.AccessFor(t.Context(), authorizerOver(w.db), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := cranAPI{newKit(Config{Store: w.db, Blobs: w.blobs})}
	for _, tc := range []struct {
		key      string
		expected error
	}{
		{"cran-empty", nil},
		{"cran-cycle", errCranVirtualCycle},
		{"cran-mixed", errCranMemberType},
	} {
		c, err := w.db.Repos().Get(t.Context(), tc.key)
		if err != nil {
			t.Fatal(err)
		}
		got, err := svc.cranCandidates(t.Context(), w.db, c, "src/contrib/PACKAGES", p, acc,
			map[string]bool{})
		if !errors.Is(err, tc.expected) || tc.expected == nil && len(got) != 0 {
			t.Errorf("%s: candidates=%v error=%v", tc.key, got, err)
		}
	}
	if _, err := svc.cranMemberIndex(t.Context(), w.db,
		repo.Config{Key: "cran-local", Class: repo.Local}, true,
		"src/contrib/PACKAGES", p, acc); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("local cache alias accepted: %v", err)
	}
}
