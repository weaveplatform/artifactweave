package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccCRANArchiveDeletedAfterEnumeration(t *testing.T) {
	w := newItemWorld(t, false)
	c := *repo.Defaults(&repo.Config{Key: "cran-migrate", Class: repo.Local, PackageType: repo.Cran})
	cranRepo(t, w, c)
	body := string(cranFixtureBytes(t, "assertthat_0.2.1.tar.gz"))
	wantStatus(t, w.do("PUT", "/artifactory/cran-migrate/"+cranLegacyPath, "admin", body), 201)
	listed := w.item(c.Key, false, cranLegacyPath)
	wantStatus(t, w.do("DELETE", "/artifactory/cran-migrate/"+cranLegacyPath, "admin", ""), 204)
	svc := &item.Service{Store: w.db, Blobs: w.blobs, Formats: ecosystem.For}
	// A READ COMMITTED listing can precede another request's deletion. The
	// migration must not resurrect that stale listed item after taking its lock.
	moved, err := moveCranArchive(t.Context(), svc, c, listed, cranArchivePath, "admin")
	if err != nil || moved != 0 {
		t.Fatalf("deleted archive: moved %d, error %v", moved, err)
	}
	if _, err := w.db.Items().
		Get(t.Context(), c.Key, false, cranArchivePath); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("deleted archive resurrected: %v", err)
	}
}

type (
	cranMigrationFailure struct {
		store.Store
		fail   bool
		mode   string
		cancel context.CancelFunc
	}
	cranMigrationItems struct {
		store.ItemStore
		owner *cranMigrationFailure
	}
)

func (s *cranMigrationFailure) Items() store.ItemStore { return cranMigrationItems{s.Store.Items(), s} }

func (s cranMigrationItems) Create(ctx context.Context, it types.Item) (types.Item, error) {
	if s.owner.fail {
		return types.Item{}, errors.New("storage unavailable")
	}
	return s.ItemStore.Create(ctx, it)
}

func (s cranMigrationItems) Descendants(
	ctx context.Context,
	key string,
	cached bool,
	folder string,
	page store.Page,
) ([]types.Item, string, error) {
	if s.owner.mode == "listing" {
		return nil, "", errors.New("archive listing unavailable")
	}
	entries, next, err := s.ItemStore.Descendants(ctx, key, cached, folder, page)
	if s.owner.cancel != nil {
		s.owner.cancel()
	}
	return entries, next, err
}

func (s cranMigrationItems) GetByID(ctx context.Context, id int64) (types.Item, error) {
	if s.owner.mode == "locking" {
		return types.Item{}, errors.New("archive lock unavailable")
	}
	return s.ItemStore.GetByID(ctx, id)
}

func (s cranMigrationItems) Get(
	ctx context.Context,
	key string,
	cached bool,
	p string,
) (types.Item, error) {
	if s.owner.mode == "destination" && p == cranArchivePath {
		return types.Item{}, errors.New("archive destination unavailable")
	}
	return s.ItemStore.Get(ctx, key, cached, p)
}

func TestAccCRANArchiveMigrationPaginationAndCancellation(t *testing.T) {
	var cfg Config
	w := newItemWorld(t, false, func(c *Config) { c.CranArchiveMoverEnabled = true; cfg = *c })
	c := *repo.Defaults(&repo.Config{Key: "cran-migrate", Class: repo.Local, PackageType: repo.Cran})
	cranRepo(t, w, c)
	body := string(cranFixtureBytes(t, "assertthat_0.2.1.tar.gz"))
	wantStatus(t, w.do("PUT", "/artifactory/cran-migrate/"+cranLegacyPath, "admin", body), 201)
	// These unrelated stored notes must survive; the archive after them must
	// still be found when the listing spans more than a full page.
	for n := range store.MaxLimit + 1 {
		if _, err := w.db.Items().
			Put(t.Context(), types.Item{RepoKey: c.Key, Name: fmt.Sprintf("src/contrib/Archive/a-notes/%04d", n)}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	failure := &cranMigrationFailure{Store: w.db, cancel: cancel}
	service := CranAPI{Kit: newKit(cfg)}
	if _, err := service.moveCranArchives(
		ctx,
		failure,
		c,
		"admin",
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("cancelled migration: %v", err)
	}
	w.item(c.Key, false, cranLegacyPath)
	res := w.do("GET", cranMoveURL, "admin", "")
	wantStatus(t, res, 200)
	if !strings.Contains(res.Body.String(), `"moved":1`) {
		t.Fatalf("paginated report: %s", res.Body)
	}
	w.item(c.Key, false, "src/contrib/Archive/a-notes/0000")
	w.item(c.Key, false, cranArchivePath)
}
