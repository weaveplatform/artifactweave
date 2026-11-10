package npm_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	npmfmt "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/npm"
)

// npmLifecycleMatrixPart is the matrix's share for npm dist-tag, deprecate
// and unpublish (#289, #290). Each principal acts on versions or packages
// of its own, named after it, which a pre-release version or a package
// name can carry: work's under the writer's grants, gone's outside them.
var npmLifecycleMatrixPart = matrixPart{
	rows: func() []matrixRow {
		local := npmBase + "npm-local/"
		on := func(name string, a api.Actions) rule {
			return holds("npm-local", npmfmt.CanonicalPath(name, "1.0.0"), a)
		}
		return []matrixRow{
			row(npmTagList, local+"-/package/work/dist-tags", "",
				on("work", api.ActionRead), 200),
			row(npmTagAdd, local+"-/package/work/dist-tags/stable", `"1.0.0"`,
				on("work", api.ActionAnnotate), 200),
			row(npmTagRemove, local+"-/package/work/dist-tags/t-{p}", "",
				on("work", api.ActionAnnotate), 200).
				seeding(npmVersion("work", "3.0.0-{p}", model.Properties{
					npmfmt.PropDistTag: {"t-{p}"},
				})),
			// npm deprecate; each message is new, so each principal's
			// request changes the tarball.
			row(npmPublish, local+"work", `{"versions":{"1.0.0":{"deprecated":"old {p}"}}}`,
				on("work", api.ActionAnnotate), 200),
			// Each keeps its own package's 1.0.0 and so removes its 2.0.0.
			row(npmUnpublishVersions, local+"keep-{p}/-rev/1-0", `{"versions":{"1.0.0":{}}}`,
				on("keep", api.ActionDelete), 200).
				seeding(npmVersions("keep-{p}", "1.0.0", "2.0.0")),
			row(npmUnpublishTarball, local+"work/-/work-4.0.0-{p}.tgz/-rev/1-0", "",
				on("work", api.ActionDelete), 200).
				seeding(npmVersion("work", "4.0.0-{p}", nil)),
			row(npmUnpublish, local+"gone-{p}/-rev/1-0", "",
				on("gone", api.ActionDelete), 200).
				seeding(npmVersions("gone-{p}", "1.0.0")),
			// The conversion needs Manage on the repository; asking again
			// joins the conversion already queued.
			row(npmMigrate, npmMigrations+"npm-local/migrate", "", manages, 202),
			// A resume needs a parked conversion, which one resume queues
			// again, so each principal meets its own.
			row(npmResume, npmMigrations+"npm-local/resume", "", manages, 202).alone().
				seeding(func(t *testing.T, f *matrixFixture, _ string) {
					t.Helper()
					park(t, f.db, npmfmt.KindConvert, "npm-local")
				}),
		}
	},
}

// manages: a caller holding Manage on npm-local.
var manages = holds("npm-local", "", api.ActionManage)

// npmVersion seeds name at version in npm-local, {p} in either and in
// props' values the principal's name.
func npmVersion(name, version string, props model.Properties) seeder {
	return func(t *testing.T, f *matrixFixture, p string) {
		t.Helper()
		name, version := strings.ReplaceAll(name, "{p}", p), strings.ReplaceAll(version, "{p}", p)
		own := model.Properties{}
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
