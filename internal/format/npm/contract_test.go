package npm_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// A version is its tarball at the canonical path; "beta" is scoped, so the
// scope's folder is in both the path and the file name. Each version also
// has a copy of its tarball under another name, which strict layout stores
// but does not index. Dist-tags and deprecation are the properties the
// index follows.
func TestUnitContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type: repo.NPM,
		Version: func(p, v string, n int) formattest.Version {
			name := map[string]string{"alpha": "alpha", "beta": "@formattest/beta"}[p]
			version := v + ".0"
			body := tgz(file{name: "package/package.json", body: fmt.Sprintf(
				`{"name":%q,"version":%q,"description":"deploy %d"}`, name, version, n)})
			canonical := npm.CanonicalPath(name, version)
			return formattest.Version{
				Package: name, Version: version,
				Files: []formattest.File{
					{Path: canonical, Body: body},
					{Path: strings.TrimSuffix(canonical, ".tgz") + "-copy.tgz", Body: body},
				},
				Linked: []string{canonical},
			}
		},
		Properties: []types.Properties{
			{npm.PropDistTag: {"beta"}},
			{npm.PropDistTag: {"beta", "next"}, npm.PropDeprecated: {"use 2.0.0"}},
		},
	})
}
