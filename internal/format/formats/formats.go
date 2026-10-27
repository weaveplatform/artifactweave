// Package formats picks the format of a package type. It is apart from
// internal/format so a format package can implement that contract without
// an import cycle.
package formats

import (
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/agentpackages"
	"github.com/weaveplatform/artifactweave/internal/format/agentplugins"
	"github.com/weaveplatform/artifactweave/internal/format/aieditorextensions"
	"github.com/weaveplatform/artifactweave/internal/format/alpine"
	"github.com/weaveplatform/artifactweave/internal/format/bazelmodules"
	"github.com/weaveplatform/artifactweave/internal/format/bower"
	"github.com/weaveplatform/artifactweave/internal/format/cargo"
	"github.com/weaveplatform/artifactweave/internal/format/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/format/composer"
	"github.com/weaveplatform/artifactweave/internal/format/conan"
	"github.com/weaveplatform/artifactweave/internal/format/conda"
	"github.com/weaveplatform/artifactweave/internal/format/cran"
	"github.com/weaveplatform/artifactweave/internal/format/debian"
	"github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/format/gems"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/format/gitlfs"
	"github.com/weaveplatform/artifactweave/internal/format/golang"
	"github.com/weaveplatform/artifactweave/internal/format/helm"
	"github.com/weaveplatform/artifactweave/internal/format/hex"
	"github.com/weaveplatform/artifactweave/internal/format/huggingface"
	"github.com/weaveplatform/artifactweave/internal/format/luarocks"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/format/opkg"
	"github.com/weaveplatform/artifactweave/internal/format/pub"
	"github.com/weaveplatform/artifactweave/internal/format/pypi"
	"github.com/weaveplatform/artifactweave/internal/format/rpm"
	"github.com/weaveplatform/artifactweave/internal/format/skills"
	"github.com/weaveplatform/artifactweave/internal/format/swift"
	"github.com/weaveplatform/artifactweave/internal/format/terraform"
	"github.com/weaveplatform/artifactweave/internal/format/terraformbackend"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// Settings are the server-wide options of the formats that take any, from
// the configuration (decision 0009), each format's in a field of its own.
// The zero value is every format's default, which is 7.x's. A format's
// handler carries its settings, so every write that reaches it through an
// item service built with Settings.For applies them, whichever route the
// write came by.
type Settings struct {
	// Helm is the classic Helm protocol's URL mode (decision 0039).
	Helm helm.Settings
	// NPM is the npm format's (decision 0034).
	NPM npm.Settings
	// PyPI is the PyPI format's (decision 0037).
	PyPI pypi.Settings
	// TerraformBackend is the Terraform backend format's (decision 0041).
	TerraformBackend terraformbackend.Settings
}

// handlers are the formats with handlers of their own, each with the test
// for the package types it serves. A format adds one line here.
var handlers = []struct {
	serves  func(repo.PackageType) bool
	handler func(Settings) format.Handler
}{
	{
		aieditorextensions.Serves,
		func(Settings) format.Handler { return aieditorextensions.Handler{} },
	},
	{alpine.Serves, func(Settings) format.Handler { return alpine.Handler{} }},
	{bazelmodules.Serves, func(Settings) format.Handler { return bazelmodules.Handler{} }},
	{bower.Serves, func(Settings) format.Handler { return bower.Handler{} }},
	{cargo.Serves, func(Settings) format.Handler { return cargo.Handler{} }},
	{cocoapods.Serves, func(Settings) format.Handler { return cocoapods.Handler{} }},
	{composer.Serves, func(Settings) format.Handler { return composer.Handler{} }},
	{conan.Serves, func(Settings) format.Handler { return conan.Handler{} }},
	{conda.Serves, func(Settings) format.Handler { return conda.Handler{} }},
	{cran.Serves, func(Settings) format.Handler { return cran.Handler{} }},
	{agentpackages.Serves, func(Settings) format.Handler { return agentpackages.Handler{} }},
	{agentplugins.Serves, func(Settings) format.Handler { return agentplugins.Handler{} }},
	{debian.Serves, func(Settings) format.Handler { return debian.Handler{} }},
	{gems.Serves, func(Settings) format.Handler { return gems.Handler{} }},
	{gitlfs.Serves, func(Settings) format.Handler { return gitlfs.Handler{} }},
	{docker.Serves, func(Settings) format.Handler { return docker.Handler{} }},
	{golang.Serves, func(Settings) format.Handler { return golang.Handler{} }},
	{helm.Serves, func(Settings) format.Handler { return helm.Handler{} }},
	{hex.Serves, func(Settings) format.Handler { return hex.Handler{} }},
	{huggingface.Serves, func(Settings) format.Handler { return huggingface.Handler{} }},
	{luarocks.Serves, func(Settings) format.Handler { return luarocks.Handler{} }},
	{maven.Serves, func(Settings) format.Handler { return maven.Handler{} }},
	{npm.Serves, func(s Settings) format.Handler { return npm.Handler{Settings: s.NPM} }},
	{nuget.Serves, func(Settings) format.Handler { return nuget.Handler{} }},
	{opkg.Serves, func(Settings) format.Handler { return opkg.Handler{} }},
	{pypi.Serves, func(s Settings) format.Handler { return pypi.Handler{Settings: s.PyPI} }},
	{pub.Serves, func(Settings) format.Handler { return pub.Handler{} }},
	{rpm.Serves, func(Settings) format.Handler { return rpm.Handler{} }},
	{skills.Serves, func(Settings) format.Handler { return skills.Handler{} }},
	{swift.Serves, func(Settings) format.Handler { return swift.Handler{} }},
	{terraform.Serves, func(Settings) format.Handler { return terraform.Handler{} }},
	{terraformbackend.Serves, func(s Settings) format.Handler {
		return terraformbackend.Handler{Settings: s.TerraformBackend}
	}},
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
