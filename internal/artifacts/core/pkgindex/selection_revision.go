package pkgindex

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/layout"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
)

// The schema binds classifier semantics across a resumed run after an upgrade.
// Version metadata is covered by MutationRevision; layout definitions live in
// their own owner and must be bound separately, without reading credentials.
func (r indexReader) leadContext(ctx context.Context, repositories port.RepositoryIndexPages, ref api.IndexPackage) (*layout.Layout, string, error) {
	identity := "cleanup-lead-v1:" + string(ref.Repository.PackageType)
	if r.formats == nil {
		return nil, identity + ":none", nil
	}
	handler := r.formats(ref.Repository.PackageType)
	if _, ok := handler.(format.CleanupLayoutLeadResolver); !ok {
		if _, ok := handler.(format.CleanupLeadResolver); ok {
			return nil, identity + ":native", nil
		}
		return nil, identity + ":none", nil
	}
	name, err := repositories.IndexRepositoryLayout(ctx, ref.Repository.Key)
	if err != nil {
		return nil, "", fmt.Errorf("index descriptor layout reference: %w", err)
	}
	name = cmp.Or(name, repo.LayoutMaven2)
	var compiled *layout.Layout
	if r.layouts != nil {
		compiled, err = r.layouts.Layout(ctx, name)
	} else {
		compiled, err = layout.Lookup(name)
	}
	if err != nil && !errors.Is(err, layout.ErrPattern) {
		return nil, "", fmt.Errorf("index descriptor layout: %w", err)
	}
	var definition layout.Definition
	if compiled != nil {
		definition = compiled.Definition()
	}
	data, err := json.Marshal([]any{
		identity, name, compiled != nil, definition.Name, definition.ArtifactPathPattern,
		definition.DistinctiveDescriptorPathPattern, definition.DescriptorPathPattern,
		definition.FolderIntegrationRevisionRegExp, definition.FileIntegrationRevisionRegExp,
	})
	if err != nil {
		return nil, "", fmt.Errorf("encode cleanup lead binding: %w", err)
	}
	digest := sha256.Sum256(data)
	return compiled, hex.EncodeToString(digest[:]), nil
}
