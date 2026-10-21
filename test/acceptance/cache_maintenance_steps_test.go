package acceptance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/service/work"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlcommon"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqlite"
	"github.com/weaveplatform/artifactweave/pkg/blob/fs"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (w *world) registerCacheMaintenance(sc *godog.ScenarioContext) {
	sc.Step(
		`^the remote "([^"]*)" has aged "([^"]*)" content at "([^"]*)" containing "([^"]*)"$`,
		w.agedCacheContent,
	)
	sc.Step(`^the remote "([^"]*)" has a negative entry at "([^"]*)"$`, w.negativeCacheEntry)
	sc.Step(
		`^the remote "([^"]*)" retains expired metadata at "([^"]*)" containing "([^"]*)"$`,
		w.expiredCacheMetadata,
	)
	sc.Step(`^I run remote cache cleanup$`, w.runRemoteCacheCleanup)
}

func (w *world) maintenanceStore() (*sqlcommon.Store, *item.Service, error) {
	st, err := sqlite.Open(
		context.Background(),
		filepath.Join(w.dataDir, "artifactweave.db"),
		sqlite.Options{Options: sqlcommon.Options{SkipMigrate: true}},
	)
	if err != nil {
		return nil, nil, err
	}
	blobs, err := fs.New(filepath.Join(w.dataDir, "blobs"))
	if err != nil {
		_ = st.Close()
		return nil, nil, err
	}
	return st, &item.Service{Store: st, Blobs: blobs}, nil
}

func (w *world) agedCacheContent(remote, kind, path, body string) error {
	st, svc, err := w.maintenanceStore()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	c, err := st.Repos().Get(ctx, remote)
	if err != nil {
		return err
	}
	token, err := st.RemoteCache().Begin(ctx, types.RemoteCacheKey{RepoKey: remote, Path: path})
	if err != nil {
		return err
	}
	old := time.Now().Add(-4 * time.Hour)
	svc.Now = func() time.Time { return old }
	_, err = svc.Cache(
		ctx,
		item.CacheUpload{
			Token:  token,
			Repo:   c,
			Kind:   types.RemoteKind(kind),
			Body:   strings.NewReader(body),
			Length: int64(len(body)),
		},
	)
	return err
}

func (w *world) negativeCacheEntry(remote, path string) error {
	st, _, err := w.maintenanceStore()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	token, err := st.RemoteCache().Begin(ctx, types.RemoteCacheKey{RepoKey: remote, Path: path})
	if err != nil {
		return err
	}
	return st.RemoteCache().Negative(ctx, token, time.Now().Add(time.Hour))
}

func (w *world) expiredCacheMetadata(remote, path, body string) error {
	st, svc, err := w.maintenanceStore()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	e, err := st.RemoteCache().Read(ctx, types.RemoteCacheKey{RepoKey: remote, Path: path})
	if err != nil {
		return err
	}
	if e.Kind != types.RemoteMetadata || !e.ValidatedAt.IsZero() {
		return fmt.Errorf("metadata not expired: %+v", e)
	}
	r, _, err := svc.Blobs.Open(ctx, e.Blob.Checksums.SHA256)
	if err != nil {
		return err
	}
	defer r.Close()
	bytes, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if string(bytes) != body {
		return fmt.Errorf("metadata bytes changed: %q", bytes)
	}
	return nil
}

func (w *world) runRemoteCacheCleanup() error {
	st, svc, err := w.maintenanceStore()
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	q, err := work.New(st, work.Options{})
	if err != nil {
		return err
	}
	if err = svc.RegisterCacheMaintenance(ctx, q); err != nil {
		return err
	}
	if _, err = q.RunDue(ctx, item.KindCacheCleanupSchedule); err != nil {
		return err
	}
	for range 100 {
		if _, err = q.RunDue(ctx, item.KindCacheCleanup); err != nil {
			return err
		}
		stats, err := st.WorkQueue().Stats(ctx)
		if err != nil {
			return err
		}
		active := false
		for _, s := range stats {
			if s.Kind == item.KindCacheCleanup && (s.Pending > 0 || s.Leased > 0) {
				active = true
			}
		}
		if !active {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return errors.New("cache cleanup did not finish")
}
