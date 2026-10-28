package machinelearning_test

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	ml "github.com/weaveplatform/artifactweave/internal/format/machinelearning"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccPrivateModelRecordsCannotEscape(t *testing.T) {
	f := newFixture(t, repo.Local)
	if err := f.db.Repos().
		Create(t.Context(), *repo.Defaults(&repo.Config{Key: "generic", Class: repo.Local, PackageType: repo.Generic})); err != nil {
		t.Fatal(err)
	}
	tx := f.start("model", "1")
	data := []byte("private upload")
	f.status(201, "PUT", "/artifactory/ml/"+tx["filesUploadPath"]+"/model.pkl", data, "", "")
	f.commit("model", "1", tx, manifest(data), 201)
	paths := []string{
		tx["filesUploadPath"] + "/model.pkl",
		tx["leadUploadPath"] + "/" + ml.TransactionName,
		"models/model/1/" + ml.ManifestName,
	}
	for _, path := range paths {
		for _, suffix := range []string{"", ".sha256", "!/nested"} {
			for _, method := range []string{"GET", "HEAD"} {
				f.status(404, method, "/artifactory/ml/"+path+suffix, nil, "", "")
			}
		}
		for _, op := range []string{"copy", "move"} {
			response := f.status(
				200,
				"POST",
				"/artifactory/api/"+op+"/ml/"+path+"?to=generic/escaped",
				nil,
				"",
				"",
			)
			if !strings.Contains(string(response), "private model records") {
				t.Fatalf("transfer bypass: %s", response)
			}
			f.status(404, "GET", "/artifactory/generic/escaped", nil, "", "")
		}
	}
	if err := f.db.Settings().
		Put(t.Context(), types.SettingFolderDownload, types.FolderDownloadSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	archive := f.status(
		200,
		"GET",
		"/artifactory/api/archive/download/ml/models?archiveType=zip&includeChecksumFiles=true",
		nil,
		"",
		"",
	)
	files, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files.File {
		if strings.Contains(file.Name, "/tmp/") || strings.Contains(file.Name, ml.ManifestName) {
			t.Fatalf("private archive entry %s", file.Name)
		}
	}
	// Canonical content may be reused, but a private manifest cannot be copied by digest.
	it, err := f.db.Items().Get(t.Context(), "ml", false, "models/model/1/"+ml.ManifestName)
	if err != nil {
		t.Fatal(err)
	}
	status, body, _ := f.request(
		"PUT",
		"/artifactory/generic/by-checksum",
		nil,
		"",
		"",
		"X-Checksum-Deploy",
		"true",
		"X-Checksum-Sha256",
		it.Actual.SHA256,
	)
	if status != 404 {
		t.Fatalf("checksum bypass: %d %s", status, body)
	}
	f.grant(
		"manager",
		types.ActionRead|types.ActionWrite|types.ActionDelete|types.ActionManage,
		[]string{"**"},
		nil,
	)
	f.status(
		204,
		"DELETE",
		"/artifactory/ml/"+tx["leadUploadPath"]+"/"+ml.TransactionName,
		nil,
		"",
		"manager",
	)
	f.status(
		400,
		"POST",
		"/artifactory/api/trash/restore/ml/"+tx["leadUploadPath"]+"/"+ml.TransactionName+"?to=ml/relocated",
		nil,
		"",
		"manager",
	)
}

func TestAccConcurrentModelCommitsPublishOneVersion(t *testing.T) {
	f := newFixture(t, repo.Local)
	type request struct {
		body        []byte
		media, path string
	}
	requests := make([]request, 2)
	for i := range requests {
		tx := f.start("model", "1")
		data := bytes.Repeat([]byte{byte(i + 1)}, 100)
		f.status(201, "PUT", "/artifactory/ml/"+tx["filesUploadPath"]+"/model.pkl", data, "", "")
		body, media := multipartBody(t, manifest(data))
		requests[i] = request{body, media, api + "entity-manifest/model/1/" + tx["transactionId"]}
	}
	result := make(chan int, 2)
	for _, r := range requests {
		go func() { status, _, _ := f.request("PUT", r.path, r.body, r.media, ""); result <- status }()
	}
	a, b := <-result, <-result
	if !(a == 201 && b == 409 || a == 409 && b == 201) {
		t.Fatalf("concurrent outcomes: %d %d", a, b)
	}
	f.status(200, "GET", api+"entity-manifest/model/1", nil, "", "")
	p, err := f.db.Packages().Find(t.Context(), "ml", "model")
	if err != nil {
		t.Fatal(err)
	}
	versions, err := f.db.Packages().Versions(t.Context(), p.ID)
	if err != nil || len(versions) != 1 {
		t.Fatalf("versions=%v error=%v", versions, err)
	}
}
