package machinelearning_test

import (
	"encoding/json"
	"strings"
	"testing"

	ml "github.com/weaveplatform/artifactweave/internal/format/machinelearning"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func (f *fixture) grant(name string, actions types.Actions, include, exclude []string) {
	f.t.Helper()
	p := types.Permission{
		Name: name,
		Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{name: actions}},
				Targets: map[string]types.Target{"ml": {Include: include, Exclude: exclude}},
			},
		},
	}
	if err := f.db.Permissions().Create(f.t.Context(), p); err != nil {
		f.t.Fatal(err)
	}
}

func TestAccModelArtifactPermissionsAndOwnership(t *testing.T) {
	f := newFixture(t, repo.Local)
	data := []byte("controlled model")
	tx := f.start("model", "1")
	m := manifest(data)
	m.Dependencies = []ml.Artifact{
		{
			Path:        "requirements.txt",
			Size:        int64(len(data)),
			Checksums:   ml.Checksums{SHA256: checksum(data)},
			DownloadURL: "https://untrusted.example/dependency",
		},
	}
	m.Code = &ml.Artifact{
		Path:        "code.zip",
		Size:        int64(len(data)),
		Checksums:   ml.Checksums{SHA256: checksum(data)},
		DownloadURL: "https://untrusted.example/code",
	}
	m.Artifacts[0].DownloadURL = "https://untrusted.example/model"
	for _, file := range []struct{ key, path string }{{"filesUploadPath", "model.pkl"}, {"dependenciesUploadPath", "requirements.txt"}, {"codeUploadPath", "code.zip"}} {
		f.status(201, "PUT", "/artifactory/ml/"+tx[file.key]+"/"+file.path, data, "", "")
	}
	f.grant("other", types.ActionRead|types.ActionWrite, []string{"**"}, nil)
	body, media := multipartBody(t, m)
	f.status(403, "PUT", api+"entity-manifest/model/1/"+tx["transactionId"], body, media, "other")
	f.status(403, "PUT", "/artifactory/ml/"+tx["filesUploadPath"]+"/model.pkl", data, "", "other")
	f.commit("model", "1", tx, m, 201)
	response := f.status(200, "GET", api+"entity-manifest/model/1", nil, "", "")
	if strings.Contains(string(response), "untrusted.example") {
		t.Fatal("accepted client-supplied download URL")
	}
	var got ml.Manifest
	if err := json.Unmarshal(response, &got); err != nil {
		t.Fatal(err)
	}
	if got.Code.DownloadURL == "" || got.Dependencies[0].DownloadURL == "" {
		t.Fatal("missing code/dependency URL")
	}
	f.grant("manifest-only", types.ActionRead, []string{"models/model/1/model-manifest.json"}, nil)
	f.status(403, "GET", api+"entity-manifest/model/1", nil, "", "manifest-only")
	f.grant(
		"no-code",
		types.ActionRead,
		[]string{"models/model/1/**"},
		[]string{"models/model/1/code/**"},
	)
	f.status(403, "GET", api+"entity-manifest/model/1", nil, "", "no-code")
	// A changed manifest or property set is not a replay of the original intent.
	m.Created = "2026-10-27T01:02:03Z"
	f.commit("model", "1", tx, m, 409)
}

func TestAccModelCommitRechecksEveryDestination(t *testing.T) {
	for _, exclude := range []string{"models/model/1/files/**", "models/model/1/model-manifest.json"} {
		t.Run(exclude, func(t *testing.T) {
			f := newFixture(t, repo.Local)
			f.grant("writer", types.ActionWrite, []string{"models/model/**"}, []string{exclude})
			response := f.status(200, "POST", api+"model/start-transaction/1", nil, "", "writer")
			var tx map[string]string
			if err := json.Unmarshal(response, &tx); err != nil {
				t.Fatal(err)
			}
			data := []byte("model")
			f.status(
				201,
				"PUT",
				"/artifactory/ml/"+tx["filesUploadPath"]+"/model.pkl",
				data,
				"",
				"writer",
			)
			body, media := multipartBody(t, manifest(data))
			f.status(
				403,
				"PUT",
				api+"entity-manifest/model/1/"+tx["transactionId"],
				body,
				media,
				"writer",
			)
			f.status(404, "GET", api+"entity-manifest/model/1", nil, "", "")
		})
	}
}

func TestAccModelIdentityAndImmutableCommit(t *testing.T) {
	f := newFixture(t, repo.Local)
	for _, version := range []string{"tmp", "files", "dependencies", "code"} {
		f.status(400, "POST", api+"namespace/model/start-transaction/"+version, nil, "", "")
	}
	tx := f.start("model", "1")
	data := []byte("model")
	m := manifest(data)
	m.ID = "wrong"
	f.commit("model", "1", tx, m, 400)
	m.ID = ""
	m.Version = "wrong"
	f.commit("model", "1", tx, m, 400)
	m.Version = ""
	f.status(201, "PUT", "/artifactory/ml/"+tx["filesUploadPath"]+"/model.pkl", data, "", "")
	f.commit("model", "1", tx, m, 201)
	body, media := multipartBody(t, m)
	body = []byte(strings.Replace(string(body), "controlled", "different", 1))
	f.status(409, "PUT", api+"entity-manifest/model/1/"+tx["transactionId"], body, media, "")
}
