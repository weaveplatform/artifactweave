package project

import (
	"errors"
	"math"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitProjectStorageAdmission(t *testing.T) {
	for _, tc := range []struct {
		name             string
		limit, used, add int64
		soft             bool
		want             int64
		err              error
	}{
		{name: "exact hard boundary", limit: 100, used: 99, add: 1, want: 100},
		{name: "hard growth refused", limit: 100, used: 99, add: 2, want: 99, err: ErrStorageQuota},
		{name: "already over and growing", limit: 50, used: 100, add: 1, want: 100, err: ErrStorageQuota},
		{name: "already over unchanged", limit: 50, used: 100, want: 100},
		{name: "already over shrinking", limit: 50, used: 100, add: -1, want: 99},
		{name: "delete all", limit: 50, used: 100, add: -100},
		{name: "soft growth", limit: 50, used: 100, add: 99, soft: true, want: 199},
		{name: "minus one unlimited", limit: -1, used: 100, add: 99, want: 199},
		{name: "zero unlimited", used: 100, add: 99, want: 199},
		{name: "largest valid accounting", used: math.MaxInt64 - 1, add: 1, want: math.MaxInt64},
		{name: "overflow refused", used: math.MaxInt64, add: 1, want: math.MaxInt64, err: kernel.ErrStore},
		{name: "negative counter refused", used: -1, want: -1, err: kernel.ErrStore},
		{name: "underflow refused", used: 1, add: -2, want: 1, err: kernel.ErrStore},
		{name: "minimum delta refused", used: math.MaxInt64, add: math.MinInt64, want: math.MaxInt64, err: kernel.ErrStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := model.Project{StorageQuotaBytes: tc.limit, SoftLimit: tc.soft}
			got, err := AdmitStorage(p, tc.used, tc.add)
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("admission=%d,%v; want %d,%v", got, err, tc.want, tc.err)
			}
		})
	}
}

func TestUnitProjectStorageWarningBoundary(t *testing.T) {
	for _, tc := range []struct{ limit, threshold int64 }{{1, 1}, {2, 2}, {3, 3}, {4, 3}, {99, 75}, {100, 75}, {math.MaxInt64, 6917529027641081856}} {
		p := model.Project{StorageQuotaBytes: tc.limit, QuotaEmail: true}
		threshold := tc.threshold
		if StorageWarning(p, threshold-1) || !StorageWarning(p, threshold) || !StorageWarning(p, math.MaxInt64) {
			t.Fatalf("75%% boundary wrong for quota %d", tc.limit)
		}
		p.SoftLimit = true
		if !StorageWarning(p, threshold) {
			t.Fatal("soft quota suppressed configured notification")
		}
		p.QuotaEmail = false
		if StorageWarning(p, threshold) {
			t.Fatal("disabled notification remained active")
		}
	}
	for _, limit := range []int64{-1, 0} {
		if StorageWarning(model.Project{StorageQuotaBytes: limit, QuotaEmail: true}, math.MaxInt64) {
			t.Fatal("unlimited quota produced a warning")
		}
	}
	if StorageWarning(model.Project{StorageQuotaBytes: 100, QuotaEmail: true}, -1) {
		t.Fatal("invalid negative usage produced a warning")
	}
}
