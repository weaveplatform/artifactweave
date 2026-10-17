package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"testing"

	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// npmRepos are npm's fixture repositories: npm-local, npm-remote and
// npm-virtual.
var npmRepos = fixtureRepos("npm", repo.NPM)

const (
	npmBase = "/artifactory/api/npm/"
	// npmReindex is the JFrog reindex operation, live since the npm format
	// rebuilds its index (#286).
	npmReindex = "POST " + npmBase + "{repoKey}/reindex"
	// The registry's reads (#287), routed by what follows the package's
	// name (decision 0034).
	npmPackument   = "GET " + npmBase + "{repoKey}/{package}"
	npmManifest    = "GET " + npmBase + "{repoKey}/{package}/{version}"
	npmTarball     = "GET " + npmBase + "{repoKey}/{package}/-/{tarball}"
	npmTarballHead = "HEAD " + npmBase + "{repoKey}/{package}/-/{tarball}"
	npmPing        = "GET " + npmBase + "{repoKey}/-/ping"
	npmWhoami      = "GET " + npmBase + "{repoKey}/-/whoami"
)

// npmStatusPart is npm's share of the status table, which its pull
// requests (#286–#294) fill in.
var npmStatusPart = statusPart{
	handwritten: map[string]bool{
		npmPackument: true, npmManifest: true, npmTarball: true, npmTarballHead: true,
		npmPing: true, npmWhoami: true,
	},
	generated: map[string]bool{npmReindex: true},
	rows: func() []statusCase {
		local := npmBase + "npm-local/"
		tarball := local + "status-pkg/-/status-pkg-1.0.0.tgz"
		return []statusCase{
			{npmReindex, local + "reindex", "admin", "", 200},
			{npmReindex, local + "reindex", "user", "", 403},
			{npmReindex, local + "reindex", "anonymous", "", 401},
			{npmReindex, local + "reindex", "", "", 401},
			{npmReindex, npmBase + "missing/reindex", "admin", "", 404},
			{npmReindex, npmBase + "libs-local/reindex", "admin", "", 400},
			// Only local repositories have tarballs to index (decision 0034).
			{npmReindex, npmBase + "npm-remote/reindex", "admin", "", 400},
			{npmReindex, npmBase + "npm-virtual/reindex", "admin", "", 400},

			{npmPackument, local + "status-pkg", "admin", "", 200},
			{npmPackument, local + "@acme%2fscoped", "admin", "", 200},
			{npmPackument, local + "@acme/scoped", "admin", "", 200},
			// "reindex" is a package name to a GET; only POST is the operation.
			{npmPackument, local + "reindex", "admin", "", 404},
			{npmPackument, local + "absent", "admin", "", 404},
			{npmPackument, local + "status-pkg", "user", "", 403},
			{npmPackument, local + "absent", "user", "", 403},
			{npmPackument, local + "status-pkg", "anonymous", "", 401},
			{npmPackument, local + "status-pkg", "", "", 401},
			{npmPackument, npmBase + "missing/status-pkg", "admin", "", 404},
			{npmPackument, npmBase + "libs-local/status-pkg", "admin", "", 404},
			// Remote and virtual npm repositories are P9's and P10's.
			{npmPackument, npmBase + "npm-remote/status-pkg", "admin", "", 501},
			{npmPackument, npmBase + "npm-virtual/status-pkg", "admin", "", 501},
			// npm's own operations under /-/ that no route serves yet.
			{npmPackument, local + "-/v1/login", "admin", "", 404},
			{npmPackument, local + "status-pkg/a/b", "admin", "", 404},

			{npmManifest, local + "status-pkg/1.0.0", "admin", "", 200},
			{npmManifest, local + "status-pkg/v1.0.0", "admin", "", 200},
			{npmManifest, local + "status-pkg/latest", "admin", "", 200},
			{npmManifest, local + "status-pkg/next", "admin", "", 200},
			{npmManifest, local + "@acme%2fscoped/1.0.0", "admin", "", 200},
			{npmManifest, local + "status-pkg/9.9.9", "admin", "", 404},
			{npmManifest, local + "status-pkg/nope", "admin", "", 404},
			{npmManifest, local + "status-pkg/1.0.0", "user", "", 403},
			{npmManifest, local + "status-pkg/1.0.0", "anonymous", "", 401},

			{npmTarball, tarball, "admin", "", 200},
			{npmTarball, local + "@acme%2fscoped/-/@acme%2fscoped-1.0.0.tgz", "admin", "", 200},
			{npmTarball, local + "@acme/scoped/-/@acme/scoped-1.0.0.tgz", "admin", "", 200},
			{npmTarball, local + "status-pkg/-/absent.tgz", "admin", "", 404},
			{npmTarball, tarball, "user", "", 403},
			{npmTarball, tarball, "anonymous", "", 401},
			{npmTarball, tarball, "", "", 401},
			{npmTarballHead, tarball, "admin", "", 200},
			{npmTarballHead, tarball, "user", "", 403},

			{npmPing, local + "-/ping", "admin", "", 200},
			{npmPing, local + "-/ping", "user", "", 200},
			{npmPing, local + "-/ping", "anonymous", "", 200},
			{npmPing, local + "-/ping", "", "", 401},
			{npmPing, npmBase + "npm-remote/-/ping", "admin", "", 501},
			{npmPing, npmBase + "missing/-/ping", "admin", "", 404},
			{npmWhoami, local + "-/whoami", "admin", "", 200},
			{npmWhoami, local + "-/whoami", "user", "", 200},
			{npmWhoami, local + "-/whoami", "anonymous", "", 401},
			{npmWhoami, local + "-/whoami", "", "", 401},
		}
	},
	seed: func(t *testing.T, db store.Store, blobs blob.Store) {
		t.Helper()
		items := &item.Service{Store: db, Blobs: blobs}
		next := types.Properties{npmfmt.PropDistTag: {"next"}}
		deployNPM(t, items, "npm-local", "status-pkg", "1.0.0", nil)
		deployNPM(t, items, "npm-local", "status-pkg", "1.1.0", nil)
		deployNPM(t, items, "npm-local", "status-pkg", "2.0.0-rc.1", next)
		deployNPM(t, items, "npm-local", "@acme/scoped", "1.0.0", nil)
	},
}

// deployNPM deploys the tarball of name at version to its canonical path
// in key, through items, so the npm format indexes it.
func deployNPM(
	t *testing.T,
	items *item.Service,
	key, name, version string,
	props types.Properties,
) {
	t.Helper()
	ctx := context.Background()
	c, err := items.Store.Repos().Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := items.Deploy(ctx, item.Deploy{
		Target:     item.Target{Repo: c, Path: npmfmt.CanonicalPath(name, version)},
		Body:       bytes.NewReader(npmTarballOf(name, version)),
		Properties: props,
	}); err != nil {
		t.Fatal(err)
	}
}

// npmTarballOf is a package tarball whose package.json gives name and
// version. Writes to a buffer cannot fail, so neither can this.
func npmTarballOf(name, version string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	manifest := `{"name":"` + name + `","version":"` + version + `"}`
	_ = tw.WriteHeader(&tar.Header{
		Name: "package/package.json", Mode: 0o644, Size: int64(len(manifest)),
		Typeflag: tar.TypeReg,
	})
	_, _ = tw.Write([]byte(manifest))
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}
