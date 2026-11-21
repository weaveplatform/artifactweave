package project

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ChangeStorage admits a known net logical delta and its bounded local
// publication as one transaction. Call before artifact/source/item locks; no
// callback may stream a client body or perform network I/O. Compound transfers
// supply their net project delta, so an in-project move needs no extra capacity.
// The caller separately proves current publication authority and actual bytes.
func (s Service) ChangeStorage(ctx context.Context, key string, delta int64, publish func(context.Context) error) error {
	return s.tx(ctx, func(ctx context.Context) error {
		ps := s.Store.Projects()
		if err := ps.Guard(ctx); err != nil {
			return fmt.Errorf("guard project storage publication: %w", err)
		}
		p, err := ps.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("current project quota: %w", err)
		}
		state, initialized, err := ps.Storage(ctx, key)
		if err != nil {
			return fmt.Errorf("current project storage: %w", err)
		}
		if !initialized {
			if s.StorageBytes == nil {
				return fmt.Errorf("project storage initializer unavailable: %w", kernel.ErrStore)
			}
			state.Bytes, err = s.StorageBytes(ctx, key)
			if err != nil {
				return fmt.Errorf("initialize project storage: %w", err)
			}
		}
		state.Bytes, err = AdmitStorage(p, state.Bytes, delta)
		if err != nil {
			return err
		}
		newWarning := false
		if !StorageWarning(p, state.Bytes) {
			state.WarningKey = ""
		} else if state.WarningKey == "" {
			state.WarningKey, newWarning = rand.Text(), true
		}
		var warning model.ProjectStorageWarning
		if newWarning {
			if s.StorageWarning == nil {
				return fmt.Errorf("project storage notification handoff unavailable: %w", kernel.ErrStore)
			}
			authority, err := ps.Authority(ctx, key)
			if err != nil {
				return fmt.Errorf("project storage warning lifetime: %w", err)
			}
			warning = model.ProjectStorageWarning{ProjectKey: key, Incarnation: authority.Incarnation, WarningKey: state.WarningKey, Bytes: state.Bytes, Limit: p.StorageQuotaBytes}
		}
		if err := ps.SetStorage(ctx, key, state); err != nil {
			return fmt.Errorf("save project storage: %w", err)
		}
		if publish != nil {
			if err := publish(ctx); err != nil {
				return fmt.Errorf("project storage publication: %w", err)
			}
		}
		if newWarning {
			if err := s.StorageWarning(ctx, warning); err != nil {
				return fmt.Errorf("enqueue project storage warning: %w", err)
			}
		}
		return nil
	})
}

// ErrStorageQuota refuses positive logical growth beyond a hard project quota.
// It carries no project names or usage totals that an HTTP error could disclose.
var ErrStorageQuota = kernel.ErrStorageQuota

// AdmitStorage computes a logical byte delta under the caller's transactional
// project/accounting guard. Reaching the quota exactly is allowed. Lowering a
// quota below current usage must still allow zero-growth and shrinking writes;
// positive growth requires either room or the explicit soft-limit setting.
// This calculation alone neither reserves capacity nor authorizes a write.
func AdmitStorage(p model.Project, used, delta int64) (int64, error) {
	if used < 0 || delta < -used || delta > 0 && delta > math.MaxInt64-used {
		return used, fmt.Errorf("invalid project storage accounting delta: %w", kernel.ErrStore)
	}
	next := used + delta
	if delta > 0 && p.StorageQuotaBytes > 0 && !p.SoftLimit && next > p.StorageQuotaBytes {
		return used, ErrStorageQuota
	}
	return next, nil
}

// StorageWarning reports the documented 75% notification condition. Crossing
// and retry identity belong to durable accounting, rather than this predicate.
// Integer ceiling keeps byte-sized quotas exact without multiplication overflow.
func StorageWarning(p model.Project, used int64) bool {
	limit := p.StorageQuotaBytes
	if !p.QuotaEmail || limit <= 0 || used < 0 {
		return false
	}
	threshold := limit/4*3 + (limit%4*3+3)/4
	return used >= threshold
}
