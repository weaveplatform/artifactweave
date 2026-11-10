package huggingfacehttp_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

var errMigrationStorage = errors.New("injected migration storage failure")

// Keep the real transactional store underneath every fault: a handler returning
// an error is insufficient unless all previously published state rolls back.
type migrationFaultStore struct {
	serverStore
	fault    string
	active   atomic.Bool
	hit      atomic.Bool
	planning atomic.Bool
}

func (s *migrationFaultStore) fails(operation string) bool {
	if s.active.Load() && s.fault == operation {
		s.hit.Store(true)
		return true
	}
	return false
}

func (s *migrationFaultStore) Items() store.ItemStore {
	return migrationFaultItems{s.serverStore.Items(), s}
}

func (s *migrationFaultStore) Packages() store.PackageStore {
	return migrationFaultPackages{s.serverStore.Packages(), s}
}

func (s *migrationFaultStore) Repos() store.RepoStore {
	return migrationFaultRepos{s.serverStore.Repos(), s}
}

func (s *migrationFaultStore) Permissions() store.PermissionStore {
	return migrationFaultPermissions{s.serverStore.Permissions(), s}
}

type migrationFaultRepos struct {
	store.RepoStore
	faults *migrationFaultStore
}

func (s migrationFaultRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if s.faults.planning.Load() && s.faults.fails("repository policy") {
		return repo.Config{}, errMigrationStorage
	}
	return s.RepoStore.Get(ctx, key)
}

type migrationFaultPermissions struct {
	store.PermissionStore
	faults *migrationFaultStore
}

func (s migrationFaultPermissions) List(
	ctx context.Context,
	page store.Page,
) ([]types.Permission, string, error) {
	if s.faults.planning.Load() && s.faults.fails("permission policy") {
		return nil, "", errMigrationStorage
	}
	return s.PermissionStore.List(ctx, page)
}

type migrationFaultItems struct {
	store.ItemStore
	faults *migrationFaultStore
}

func (s migrationFaultItems) Descendants(
	ctx context.Context,
	key string,
	cached bool,
	path string,
	page store.Page,
) ([]types.Item, string, error) {
	if s.faults.active.Load() && strings.HasPrefix(path, "models/acme/") {
		s.faults.planning.Store(true)
	}
	items, next, err := s.ItemStore.Descendants(ctx, key, cached, path, page)
	if strings.HasPrefix(path, "models/acme/") && s.faults.fails("source listing") ||
		strings.HasPrefix(path, "models--") && len(items) != 0 &&
			s.faults.fails("published listing") {
		return nil, "", errMigrationStorage
	}
	return items, next, err
}

func (s migrationFaultItems) GetByID(ctx context.Context, id int64) (types.Item, error) {
	if s.faults.fails("source lock") {
		return types.Item{}, errMigrationStorage
	}
	it, err := s.ItemStore.GetByID(ctx, id)
	if s.faults.fails("source changed before lock") {
		it.Size++
	}
	return it, err
}

func (s migrationFaultItems) Properties(ctx context.Context, id int64) (types.Properties, error) {
	it, err := s.ItemStore.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(it.FullPath(), "models/acme/") && s.faults.fails("source properties") ||
		strings.HasPrefix(it.FullPath(), "models--") && s.faults.fails("published properties") {
		return nil, errMigrationStorage
	}
	return s.ItemStore.Properties(ctx, id)
}

func (s migrationFaultItems) Put(ctx context.Context, it types.Item) (types.Item, error) {
	if strings.Contains(it.FullPath(), "/refs/") && s.faults.fails("ref publication") {
		return types.Item{}, errMigrationStorage
	}
	return s.ItemStore.Put(ctx, it)
}

type migrationFaultPackages struct {
	store.PackageStore
	faults *migrationFaultStore
}

func (s migrationFaultPackages) LinkFile(ctx context.Context, file types.VersionFile) error {
	if s.faults.fails("index link") {
		return errMigrationStorage
	}
	return s.PackageStore.LinkFile(ctx, file)
}

func (s migrationFaultPackages) VersionOf(
	ctx context.Context,
	id int64,
) (types.PackageVersion, error) {
	if s.faults.fails("index lookup") {
		return types.PackageVersion{}, errMigrationStorage
	}
	if s.faults.fails("missing index") {
		return types.PackageVersion{}, store.ErrNotFound
	}
	return s.PackageStore.VersionOf(ctx, id)
}

func (s migrationFaultPackages) PutVersion(
	ctx context.Context,
	version types.PackageVersion,
) (types.PackageVersion, error) {
	if len(version.Metadata) != 0 && s.faults.fails("version metadata") {
		return types.PackageVersion{}, errMigrationStorage
	}
	return s.PackageStore.PutVersion(ctx, version)
}

func TestAccLegacyMigrationStorageFailuresRollBackAndRetry(t *testing.T) {
	for _, fault := range []string{
		"source listing", "source lock", "source properties", "index link",
		"index lookup", "missing index", "version metadata", "ref publication",
		"published listing", "published properties",
		"repository policy", "permission policy",
		"source changed before lock",
	} {
		t.Run(fault, func(t *testing.T) {
			var faults *migrationFaultStore
			f := newFixture(t, repo.Local, func(f *fixture) *source.Service {
				faults = &migrationFaultStore{serverStore: f.db, fault: fault}
				f.db, f.items.Store = faults, faults
				return nil
			})
			f.legacyRevision("models", "1700000000", sha, false)
			before, _, err := f.db.Items().
				Descendants(t.Context(), "hf", false, "models/acme/demo", store.Page{})
			if err != nil {
				t.Fatal(err)
			}
			body := migrationBody("models", "1700000000", sha, "main", false)
			faults.active.Store(true)
			want := http.StatusInternalServerError
			if fault == "source changed before lock" {
				want = http.StatusConflict
			}
			f.migrate(body, want)
			faults.active.Store(false)
			if !faults.hit.Load() {
				t.Fatal("request did not reach the injected storage failure")
			}
			items, _, err := f.db.Items().
				Descendants(t.Context(), "hf", false, "models--acme--demo", store.Page{})
			if err != nil || len(items) != 0 {
				t.Fatalf("failed migration left destination items: %v %v", items, err)
			}
			if _, err := f.db.Packages().
				Find(t.Context(), "hf", "models/acme/demo"); !errors.Is(
				err,
				store.ErrNotFound,
			) {
				t.Fatalf("failed migration left package index: %v", err)
			}
			for _, original := range before {
				current, err := f.db.Items().GetByID(t.Context(), original.ID)
				if err != nil || current != original {
					t.Fatalf("failed migration changed source: %v %v", current, err)
				}
			}
			for range 2 {
				status := f.migrate(body, http.StatusOK)
				if status["state"] != "complete" || status["published_files"] != float64(2) {
					t.Fatalf("migration retry was not complete: %v", status)
				}
			}
		})
	}
}

func TestAccLegacyMigrationRejectsSourceProvenanceAndSkipsFolders(t *testing.T) {
	f := newFixture(t, repo.Local)
	f.legacyRevision("models", "1700000000", sha, false)
	it := f.legacyItem(
		"models/acme/demo/main/1700000000/config.json",
		[]byte(`{}`),
		types.Properties{
			"artifactweave.huggingface.migration.source": {"unrelated source"},
		},
	)
	f.migrate(migrationBody("models", "1700000000", sha, "main", false), http.StatusConflict)
	if err := f.db.Items().SetProperties(t.Context(), it.ID, nil, it.Created); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Items().
		Put(t.Context(), types.Item{RepoKey: "hf", Name: "models/acme/demo/main/1700000000/sub", Folder: true}); err != nil {
		t.Fatal(err)
	}
	status := f.migrate(migrationBody("models", "1700000000", sha, "main", false), http.StatusOK)
	if status["files"] != float64(2) {
		t.Fatalf("folders were treated as files: %v", status)
	}
}

type migrationFaultBlobs struct {
	blob.Store
	fault  string
	closed *atomic.Bool
}

func (b migrationFaultBlobs) Open(
	ctx context.Context,
	digest string,
) (io.ReadSeekCloser, blob.Info, error) {
	if b.fault == "open" {
		return nil, blob.Info{}, errMigrationStorage
	}
	r, info, err := b.Store.Open(ctx, digest)
	if err != nil {
		return nil, info, err
	}
	return &migrationFaultReader{
		ReadSeekCloser: r,
		remaining:      map[string]int{"inspection": 0, "verification": 4096}[b.fault],
		closed:         b.closed,
	}, info, nil
}

type migrationFaultReader struct {
	io.ReadSeekCloser
	remaining int
	closed    *atomic.Bool
}

func (r *migrationFaultReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	n, err := r.ReadSeekCloser.Read(p[:min(len(p), r.remaining)])
	r.remaining -= n
	return n, err
}

func (r *migrationFaultReader) Close() error {
	r.closed.Store(true)
	return r.ReadSeekCloser.Close()
}

func TestAccLegacyMigrationBlobFailuresLeaveSourceRetriable(t *testing.T) {
	for _, fault := range []string{"open", "inspection", "verification"} {
		t.Run(fault, func(t *testing.T) {
			f := newFixture(t, repo.Local)
			f.legacyRevision("models", "1700000000", sha, false)
			f.legacyItem(
				"models/acme/demo/main/1700000000/config.json",
				[]byte(strings.Repeat("x", 8192)),
				nil,
			)
			original := f.items.Blobs
			var closed atomic.Bool
			f.items.Blobs = migrationFaultBlobs{Store: original, fault: fault, closed: &closed}
			body := migrationBody("models", "1700000000", sha, "main", false)
			f.migrate(body, http.StatusInternalServerError)
			if fault != "open" && !closed.Load() {
				t.Fatal("failed migration leaked its blob reader")
			}
			items, _, err := f.db.Items().
				Descendants(t.Context(), "hf", false, "models--acme--demo", store.Page{})
			if err != nil || len(items) != 0 {
				t.Fatalf("failed verification published files: %v %v", items, err)
			}
			f.items.Blobs = original
			f.migrate(body, http.StatusOK)
		})
	}
}
