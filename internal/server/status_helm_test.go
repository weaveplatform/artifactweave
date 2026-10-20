package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// helmRepos are Helm's fixture repositories: helm-local, helm-remote and
// helm-virtual.
var helmRepos = fixtureRepos("helm", repo.Helm)

// helmEnforce is a Helm repository with both Enforce Layout fields on,
// which the Helm status rows seed.
const helmEnforce = "helm-enforce"

// helmChart is a chart archive as helm package writes one, of the chart
// name at version.
func helmChart(name, version string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", name, version)
	// Writes to a buffer cannot fail.
	_ = tw.WriteHeader(&tar.Header{
		Name: name + "/Chart.yaml", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	})
	_, _ = io.WriteString(tw, body)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// seedHelm deploys a chart at each path in the repository key, with its
// .prov beside it when prov is set.
func seedHelm(
	t *testing.T,
	items *item.Service,
	db store.Store,
	key string,
	prov bool,
	paths ...string,
) {
	t.Helper()
	ctx := context.Background()
	c, err := db.Repos().Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		files := map[string][]byte{p: helmChart("web", "1.0.0")}
		if prov {
			files[p+".prov"] = []byte("-----BEGIN PGP SIGNED MESSAGE-----\n")
		}
		for fp, body := range files {
			if _, err := items.Deploy(ctx, item.Deploy{
				Target: item.Target{Repo: c, Path: fp}, Body: bytes.NewReader(body),
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// grantCharter gives the user charter Read, Deploy and Delete on every
// path of helm-local but its .prov files, which its permission excludes:
// the caller a chart's operation must refuse when it would take a .prov
// along (decision 0039).
func grantCharter(t *testing.T, db store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := db.Users().
		Create(ctx, types.User{Name: "charter", PasswordHash: cheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Permissions().Create(ctx, types.Permission{
		Name: "helm-charter",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"charter": rwd}},
			Targets: map[string]types.Target{"helm-local": {
				Include: []string{"**"}, Exclude: []string{"**/*.prov"},
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

// helmStatusPart is Helm's share of the status table (#548): the chart's
// .prov in a delete, copy or move, Enforce Layout on a deploy, and the
// Enforce Layout fields on an update. Helm's reindex operation answers
// since the format rebuilds its index; reindexStatusPart has its rows.
var helmStatusPart = statusPart{
	generated: map[string]bool{reindexHelm: true},
	seed: func(t *testing.T, db store.Store, blobs blob.Store) {
		t.Helper()
		yes := true
		c := repo.Config{
			Key: helmEnforce, Class: repo.Local, PackageType: repo.Helm,
			ForceNonDuplicateChart: &yes, ForceMetadataNameVersion: &yes,
		}
		if err := db.Repos().Create(context.Background(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
		items := &item.Service{Store: db, Blobs: blobs}
		seedHelm(t, items, db, "helm-local", true,
			"status/del-prov/web-1.0.0.tgz", "status/cp-prov/web-1.0.0.tgz",
			"status/mv-prov/web-1.0.0.tgz")
		seedHelm(t, items, db, "helm-local", false,
			"status/del-bare/web-1.0.0.tgz", "status/cp-bare/web-1.0.0.tgz",
			"status/mv-bare/web-1.0.0.tgz")
		seedHelm(t, items, db, helmEnforce, false, "a/web-1.0.0.tgz")
		grantCharter(t, db)
	},
	rows: helmStatusRows,
}

func helmStatusRows() []statusCase {
	const (
		del    = "DELETE /artifactory/{repoKey}/{filePath}"
		put    = "PUT /artifactory/{repoKey}/{filePath}"
		cp     = "POST /artifactory/api/copy/{srcRepoKey}/{srcFilePath}"
		mv     = "POST /artifactory/api/move/{srcRepoKey}/{srcFilePath}"
		update = "POST /artifactory/api/repositories/{repoKey}"
		batch  = "POST /artifactory/api/v2/repositories/batch"
		patch  = "PATCH /artifactory/api/system/configuration"
		local  = "/artifactory/helm-local/status/"
		chart  = "/web-1.0.0.tgz"
		as     = "charter:pw"
	)
	transfer := func(verb, from, to string) string {
		return "/artifactory/api/" + verb + "/helm-local/status/" + from + chart +
			"?to=/helm-local/status/" + to + chart
	}
	enforce := "/artifactory/" + helmEnforce + "/b/"
	return []statusCase{
		// A chart takes its .prov along, which charter may not change.
		{del, local + "del-prov" + chart, as, "", 403},
		{del, local + "del-prov" + chart, "admin", "", 204},
		{del, local + "del-bare" + chart, as, "", 204},
		{cp, transfer("copy", "cp-prov", "cp-to"), as, "", 403},
		{cp, transfer("copy", "cp-prov", "cp-to"), "admin", "", 200},
		{cp, transfer("copy", "cp-bare", "cp-bare-to"), as, "", 200},
		{mv, transfer("move", "mv-prov", "mv-to"), as, "", 403},
		{mv, transfer("move", "mv-bare", "mv-bare-to"), as, "", 200},

		// Enforce Layout's 403s, and a chart that keeps it.
		{put, enforce + "web-1.0.0.tgz", "admin", string(helmChart("web", "1.0.0")), 403},
		{put, enforce + "misnamed.tgz", "admin", string(helmChart("web", "2.0.0")), 403},
		{put, enforce + "web-2.0.0.tgz", "admin", string(helmChart("web", "2.0.0")), 201},

		// The Enforce Layout fields are set only when the repository is
		// created, whichever entry point updates it.
		{
			update, "/artifactory/api/repositories/" + helmEnforce, "admin",
			`{"forceNonDuplicateChart": false}`, 400,
		},
		{
			update, "/artifactory/api/repositories/" + helmEnforce, "admin",
			`{"forceMetadataNameVersion": null}`, 400,
		},
		{
			update, "/artifactory/api/repositories/" + helmEnforce, "admin",
			`{"forceNonDuplicateChart": true, "description": "charts"}`, 200,
		},
		{
			batch, "/artifactory/api/v2/repositories/batch", "admin",
			`[{"key": "` + helmEnforce + `", "forceMetadataNameVersion": false}]`, 400,
		},
		{
			patch,
			"/artifactory/api/system/configuration",
			"admin",
			"localRepositories:\n  " + helmEnforce + ":\n    type: helm\n    forceNonDuplicateChart: false\n",
			400,
		},
		{
			patch,
			"/artifactory/api/system/configuration",
			"admin",
			"localRepositories:\n  " + helmEnforce + ":\n    type: helm\n    forceNonDuplicateChart: true\n",
			200,
		},
	}
}
