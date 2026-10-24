package docker_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
)

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func TestUnitPaths(t *testing.T) {
	if got := layout.BlobFile("ab"); got != "sha256__ab" {
		t.Fatal(got)
	}
	if got := layout.UploadPath("team/app", "ab"); got != "team/app/_uploads/sha256__ab" {
		t.Fatal(got)
	}
	for pt, want := range map[repo.PackageType]bool{
		repo.Docker: true, repo.OCI: true, repo.HelmOCI: true, repo.Helm: false, repo.Generic: false,
	} {
		if layout.Serves(pt) != want {
			t.Errorf("%s: %v", pt, !want)
		}
	}
}

// FindBlob is an AQL query, so it runs on the reference evaluator and on
// SQL, where a name's _ and . must not act as wildcards.
func TestUnitFindBlob(t *testing.T) {
	for name, open := range map[string]func(t *testing.T) store.Store{
		"inmem": func(*testing.T) store.Store { return inmem.New() },
		"sqlite": func(t *testing.T) store.Store {
			st, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "aw.db"),
				sqlite.Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			return st
		},
	} {
		t.Run(name, func(t *testing.T) { findBlob(t, open(t)) })
	}
}

func findBlob(t *testing.T, db store.Store) {
	ctx := context.Background()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	items := &item.Service{Store: db, Blobs: blobs}
	configs := map[string]repo.Config{}
	for _, key := range []string{"docker-local", "other"} {
		c := *repo.Defaults(&repo.Config{Key: key, Class: repo.Local, PackageType: repo.Docker})
		if err := db.Repos().Create(ctx, c); err != nil {
			t.Fatal(err)
		}
		configs[key] = c
	}
	deploy := func(key, path, body string) {
		t.Helper()
		if _, err := items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: configs[key], Path: path}, Body: strings.NewReader(body),
		}); err != nil {
			t.Fatal(err)
		}
	}
	upload, tagged, digest := sum("upload"), sum("tagged"), sum("by digest")
	deploy("docker-local", "a_b.c/_uploads/sha256__"+upload, "upload")
	deploy("docker-local", "a_b.c/v1/sha256__"+tagged, "tagged")
	deploy("docker-local", "a_b.c/"+layout.DigestFolder(sum("m"))+"/sha256__"+digest, "by digest")
	// Only deeper, in another image named a_b.c/sub.
	deploy("docker-local", "a_b.c/sub/v1/sha256__"+sum("deep"), "deep")
	// Under the name's look-alike, which a LIKE wildcard would match.
	deploy("docker-local", "aXb.c/v1/sha256__"+sum("lookalike"), "lookalike")
	// In another repository.
	deploy("other", "a_b.c/v1/sha256__"+sum("elsewhere"), "elsewhere")
	// Named for content it does not hold.
	deploy("docker-local", "a_b.c/v2/sha256__"+sum("claimed"), "actual")
	for _, hex := range []string{upload, tagged, digest} {
		it, err := layout.FindBlob(ctx, db.Items(), "docker-local", "a_b.c", hex)
		if err != nil || it.Actual.SHA256 != hex || it.Name != "sha256__"+hex {
			t.Errorf("%s: %+v %v", hex, it, err)
		}
	}
	for name, hex := range map[string]string{
		"deeper": sum("deep"), "look-alike": sum("lookalike"), "elsewhere": sum("elsewhere"),
		"claimed": sum("claimed"), "absent": sum("absent"),
	} {
		if _, err := layout.FindBlob(ctx, db.Items(), "docker-local", "a_b.c", hex); !errors.Is(
			err, store.ErrNotFound,
		) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if it, err := layout.FindBlob(
		ctx,
		db.Items(),
		"docker-local",
		"a_b.c/sub",
		sum("deep"),
	); err != nil ||
		it.Path != "a_b.c/sub/v1" {
		t.Errorf("the deeper image: %+v %v", it, err)
	}
}

type failingSearch struct{ store.ItemStore }

func (failingSearch) Search(context.Context, *aql.Query, aql.Options) ([]aql.Hit, error) {
	return nil, errors.New("boom")
}

func TestUnitFindBlobFails(t *testing.T) {
	_, err := layout.FindBlob(context.Background(), failingSearch{}, "r", "app", sum("x"))
	if err == nil || errors.Is(err, store.ErrNotFound) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
}
