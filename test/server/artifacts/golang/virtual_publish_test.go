package golang_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestAccGoVirtualPublishDDRAndReadback(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Federated} {
		t.Run(string(class), func(t *testing.T) {
			w := goVirtualWorld(t)
			local, err := w.Runtime.Store.Repos().Get(t.Context(), "local")
			if err != nil {
				t.Fatal(err)
			}
			local.Class = class
			if err = w.Runtime.Store.Repos().Update(t.Context(), local); err != nil {
				t.Fatal(err)
			}
			v, err := w.Runtime.Store.Repos().Get(t.Context(), "virtual")
			if err != nil {
				t.Fatal(err)
			}
			v.DefaultDeploymentRepo = "local"
			if err = w.Runtime.Store.Repos().Update(t.Context(), v); err != nil {
				t.Fatal(err)
			}
			if err = w.Runtime.Store.Permissions().
				Create(t.Context(), accesstest.Permission{Name: "ddr-publish", Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {Actions: accesstest.Grants{Users: map[string]api.Actions{"reader": api.ActionRead | api.ActionWrite}}, Targets: map[string]accesstest.Target{"local": {Include: []string{"example.com/Team/Publish/**"}}}}}}); err != nil {
				t.Fatal(err)
			}
			const m = "example.com/Team/Publish"
			files := virtualModule(t, m, "v1.0.0", "published")
			for _, ext := range []string{"zip", "mod", "info"} {
				path := m + "/@v/v1.0.0." + ext
				res := w.Request("PUT", "/artifactory/api/go/virtual/"+path+";go.version=v1.0.0;build.name=go-virtual", "reader", string(files["v1.0.0."+ext]))
				var answer struct {
					Repo        string `json:"repo"`
					DownloadURI string `json:"downloadUri"`
				}
				if err = json.Unmarshal(res.Body.Bytes(), &answer); err != nil {
					t.Fatal(err, res.Body.String())
				}
				if res.Code != 201 || answer.Repo != "local" || !strings.Contains(answer.DownloadURI, "/artifactory/local/"+path) {
					t.Fatal(res.Code, res.Body.String())
				}
				it, getErr := w.Runtime.Store.Items().Get(t.Context(), "local", false, path)
				if getErr != nil {
					t.Fatal(getErr)
				}
				if res.Header().Get("X-Checksum-Sha1") != it.Actual.SHA1 || res.Header().Get("X-Checksum-Sha256") != it.Actual.SHA256 ||
					res.Header().Get("X-Checksum-Md5") != it.Actual.MD5 {
					t.Fatal("checksum headers", res.Header())
				}
				props, propErr := w.Runtime.Store.Items().Properties(t.Context(), it.ID)
				if propErr != nil || len(props["build.name"]) != 1 || props["build.name"][0] != "go-virtual" {
					t.Fatal(props, propErr)
				}
			}
			for _, path := range []string{"/@v/list", "/@latest", "/@v/v1.0.0.info", "/@v/v1.0.0.mod", "/@v/v1.0.0.zip"} {
				res := w.Request("GET", "/artifactory/api/go/virtual/"+m+path, "reader", "")
				if res.Code != 200 {
					t.Fatal(path, res.Code, res.Body.String())
				}
			}
			for _, ext := range []string{"zip", "mod"} {
				path := "/artifactory/api/go/virtual/" + m + "/@v/v1.0.0." + ext
				res := w.Request("PUT", path, "reader", string(files["v1.0.0."+ext]))
				if res.Code != 201 {
					t.Fatal("identical republish", res.Code, res.Body.String())
				}
				res = w.Request("PUT", path, "admin", "changed immutable content")
				if res.Code != 409 {
					t.Fatal("changed republish", res.Code, res.Body.String())
				}
			}
			deniedPath := "/artifactory/api/go/virtual/example.com/elsewhere/@v/v1.0.0.mod"
			if res := w.Request("PUT", deniedPath, "reader", "module example.com/elsewhere"); res.Code != 403 {
				t.Fatal("DDR path grant", res.Code, res.Body.String())
			}
			if _, err = w.Runtime.Store.Items().Get(t.Context(), "virtual", false, m+"/@v/v1.0.0.mod"); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("virtual owned the upload", err)
			}
		})
	}
}

func TestAccGoVirtualPublishNoDDR(t *testing.T) {
	w := goVirtualWorld(t)
	for _, ext := range []string{"zip", "mod", "info"} {
		res := w.Request("PUT", "/artifactory/api/go/virtual/example.com/lib/@v/v1.0.0."+ext, "admin", "body")
		if res.Code != 405 || res.Header().Get("Allow") != "GET" ||
			res.Body.String() != "No local repository was configured as local deployment repository for the (virtual) virtual repository.\n" {
			t.Fatal(ext, res.Code, res.Header(), res.Body.String())
		}
	}
}
