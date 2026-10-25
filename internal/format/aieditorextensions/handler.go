package aieditorextensions

import (
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Handler keeps the shared deployment policy and classifies gallery metadata.
// Repositories of this format are remote-only; the protocol validates every
// fetched representation before the source cache can publish it.
type Handler struct{ generic.Handler }

func (Handler) Classify(_ repo.Config, path string) format.Class {
	if strings.HasPrefix(path, "gallery/queries/") ||
		strings.HasSuffix(path, "/manifest.json") ||
		strings.HasSuffix(path, "/signature.zip") {
		return format.ClassMetadata
	}
	return format.ClassUnique
}
