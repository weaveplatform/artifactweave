package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	accmodel "github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	platformblob "github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/tx"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// RunProjectStorage exercises the application composition's actual metadata
// writes and counters together, rather than the standalone arithmetic seam.
func RunProjectStorage(t *testing.T, newStore Factory) {
	t.Helper()
	for name, run := range map[string]func(*testing.T, Store){
		"parallel growth":                       projectStorageParallel,
		"net zero and ownership":                projectStorageOwnership,
		"raw cache and virtual copies":          projectStorageRepresentations,
		"orphan repository reuse":               projectStorageReuse,
		"durable warning and publication fence": projectStorageWarning,
		"quota policy warning transitions":      projectStoragePolicy,
	} {
		t.Run(name, func(t *testing.T) { run(t, newStore(t)) })
	}
}

func storageProject(t *testing.T, s Store, key string, limit int64, email bool) {
	t.Helper()
	storekit.Must(t, s.Projects().Create(t.Context(), accmodel.Project{Key: key, StorageQuotaBytes: limit, QuotaEmail: email}))
}

func storageRepo(t *testing.T, s Store, key, owner string, class repo.Class) repo.Config {
	t.Helper()
	c := repo.Config{Key: key, Class: class, ProjectKey: owner}
	storekit.Must(t, s.Repos().Create(t.Context(), c))
	return c
}

func storageItem(t *testing.T, s Store, repository, name string, size int64) model.Item {
	t.Helper()
	return storekit.Get[model.Item](t)(s.Items().Put(t.Context(), model.Item{RepoKey: repository, Name: name, Size: size}))
}

func storageBytes(t *testing.T, s Store, key string, want int64) {
	t.Helper()
	state, exists, err := s.Projects().Storage(t.Context(), key)
	if err != nil || !exists || state.Bytes != want {
		t.Fatalf("project %q counter = %+v, initialized %v, want %d: %v", key, state, exists, want, err)
	}
	usage := s.Repos().(api.StorageUsage)
	got := storekit.Get[int64](t)(usage.ProjectStorageBytes(t.Context(), key))
	if got != want {
		t.Fatalf("project %q actual bytes = %d, want %d", key, got, want)
	}
}

func projectStorageParallel(t *testing.T, s Store) {
	ctx := t.Context()
	storageProject(t, s, "alpha", 100, false)
	storageRepo(t, s, "local", "alpha", repo.Local)
	storageItem(t, s, "local", "base", 70)
	start, results := make(chan struct{}), make(chan error, 2)
	var writers sync.WaitGroup
	for i := range 2 {
		writers.Go(func() {
			<-start
			_, err := s.Items().Put(ctx, model.Item{RepoKey: "local", Name: fmt.Sprintf("writer-%d", i), Size: 20})
			results <- err
		})
	}
	close(start)
	writers.Wait()
	close(results)
	accepted, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, project.ErrStorageQuota):
			refused++
		default:
			t.Fatal(err)
		}
	}
	if accepted != 1 || refused != 1 {
		t.Fatal("competing artifact writes did not serialize admission", accepted, refused)
	}
	storageBytes(t, s, "alpha", 90)
}

func projectStorageOwnership(t *testing.T, s Store) {
	ctx := t.Context()
	storageProject(t, s, "alpha", 100, false)
	storageProject(t, s, "beta", 50, false)
	storageRepo(t, s, "empty-beta", "beta", repo.Local)
	c := storageRepo(t, s, "local", "alpha", repo.Local)
	storageItem(t, s, c.Key, "before", 70)
	p := storekit.Get[accmodel.Project](t)(s.Projects().Get(ctx, "alpha"))
	p.StorageQuotaBytes = 50
	storekit.Must(t, s.Projects().Update(ctx, p))
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
		// Copy first, then remove: transient growth must not refuse a net-zero
		// operation when the administrator lowered the quota below usage.
		if _, err := s.Items().Put(ctx, model.Item{RepoKey: c.Key, Name: "after", Size: 70}); err != nil {
			return fmt.Errorf("copy net-zero fixture: %w", err)
		}
		_, err := s.Items().Delete(ctx, c.Key, false, "before")
		return err //nolint:wrapcheck // the fixture matches the store's delete error
	}))
	storageBytes(t, s, "alpha", 70)
	c.ProjectKey = "beta"
	storekit.WantErr(t, s.Repos().Update(ctx, c), project.ErrStorageQuota)
	current := storekit.Get[repo.Config](t)(s.Repos().Get(ctx, c.Key))
	if current.ProjectKey != "alpha" {
		t.Fatal("refused transfer changed canonical ownership", current.ProjectKey)
	}
	storageBytes(t, s, "alpha", 70)
	storageBytes(t, s, "beta", 0)
	p = storekit.Get[accmodel.Project](t)(s.Projects().Get(ctx, "beta"))
	p.StorageQuotaBytes = 70
	storekit.Must(t, s.Projects().Update(ctx, p))
	storekit.Must(t, s.Repos().Update(ctx, c))
	storageBytes(t, s, "alpha", 0)
	storageBytes(t, s, "beta", 70)
}

func projectStorageRepresentations(t *testing.T, s Store) {
	ctx := t.Context()
	storageProject(t, s, "alpha", 100, false)
	storageRepo(t, s, "remote", "alpha", repo.Remote)
	storageRepo(t, s, "virtual", "alpha", repo.Virtual)
	cache := s.RemoteCache()
	put := func(path string, size, id int64) model.RemoteCacheEntry {
		digest := strings.Repeat("a", 64)
		if id != 0 {
			digest = strings.Repeat("b", 64)
		}
		token := storekit.Get[model.RemoteCacheToken](t)(cache.Begin(ctx, model.RemoteCacheKey{RepoKey: "remote", Path: path}))
		return storekit.Get[model.RemoteCacheEntry](t)(cache.Put(ctx, token, model.RemoteCacheEntry{
			Token: token, Kind: model.RemoteMetadata, ItemID: id,
			Blob: platformblob.Blob{Size: size, Checksums: kernel.Checksums{SHA256: digest}},
		}))
	}
	raw := put("raw.json", 7, 0)
	storekit.Must(t, cache.Negative(ctx, raw.Token, storekit.At(10)))
	storageBytes(t, s, "alpha", 7)
	item := storekit.Get[model.Item](
		t,
	)(
		s.Items().Put(ctx, model.Item{RepoKey: "remote", Name: "materialized", Cached: true, Size: 5, Actual: kernel.Checksums{SHA256: strings.Repeat("b", 64)}}),
	)
	put("materialized", 5, item.ID)
	storageBytes(t, s, "alpha", 12)
	c := virtual.Copy{
		Virtual: "virtual", Path: "transformed.pom", Member: "remote", SourceSHA256: strings.Repeat("b", 64),
		Item: model.Item{RepoKey: "descriptive-not-owner", Size: 11, Actual: kernel.Checksums{SHA256: strings.Repeat("c", 64)}},
	}
	for range 2 {
		storekit.Must(t, s.VirtualCopies().Put(ctx, c))
	}
	storageBytes(t, s, "alpha", 23)
	c.Item.Size, c.Item.Actual.SHA256 = 13, strings.Repeat("d", 64)
	storekit.Must(t, s.VirtualCopies().Put(ctx, c))
	storageBytes(t, s, "alpha", 25)
	p := storekit.Get[accmodel.Project](t)(s.Projects().Get(ctx, "alpha"))
	p.StorageQuotaBytes = 25
	storekit.Must(t, s.Projects().Update(ctx, p))
	tooLarge := c
	tooLarge.Item.Size, tooLarge.Item.Actual.SHA256 = 14, strings.Repeat("e", 64)
	storekit.WantErr(t, s.VirtualCopies().Put(ctx, tooLarge), project.ErrStorageQuota)
	retained := storekit.Get[virtual.Copy](t)(s.VirtualCopies().Get(ctx, c.Virtual, c.Path))
	if retained != c {
		t.Fatal("refused copy replacement changed metadata", retained)
	}
	_, err := s.Blobs().Get(ctx, tooLarge.Item.Actual.SHA256)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storageBytes(t, s, "alpha", 25)
	storekit.Must(t, s.Repos().Delete(ctx, "virtual"))
	storageBytes(t, s, "alpha", 12)
	refs := storekit.Get[platformblob.Blob](t)(s.Blobs().Get(ctx, c.Item.Actual.SHA256))
	if refs.Refs != 0 {
		t.Fatal("deletion retained transformed-copy blob references", refs.Refs)
	}
	current := storekit.Get[model.RemoteCacheEntry](t)(cache.Read(ctx, raw.Token.Key))
	storekit.Must(t, cache.Evict(ctx, current.Token))
	storageBytes(t, s, "alpha", 5)
	storekit.Must(t, s.Repos().Delete(ctx, "remote"))
	storageBytes(t, s, "alpha", 0)
}

func projectStorageReuse(t *testing.T, s Store) {
	ctx := t.Context()
	storageProject(t, s, "alpha", 9, false)
	c := storageRepo(t, s, "local", "alpha", repo.Local)
	storageItem(t, s, c.Key, "retained", 9)
	storekit.Must(t, s.Repos().Delete(ctx, c.Key))
	storageBytes(t, s, "alpha", 0)
	p := storekit.Get[accmodel.Project](t)(s.Projects().Get(ctx, "alpha"))
	p.StorageQuotaBytes = 8
	storekit.Must(t, s.Projects().Update(ctx, p))
	storekit.WantErr(t, s.Repos().Create(ctx, c), project.ErrStorageQuota)
	_, err := s.Repos().Get(ctx, c.Key)
	storekit.WantErr(t, err, kernel.ErrNotFound)
	p.StorageQuotaBytes = 9
	storekit.Must(t, s.Projects().Update(ctx, p))
	storekit.Must(t, s.Repos().Create(ctx, c))
	storageBytes(t, s, "alpha", 9)
}

func projectStorageWarning(t *testing.T, s Store) {
	ctx := t.Context()
	storageProject(t, s, "alpha", 100, true)
	storageRepo(t, s, "local", "alpha", repo.Local)
	storageItem(t, s, "local", "base", 74)
	_, err := s.Items().Put(ctx, model.Item{RepoKey: "local", Name: "refused", Size: 27})
	storekit.WantErr(t, err, project.ErrStorageQuota)
	stats := storekit.Get[[]work.WorkStats](t)(s.WorkQueue().Stats(ctx))
	if len(stats) != 0 {
		t.Fatal("refused publication left durable warning work", stats)
	}
	storekit.Must(t, s.Tx(ctx, func(ctx context.Context) error {
		if _, err := s.Items().Put(ctx, model.Item{RepoKey: "local", Name: "threshold", Size: 1}); err != nil {
			return fmt.Errorf("publish warning threshold fixture: %w", err)
		}
		return tx.CheckPublication(ctx)
	}))
	storageBytes(t, s, "alpha", 75)
	storageItem(t, s, "local", "above", 1)
	now := time.Now().Add(time.Second)
	jobs := storekit.Get[[]work.WorkItem](t)(s.WorkQueue().Lease(ctx, work.WorkLease{
		Kinds: []string{project.StorageWarningKind}, Holder: "fixture", Limit: 10, Now: now, Until: now.Add(time.Minute),
	}))
	if len(jobs) != 1 {
		t.Fatal("warning occurrence was missing or duplicated", len(jobs))
	}
	var warning accmodel.ProjectStorageWarning
	storekit.Must(t, json.Unmarshal(jobs[0].Payload, &warning))
	state, exists, err := s.Projects().Storage(ctx, "alpha")
	authority := storekit.Get[accmodel.ProjectAuthority](t)(s.Projects().Authority(ctx, "alpha"))
	if err != nil || !exists || warning.Bytes != 75 || warning.Limit != 100 || warning.WarningKey != state.WarningKey || warning.Incarnation != authority.Incarnation {
		t.Fatal("warning handoff lost immutable occurrence data", warning, state, err)
	}
	// A late write after the publication boundary must roll the whole root
	// back even if a caller tries to ignore the nested write's error.
	err = s.Tx(ctx, func(ctx context.Context) error {
		if err := tx.CheckPublication(ctx); err != nil {
			return fmt.Errorf("settle late-write fixture: %w", err)
		}
		_, _ = s.Items().Put(ctx, model.Item{RepoKey: "local", Name: "after-fence", Size: 1})
		return nil
	})
	storekit.WantErr(t, err, kernel.ErrConflict)
	_, err = s.Items().Get(ctx, "local", false, "after-fence")
	storekit.WantErr(t, err, kernel.ErrNotFound)
	storageBytes(t, s, "alpha", 76)
}

func projectStoragePolicy(t *testing.T, s Store) {
	ctx := t.Context()
	storageProject(t, s, "alpha", 100, false)
	storageRepo(t, s, "local", "alpha", repo.Local)
	storageItem(t, s, "local", "base", 75)
	warnings := func(want int64) {
		t.Helper()
		stats := storekit.Get[[]work.WorkStats](t)(s.WorkQueue().Stats(ctx))
		var got int64
		for _, row := range stats {
			if row.Kind == project.StorageWarningKind {
				got += row.Pending + row.Leased + row.Parked
			}
		}
		if got != want {
			t.Fatalf("durable warning occurrences = %d, want %d", got, want)
		}
	}
	state := func() accmodel.ProjectStorage {
		t.Helper()
		got, exists, err := s.Projects().Storage(ctx, "alpha")
		if err != nil || !exists || got.Bytes != 75 {
			t.Fatal("policy edit changed logical bytes", got, exists, err)
		}
		return got
	}
	warnings(0)
	p := storekit.Get[accmodel.Project](t)(s.Projects().Get(ctx, "alpha"))
	p.QuotaEmail = true
	storekit.Must(t, s.Projects().Update(ctx, p))
	first := state().WarningKey
	if first == "" {
		t.Fatal("enabling warnings above the threshold did not create an occurrence")
	}
	warnings(1)
	p.QuotaEmail = false
	storekit.Must(t, s.Projects().Update(ctx, p))
	if state().WarningKey != "" {
		t.Fatal("disabling warning notifications did not rearm the transition")
	}
	p.QuotaEmail = true
	err := s.Tx(ctx, func(ctx context.Context) error {
		if err := s.Projects().Update(ctx, p); err != nil {
			return fmt.Errorf("change rollback warning policy: %w", err)
		}
		return errBoom
	})
	storekit.WantErr(t, err, errBoom)
	if state().WarningKey != "" {
		t.Fatal("failed policy transaction retained its warning identity")
	}
	warnings(1)
	storekit.Must(t, s.Projects().Update(ctx, p))
	if state().WarningKey == "" || state().WarningKey == first {
		t.Fatal("rearmed warning reused an earlier occurrence")
	}
	warnings(2)
	p.StorageQuotaBytes = 200
	storekit.Must(t, s.Projects().Update(ctx, p))
	if state().WarningKey != "" {
		t.Fatal("raising the limit below the warning threshold did not rearm")
	}
	p.StorageQuotaBytes = 100
	storekit.Must(t, s.Projects().Update(ctx, p))
	current := state().WarningKey
	warnings(3)
	p.Description = "ordinary metadata edit"
	storekit.Must(t, s.Projects().Update(ctx, p))
	if state().WarningKey != current {
		t.Fatal("metadata-only edit changed the warning occurrence")
	}
	warnings(3)
	err = s.Tx(ctx, func(ctx context.Context) error {
		if err := tx.CheckPublication(ctx); err != nil {
			return fmt.Errorf("settle quota-policy fixture: %w", err)
		}
		late := p
		late.Description = "must roll back"
		_ = s.Projects().Update(ctx, late)
		return nil
	})
	storekit.WantErr(t, err, kernel.ErrConflict)
	retained := storekit.Get[accmodel.Project](t)(s.Projects().Get(ctx, "alpha"))
	if retained.Description != p.Description {
		t.Fatal("dropped post-publication policy refusal retained the metadata write")
	}
	p.StorageQuotaBytes = 50
	storekit.Must(t, s.Projects().Update(ctx, p))
	storageBytes(t, s, "alpha", 75)
	_, err = s.Items().Put(ctx, model.Item{RepoKey: "local", Name: "hard-refused", Size: 1})
	storekit.WantErr(t, err, project.ErrStorageQuota)
	p.SoftLimit = true
	storekit.Must(t, s.Projects().Update(ctx, p))
	storageItem(t, s, "local", "soft-accepted", 200)
	storageBytes(t, s, "alpha", 275)
	p.StorageQuotaBytes = -1
	storekit.Must(t, s.Projects().Update(ctx, p))
	storageItem(t, s, "local", "unlimited-accepted", 1000)
	storageBytes(t, s, "alpha", 1275)
	warnings(3)
}
