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
	"github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Settings are the server-wide options of the formats that take any, from
// the configuration (decision 0009), each format's in a field of its own.
// The zero value is every format's default, which is 7.x's. A format's
// handler carries its settings, so every write that reaches it through an
// item service built with Settings.For applies them, whichever route the
// write came by.
type Settings struct {
	// NPM is the npm format's (decision 0034).
	NPM npm.Settings
	// PyPI is the PyPI format's (decision 0037).
	PyPI pypi.Settings
}

// handlers are the formats with handlers of their own, each with the test
// for the package types it serves. A format adds one line here.
var handlers = []struct {
	serves  func(repo.PackageType) bool
	handler func(Settings) format.Handler
}{
	{docker.Serves, func(Settings) format.Handler { return docker.Handler{} }},
	{golang.Serves, func(Settings) format.Handler { return golang.Handler{} }},
	{maven.Serves, func(Settings) format.Handler { return maven.Handler{} }},
	{npm.Serves, func(s Settings) format.Handler { return npm.Handler{Settings: s.NPM} }},
	{nuget.Serves, func(Settings) format.Handler { return nuget.Handler{} }},
	{pypi.Serves, func(s Settings) format.Handler { return pypi.Handler{Settings: s.PyPI} }},
	{terraform.Serves, func(Settings) format.Handler { return terraform.Handler{} }},
}

// For returns the format of a package type with s applied. A type is
// generic until its format lands: generic keeps no package index.
func (s Settings) For(t repo.PackageType) format.Handler {
	for _, h := range handlers {
		if h.serves(t) {
			return h.handler(s)
		}
	}
	return generic.Handler{}
}

// For returns the format of a package type with its default settings.
func For(t repo.PackageType) format.Handler { return Settings{}.For(t) }
