package machinelearninghttp_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	ml "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/machinelearning"
)

// The manifest GET answers the stored model-manifest.json, which 7.x reads
// through its download path, so the download policy refuses it at its own
// path: a blacked-out repository, and patterns rejecting that path, answer
// the item routes' 404 to administrators and readers alike. Patterns
// rejecting a declared file leave the manifest answered, the file listed,
// and refuse only the file's download. A caller who may not read the
// manifest is refused first and learns nothing of the setting.
func TestAccManifestDownloadPolicy(t *testing.T) {
	f := newFixture(t, repo.Local)
	data := []byte("controlled model bytes")
	tx := f.start("classifier", "1.2.3")
	f.status(201, "PUT", "/artifactory/ml/"+tx["filesUploadPath"]+"/model.pkl", data, "", "")
	f.commit("classifier", "1.2.3", tx, manifest(data), 201)
	f.grant("reader", accessapi.ActionRead, []string{"**"}, nil)
	const (
		route    = api + "entity-manifest/classifier/1.2.3"
		document = "models/classifier/1.2.3/model-manifest.json"
		file     = "models/classifier/1.2.3/files/model.pkl"
	)
	patch := func(change func(*repo.Config)) {
		t.Helper()
		c, err := f.db.Repos().Get(t.Context(), "ml")
		if err != nil {
			t.Fatal(err)
		}
		change(&c)
		if err := f.db.Repos().Update(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	refused := func(message string) {
		t.Helper()
		for _, user := range []string{"admin", "reader"} {
			body := string(f.status(http.StatusNotFound, "GET", route, nil, "", user))
			if !strings.Contains(body, message) {
				t.Fatalf("GET %s as %s: %s, want %q", route, user, body, message)
			}
		}
		body := string(f.status(http.StatusForbidden, "GET", route, nil, "", "outsider"))
		if strings.Contains(body, "'ml'") {
			t.Fatalf("GET %s as outsider names the setting: %s", route, body)
		}
	}
	served := func() ml.Manifest {
		t.Helper()
		var got ml.Manifest
		if err := json.Unmarshal(f.status(http.StatusOK, "GET", route, nil, "", "reader"), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	blackedOut, excluded := true, document
	patch(func(c *repo.Config) { c.BlackedOut = &blackedOut })
	refused("The repository 'ml' is blacked out and cannot serve artifact 'ml:" + document + "'.")

	blackedOut = false
	patch(func(c *repo.Config) { c.BlackedOut, c.ExcludesPattern = &blackedOut, &excluded })
	refused("The repository 'ml' rejected the artifact 'ml:" + document +
		"' due to its include/exclude pattern settings.")

	excluded = "models/classifier/1.2.3/files/**"
	patch(func(c *repo.Config) { c.ExcludesPattern = &excluded })
	got := served()
	if len(got.Artifacts) != 1 ||
		!strings.HasSuffix(got.Artifacts[0].DownloadURL, "/artifactory/ml/"+file) {
		t.Fatalf("manifest lists %+v, want the excluded file with its download URL", got.Artifacts)
	}
	body := string(f.status(http.StatusNotFound, "GET", "/artifactory/ml/"+file, nil, "", "reader"))
	if !strings.Contains(body, "rejected the artifact 'ml:"+file+"'") {
		t.Fatalf("GET %s: %s, want the patterns' refusal", file, body)
	}

	excluded = ""
	patch(func(c *repo.Config) { c.ExcludesPattern = &excluded })
	served()
}
