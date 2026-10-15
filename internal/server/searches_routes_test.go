package server

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// seedStatusSearches deploys libs-local's maven/lib/1.0/lib-1.0.jar, a
// Maven-layout file carrying version=1.0, for the layout and version
// searches' rows.
func seedStatusSearches(t *testing.T, db store.Store, blobs blob.Store) {
	t.Helper()
	ctx := context.Background()
	libs, err := db.Repos().Get(ctx, "libs-local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&item.Service{Store: db, Blobs: blobs}).Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: libs, Path: "maven/lib/1.0/lib-1.0.jar"},
		Body:   strings.NewReader("lib"), Properties: types.Properties{"version": {"1.0"}},
	}); err != nil {
		t.Fatal(err)
	}
}

// moreSearchStatusRows: GAVC, versions and dependency search answer anyone
// while anonymous access is on, with what each may read; latest version
// answers 404 to a caller who reads no match. The build artifacts search
// needs read on the run, and the latest version by properties a named user.
func moreSearchStatusRows() []statusCase {
	const (
		gavc       = "GET /artifactory/api/search/gavc"
		versions   = "GET /artifactory/api/search/versions"
		latest     = "GET /artifactory/api/search/latestVersion"
		dependency = "GET /artifactory/api/search/dependency"
		artifacts  = "POST /artifactory/api/search/buildArtifacts"
		byProps    = "GET /artifactory/api/versions/{repo}/{path}"
		s          = "/artifactory/api/search/"
		v          = "/artifactory/api/versions/libs-local/maven"
		run        = `{"buildName":"st-app","buildNumber":"1"}`
	)
	var rows []statusCase
	for _, op := range []struct{ route, path, invalid string }{
		{gavc, s + "gavc?a=lib", s + "gavc"},
		{versions, s + "versions?a=lib", s + "versions?v=1.0"},
		{dependency, s + "dependency?sha1=" + sha1Hex("fixture"), s + "dependency?sha1=x"},
	} {
		rows = append(rows,
			statusCase{op.route, op.path, "admin", "", 200},
			statusCase{op.route, op.path, "user", "", 200},
			statusCase{op.route, op.path, "anonymous", "", 200},
			statusCase{op.route, op.path, "", "", 401},
			statusCase{op.route, op.invalid, "admin", "", 400},
		)
	}
	return append(
		rows,
		statusCase{latest, s + "latestVersion?a=lib", "admin", "", 200},
		statusCase{latest, s + "latestVersion?a=lib", "user", "", 404},
		statusCase{latest, s + "latestVersion?a=lib", "anonymous", "", 404},
		statusCase{latest, s + "latestVersion?a=lib", "", "", 401},
		statusCase{latest, s + "latestVersion?a=lib&remote=3", "admin", "", 400},
		statusCase{byProps, v, "admin", "", 200},
		statusCase{byProps, v, "user", "", 404},
		statusCase{byProps, v, "anonymous", "", 401},
		statusCase{byProps, v, "", "", 401},
		statusCase{byProps, v + "?listFiles=x", "admin", "", 400},
		statusCase{artifacts, s + "buildArtifacts", "admin", run, 200},
		statusCase{
			artifacts,
			s + "buildArtifacts",
			"admin",
			`{"buildName":"absent","buildNumber":"1"}`,
			404,
		},
		statusCase{artifacts, s + "buildArtifacts", "admin", `{"buildName":"st-app"}`, 400},
		statusCase{artifacts, s + "buildArtifacts", "user", run, 403},
		statusCase{artifacts, s + "buildArtifacts", "anonymous", run, 401},
		statusCase{artifacts, s + "buildArtifacts", "", run, 401},
	)
}

// readsOrMissing: a caller reading under path in repoKey finds the match
// it asks for; anyone else allowed to search gets 404, as if there were
// none. named refuses anonymous callers.
func readsOrMissing(repoKey, path string, named bool) rule {
	return func(p matrixPrincipal) int {
		switch {
		case p.anonymous && (named || !p.anonymousOn):
			return http.StatusUnauthorized
		case p.holds(repoKey, path, types.ActionRead):
			return 0
		}
		return http.StatusNotFound
	}
}

// versioned seeds a file at path in libs-local carrying version=1.0, {p}
// the principal's name.
func versioned(path string) seeder {
	return func(t *testing.T, f *matrixFixture, p string) {
		t.Helper()
		ctx := context.Background()
		c, err := f.db.Repos().Get(ctx, "libs-local")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.items.Deploy(ctx, item.Deploy{
			Target: item.Target{Repo: c, Path: strings.ReplaceAll(path, "{p}", p)},
			Body:   strings.NewReader(p), Properties: types.Properties{"version": {"1.0"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// moreSearchMatrix: GAVC, versions and dependency search answer anyone;
// latest version and latest version by properties find a match only for
// who reads it; build artifacts search needs read on the run.
func moreSearchMatrix() []matrixRow {
	const s = "/artifactory/api/search/"
	bi := types.BuildInfoRepository
	return []matrixRow{
		row("GET "+s+"gavc", s+"gavc?a=lib", "", anyone, 200),
		row("GET "+s+"versions", s+"versions?a=lib", "", anyone, 200),
		row("GET "+s+"dependency", s+"dependency?sha1="+sha1Hex("matrix"), "", anyone, 200),
		row(
			"GET "+s+"latestVersion", s+"latestVersion?a=lib-{p}&repos=libs-local", "",
			readsOrMissing("libs-local", "maven/", false), 200,
		).seeding(deployed("maven/lib-{p}/1.0/lib-{p}-1.0.jar")),
		row(
			"GET /artifactory/api/versions/{repo}/{path}",
			"/artifactory/api/versions/libs-local/maven/v-{p}", "",
			readsOrMissing("libs-local", "maven/", true), 200,
		).seeding(versioned("maven/v-{p}/app.zip")),
		row(
			"POST "+s+"buildArtifacts",
			s+"buildArtifacts",
			`{"buildName":"b-{p}","buildNumber":"1"}`,
			holds(bi, "", types.ActionRead),
			200,
		).seeding(published("b-{p}")),
	}
}
