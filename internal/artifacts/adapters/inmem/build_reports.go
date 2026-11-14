package inmem

import (
	"cmp"
	"context"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

func (b buildStore) reports() *memtx.Table[int64, model.BuildReport] {
	return memtx.TableOf[int64, model.BuildReport](b.s.Store, "build_reports")
}

func (b buildStore) PutReport(ctx context.Context, r model.BuildReport) (int64, error) {
	if err := port.CheckBuildReport(r); err != nil {
		return 0, fmt.Errorf("build report of %s %s/%s: %w", r.Action, r.ProjectKey, r.Name, err)
	}
	err := b.s.Do(ctx, func() error {
		r.ID = b.s.NextID("build_reports")
		b.reports().Put(r.ID, r)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return r.ID, nil
}

func (b buildStore) ReportForUpdate(ctx context.Context, id int64) (model.BuildReport, error) {
	if !b.s.InTx(ctx) {
		return model.BuildReport{}, fmt.Errorf(
			"get build report for update requires a transaction: %w",
			kernel.ErrInvalid,
		)
	}
	var out model.BuildReport
	err := b.s.Do(ctx, func() error {
		var ok bool
		if out, ok = b.reports().Get(id); !ok {
			return memtx.NotFound("build report", id)
		}
		return nil
	})
	return out, err
}

func (b buildStore) PruneReports(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit < 1 || limit > kernel.MaxLimit {
		return 0, fmt.Errorf("build report prune limit: %w", kernel.ErrInvalid)
	}
	var n int64
	err := b.s.Do(ctx, func() error {
		expired := b.reports().All(
			func(_ int64, r model.BuildReport) bool { return !r.Expires.After(now) },
			func(x, y model.BuildReport) int {
				if c := x.Expires.Compare(y.Expires); c != 0 {
					return c
				}
				return cmp.Compare(x.ID, y.ID)
			},
		)
		for _, r := range expired[:min(len(expired), limit)] {
			b.reports().Del(r.ID)
			n++
		}
		return nil
	})
	return n, err
}
