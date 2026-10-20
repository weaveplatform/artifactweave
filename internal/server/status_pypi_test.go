package server

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

// pypiRepos are PyPI's fixture repositories: pypi-local, pypi-remote and
// pypi-virtual.
var pypiRepos = fixtureRepos("pypi", repo.PyPI)

const (
	pypiBase         = "/artifactory/api/pypi/"
	pypiUpload       = "POST " + pypiBase + "{repoKey}"
	pypiUploadSlash  = "POST " + pypiBase + "{repoKey}/"
	pypiUploadFolder = "POST " + pypiBase + "{repoKey}/{folder}"
	pypiPut          = "PUT " + pypiBase + "{repoKey}/{filePath}"
	// The simple index (#538), by the paths decision 0037 lists: the
	// redirect from /simple, the project list and a project's page, and a
	// file or a wheel's core metadata.
	pypiSimple       = "GET " + pypiBase + "{repoKey}/simple"
	pypiSimpleHead   = "HEAD " + pypiBase + "{repoKey}/simple"
	pypiList         = "GET " + pypiBase + "{repoKey}/simple/"
	pypiListHead     = "HEAD " + pypiBase + "{repoKey}/simple/"
	pypiProject      = "GET " + pypiBase + "{repoKey}/simple/{project}/"
	pypiProjectHead  = "HEAD " + pypiBase + "{repoKey}/simple/{project}/"
	pypiFile         = "GET " + pypiBase + "{repoKey}/{filePath}"
	pypiFileHead     = "HEAD " + pypiBase + "{repoKey}/{filePath}"
	pypiMetadata     = "GET " + pypiBase + "{repoKey}/{wheelPath}.metadata"
	pypiMetadataHead = "HEAD " + pypiBase + "{repoKey}/{wheelPath}.metadata"
)

// pypiWheelPath is the wheel the fixtures seed in pypi-local, at the
// canonical path of status-pkg 1.0.
const pypiWheelPath = "status-pkg/1.0/status_pkg-1.0-py3-none-any.whl"

// pypiWheel is a wheel of name at version whose METADATA names them.
func pypiWheel(t *testing.T, name, version string) []byte {
	t.Helper()
	dist := strings.ReplaceAll(name, "-", "_")
	return zipBytes(t, map[string]string{
		dist + "-" + version + ".dist-info/METADATA": "Metadata-Version: 2.1\nName: " + name +
			"\nVersion: " + version + "\nRequires-Python: >=3.9\n",
	})
}

// seedPypi deploys status-pkg 1.0's wheel to pypi-local through the item
// service, which indexes it as the PyPI format does.
func seedPypi(t *testing.T, items *item.Service, db store.Store, path, name string) {
	t.Helper()
	c, err := db.Repos().Get(context.Background(), "pypi-local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := items.Deploy(context.Background(), item.Deploy{
		Target: item.Target{Repo: c, Path: path},
		Body:   strings.NewReader(string(pypiWheel(t, name, "1.0"))),
	}); err != nil {
		t.Fatal(err)
	}
}

// pypiStatusPart is PyPI's share of the status table (#538–#540).
var pypiStatusPart = statusPart{
	handwritten: map[string]bool{
		pypiSimple: true, pypiSimpleHead: true, pypiList: true, pypiListHead: true,
		pypiProject: true, pypiProjectHead: true, pypiFile: true, pypiFileHead: true,
		pypiMetadata: true, pypiMetadataHead: true,
		pypiUpload: true, pypiUploadSlash: true, pypiUploadFolder: true, pypiPut: true,
	},
	seed: func(t *testing.T, db store.Store, blobs blob.Store) {
		t.Helper()
		seedPypi(t, &item.Service{Store: db, Blobs: blobs}, db, pypiWheelPath, "status-pkg")
	},
	// The JSON form and PEP 658 are on, so their rows answer too.
	tune: func(c *Config) {
		c.FormatSettings.PyPI.SimpleJSON = true
		c.FormatSettings.PyPI.CoreMetadata = true
	},
	rows: pypiStatusRows,
}

func pypiStatusRows() []statusCase {
	local := pypiBase + "pypi-local/"
	page := local + "simple/status-pkg/"
	wheel := local + pypiWheelPath
	return append([]statusCase{
		{pypiSimple, local + "simple", "admin", "", 302},
		{pypiSimple, local + "simple", "user", "", 302},
		{pypiSimple, local + "simple", "anonymous", "", 302},
		{pypiSimple, local + "simple", "", "", 401},
		{pypiSimple, pypiBase + "missing/simple", "admin", "", 404},
		{pypiSimpleHead, local + "simple", "admin", "", 302},

		{pypiList, local + "simple/", "admin", "", 200},
		{pypiList, local + "simple/", "user", "", 403},
		{pypiList, local + "simple/", "anonymous", "", 401},
		{pypiList, local + "simple/", "", "", 401},
		{pypiList, local + "simple/", "forged", "", 401},
		{pypiList, pypiBase + "missing/simple/", "admin", "", 404},
		// Another package type's repository is not PyPI's.
		{pypiList, pypiBase + "libs-local/simple/", "admin", "", 404},
		{pypiList, pypiBase + "npm-local/simple/", "admin", "", 404},
		// Remote and virtual PyPI repositories are P9's and P10's.
		{pypiList, pypiBase + "pypi-remote/simple/", "admin", "", 501},
		{pypiList, pypiBase + "pypi-virtual/simple/", "admin", "", 501},
		{pypiListHead, local + "simple/", "admin", "", 200},

		{pypiProject, page, "admin", "", 200},
		{pypiProject, local + "simple/Status_Pkg/", "admin", "", 302},
		{pypiProject, local + "simple/status-pkg", "admin", "", 302},
		{pypiProject, local + "simple/absent/", "admin", "", 404},
		{pypiProject, local + "simple/-x-/", "admin", "", 404},
		{pypiProject, page, "user", "", 403},
		{pypiProject, local + "simple/absent/", "user", "", 403},
		{pypiProject, page, "anonymous", "", 401},
		{pypiProject, page, "", "", 401},
		{pypiProject, page, "expired", "", 401},
		{pypiProject, pypiBase + "pypi-remote/simple/status-pkg/", "admin", "", 501},
		{pypiProjectHead, page, "admin", "", 200},

		{pypiFile, wheel, "admin", "", 200},
		{pypiFile, local + "status-pkg/1.0/absent.whl", "admin", "", 404},
		{pypiFile, local + "status-pkg/1.0/", "admin", "", 404},
		{pypiFile, wheel, "user", "", 403},
		{pypiFile, wheel, "anonymous", "", 401},
		{pypiFile, wheel, "", "", 401},
		{pypiFile, pypiBase + "pypi-virtual/" + pypiWheelPath, "admin", "", 501},
		{pypiFileHead, wheel, "admin", "", 200},
		{pypiFileHead, wheel, "user", "", 403},

		{pypiMetadata, wheel + ".metadata", "admin", "", 200},
		{pypiMetadata, local + "status-pkg/1.0/absent.whl.metadata", "admin", "", 404},
		{pypiMetadata, wheel + ".metadata", "user", "", 403},
		{pypiMetadata, wheel + ".metadata", "anonymous", "", 401},
		{pypiMetadata, wheel + ".metadata", "", "", 401},
		{pypiMetadataHead, wheel + ".metadata", "admin", "", 200},
	}, pypiUploadStatusRows()...)
}

func pypiUploadStatusRows() []statusCase {
	local := pypiBase + "pypi-local"
	body := pypiUploadBody("upload")
	return []statusCase{
		{pypiUpload, local, "admin", body, 200},
		{pypiUploadSlash, local + "/", "admin", body, 200},
		{pypiUpload, local, "user", body, 403},
		{pypiUpload, local, "anonymous", body, 401},
		{pypiUpload, local, "", body, 401},
		{pypiUpload, pypiBase + "pypi-remote", "admin", body, 501},
		{pypiUpload, pypiBase + "pypi-virtual", "admin", body, 501},
		{pypiUpload, local, "admin", "xml", 415},
		{pypiUploadFolder, local + "/folder", "admin", body, 405},
		{pypiUploadFolder, local + "/simple/", "admin", body, 405},
		{pypiPut, local + "/notes.txt", "admin", "notes", 201},
		{pypiPut, local + "/notes.txt", "user", "notes", 403},
		{pypiPut, local + "/packages/notes.txt", "admin", "notes", 400},
		{pypiPut, local + "/simple/notes.txt", "admin", "notes", 400},
	}
}

// pypiUploadBody carries an actual twine-compatible wheel form through the
// status and principal-matrix request body's Content-Type marker.
func pypiUploadBody(name string) string {
	var wheel bytes.Buffer
	zipWriter := zip.NewWriter(&wheel)
	part, _ := zipWriter.Create(name + "-1.0.dist-info/METADATA")
	_, _ = io.WriteString(part, "Metadata-Version: 2.1\nName: "+name+"\nVersion: 1.0\n")
	_ = zipWriter.Close()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	_ = form.SetBoundary("artifactweave-pypi-form")
	_ = form.WriteField(":action", "file_upload")
	_ = form.WriteField("sha256_digest", sha256Hex(wheel.String()))
	content, _ := form.CreateFormFile("content", name+"-1.0-py3-none-any.whl")
	_, _ = content.Write(wheel.Bytes())
	_ = form.Close()
	return "multipart:" + form.Boundary() + "\n" + body.String()
}
