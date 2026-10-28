package server

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func chefCachedDocument(t *testing.T, w *itemWorld, key string) types.Item {
	t.Helper()
	up, err := w.blobs.BeginUpload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = up.Append(
		t.Context(),
		strings.NewReader(`{"name":"private","versions":["private-version"]}`),
	); err != nil {
		t.Fatal(err)
	}
	info, err := up.Commit(t.Context(), blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{"api", "api/v1", "api/v1/cookbooks"} {
		path, name, _ := strings.Cut(folder, "/")
		if folder == "api" {
			path, name = "", "api"
		} else if folder == "api/v1/cookbooks" {
			path, name = "api/v1", "cookbooks"
		}
		if _, err = w.db.Items().
			Put(t.Context(), types.Item{RepoKey: key, Path: path, Name: name, Folder: true}); err != nil {
			t.Fatal(err)
		}
	}
	it, err := w.db.Items().
		Put(t.Context(), types.Item{RepoKey: key, Path: "api/v1/cookbooks", Name: "private", Size: info.Size, Actual: types.Checksums{SHA256: info.Digests.SHA256}})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func chefMetadataPermission(t *testing.T, w *itemWorld, keys ...string) {
	t.Helper()
	targets := map[string]types.Target{}
	for _, key := range keys {
		targets[key] = types.Target{Include: []string{"**"}, Exclude: []string{"**/*.tar.gz"}}
	}
	if err := w.db.Permissions().
		Create(t.Context(), types.Permission{Name: "chef-metadata", Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{
					Users: map[string]types.Actions{
						"user": types.ActionRead | types.ActionWrite | types.ActionDelete | types.ActionManage,
					},
				},
				Targets: targets,
			},
		}}); err != nil {
		t.Fatal(err)
	}
}

func TestAccChefCachedMetadataHasNoGenericDownloadBypass(t *testing.T) {
	reads := 0
	sources := &source.Service{
		Resolve: sourceIdentity,
		HTTP: sourceBackend(
			func(context.Context, source.Request, source.Permit) (source.Result, error) {
				reads++
				return source.Result{}, store.ErrNotFound
			},
		),
	}
	w := newItemWorld(t, false, func(c *Config) { c.RemoteSources = sources })
	keys := []string{"chef-local", "chef-fed", "chef-remote", "chef-virtual"}
	for i, class := range []repo.Class{repo.Local, repo.Federated, repo.Remote, repo.Virtual} {
		c := *repo.Defaults(&repo.Config{Key: keys[i], Class: class, PackageType: repo.Chef, URL: "https://up.example", Repositories: []string{"chef-local"}})
		if err := w.db.Repos().Create(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	chefMetadataPermission(t, w, keys...)
	chefCachedDocument(t, w, "chef-local")
	for _, key := range append(keys, "chef-remote-cache") {
		for _, suffix := range []string{"", ".sha256", "!/secret"} {
			for _, method := range []string{"GET", "HEAD"} {
				wantStatus(
					t,
					w.do(
						method,
						"/artifactory/"+key+"/api/v1/cookbooks/private"+suffix,
						"user",
						"",
					),
					404,
				)
			}
		}
	}
	if reads != 0 {
		t.Fatalf("raw metadata request fetched upstream: %d", reads)
	}
	if err := w.db.Settings().
		Put(t.Context(), types.SettingFolderDownload, types.FolderDownloadSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	got := w.do(
		"GET",
		"/artifactory/api/archive/download/chef-local/api?archiveType=zip&includeChecksumFiles=true",
		"user",
		"",
	)
	wantStatus(t, got, 200)
	files, err := zip.NewReader(bytes.NewReader(got.Body.Bytes()), int64(got.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files.File {
		if strings.Contains(file.Name, "private") {
			t.Fatalf("folder archive leaked %s", file.Name)
		}
	}
	wantStatus(
		t,
		w.do(
			"PUT",
			"/artifactory/other-local/api/v1/cookbooks/private",
			"admin",
			"ordinary generic document",
		),
		201,
	)
	wantStatus(
		t,
		w.do("GET", "/artifactory/other-local/api/v1/cookbooks/private", "admin", ""),
		200,
	)
}

func TestAccChefCachedMetadataCannotEscapeByTransferChecksumOrRestore(t *testing.T) {
	for _, operation := range []string{"copy", "move"} {
		t.Run(operation, func(t *testing.T) {
			w := newItemWorld(t, false)
			c := *repo.Defaults(&repo.Config{Key: "chef-local", Class: repo.Local, PackageType: repo.Chef})
			if err := w.db.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			chefMetadataPermission(t, w, c.Key, "other-local")
			it := chefCachedDocument(t, w, c.Key)
			for _, path := range []string{it.FullPath(), "api"} {
				got := w.do(
					"POST",
					"/artifactory/api/"+operation+"/chef-local/"+path+"?to=other-local/escaped",
					"user",
					"",
				)
				wantStatus(t, got, 200)
				if !strings.Contains(got.Body.String(), "native cookbook endpoint") {
					t.Fatal(got.Body.String())
				}
				destination := "/artifactory/other-local/escaped"
				if path == "api" {
					destination += "/v1/cookbooks/private"
				}
				wantStatus(t, w.do("GET", destination, "user", ""), 404)
			}
			wantStatus(
				t,
				w.do(
					"PUT",
					"/artifactory/other-local/by-checksum",
					"user",
					"",
					"X-Checksum-Deploy",
					"true",
					"X-Checksum-Sha256",
					it.Actual.SHA256,
				),
				404,
			)
			wantStatus(t, w.do("DELETE", "/artifactory/chef-local/"+it.FullPath(), "user", ""), 204)
			endpoint := "/artifactory/api/trash/restore/chef-local/" + it.FullPath()
			for _, target := range []string{"other-local/escaped", "chef-local/renamed"} {
				wantStatus(
					t,
					w.do("POST", endpoint+"?to="+target+"&transaction-size=1", "user", ""),
					400,
				)
			}
			wantStatus(t, w.do("POST", endpoint+"?to=chef-local/"+it.FullPath(), "user", ""), 202)
			wantStatus(t, w.do("GET", "/artifactory/chef-local/"+it.FullPath(), "user", ""), 404)
		})
	}
}
