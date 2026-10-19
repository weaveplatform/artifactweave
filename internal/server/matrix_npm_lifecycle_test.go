package server

import (
	"bytes"
	"context"
	"strings"
	"testing"

	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// npmLifecycleMatrixPart is the matrix's share for npm dist-tag, deprecate
// and unpublish (#289, #290). Each principal acts on versions or packages
// of its own, named after it, which a pre-release version or a package
// name can carry: work's under the writer's grants, gone's outside them.
var npmLifecycleMatrixPart = matrixPart{
	rows: func() []matrixRow {
		local := npmBase + "npm-local/"
		on := func(name string, a types.Actions) rule {
			return holds("npm-local", npmfmt.CanonicalPath(name, "1.0.0"), a)
		}
		return []matrixRow{
			row(npmTagList, local+"-/package/work/dist-tags", "",
				on("work", types.ActionRead), 200),
			row(npmTagAdd, local+"-/package/work/dist-tags/stable", `"1.0.0"`,
				on("work", types.ActionAnnotate), 200),
			row(npmTagRemove, local+"-/package/work/dist-tags/t-{p}", "",
				on("work", types.ActionAnnotate), 200).
				seeding(npmVersion("work", "3.0.0-{p}", types.Properties{
					npmfmt.PropDistTag: {"t-{p}"},
				})),
			// npm deprecate; each message is new, so each principal's
			// request changes the tarball.
			row(npmPublish, local+"work", `{"versions":{"1.0.0":{"deprecated":"old {p}"}}}`,
				on("work", types.ActionAnnotate), 200),
			// Each keeps its own package's 1.0.0 and so removes its 2.0.0.
			row(npmUnpublishVersions, local+"keep-{p}/-rev/1-0", `{"versions":{"1.0.0":{}}}`,
				on("keep", types.ActionDelete), 200).
				seeding(npmVersions("keep-{p}", "1.0.0", "2.0.0")),
			row(npmUnpublishTarball, local+"work/-/work-4.0.0-{p}.tgz/-rev/1-0", "",
				on("work", types.ActionDelete), 200).
				seeding(npmVersion("work", "4.0.0-{p}", nil)),
			row(npmUnpublish, local+"gone-{p}/-rev/1-0", "",
				on("gone", types.ActionDelete), 200).
				seeding(npmVersions("gone-{p}", "1.0.0")),
		}
	},
}

// npmVersion seeds name at version in npm-local, {p} in either and in
// props' values the principal's name.
func npmVersion(name, version string, props types.Properties) seeder {
	return func(t *testing.T, f *matrixFixture, p string) {
		t.Helper()
		name, version := strings.ReplaceAll(name, "{p}", p), strings.ReplaceAll(version, "{p}", p)
		own := types.Properties{}
		for k, vs := range props {
			for _, v := range vs {
				own[k] = append(own[k], strings.ReplaceAll(v, "{p}", p))
			}
		}
		c, err := f.db.Repos().Get(context.Background(), "npm-local")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.items.Deploy(context.Background(), item.Deploy{
			Target:     item.Target{Repo: c, Path: npmfmt.CanonicalPath(name, version)},
			Body:       bytes.NewReader(npmTarballOf(name, version)),
			Properties: own,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// npmVersions seeds name at each of versions in npm-local.
func npmVersions(name string, versions ...string) seeder {
	return func(t *testing.T, f *matrixFixture, p string) {
		t.Helper()
		for _, v := range versions {
			npmVersion(name, v, nil)(t, f, p)
		}
	}
}
