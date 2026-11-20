package format

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
)

// CleanupLeadResolver classifies only existing linked index files. The format
// owns canonical archive/descriptor identity and any choice between duplicates.
// Constructing a selector may decode this version's opaque index metadata, but
// must not read blobs, query stores or derive package identity from a file path.
type CleanupLeadResolver interface {
	CleanupLead(model.PackageVersion) (CleanupLeadSelector, error)
}

// CleanupLeadSelector supplies bounded facts that can be reduced across pages.
// The reader never substitutes an arbitrary first file for a missing lead.
type CleanupLeadSelector func(api.IndexFile) api.IndexLead
