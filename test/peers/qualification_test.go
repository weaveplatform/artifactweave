package peers_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	apm "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/agentpackages"
	ml "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/machinelearning"
	nixfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nix"
	pubfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/pub"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/repopath"
	"github.com/weaveplatform/artifactweave/test/peers"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

func TestAccFederatedAPIExitSQLite(t *testing.T) { t.Parallel(); testFederatedAPIExit(t, peers.SQLite) }

func TestAccFederatedAPIExitPostgres(t *testing.T) {
	t.Parallel()
	testFederatedAPIExit(t, peers.Postgres)
}

// These types have no federated resource in provider 12.11.14. Repository
// creation and subsequent item effects must therefore be proved via HTTP.
func testFederatedAPIExit(t *testing.T, driver peers.Driver) {
	_, all := federation(t, driver)
	for _, kind := range []string{"pub", "nix", "machinelearning", "bazelmodules", "luarocks", "agentpackages"} {
		t.Run(kind, func(t *testing.T) {
			key := "exit-" + kind
			federate(t, all, key, kind)
			for i, origin := range all {
				path, content := publishAPIExit(t, origin, key, kind, i)
				awaitFile(t, all, path, content)
				do(t, origin, http.MethodPut, "/artifactory/api/storage/"+path+"?properties=qualification="+origin.Name, "", http.StatusNoContent)
				for _, member := range all {
					eventually(t, member, "origin's property", func() (bool, string) {
						got := properties(t, member, path)["qualification"]
						return slices.Equal(got, []string{origin.Name}), strings.Join(got, ",")
					})
				}
				do(t, origin, http.MethodDelete, "/artifactory/"+path, "", http.StatusNoContent)
				awaitFile(t, all, path, "")
			}
		})
	}
}

// publishAPIExit uses each format's accepted publication contract; it never
// disables validation to make a generic payload pass a format-specific route.
func publishAPIExit(t *testing.T, origin *peers.Instance, key, kind string, index int) (string, string) {
	t.Helper()
	name := fmt.Sprintf("origin%d", index)
	body := "qualification " + name
	path := "qualification/" + name + ".txt"
	switch kind {
	case "pub":
		path = pubfmt.Path(name, "1.0.0")
		body = support.TarGz(t, map[string]string{"pubspec.yaml": "name: " + name + "\nversion: 1.0.0\n", "lib/value.dart": body})
	case "nix":
		const fixtures = "internal/artifacts/ecosystem/nix/testdata/cache/"
		const infoFile = "fyli4q2yj6vh01bs0jxqbvj5hir5zvqc.narinfo"
		info, err := nixfmt.Parse(repopath.Read(t, fixtures+infoFile), infoFile)
		if err != nil {
			t.Fatal(err)
		}
		path = info.URL
		body = string(repopath.Read(t, fixtures+info.URL))
	case "bazelmodules":
		path = "modules/" + name + "/1.0.0/MODULE.bazel"
		body = `module(name="` + name + `", version="1.0.0")` + "\n"
	case "agentpackages":
		path = apm.Path("qualification", name, "1.0.0")
		var archive bytes.Buffer
		z := zip.NewWriter(&archive)
		for filename, content := range map[string]string{"apm.yml": "name: " + name + "\nversion: 1.0.0\n", "README.md": body} {
			entry, err := z.Create(filename)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := entry.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
		body = archive.String()
	case "machinelearning":
		return publishModelExit(t, origin, key, name, body), body
	}
	path = key + "/" + path
	do(t, origin, http.MethodPut, "/artifactory/"+path, body, http.StatusCreated)
	return path, body
}

func publishModelExit(t *testing.T, origin *peers.Instance, key, name, content string) string {
	t.Helper()
	api := "/artifactory/api/machinelearning/" + key + "/model/"
	status, body := origin.Do(t, http.MethodPost, api+name+"/start-transaction/1.0.0", "")
	var tx map[string]string
	if status != http.StatusOK || json.Unmarshal([]byte(body), &tx) != nil || tx["transactionId"] == "" || tx["filesUploadPath"] == "" {
		t.Fatal("model transaction did not start")
	}
	do(t, origin, http.MethodPut, "/artifactory/"+key+"/"+tx["filesUploadPath"]+"/model.pkl", content, http.StatusCreated)
	sum := sha256.Sum256([]byte(content))
	manifest := ml.Manifest{
		Created:   time.Now().UTC().Format(time.RFC3339Nano),
		Artifacts: []ml.Artifact{{Path: "model.pkl", Size: int64(len(content)), Checksums: ml.Checksums{SHA256: hex.EncodeToString(sum[:])}}},
		Format:    ml.Serialization{Framework: "files", Format: "pkl", Runtime: "python", RuntimeVersion: "3.13"},
	}
	var payload bytes.Buffer
	writer := multipart.NewWriter(&payload)
	part, err := writer.CreateFormFile("modelManifest", "modelManifest")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(part).Encode(manifest); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	status, _, _ = origin.DoHeader(
		t,
		http.MethodPut,
		api+"entity-manifest/"+name+"/1.0.0/"+tx["transactionId"],
		payload.String(),
		http.Header{"Content-Type": []string{writer.FormDataContentType()}},
	)
	if status != http.StatusCreated {
		t.Fatalf("model publication returned %d", status)
	}
	return key + "/models/" + name + "/1.0.0/files/model.pkl"
}
