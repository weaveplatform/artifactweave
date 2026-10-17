// Package formats picks the format of a package type. It is apart from
// internal/format so a format package can implement that contract without
// an import cycle.
package formats

import (
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// For returns the format of a package type. A type is generic until its
// format lands: generic keeps no package index.
func For(t repo.PackageType) format.Handler {
	if docker.Serves(t) {
		return docker.Handler{}
	}
	return generic.Handler{}
}
