package nix_test

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	nixfmt "github.com/weaveplatform/artifactweave/internal/format/nix"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/trash"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// Nix's content-addressed archives are immutable, so this contract exercises
// overwrite refusal explicitly instead of mutating bytes at a fixed hash path.
func TestAccNixFormatLifecycle(t *testing.T) {
	const path = "fyli4q2yj6vh01bs0jxqbvj5hir5zvqc.narinfo"
	meta, err := os.ReadFile("testdata/cache/" + path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := nixfmt.Parse(meta, path)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile("testdata/cache/" + info.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range formattest.Backends() {
		t.Run(backend.Name, func(t *testing.T) {
			db := backend.Open(t)
			blobs, err := fs.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			items := &item.Service{Store: db, Blobs: blobs}
			trashSvc := &trash.Service{Store: db, Items: items}
			c := *repo.Defaults(&repo.Config{Key: "nix", Class: repo.Local, PackageType: repo.Nix})
			other := c
			other.Key = "copy"
			for _, config := range []repo.Config{c, other} {
				if err = db.Repos().Create(t.Context(), config); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range []struct {
				path string
				body []byte
			}{{info.URL, archive}, {path, meta}} {
				if _, err = items.Deploy(
					t.Context(),
					item.Deploy{
						Target: item.Target{Repo: c, Path: f.path},
						Body:   bytes.NewReader(f.body),
					},
				); err != nil {
					t.Fatal(err)
				}
			}
			assertVersion := func(c repo.Config, want bool) {
				t.Helper()
				p, err := db.Packages().Find(t.Context(), c.Key, "weave-fixture")
				if !want {
					if !errors.Is(err, store.ErrNotFound) {
						versions, _ := db.Packages().Versions(t.Context(), p.ID)
						if len(versions) > 0 {
							t.Fatalf("orphan index published: %v", versions)
						}
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				vs, err := db.Packages().Versions(t.Context(), p.ID)
				if err != nil || len(vs) != 1 {
					t.Fatalf("versions %v %v", vs, err)
				}
				files, err := db.Packages().Files(t.Context(), vs[0].ID)
				if err != nil || len(files) != 2 {
					t.Fatalf("files %v %v", files, err)
				}
			}
			assertVersion(c, true)
			if err = items.Reindex(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			assertVersion(c, true)
			result, err := items.Transfer(
				t.Context(),
				item.Transfer{
					From:         item.Target{Repo: c},
					To:           item.Target{Repo: other},
					CanOverwrite: true,
				},
			)
			if err != nil || result.Failed != 0 {
				t.Fatalf("copy: %#v %v", result, err)
			}
			assertVersion(other, true)
			if _, err = items.Deploy(
				t.Context(),
				item.Deploy{
					Target: item.Target{Repo: c, Path: info.URL},
					Body:   bytes.NewReader([]byte("tampered")),
				},
			); err == nil {
				t.Fatal("corrupted overwrite accepted")
			}
			assertVersion(c, true)
			if _, err = items.Delete(
				t.Context(),
				item.Target{Repo: c, Path: info.URL},
				"admin",
			); err != nil {
				t.Fatal(err)
			}
			assertVersion(c, false)
			if _, err = trashSvc.Restore(
				t.Context(),
				c.Key,
				info.URL,
				trash.Destination{Repo: c},
				"admin",
				nil,
			); err != nil {
				t.Fatal(err)
			}
			assertVersion(c, true)
			if _, err = items.Delete(t.Context(), item.Target{Repo: c}, "admin"); err != nil {
				t.Fatal(err)
			}
			assertVersion(c, false)
			if _, err = trashSvc.Restore(
				t.Context(),
				c.Key,
				"",
				trash.Destination{Repo: c},
				"admin",
				nil,
			); err != nil {
				t.Fatal(err)
			}
			assertVersion(c, true)
		})
	}
}
