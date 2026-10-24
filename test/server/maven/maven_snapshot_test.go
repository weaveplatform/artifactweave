package maven_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// A -SNAPSHOT deploy to a unique Maven repository answers with the path it
// was stored at, a sidecar of it names that file in Location, as every
// sidecar names its file, and replacing a file through a -SNAPSHOT name
// needs the right to delete it where it is stored (#280).
func TestAccMavenSnapshotDeploy(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := context.Background()
	c := repo.Config{Key: "maven-local", Class: repo.Local, PackageType: repo.Maven}
	if err := w.db.Repos().Create(ctx, *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(ctx, types.Permission{
		Name: "deploy-maven",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"deployer": types.ActionRead | types.ActionWrite,
			}},
			Targets: map[string]types.Target{"maven-local": {Include: []string{"**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	dir := "com/acme/app/1.0-SNAPSHOT/"
	base := "/artifactory/maven-local/" + dir
	ts := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC).UnixMilli()
	stamp := ";build.timestamp=" + strconv.FormatInt(ts, 10)

	rec := w.do("PUT", base+"app-1.0-SNAPSHOT.jar"+stamp, "deployer", "jar")
	wantStatus(t, rec, http.StatusCreated)
	var created struct {
		Path        string `json:"path"`
		DownloadURI string `json:"downloadUri"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	unique := dir + "app-1.0-20261017.093000-1.jar"
	if created.Path != "/"+unique ||
		!strings.HasSuffix(created.DownloadURI, "/artifactory/maven-local/"+unique) {
		t.Fatalf("created %+v", created)
	}

	rec = w.do("PUT", base+"app-1.0-SNAPSHOT.jar.sha1", "deployer", sha1Hex("jar"))
	wantStatus(t, rec, http.StatusCreated)
	if got := rec.Header().Get("Location"); got != created.DownloadURI {
		t.Errorf("sidecar Location %q, want %q", got, created.DownloadURI)
	}

	sources := base + "app-1.0-SNAPSHOT-sources.jar" + stamp
	wantStatus(t, w.do("PUT", sources, "deployer", "sources"), http.StatusCreated)
	// The same build's sources again replace the stored file, which the
	// deployer may not delete.
	wantStatus(t, w.do("PUT", sources, "deployer", "sources again"), http.StatusForbidden)
	wantStatus(t, w.do("PUT", sources, "admin", "sources again"), http.StatusCreated)

	// Any repository's sidecar names its file.
	wantStatus(t, w.do("PUT", "/artifactory/libs-local/org/app/app-1.0.jar", "admin", "lib"),
		http.StatusCreated)
	rec = w.do("PUT", "/artifactory/libs-local/org/app/app-1.0.jar.sha1", "admin", sha1Hex("lib"))
	wantStatus(t, rec, http.StatusCreated)
	if got := rec.Header().Get("Location"); !strings.HasSuffix(got,
		"/artifactory/libs-local/org/app/app-1.0.jar") {
		t.Errorf("generic sidecar Location %q", got)
	}
}
