// Package formats picks the format of a package type. It is apart from
// internal/format so a format package can implement that contract without
// an import cycle.
package formats

import (
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/format/golang"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// handlers are the formats with handlers of their own, each with the test
// for the package types it serves. A format adds one line here.
var handlers = []struct {
	serves  func(repo.PackageType) bool
	handler format.Handler
}{
	{docker.Serves, docker.Handler{}},
	{golang.Serves, golang.Handler{}},
	{maven.Serves, maven.Handler{}},
	{npm.Serves, npm.Handler{}},
}

// For returns the format of a package type. A type is generic until its
// format lands: generic keeps no package index.
func For(t repo.PackageType) format.Handler {
	for _, h := range handlers {
		if h.serves(t) {
			return h.handler
		}
	}
	return generic.Handler{}
}
