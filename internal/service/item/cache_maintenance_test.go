package item

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func cacheContent(
	t *testing.T,
	w *world,
	u CacheUpload,
	path string,
	kind types.RemoteKind,
	body string,
) CacheResult {
	t.Helper()
	u.Token.Key.Path = path
	u.Kind = kind
	u.Body = strings.NewReader(body)
	u.Length = int64(len(body))
	var err error
	u.Token, err = w.db.RemoteCache().Begin(context.Background(), u.Token.Key)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.svc.Cache(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCacheMetadataZapAndDelete(t *testing.T) {
	ctx := context.Background()
	w, u := remoteWorld(t)
	unique := cacheContent(t, w, u, "pkg/a.jar", types.RemoteUnique, "unique")
	mutable := cacheContent(t, w, u, "pkg/snapshot.jar", types.RemoteMutable, "mutable")
	metadata := cacheContent(t, w, u, "pkg/index", types.RemoteMetadata, "versions")
	outside := cacheContent(t, w, u, "else/index", types.RemoteMetadata, "outside")
	missing := u.Token.Key
	missing.Path = "pkg/missing"
	negative, err := w.db.RemoteCache().Begin(ctx, missing)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.db.RemoteCache().Negative(ctx, negative, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	pendingKey := missing
	pendingKey.Path = "pkg/pending"
	pending, err := w.db.RemoteCache().Begin(ctx, pendingKey)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := w.svc.Zap(
		ctx,
		Target{Repo: u.Repo, Cached: true, Path: "pkg"},
	); err != nil ||
		n != 3 {
		t.Fatal("zap", n, err)
	}
	for _, e := range []CacheResult{unique, mutable} {
		saved, err := w.db.RemoteCache().Read(ctx, e.Entry.Token.Key)
		if err != nil || saved.Token != e.Entry.Token || saved.Blob != e.Entry.Blob {
			t.Fatal("binary zapped", err)
		}
		it, err := w.db.Items().GetByID(ctx, e.Item.ID)
		if err != nil || !it.Updated.Equal(e.Item.Updated) {
			t.Fatal("binary timestamp changed", err)
		}
	}
	saved, err := w.db.RemoteCache().Read(ctx, metadata.Entry.Token.Key)
	if err != nil || !saved.ValidatedAt.IsZero() ||
		saved.Blob.Checksums != metadata.Entry.Blob.Checksums {
		t.Fatal("metadata lost bytes", err)
	}
	unchanged, err := w.db.RemoteCache().Read(ctx, outside.Entry.Token.Key)
	if err != nil || unchanged.Token != outside.Entry.Token {
		t.Fatal("zap escaped path", err)
	}
	if err = w.db.RemoteCache().Check(ctx, pending); !errors.Is(err, store.ErrConflict) {
		t.Fatal("late fill survived zap", err)
	}
	if _, err = w.db.RemoteCache().Read(ctx, missing); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("negative survived zap", err)
	}
	// Deleting only raw metadata succeeds and releases its independent reference.
	if _, err = w.svc.Delete(
		ctx,
		Target{Repo: u.Repo, Cached: true, Path: "pkg/index"},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	if w.refs(metadata.Info.Digests.SHA256) != 0 {
		t.Fatal("raw metadata reference leaked")
	}
	if _, err = w.svc.Delete(
		ctx,
		Target{Repo: u.Repo, Cached: true, Path: "pkg/a.jar"},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	if w.refs(unique.Info.Digests.SHA256) != 0 {
		t.Fatal("cache delete reference leaked")
	}
	trash, err := w.db.Trash().Find(ctx, u.Repo.Key, "pkg/a.jar")
	if err != nil || len(trash) != 0 {
		t.Fatal("remote cache entered trash", err)
	}
	// Negative-only and pending-only paths can be deleted without an Item row.
	for _, key := range []types.RemoteCacheKey{missing, pendingKey} {
		token, err := w.db.RemoteCache().Begin(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.svc.Delete(
			ctx,
			Target{Repo: u.Repo, Cached: true, Path: key.Path},
			"admin",
		); err != nil {
			t.Fatal(err)
		}
		if err = w.db.RemoteCache().Check(ctx, token); !errors.Is(err, store.ErrConflict) {
			t.Fatal("delete left pending work", err)
		}
	}
}

func TestCacheDeleteAndRestoreRollback(t *testing.T) {
	ctx := context.Background()
	w, u := remoteWorld(t)
	seed := cacheContent(t, w, u, "pkg/a.bin", types.RemoteUnique, "seed")
	w.svc.Store = faulty{w.db, "delete"}
	if _, err := w.svc.Delete(
		ctx,
		Target{Repo: u.Repo, Cached: true, Path: "pkg/a.bin"},
		"admin",
	); !errors.Is(
		err,
		errBoom,
	) {
		t.Fatal(err)
	}
	if err := w.db.RemoteCache().Check(ctx, seed.Entry.Token); err != nil {
		t.Fatal("failed deletion changed epoch", err)
	}
	w.svc.Store = w.db
	// A trash record owns a separate reference which Restore takes over.
	if _, err := w.db.Blobs().Ref(ctx, seed.Entry.Blob, 1); err != nil {
		t.Fatal(err)
	}
	pending, err := w.db.RemoteCache().Begin(ctx, seed.Entry.Token.Key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.svc.Restore(
		ctx,
		Target{Repo: u.Repo, Cached: true, Path: "pkg/a.bin"},
		types.TrashRecord{Item: seed.Item},
		"admin",
	); err != nil {
		t.Fatal(err)
	}
	if err = w.db.RemoteCache().Check(ctx, pending); !errors.Is(err, store.ErrConflict) {
		t.Fatal("restore retained stale fence", err)
	}
	if _, err = w.db.RemoteCache().Read(ctx, pending.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("restore invented validation", err)
	}
	if w.refs(seed.Info.Digests.SHA256) != 1 {
		t.Fatal("restore lost the retained blob reference")
	}
}

func maintenanceQueue(t *testing.T, w *world, now *time.Time) *work.Queue {
	t.Helper()
	w.svc.Now = func() time.Time { return *now }
	q := newQueue(t, w.db, work.Options{Now: w.svc.Now})
	if err := w.svc.RegisterCacheMaintenance(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	return q
}

func runCleanup(t *testing.T, q *work.Queue) int {
	t.Helper()
	ctx := context.Background()
	if _, err := q.RunDue(ctx, KindCacheCleanupSchedule); err != nil {
		t.Fatal(err)
	}
	total := 0
	for range 20 {
		n, err := q.RunDue(ctx, KindCacheCleanup)
		if err != nil {
			t.Fatal(err)
		}
		total += n
		if n == 0 {
			return total
		}
	}
	t.Fatal("cleanup failed to drain bounded pages")
	return 0
}

func TestCacheCleanupUsesActivityAndDisabling(t *testing.T) {
	ctx := context.Background()
	w, u := remoteWorld(t)
	hours := 1
	u.Repo.UnusedArtifactsCleanupPeriodHours = &hours
	if err := w.db.Repos().Update(ctx, u.Repo); err != nil {
		t.Fatal(err)
	}
	old := cacheContent(t, w, u, "old.bin", types.RemoteUnique, "old")
	downloaded := cacheContent(t, w, u, "download.bin", types.RemoteUnique, "download")
	touched := cacheContent(t, w, u, "touch.bin", types.RemoteUnique, "touch")
	raw := cacheContent(t, w, u, "index", types.RemoteMetadata, "raw")
	freshRaw := cacheContent(t, w, u, "fresh-index", types.RemoteMetadata, "fresh")
	now := at.Add(2 * time.Hour)
	if err := w.db.Items().
		RecordDownload(ctx, downloaded.Item.ID, store.Download{At: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []types.RemoteCacheEntry{touched.Entry, freshRaw.Entry} {
		if err := w.db.RemoteCache().Touch(ctx, e.Token, now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	key := u.Token.Key
	key.Path = "negative"
	token, err := w.db.RemoteCache().Begin(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.db.RemoteCache().Negative(ctx, token, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	q := maintenanceQueue(t, w, &now)
	if n := runCleanup(t, q); n != 2 {
		t.Fatal("expected entry and item batches", n)
	}
	for _, e := range []CacheResult{old, raw} {
		if _, err := w.db.RemoteCache().
			Read(ctx, e.Entry.Token.Key); !errors.Is(
			err,
			store.ErrNotFound,
		) {
			t.Fatal("unused content retained", err)
		}
	}
	if w.refs(old.Info.Digests.SHA256) != 0 || w.refs(raw.Info.Digests.SHA256) != 0 {
		t.Fatal("cleanup leaked references")
	}
	for _, e := range []CacheResult{downloaded, touched, freshRaw} {
		if _, err := w.db.RemoteCache().Read(ctx, e.Entry.Token.Key); err != nil {
			t.Fatal("recent content removed", err)
		}
	}
	if _, err := w.db.RemoteCache().Read(ctx, key); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expired negative retained", err)
	}
	// A zero setting disables queued work, including a job left from before edit.
	if _, err = q.RunDue(ctx, KindCacheCleanupSchedule); err != nil {
		t.Fatal(err)
	}
	zero := 0
	u.Repo.UnusedArtifactsCleanupPeriodHours = &zero
	if err = w.db.Repos().Update(ctx, u.Repo); err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	runCleanup(t, q)
	if _, err = w.db.Items().GetByID(ctx, downloaded.Item.ID); err != nil {
		t.Fatal("zero cleanup removed content", err)
	}
	if _, err = w.db.WorkQueue().
		Get(ctx, KindCacheCleanup, u.Repo.Key); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal("zero cleanup requeued", err)
	}
}

func TestCacheCleanupBatchesAndLifetime(t *testing.T) {
	ctx := context.Background()
	w, u := remoteWorld(t)
	hours := 1
	u.Repo.UnusedArtifactsCleanupPeriodHours = &hours
	if err := w.db.Repos().Update(ctx, u.Repo); err != nil {
		t.Fatal(err)
	}
	for n := range CacheCleanupBatch + 2 {
		cacheContent(t, w, u, fmt.Sprintf("raw/%03d", n), types.RemoteMetadata, "same")
	}
	now := at.Add(2 * time.Hour)
	q := maintenanceQueue(t, w, &now)
	if _, err := q.RunDue(ctx, KindCacheCleanupSchedule); err != nil {
		t.Fatal(err)
	}
	if n, err := q.RunDue(ctx, KindCacheCleanup); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	rows, _, err := w.db.RemoteCache().
		List(ctx, u.Repo.Key, "raw", store.Page{Limit: store.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	present := 0
	for _, e := range rows {
		if e.Blob.Checksums.SHA256 != "" {
			present++
		}
	}
	if present < 2 || present > 3 {
		t.Fatal("batch exceeded bound", present)
	}
	// Re-registering a restarted worker preserves its queued continuation.
	q = maintenanceQueue(t, w, &now)
	if n := runCleanup(t, q); n != 2 {
		t.Fatal("restart lost continuation", n)
	}
	// Work for an old incarnation must never act on a recreated repository.
	if _, err := q.RunDue(ctx, KindCacheCleanupSchedule); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Repos().Delete(ctx, u.Repo.Key); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Repos().Create(ctx, u.Repo); err != nil {
		t.Fatal(err)
	}
	fresh := cacheContent(t, w, u, "new.bin", types.RemoteUnique, "new")
	if _, err := q.RunDue(ctx, KindCacheCleanup); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Items().GetByID(ctx, fresh.Item.ID); err != nil {
		t.Fatal("old work deleted recreated content", err)
	}
}

func TestCacheCleanupSidecarActivityAndReplacement(t *testing.T) {
	ctx := context.Background()
	w, u := remoteWorld(t)
	hours := 1
	u.Repo.UnusedArtifactsCleanupPeriodHours = &hours
	if err := w.db.Repos().Update(ctx, u.Repo); err != nil {
		t.Fatal(err)
	}
	w.svc.Formats = func(repo.PackageType) format.Handler { return keeper{} }
	owner := cacheContent(t, w, u, "owned.bin", types.RemoteUnique, "owner")
	signature := cacheContent(t, w, u, "owned.bin.sig", types.RemoteUnique, "signature")
	stale := cacheContent(t, w, u, "replaced.bin", types.RemoteMutable, "stale")
	now := at.Add(2 * time.Hour)
	q := maintenanceQueue(t, w, &now)
	if err := w.db.Items().
		RecordDownload(ctx, signature.Item.ID, store.Download{At: now}); err != nil {
		t.Fatal(err)
	}
	replacement := cacheContent(t, w, u, "replaced.bin", types.RemoteMutable, "new")
	source, err := w.db.RemoteCache().Source(ctx, u.Repo.Key)
	if err != nil {
		t.Fatal(err)
	}
	cursor := cacheCleanupCursor{Repository: u.Repo.Key, Incarnation: source.Repository}
	if err = w.svc.cleanupCachedItem(ctx, cursor, stale.Item); err != nil {
		t.Fatal(err)
	}
	runCleanup(t, q)
	for _, cached := range []CacheResult{owner, signature, replacement} {
		it, err := w.db.Items().GetByID(ctx, cached.Item.ID)
		if err != nil || it.Actual != cached.Item.Actual {
			t.Fatal("recent sidecar or replacement removed", it, err)
		}
	}
	now = now.Add(2 * time.Hour)
	runCleanup(t, q)
	for _, cached := range []CacheResult{owner, signature, replacement} {
		if _, err = w.db.Items().GetByID(ctx, cached.Item.ID); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("expired family retained", err)
		}
		if w.refs(cached.Info.Digests.SHA256) != 0 {
			t.Fatal("family reference leaked")
		}
	}
}

// The adapter owns its derived view; reindexing must not overwrite raw remote
// metadata even when the two representations have the same logical path.
type maintenanceFormat struct{ *recorder }

func (maintenanceFormat) Classify(_ repo.Config, p string) format.Class {
	if p == "index" {
		return format.ClassDerived
	}
	return format.ClassUnique
}

func (maintenanceFormat) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	_, err := env.Derive(
		ctx,
		format.Derived{
			Repo:   c,
			Cached: true,
			Path:   "index",
			Body:   strings.NewReader("cached subset"),
		},
	)
	return err
}

func TestCacheCleanupFormatIndexAndRawIsolation(t *testing.T) {
	ctx := context.Background()
	w, u := remoteWorld(t)
	hours := 1
	u.Repo.UnusedArtifactsCleanupPeriodHours = &hours
	if err := w.db.Repos().Update(ctx, u.Repo); err != nil {
		t.Fatal(err)
	}
	r := &recorder{calls: map[string]bool{}}
	w.svc.Formats = func(repo.PackageType) format.Handler { return maintenanceFormat{r} }
	binary := cacheContent(t, w, u, "pkg/1/file.bin", types.RemoteUnique, "binary")
	raw := cacheContent(t, w, u, "index", types.RemoteMetadata, "complete upstream versions")
	version := pkgindex.Version{
		RepoKey:     u.Repo.Key,
		PackageType: repo.Generic,
		Package:     "pkg",
		Version:     "1",
	}
	if err := pkgindex.Link(ctx, w.db, version, binary.Item.ID, at); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Reindex(ctx, u.Repo); err != nil {
		t.Fatal(err)
	}
	stored, err := w.db.RemoteCache().Read(ctx, raw.Entry.Token.Key)
	if err != nil || stored.Blob != raw.Entry.Blob {
		t.Fatal("reindex replaced authoritative metadata", err)
	}
	derived, err := w.db.Items().Get(ctx, u.Repo.Key, true, "index")
	if err != nil || derived.Actual == stored.Blob.Checksums {
		t.Fatal("derived view not separate", err)
	}
	now := at.Add(2 * time.Hour)
	q := maintenanceQueue(t, w, &now)
	runCleanup(t, q)
	if _, err = w.db.Packages().Find(ctx, u.Repo.Key, "pkg"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("package index survived last cached file", err)
	}
	if !r.calls["deleting pkg/1/file.bin"] || !r.calls["deleted 1"] {
		t.Fatal("cleanup bypassed transactional format hooks", r.calls)
	}
	if _, err = w.db.Items().GetByID(ctx, derived.ID); err != nil {
		t.Fatal("cleanup removed derived view", err)
	}
	if w.refs(raw.Info.Digests.SHA256) != 0 || w.refs(binary.Info.Digests.SHA256) != 0 ||
		w.refs(derived.Actual.SHA256) != 1 {
		t.Fatal("cleanup references inconsistent")
	}
}

func TestCacheCleanupFaultRollbackAndEntryRaces(t *testing.T) {
	ctx := context.Background()
	w, u := remoteWorld(t)
	hours := 1
	u.Repo.UnusedArtifactsCleanupPeriodHours = &hours
	if err := w.db.Repos().Update(ctx, u.Repo); err != nil {
		t.Fatal(err)
	}
	original := cacheContent(t, w, u, "item.bin", types.RemoteUnique, "old")
	raw := cacheContent(t, w, u, "index", types.RemoteMetadata, "index")
	source, err := w.db.RemoteCache().Source(ctx, u.Repo.Key)
	if err != nil {
		t.Fatal(err)
	}
	cursor := cacheCleanupCursor{Repository: u.Repo.Key, Incarnation: source.Repository}
	now := at.Add(2 * time.Hour)
	w.svc.Now = func() time.Time { return now }
	for _, fail := range []string{"stats", "delete", "unref", "versionOf"} {
		w.svc.Store = faulty{w.db, fail}
		if err = w.svc.cleanupCachedItem(ctx, cursor, original.Item); !errors.Is(err, errBoom) {
			t.Fatal(fail, err)
		}
		if err = w.db.RemoteCache().Check(ctx, original.Entry.Token); err != nil {
			t.Fatal("failed cleanup invalidated entry", fail, err)
		}
		if w.refs(original.Info.Digests.SHA256) != 1 {
			t.Fatal("failed cleanup released blob", fail)
		}
	}
	w.svc.Store = w.db
	// A refreshed metadata entry invalidates an old scan candidate.
	replacement := cacheContent(t, w, u, "index", types.RemoteMetadata, "new index")
	if err = w.svc.cleanupCacheEntry(ctx, cursor, raw.Entry); err != nil {
		t.Fatal(err)
	}
	if got, err := w.db.RemoteCache().
		Read(ctx, replacement.Entry.Token.Key); err != nil ||
		got.Blob != replacement.Entry.Blob {
		t.Fatal("old scan removed fresh metadata", err)
	}
	// A stale variant can be evicted without deleting the item's newer bytes.
	other := u
	other.Token.Key.Variant = "alternate"
	fresh := cacheContent(t, w, other, "item.bin", types.RemoteUnique, "alternate")
	if err = w.svc.cleanupCacheEntry(ctx, cursor, original.Entry); err != nil {
		t.Fatal(err)
	}
	if got, err := w.db.Items().
		GetByID(ctx, fresh.Item.ID); err != nil ||
		got.Actual != fresh.Item.Actual {
		t.Fatal("stale variant deleted new item", err)
	}
	negativeKey := u.Token.Key
	negativeKey.Path = "fresh-negative"
	negative, err := w.db.RemoteCache().Begin(ctx, negativeKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.db.RemoteCache().Negative(ctx, negative, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	entry, err := w.db.RemoteCache().Read(ctx, negativeKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.svc.cleanupCacheEntry(ctx, cursor, entry); err != nil {
		t.Fatal(err)
	}
	if _, err = w.db.RemoteCache().Read(ctx, negativeKey); err != nil {
		t.Fatal("cleanup removed unexpired negative", err)
	}
	if err = w.svc.cacheCleanupWork(ctx, types.WorkItem{Payload: []byte("bad-json")}); err == nil {
		t.Fatal("corrupt job accepted")
	}
	if cleanupUnused(u.Repo, now, time.Time{}) || cleanupUnused(u.Repo, now, now.Add(time.Hour)) {
		t.Fatal("unknown/future activity expired")
	}
}
