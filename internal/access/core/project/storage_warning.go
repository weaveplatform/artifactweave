package project

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// StorageWarningKind owns a full immutable quota-warning handoff, independent
// of prunable event references. Each occurrence/lifetime gets its own queue key.
const StorageWarningKind = "project.storage-warning"

// EnqueueStorageWarning joins the caller's metadata transaction. Network mail
// delivery is separate and retries the same occurrence identity.
func EnqueueStorageWarning(ctx context.Context, queue work.WorkQueue, warning model.ProjectStorageWarning, now time.Time) error {
	if queue == nil || warning.ProjectKey == "" || warning.Incarnation == "" || warning.WarningKey == "" || warning.Bytes < 0 || warning.Limit <= 0 {
		return fmt.Errorf("invalid project storage warning handoff: %w", kernel.ErrStore)
	}
	payload, err := json.Marshal(warning)
	if err != nil {
		return fmt.Errorf("encode project storage warning: %w", err)
	}
	key := storageWarningKey(warning)
	if err := queue.Enqueue(ctx, StorageWarningKind, key, payload, now); err != nil {
		return fmt.Errorf("queue project storage warning: %w", err)
	}
	return nil
}

func storageWarningKey(warning model.ProjectStorageWarning) string {
	return warning.ProjectKey + "/" + string(warning.Incarnation) + "/" + warning.WarningKey
}
