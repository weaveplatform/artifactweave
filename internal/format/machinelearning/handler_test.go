package machinelearning_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	ml "github.com/weaveplatform/artifactweave/internal/format/machinelearning"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func rejectStatus(t *testing.T, err error, want int) {
	t.Helper()
	var rejected *format.RejectionError
	if !errors.As(err, &rejected) || rejected.Status != want {
		t.Fatalf("rejection %v, want HTTP %d", err, want)
	}
}

func TestUnitStagingAdmissionRequiresLiveOwnedTransaction(t *testing.T) {
	f := newFormatFixture(t)
	h := ml.Handler{}
	id := ml.Identity{Name: "group/model", Version: "1"}
	tx := "1793180000000"
	marker := f.put(id.Staging(tx)+"/"+ml.TransactionName, []byte("{}"))
	good := format.Write{
		Op:   format.OpDeploy,
		Path: id.Staging(tx) + "/files/weights.bin",
		Size: 5,
		By:   "owner",
	}
	if !ml.Serves(repo.MachineLearning) || ml.Serves(repo.Generic) {
		t.Fatal("wrong format ownership")
	}
	if _, err := h.Admit(f.ctx, f.env, f.c, good); err != nil {
		t.Fatal(err)
	}
	if err := ml.CheckTransaction(f.ctx, f.env, f.c, id, tx, "owner"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*format.Write)
		code   int
	}{
		{"invalid path", func(w *format.Write) { w.Path = "arbitrary" }, 400},
		{"cached", func(w *format.Write) { w.Cached = true }, 400},
		{"oversized", func(w *format.Write) { w.Size = ml.MaxFileSize + 1 }, 400},
		{"committed overwrite", func(w *format.Write) { w.Path = id.Prefix() + "/files/weights.bin" }, 409},
		{"transaction overwrite", func(w *format.Write) { w.Path = marker.FullPath() }, 409},
		{"checksum write", func(w *format.Write) { w.Op = format.OpChecksum }, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := good
			tc.change(&w)
			_, err := h.Admit(f.ctx, f.env, f.c, w)
			rejectStatus(t, err, tc.code)
		})
	}
	published := good
	published.Path = id.Manifest()
	if _, err := h.Admit(ml.Publication(f.ctx), f.env, f.c, published); err != nil {
		t.Fatalf("native publication refused: %v", err)
	}
	blocked := f.c
	blocked.BlackedOut = new(true)
	if _, err := h.Admit(ml.Publication(f.ctx), f.env, blocked, published); err == nil {
		t.Fatal("publication bypassed repository policy")
	}
	for _, by := range []string{"", "outsider", "anonymous"} {
		denied := good
		denied.By = by
		_, err := h.Admit(f.ctx, f.env, f.c, denied)
		rejectStatus(t, err, 403)
	}

	bad := marker
	bad.Folder = true
	if _, err := f.db.Items().Put(f.ctx, bad); err != nil {
		t.Fatal(err)
	}
	rejectStatus(t, ml.CheckTransaction(f.ctx, f.env, f.c, id, tx, "owner"), 403)
	if _, err := f.db.Items().Put(f.ctx, marker); err != nil {
		t.Fatal(err)
	}
	f.env.Now = func() time.Time { return f.now.Add(24*time.Hour + time.Second) }
	rejectStatus(t, ml.CheckTransaction(f.ctx, f.env, f.c, id, tx, "owner"), 409)
	f.env.Now = nil
	if err := ml.CheckTransaction(
		f.ctx,
		f.env,
		f.c,
		id,
		"1793180000001",
		"owner",
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal(err)
	}
	// The fallback wall clock still applies expiry even without a supplied clock.
	marker.Created = time.Now().Add(-25 * time.Hour)
	if _, err := f.db.Items().Put(f.ctx, marker); err != nil {
		t.Fatal(err)
	}
	rejectStatus(t, ml.CheckTransaction(f.ctx, f.env, f.c, id, tx, "owner"), 409)
}

func TestUnitPublicationIndexFollowsCompleteStoredVersions(t *testing.T) {
	f := newFormatFixture(t)
	h := ml.Handler{}
	id := ml.Identity{Name: "group/model", Version: "1"}
	_, manifest, it := f.publish(id)
	f.publish(ml.Identity{Name: id.Name, Version: "2"})
	f.publish(ml.Identity{Name: "another", Version: "3"})
	f.put("models/unrecognized", []byte("legacy"))
	f.put(id.Staging("1793180000000")+"/"+ml.TransactionName, []byte("{}"))
	if _, err := f.db.Items().
		Put(f.ctx, types.Item{RepoKey: f.c.Key, Name: "models/folder", Folder: true}); err != nil {
		t.Fatal(err)
	}
	f.env.Store = limitedStore{Store: f.db}
	if err := h.Reindex(f.ctx, f.env, f.c); err != nil {
		t.Fatal(err)
	}
	pkg, err := f.db.Packages().Find(f.ctx, f.c.Key, id.Name)
	if err != nil {
		t.Fatal(err)
	}
	versions, err := f.db.Packages().Versions(f.ctx, pkg.ID)
	if err != nil || len(versions) != 2 {
		t.Fatal(versions, err)
	}
	if path, err := h.Resolve(
		f.ctx,
		f.env,
		f.c,
		format.Download{Path: it.FullPath()},
	); err != nil ||
		path != it.FullPath() {
		t.Fatal(path, err)
	}
	if err := h.GuardTransferSource(f.ctx, f.env, f.c, it); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{manifest.FullPath(), id.Staging("1793180000000") + "/files/weights.bin"} {
		rejectStatus(t, h.GuardTransferSource(f.ctx, f.env, f.c, types.Item{Name: p}), 403)
		if h.Classify(f.c, p) != format.ClassMetadata {
			t.Fatal("private record classified as artifact")
		}
	}
	if h.Classify(f.c, it.FullPath()) != format.ClassUnique {
		t.Fatal("artifact classified as metadata")
	}
	for _, p := range []string{"invalid", id.Manifest(), id.Staging("1793180000000") + "/files/weights.bin", id.Prefix() + "/files/undeclared.bin"} {
		_, err := h.Resolve(f.ctx, f.env, f.c, format.Download{Path: p})
		rejectStatus(t, err, 404)
	}
	rejectStatus(t, h.GuardTransferSource(f.ctx, f.env, f.c, types.Item{Name: "invalid"}), 400)
	if _, err := f.db.Items().Delete(f.ctx, f.c.Key, false, it.FullPath()); err != nil {
		t.Fatal(err)
	}
	if err := h.Deleted(f.ctx, f.env, f.c, []types.Item{it}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Packages().
		Version(f.ctx, pkg.ID, id.Version); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatalf("incomplete model still indexed: %v", err)
	}
	if _, err := h.Resolve(
		f.ctx,
		f.env,
		f.c,
		format.Download{Path: it.FullPath()},
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal(err)
	}
	if err := h.GuardTransferSource(f.ctx, f.env, f.c, it); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := f.db.Items().Put(f.ctx, it); err != nil {
		t.Fatal(err)
	}
	if err := h.Deployed(f.ctx, f.env, f.c, it); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Packages().Version(f.ctx, pkg.ID, id.Version); err != nil {
		t.Fatalf("restored model not indexed: %v", err)
	}
	for _, record := range []types.Item{{Folder: true}, manifest, {Name: id.Staging("1793180000000") + "/files/weights.bin"}} {
		if err := h.Deployed(f.ctx, f.env, f.c, record); err != nil {
			t.Fatal(err)
		}
	}
	for _, run := range []func() error{func() error { return h.Deployed(ml.Publication(f.ctx), f.env, f.c, types.Item{Name: "ignored"}) }, func() error { return h.Deleted(ml.Publication(f.ctx), f.env, f.c, nil) }} {
		if err := run(); err != nil {
			t.Fatal(err)
		}
	}
	rejectStatus(t, h.Deployed(f.ctx, f.env, f.c, types.Item{Name: "invalid"}), 400)
	bad := it
	bad.Actual.SHA256 = "bad"
	rejectStatus(t, h.Deployed(f.ctx, f.env, f.c, bad), 409)
	if _, err := f.db.Items().Delete(f.ctx, f.c.Key, false, id.Manifest()); err != nil {
		t.Fatal(err)
	}
	if err := h.Deployed(f.ctx, f.env, f.c, it); err != nil {
		t.Fatal(err)
	}
	if err := h.GuardTransferSource(f.ctx, f.env, f.c, it); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := h.Resolve(
		f.ctx,
		f.env,
		f.c,
		format.Download{Path: it.FullPath()},
	); !errors.Is(
		err,
		store.ErrNotFound,
	) {
		t.Fatal(err)
	}
}

type limitedStore struct {
	store.Store
	fault string
}

func (s limitedStore) Items() store.ItemStore { return limitedItems{s.Store.Items(), s.fault} }
func (s limitedStore) Packages() store.PackageStore {
	return limitedPackages{s.Store.Packages(), s.fault}
}

func (s limitedStore) Tx(ctx context.Context, f func(context.Context) error) error {
	if s.fault == "transaction" {
		return errFixture
	}
	return s.Store.Tx(ctx, f)
}

type limitedItems struct {
	store.ItemStore
	fault string
}

func (s limitedItems) Descendants(
	ctx context.Context,
	key string,
	cached bool,
	path string,
	page store.Page,
) ([]types.Item, string, error) {
	if s.fault == "list" {
		return nil, "", errFixture
	}
	page.Limit = 1
	return s.ItemStore.Descendants(ctx, key, cached, path, page)
}

type limitedPackages struct {
	store.PackageStore
	fault string
}

func (s limitedPackages) List(
	ctx context.Context,
	key string,
	page store.Page,
) ([]types.Package, string, error) {
	if s.fault == "index" {
		return nil, "", errFixture
	}
	return s.PackageStore.List(ctx, key, page)
}

func TestUnitReindexFailsClosedOnCorruptionStorageFailureAndRevocation(t *testing.T) {
	for _, fault := range []string{"transaction", "list", "index"} {
		t.Run(fault, func(t *testing.T) {
			f := newFormatFixture(t)
			f.publish(ml.Identity{Name: "model", Version: "1"})
			f.env.Store = limitedStore{Store: f.db, fault: fault}
			if err := (ml.Handler{}).Reindex(f.ctx, f.env, f.c); !errors.Is(err, errFixture) {
				t.Fatalf("reindex failure: %v", err)
			}
		})
	}
	f := newFormatFixture(t)
	h := ml.Handler{}
	id := ml.Identity{Name: "model", Version: "1"}
	_, manifest, it := f.publish(id)
	f.env.CheckReindex = func(context.Context) error { return errFixture }
	if err := h.Reindex(f.ctx, f.env, f.c); !errors.Is(err, errFixture) {
		t.Fatal(err)
	}
	f.env.CheckReindex = nil
	f.put(id.Manifest(), []byte("invalid"))
	if err := h.Reindex(f.ctx, f.env, f.c); err == nil {
		t.Fatal("corrupt manifest indexed")
	}
	if err := h.Deployed(f.ctx, f.env, f.c, it); err == nil {
		t.Fatal("restore ignored corrupt manifest")
	}
	if _, err := f.db.Items().Put(f.ctx, manifest); err != nil {
		t.Fatal(err)
	}
	bad := it
	bad.Size++
	if _, err := f.db.Items().Put(f.ctx, bad); err != nil {
		t.Fatal(err)
	}
	if err := h.Reindex(f.ctx, f.env, f.c); err == nil {
		t.Fatal("corrupt artifact indexed")
	}
}
