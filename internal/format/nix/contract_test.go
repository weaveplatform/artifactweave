package nix_test

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format"
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
			changed := info
			changed.StorePath = strings.Replace(info.StorePath, "weave-fixture", "other-name", 1)
			if _, err = items.Deploy(
				t.Context(),
				item.Deploy{
					Target: item.Target{Repo: c, Path: path},
					Body:   bytes.NewReader(changed.Encode(changed.URL)),
				},
			); err == nil {
				t.Fatal("Nix store-path identity changed")
			}
			if _, err = db.Packages().
				Find(t.Context(), c.Key, "other-name"); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatalf("rejected identity published: %v", err)
			}
			current, err := db.Items().Get(t.Context(), c.Key, false, path)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := nixfmt.ReadInfo(
				t.Context(),
				format.Env{Store: db, Blobs: blobs},
				current,
			)
			if err != nil || restored.StorePath != info.StorePath {
				t.Fatalf("original identity lost: %v", err)
			}
			refreshed := info
			refreshed.Deriver = "unknown-deriver"
			if _, err = items.Deploy(
				t.Context(),
				item.Deploy{
					Target: item.Target{Repo: c, Path: path},
					Body:   bytes.NewReader(refreshed.Encode(refreshed.URL)),
				},
			); err != nil {
				t.Fatalf("unsigned metadata refresh refused: %v", err)
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

func TestAccNixConcurrentFirstPublicationKeepsOneIdentity(t *testing.T) {
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
			c := *repo.Defaults(&repo.Config{Key: "race", Class: repo.Local, PackageType: repo.Nix})
			if err = db.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			if _, err = items.Deploy(
				t.Context(),
				item.Deploy{
					Target: item.Target{Repo: c, Path: info.URL},
					Body:   bytes.NewReader(archive),
				},
			); err != nil {
				t.Fatal(err)
			}
			changed := info
			changed.StorePath = strings.Replace(info.StorePath, "weave-fixture", "other-name", 1)
			start := make(chan struct{})
			results := make(chan error, 2)
			for _, body := range [][]byte{meta, changed.Encode(changed.URL)} {
				go func() {
					<-start
					_, err := items.Deploy(
						t.Context(),
						item.Deploy{
							Target: item.Target{Repo: c, Path: path},
							Body:   bytes.NewReader(body),
						},
					)
					results <- err
				}()
			}
			close(start)
			accepted, rejected := 0, 0
			for range 2 {
				err := <-results
				var refusal *format.RejectionError
				if err == nil {
					accepted++
				} else if errors.As(err, &refusal) && refusal.Status == 409 {
					rejected++
				} else {
					t.Fatal(err)
				}
			}
			if accepted != 1 || rejected != 1 {
				t.Fatalf("accepted %d rejected %d", accepted, rejected)
			}
			it, err := db.Items().Get(t.Context(), c.Key, false, path)
			if err != nil {
				t.Fatal(err)
			}
			winner, err := nixfmt.ReadInfo(t.Context(), format.Env{Store: db, Blobs: blobs}, it)
			if err != nil {
				t.Fatal(err)
			}
			losing := "other-name"
			if strings.HasSuffix(winner.StorePath, "other-name") {
				losing = "weave-fixture"
			}
			if _, err = db.Packages().
				Find(t.Context(), c.Key, losing); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatalf("losing identity indexed: %v", err)
			}
		})
	}
}
