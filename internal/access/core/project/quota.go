package project

import (
	"errors"
	"fmt"
	"math"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// ErrStorageQuota refuses positive logical growth beyond a hard project quota.
// It carries no project names or usage totals that an HTTP error could disclose.
var ErrStorageQuota = errors.New("project storage quota exceeded")

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
