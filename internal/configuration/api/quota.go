package api

import "context"

// StorageCapacity measures the volume that quotaConfig limits: the partition
// holding the hot blob store, as 7.x measures the partition holding its
// filestore (artifactory.xsd, QuotaConfigType). Total and Used are bytes; Used
// includes bytes already written by uploads whose metadata has not committed,
// which is what lets a serialized admission see every racing writer. Measure
// performs local I/O only and no network I/O, so it may run inside a metadata
// transaction. An error means the volume cannot be measured now.
type StorageCapacity interface {
	Measure(ctx context.Context) (total, used int64, err error)
}
