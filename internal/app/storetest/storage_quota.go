package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	accmodel "github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/quota"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

var (
	errVolumeUnavailable = errors.New("volume unavailable")
	errLaterWrite        = errors.New("later write failed")
)

// Volume is a deterministic measured capacity for the global storage quota:
// Used is Base plus the bytes writers report as written to blob storage
// before their metadata transaction, as an upload's blob lands before its
// item commits. A suite resets it before each case.
type Volume struct {
	mu                 sync.Mutex
	Total, Base, Wrote int64
	Err                error
}

// Measure reports the current fixture measurement.
func (v *Volume) Measure(context.Context) (int64, int64, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.Total, v.Base + v.Wrote, v.Err
}

func (v *Volume) reset(total, base int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.Total, v.Base, v.Wrote, v.Err = total, base, 0, nil
}

func (v *Volume) land(bytes int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.Wrote += bytes
}

// RunStorageQuota exercises the global storage quota through the
// composition's actual metadata writes. newStore must build stores that
// measure vol, and the cases run one after another, each resetting it.
func RunStorageQuota(t *testing.T, vol *Volume, newStore Factory) {
	t.Helper()
	for _, tc := range []struct {
		name string
		run  func(*testing.T, Store, *Volume)
	}{
		{"limit refuses growth and keeps reclamation", quotaLimit},
		{"racing writers cannot pass the limit", quotaRacing},
		{"project quotas apply independently", quotaWithProjects},
		{"warning occurrence commits with its write", quotaWarning},
	} {
		t.Run(tc.name, func(t *testing.T) { vol.reset(1000, 0); tc.run(t, newStore(t), vol) })
	}
}

func quotaPolicy(t *testing.T, s Store, doc string) {
	t.Helper()
	storekit.Must(t, s.Configs().Put(t.Context(), configdoc.ConfigDocument{
		Kind: quota.ConfigKind, Key: quota.ConfigKey, Enabled: true, Document: []byte(doc),
	}))
}

func quotaItems(t *testing.T, s Store, repository string) int64 {
	t.Helper()
	var total int64
	for _, name := range []string{"base", "grown", "racer-0", "racer-1", "racer-2", "racer-3", "racer-4", "racer-5", "racer-6", "racer-7"} {
		it, err := s.Items().Get(t.Context(), repository, false, name)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		storekit.Must(t, err)
		total += it.Size
	}
	return total
}

func quotaLimit(t *testing.T, s Store, vol *Volume) {
	ctx := t.Context()
	storageProject(t, s, "alpha", 1<<40, false)
	storageRepo(t, s, "local", "alpha", repo.Local)
	storageItem(t, s, "local", "base", 100)
	quotaPolicy(t, s, `{"enabled":true,"diskSpaceLimitPercentage":95,"diskSpaceWarningPercentage":85}`)
	vol.reset(1000, 950)
	_, err := s.Items().Put(ctx, model.Item{RepoKey: "local", Name: "grown", Size: 1})
	storekit.WantErr(t, err, kernel.ErrDiskQuota)
	if quotaItems(t, s, "local") != 100 {
		t.Fatal("a refused write published its item")
	}
	storageBytes(t, s, "alpha", 100)
	// Shrinking, net-zero and deleting writes reclaim space and pass.
	storageItem(t, s, "local", "base", 60)
	storageItem(t, s, "local", "base", 60)
	_, err = s.Items().Delete(ctx, "local", false, "base")
	storekit.Must(t, err)
	storageBytes(t, s, "alpha", 0)
	// A measurement below the limit admits growth again at once.
	vol.reset(1000, 949)
	storageItem(t, s, "local", "grown", 1)
	// An unmeasurable volume fails closed for growth only.
	vol.mu.Lock()
	vol.Err = errVolumeUnavailable
	vol.mu.Unlock()
	_, err = s.Items().Put(ctx, model.Item{RepoKey: "local", Name: "base", Size: 5})
	storekit.WantErr(t, err, kernel.ErrStorageCapacity)
	_, err = s.Items().Delete(ctx, "local", false, "grown")
	storekit.Must(t, err)
	// Disabling the policy takes effect for the next write.
	quotaPolicy(t, s, `{"enabled":false,"diskSpaceLimitPercentage":95}`)
	storageItem(t, s, "local", "base", 5)
}

func quotaRacing(t *testing.T, s Store, vol *Volume) {
	ctx := t.Context()
	storageProject(t, s, "alpha", 1<<40, false)
	storageRepo(t, s, "local", "alpha", repo.Local)
	quotaPolicy(t, s, `{"enabled":true,"diskSpaceLimitPercentage":95,"diskSpaceWarningPercentage":95}`)
	vol.reset(1000, 800)
	const writers, size = 8, 20
	start, results := make(chan struct{}), make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() {
			<-start
			// The blob lands before the metadata transaction, as an upload's does.
			vol.land(size)
			_, err := s.Items().Put(ctx, model.Item{RepoKey: "local", Name: fmt.Sprintf("racer-%d", i), Size: size})
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	admitted := 0
	for err := range results {
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, kernel.ErrDiskQuota):
		default:
			t.Fatal(err)
		}
	}
	// Each admission measured every landed byte, its own included, below
	// 950: 800 + 20 * admitted < 950, so at most 7 of the 8 commit.
	if admitted >= writers || 800+size*int64(admitted) >= 950 {
		t.Fatal("racing writers passed the measured limit", admitted)
	}
	if got := quotaItems(t, s, "local"); got != size*int64(admitted) {
		t.Fatal("refused racers published bytes", got, admitted)
	}
	storageBytes(t, s, "alpha", size*int64(admitted))
}

func quotaWithProjects(t *testing.T, s Store, vol *Volume) {
	ctx := t.Context()
	storageProject(t, s, "alpha", 100, false)
	storageRepo(t, s, "local", "alpha", repo.Local)
	quotaPolicy(t, s, `{"enabled":true,"diskSpaceLimitPercentage":95}`)
	vol.reset(1000, 0)
	// Global room does not exempt the project's hard quota.
	_, err := s.Items().Put(ctx, model.Item{RepoKey: "local", Name: "base", Size: 101})
	storekit.WantErr(t, err, project.ErrStorageQuota)
	// Project room, or a soft project limit, does not exempt the global one,
	// which 7.x checks first.
	p := storekit.Get[accmodel.Project](t)(s.Projects().Get(ctx, "alpha"))
	p.SoftLimit = true
	storekit.Must(t, s.Projects().Update(ctx, p))
	vol.reset(1000, 999)
	_, err = s.Items().Put(ctx, model.Item{RepoKey: "local", Name: "base", Size: 500})
	storekit.WantErr(t, err, kernel.ErrDiskQuota)
	storageBytes(t, s, "alpha", 0)
	// A disabled global policy leaves the hard project quota in force.
	p.SoftLimit = false
	storekit.Must(t, s.Projects().Update(ctx, p))
	quotaPolicy(t, s, `{"enabled":false}`)
	_, err = s.Items().Put(ctx, model.Item{RepoKey: "local", Name: "base", Size: 101})
	storekit.WantErr(t, err, project.ErrStorageQuota)
	storageItem(t, s, "local", "base", 100)
	storageBytes(t, s, "alpha", 100)
}

func quotaWarning(t *testing.T, s Store, vol *Volume) {
	ctx := t.Context()
	storageRepo(t, s, "unowned", "", repo.Local)
	quotaPolicy(t, s, `{"enabled":true,"diskSpaceLimitPercentage":95,"diskSpaceWarningPercentage":85}`)
	vol.reset(1000, 900)
	failed := errLaterWrite
	err := s.Tx(ctx, func(ctx context.Context) error {
		if _, err := s.Items().Put(ctx, model.Item{RepoKey: "unowned", Name: "base", Size: 1}); err != nil {
			return fmt.Errorf("warning fixture: %w", err)
		}
		return failed
	})
	storekit.WantErr(t, err, failed)
	if stats := storekit.Get[[]work.WorkStats](t)(s.WorkQueue().Stats(ctx)); len(stats) != 0 {
		t.Fatal("a rolled-back write left its warning", stats)
	}
	storageItem(t, s, "unowned", "base", 1)
	storageItem(t, s, "unowned", "grown", 1)
	stats := storekit.Get[[]work.WorkStats](t)(s.WorkQueue().Stats(ctx))
	if len(stats) != 1 || stats[0].Kind != quota.WarningKind || stats[0].Pending != 1 {
		t.Fatal("the warning crossing queued no single occurrence", stats)
	}
}
