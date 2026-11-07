package store_test

import (
	"github.com/weaveplatform/artifactweave/migration/platform/outbox"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// The bridge must alias the outbox port, not declare its own (decision 0044,
// "Type-alias bridges"): the SQL and in-memory stores implement it under the
// store name, and the metadata store's Outbox returns it as store.OutboxStore,
// while the outbox package uses the outbox name, and neither side converts. A
// declared OutboxStore would still compile here, because interfaces with the
// same methods are assignable both ways. A pointer is assignable only to a
// pointer to the identical type, so this line compiles only while the pair is
// one type.
var _ *outbox.OutboxStore = (*store.OutboxStore)(nil)
