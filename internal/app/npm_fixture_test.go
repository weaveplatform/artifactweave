package app

import (
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

const npmBase = "/artifactory/api/npm/"

// Shared root fixtures still refer to npm repositories and reproducible
// tarballs while their format-specific assertions live in
// test/server/artifacts/npm.
var npmRepos = append(fixtureRepos("npm", repo.NPM), repo.Config{
	Key: "npm-releases", Class: repo.Local, PackageType: repo.NPM, HandleSnapshots: new(bool),
})

func npmPublishBody(name, version string) string { return support.NPMPublishBody(name, version) }
func npmTarballOf(name, version string) []byte   { return support.NPMTarballOf(name, version) }
