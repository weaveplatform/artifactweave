package cran_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const (
	cranLegacyPath  = "src/contrib/Archive/assertthat/0.2.1/assertthat_0.2.1.tar.gz"
	cranArchivePath = "src/contrib/Archive/assertthat/assertthat_0.2.1.tar.gz"
	cranMoveURL     = "/artifactory/api/cran/cran-migrate/move-archives"
)

func TestAccCRANArchiveMigration(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			w := newItemWorld(t, false, func(c *Config) { c.CranArchiveMoverEnabled = true })
			cranRepo(t, w, repo.Config{Key: "cran-migrate", Class: class, PackageType: repo.Cran})
			body := cranFixtureBytes(t, "assertthat_0.2.1.tar.gz")
			wantStatus(
				t,
				w.do(
					"PUT",
					"/artifactory/cran-migrate/"+cranLegacyPath+";team=research",
					"admin",
					string(body),
				),
				201,
			)
			before := w.item("cran-migrate", false, cranLegacyPath)
			ctx := t.Context()
			if err := w.db.Items().
				RecordDownload(ctx, before.ID, port.Download{By: "reader", At: time.Now()}); err != nil {
				t.Fatal(err)
			}
			props, err := w.db.Items().Properties(ctx, before.ID)
			if err != nil {
				t.Fatal(err)
			}
			stats, err := w.db.Items().Stats(ctx, before.ID)
			if err != nil {
				t.Fatal(err)
			}
			res := w.do("GET", cranMoveURL, "admin", "")
			wantStatus(t, res, 200)
			if !strings.Contains(res.Body.String(), `"moved":1`) {
				t.Fatalf("report: %s", res.Body)
			}
			after := w.item("cran-migrate", false, cranArchivePath)
			if after.Actual != before.Actual || after.Original != before.Original ||
				after.Created != before.Created {
				t.Fatalf("archive history changed: before %+v, after %+v", before, after)
			}
			gotProps, err := w.db.Items().Properties(ctx, after.ID)
			if err != nil || !reflect.DeepEqual(gotProps, props) {
				t.Fatalf("properties: %v %v", gotProps, err)
			}
			gotStats, err := w.db.Items().Stats(ctx, after.ID)
			if err != nil || gotStats != stats {
				t.Fatalf("statistics: %+v %v", gotStats, err)
			}
			blob, err := w.db.Blobs().Get(ctx, after.Actual.SHA256)
			if err != nil || blob.Refs != 1 {
				t.Fatalf("blob references: %+v %v", blob, err)
			}
			if _, err := w.db.Packages().VersionOf(ctx, after.ID); err != nil {
				t.Fatalf("migrated version link: %v", err)
			}
			wantStatus(
				t,
				w.do("GET", "/artifactory/cran-migrate/"+cranLegacyPath, "admin", ""),
				404,
			)
			res = w.do("GET", "/artifactory/cran-migrate/"+cranArchivePath, "reader", "")
			wantStatus(t, res, 200)
			if !bytes.Equal(res.Body.Bytes(), body) {
				t.Fatal("archive bytes changed")
			}
			wantStatus(
				t,
				w.do("GET", "/artifactory/cran-migrate/"+cranArchivePath, "user", ""),
				403,
			)
			res = w.do("GET", cranMoveURL, "admin", "")
			wantStatus(t, res, 200)
			if !strings.Contains(res.Body.String(), `"moved":0`) {
				t.Fatalf("repeat report: %s", res.Body)
			}
		})
	}
}

func TestAccCRANArchiveMigrationRollsBackWholeBatch(t *testing.T) {
	for _, collision := range []bool{false, true} {
		w := newItemWorld(t, false, func(c *Config) { c.CranArchiveMoverEnabled = true })
		cranRepo(t, w, repo.Config{Key: "cran-migrate", Class: repo.Local, PackageType: repo.Cran})
		body := string(cranFixtureBytes(t, "assertthat_0.2.1.tar.gz"))
		wantStatus(t, w.do("PUT", "/artifactory/cran-migrate/"+cranLegacyPath, "admin", body), 201)
		first := w.item("cran-migrate", false, cranLegacyPath)
		const secondPath = "src/contrib/Archive/zeta/1.0/zeta_1.0.tar.gz"
		want, refs := 400, int64(2)
		if collision {
			valid := conanTestArchive(t, "zeta/DESCRIPTION", "Package: zeta\nVersion: 1.0\n")
			wantStatus(t, w.do("PUT", "/artifactory/cran-migrate/"+secondPath, "admin", valid), 201)
			if _, err := w.db.Items().
				Put(t.Context(), model.Item{RepoKey: "cran-migrate", Name: "src/contrib/Archive/zeta/zeta_1.0.tar.gz", Folder: true}); err != nil {
				t.Fatal(err)
			}
			want, refs = 409, 1
		} else {
			// Imported metadata with an incorrect DESCRIPTION must be validated
			// again; a plausible filename alone is not trusted.
			second := first
			second.ID, second.Path, second.Name = 0, "", secondPath
			if _, err := w.db.Items().Put(t.Context(), second); err != nil {
				t.Fatal(err)
			}
			if _, err := w.db.Blobs().
				Ref(t.Context(), blob.Blob{Checksums: first.Actual, Size: first.Size}, 1); err != nil {
				t.Fatal(err)
			}
		}
		wantStatus(t, w.do("GET", cranMoveURL, "admin", ""), want)
		if got := w.item("cran-migrate", false, cranLegacyPath); got.ID != first.ID {
			t.Fatal("earlier source did not roll back")
		}
		w.item("cran-migrate", false, secondPath)
		if _, err := w.db.Items().
			Get(t.Context(), "cran-migrate", false, cranArchivePath); !errors.Is(
			err,
			kernel.ErrNotFound,
		) {
			t.Fatalf("earlier destination survived failure: %v", err)
		}
		if _, err := w.db.Packages().VersionOf(t.Context(), first.ID); err != nil {
			t.Fatalf("original version link lost: %v", err)
		}
		blob, err := w.db.Blobs().Get(t.Context(), first.Actual.SHA256)
		if err != nil || blob.Refs != refs {
			t.Fatalf("references changed on rollback: %+v %v", blob, err)
		}
	}
}

func TestAccCRANArchiveMigrationRefusals(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) { c.CranArchiveMoverEnabled = true })
	for _, class := range []repo.Class{repo.Local, repo.Remote, repo.Virtual} {
		cranRepo(
			t,
			w,
			repo.Config{
				Key:         "cran-" + string(class),
				Class:       class,
				PackageType: repo.Cran,
				URL:         "https://up.example",
			},
		)
	}
	for _, tc := range []struct {
		repo, user string
		status     int
	}{
		{"cran-local", "", 401},
		{"cran-local", "user", 403},
		{"cran-local", "reader", 403},
		{"missing", "admin", 404},
		{"libs-local", "admin", 400},
		{"cran-remote", "admin", 400},
		{"cran-remote-cache", "admin", 400},
		{"cran-virtual", "admin", 400},
	} {
		wantStatus(
			t,
			w.do("GET", "/artifactory/api/cran/"+tc.repo+"/move-archives", tc.user, ""),
			tc.status,
		)
	}
	disabled := newItemWorld(t, false)
	wantStatus(t, disabled.do("GET", cranMoveURL, "admin", ""), 404)
	wantStatus(t, disabled.do("GET", cranMoveURL, "user", ""), 403)
}

func TestAccCRANArchiveMigrationCollision(t *testing.T) {
	for _, folder := range []bool{false, true} {
		w := newItemWorld(t, false, func(c *Config) { c.CranArchiveMoverEnabled = true })
		cranRepo(t, w, repo.Config{Key: "cran-migrate", Class: repo.Local, PackageType: repo.Cran})
		body := string(cranFixtureBytes(t, "assertthat_0.2.1.tar.gz"))
		wantStatus(t, w.do("PUT", "/artifactory/cran-migrate/"+cranLegacyPath, "admin", body), 201)
		_, err := w.db.Items().
			Put(t.Context(), model.Item{RepoKey: "cran-migrate", Name: cranArchivePath, Folder: folder, Size: 123})
		if err != nil {
			t.Fatal(err)
		}
		wantStatus(t, w.do("GET", cranMoveURL, "admin", ""), 409)
		w.item("cran-migrate", false, cranLegacyPath)
		if got := w.item(
			"cran-migrate",
			false,
			cranArchivePath,
		); got.Size != 123 ||
			got.Folder != folder {
			t.Fatal("collision overwritten")
		}
	}
}

func TestAccCRANArchiveMigrationHonorsDestinationPolicy(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) { c.CranArchiveMoverEnabled = true })
	c := *repo.Defaults(&repo.Config{Key: "cran-migrate", Class: repo.Local, PackageType: repo.Cran})
	cranRepo(t, w, c)
	body := string(cranFixtureBytes(t, "assertthat_0.2.1.tar.gz"))
	wantStatus(t, w.do("PUT", "/artifactory/cran-migrate/"+cranLegacyPath, "admin", body), 201)
	c.ExcludesPattern = new(cranArchivePath)
	if err := w.db.Repos().Update(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do("GET", cranMoveURL, "admin", ""), 400)
	w.item(c.Key, false, cranLegacyPath)
	if _, err := w.db.Items().
		Get(t.Context(), c.Key, false, cranArchivePath); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatalf("published excluded archive: %v", err)
	}
}

type (
	cranMigrationFailure struct {
		harness.Store
		fail   bool
		mode   string
		cancel context.CancelFunc
	}
	cranMigrationItems struct {
		port.ItemStore
		owner *cranMigrationFailure
	}
)

func (s *cranMigrationFailure) Items() port.ItemStore { return cranMigrationItems{s.Store.Items(), s} }

func (s cranMigrationItems) Create(ctx context.Context, it model.Item) (model.Item, error) {
	if s.owner.fail {
		return model.Item{}, errors.New("storage unavailable")
	}
	return s.ItemStore.Create(ctx, it)
}

func (s cranMigrationItems) Descendants(
	ctx context.Context,
	key string,
	cached bool,
	folder string,
	page kernel.Page,
) ([]model.Item, string, error) {
	if s.owner.mode == "listing" {
		return nil, "", errors.New("archive listing unavailable")
	}
	entries, next, err := s.ItemStore.Descendants(ctx, key, cached, folder, page)
	if s.owner.cancel != nil {
		s.owner.cancel()
	}
	return entries, next, err
}

func (s cranMigrationItems) GetByID(ctx context.Context, id int64) (model.Item, error) {
	if s.owner.mode == "locking" {
		return model.Item{}, errors.New("archive lock unavailable")
	}
	return s.ItemStore.GetByID(ctx, id)
}

func (s cranMigrationItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	p string,
) (model.Item, error) {
	if s.owner.mode == "destination" && p == cranArchivePath {
		return model.Item{}, errors.New("archive destination unavailable")
	}
	return s.ItemStore.Get(ctx, key, cached, p)
}

func TestAccCRANArchiveMigrationStorageFailure(t *testing.T) {
	var failure *cranMigrationFailure
	w := newItemWorld(t, false, func(c *Config) {
		c.CranArchiveMoverEnabled = true
		failure = &cranMigrationFailure{Store: c.Store}
		c.Store = failure
	})
	cranRepo(t, w, repo.Config{Key: "cran-migrate", Class: repo.Local, PackageType: repo.Cran})
	body := string(cranFixtureBytes(t, "assertthat_0.2.1.tar.gz"))
	wantStatus(
		t,
		w.do("PUT", "/artifactory/cran-migrate/"+cranLegacyPath, "admin", body),
		http.StatusCreated,
	)
	failure.fail = true
	wantStatus(t, w.do("GET", cranMoveURL, "admin", ""), http.StatusInternalServerError)
	w.item("cran-migrate", false, cranLegacyPath)
	if _, err := w.db.Items().
		Get(t.Context(), "cran-migrate", false, cranArchivePath); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatalf("failed migration published destination: %v", err)
	}
	failure.fail = false
	for _, mode := range []string{"listing", "locking", "destination"} {
		failure.mode = mode
		wantStatus(t, w.do("GET", cranMoveURL, "admin", ""), http.StatusInternalServerError)
		w.item("cran-migrate", false, cranLegacyPath)
	}
	failure.mode = ""
	wantStatus(t, w.do("GET", cranMoveURL, "admin", ""), http.StatusOK)
}
