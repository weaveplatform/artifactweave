package store_test

import (
	"github.com/weaveplatform/artifactweave/migration/platform/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// The bridge must alias the session types, not declare its own (decision
// 0044, "Type-alias bridges"): the SQL and in-memory upload stores keep
// handling store.Upload values that InUploads reads as session.Upload, and
// their users move to the session names without conversions. A declared
// UploadStore would still compile, because interfaces with the same methods
// are assignable both ways. A struct is assignable only to its own type and a
// pointer only to a pointer to the identical type, so these lines compile only
// while each pair is one type.
var (
	_ session.Upload       = store.Upload{}
	_ *session.UploadStore = (*store.UploadStore)(nil)
)
