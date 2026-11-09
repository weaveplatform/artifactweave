package aieditorextensions

import (
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
)

// Handler keeps the shared deployment policy and classifies gallery metadata.
// Repositories of this format are remote-only; the protocol validates every
// fetched representation before the source cache can publish it.
type Handler struct{ generic.Handler }

// Classify classes cached gallery queries (gallery/queries/), manifests and
// signatures as metadata, whose content can change under the same path,
// and every other path, a VSIX package included, as unique content.
func (Handler) Classify(_ repo.Config, path string) format.Class {
	if strings.HasPrefix(path, "gallery/queries/") ||
		strings.HasSuffix(path, "/manifest.json") ||
		strings.HasSuffix(path, "/signature.zip") {
		return format.ClassMetadata
	}
	return format.ClassUnique
}
