package debian_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
)

func TestAccDebianSnapshotReadGuards(t *testing.T) {
	const root = "/artifactory/api/deb/deb-snapshot-target/snapshots/"
	for _, tc := range []struct {
		name, targetType, sourceType, sourceKey, rel, caller, tag string
		marker, markerFile, sourceProperty                        bool
		status                                                    int
	}{
		{name: "missing target", rel: "pool/main/a/alpha/alpha_1.0_amd64.deb", marker: true, sourceProperty: true, sourceType: string(repo.Debian), sourceKey: "deb-snapshot-source", caller: "admin", status: http.StatusNotFound},
		{name: "wrong target format", targetType: string(repo.Generic), rel: "pool/main/a/alpha/alpha_1.0_amd64.deb", marker: true, sourceProperty: true, sourceType: string(repo.Debian), sourceKey: "deb-snapshot-source", caller: "admin", status: http.StatusNotFound},
		{name: "missing marker", targetType: string(repo.Debian), rel: "pool/main/a/alpha/alpha_1.0_amd64.deb", sourceProperty: true, sourceType: string(repo.Debian), sourceKey: "deb-snapshot-source", caller: "admin", status: http.StatusNotFound},
		{name: "missing source property", targetType: string(repo.Debian), rel: "pool/main/a/alpha/alpha_1.0_amd64.deb", marker: true, sourceType: string(repo.Debian), sourceKey: "deb-snapshot-source", caller: "admin", status: http.StatusNotFound},
		{name: "missing source", targetType: string(repo.Debian), rel: "pool/main/a/alpha/alpha_1.0_amd64.deb", marker: true, sourceProperty: true, sourceKey: "missing-source", caller: "admin", status: http.StatusNotFound},
		{name: "wrong source format", targetType: string(repo.Debian), rel: "pool/main/a/alpha/alpha_1.0_amd64.deb", marker: true, sourceProperty: true, sourceType: string(repo.Generic), sourceKey: "deb-snapshot-source", caller: "admin", status: http.StatusNotFound},
		{name: "missing path", targetType: string(repo.Debian), rel: "", marker: true, sourceProperty: true, sourceType: string(repo.Debian), sourceKey: "deb-snapshot-source", caller: "admin", status: http.StatusNotFound},
		{name: "noncanonical path", targetType: string(repo.Debian), rel: "dists//Release", marker: true, sourceProperty: true, sourceType: string(repo.Debian), sourceKey: "deb-snapshot-source", caller: "admin", status: http.StatusNotFound},
		{name: "unsupported path", targetType: string(repo.Debian), rel: "other/file.bin", marker: true, sourceProperty: true, sourceType: string(repo.Debian), sourceKey: "deb-snapshot-source", caller: "admin", status: http.StatusNotFound},
		{name: "missing metadata", targetType: string(repo.Debian), rel: "dists/stable/Release", marker: true, sourceProperty: true, sourceType: string(repo.Debian), sourceKey: "deb-snapshot-source", caller: "admin", status: http.StatusNotFound},
		{name: "marker is not a folder", targetType: string(repo.Debian), rel: "dists/stable/Release", marker: true, markerFile: true, sourceProperty: true, sourceType: string(repo.Debian), sourceKey: "deb-snapshot-source", caller: "admin", status: http.StatusNotFound},
		{name: "invalid tag", targetType: string(repo.Debian), rel: "dists/stable/Release", tag: "bad!", marker: true, sourceProperty: true, sourceType: string(repo.Debian), sourceKey: "deb-snapshot-source", caller: "admin", status: http.StatusNotFound},
		{name: "anonymous denied", targetType: string(repo.Debian), rel: "dists/stable/Release", marker: true, sourceProperty: true, sourceType: string(repo.Debian), sourceKey: "deb-snapshot-source", status: http.StatusUnauthorized},
		{name: "permission required", targetType: string(repo.Debian), rel: "pool/main/a/alpha/alpha_1.0_amd64.deb", marker: true, sourceProperty: true, sourceType: string(repo.Debian), sourceKey: "deb-snapshot-source", caller: "user", status: http.StatusForbidden},
		{name: "package redirect", targetType: string(repo.Debian), rel: "pool/main/a/alpha/alpha_1.0_amd64.deb", marker: true, sourceProperty: true, sourceType: string(repo.Debian), sourceKey: "deb-snapshot-source", caller: "admin", status: http.StatusFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t, false)
			if tc.targetType != "" {
				if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
					Key: "deb-snapshot-target", Class: repo.Local, PackageType: repo.PackageType(tc.targetType),
				})); err != nil {
					t.Fatal(err)
				}
			}
			if tc.sourceType != "" {
				if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
					Key: tc.sourceKey, Class: repo.Local, PackageType: repo.PackageType(tc.sourceType),
				})); err != nil {
					t.Fatal(err)
				}
			}
			if tc.marker {
				it, err := w.db.Items().Put(t.Context(), model.Item{
					RepoKey: "deb-snapshot-target", Name: "snapshots/first", Folder: !tc.markerFile,
					Created: time.Now().UTC(), CreatedBy: "admin",
				})
				if err != nil {
					t.Fatal(err)
				}
				if tc.sourceProperty {
					if err := w.db.Items().SetProperties(t.Context(), it.ID,
						model.Properties{
							"deb.snapshot.source": {tc.sourceKey},
						}, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
			}
			tag := tc.tag
			if tag == "" {
				tag = "first"
			}
			rec := w.do(http.MethodGet, root+tag+"/"+tc.rel, tc.caller, "")
			if rec.Code != tc.status {
				t.Fatalf("snapshot read: %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if tc.status == http.StatusFound && rec.Header().Get("Location") !=
				"/artifactory/deb-snapshot-source/"+tc.rel {
				t.Fatalf("package redirect escaped source: %q", rec.Header().Get("Location"))
			}
		})
	}
}

func TestAccDebianSnapshotRefusesUnavailableOrCorruptIndex(t *testing.T) {
	w := newItemWorld(t, false)
	for _, key := range []string{"deb-snapshot-source", "deb-snapshot-target"} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
			Key: key, Class: repo.Local, PackageType: repo.Debian,
		})); err != nil {
			t.Fatal(err)
		}
	}
	marker, err := w.db.Items().Put(t.Context(), model.Item{
		RepoKey: "deb-snapshot-target", Name: "snapshots/first", Folder: true,
		Created: time.Now().UTC(), CreatedBy: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Items().SetProperties(t.Context(), marker.ID,
		model.Properties{
			"deb.snapshot.source": {"deb-snapshot-source"},
		}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	const rel = "dists/stable/main/binary-amd64/Packages.gz"
	it, err := w.db.Items().Put(t.Context(), model.Item{
		RepoKey: "deb-snapshot-target", Name: "snapshots/first/" + rel,
		Actual: kernel.Checksums{SHA256: strings.Repeat("a", 64)}, Size: 9,
		Created: time.Now().UTC(), CreatedBy: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	url := "/artifactory/api/deb/deb-snapshot-target/snapshots/first/" + rel
	if rec := w.do(http.MethodGet, url, "admin", ""); rec.Code == http.StatusOK {
		t.Fatalf("missing index blob was served: %s", rec.Body)
	}
	upload, err := w.blobs.BeginUpload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Append(t.Context(), strings.NewReader("invalid gzip")); err != nil {
		t.Fatal(err)
	}
	info, err := upload.Commit(t.Context(), blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	it.Actual = kernel.Checksums{SHA256: info.Digests.SHA256}
	it.Size = info.Size
	if _, err := w.db.Items().Put(t.Context(), it); err != nil {
		t.Fatal(err)
	}
	if rec := w.do(http.MethodGet, url, "admin", ""); rec.Code == http.StatusOK {
		t.Fatalf("corrupt index was served: %s", rec.Body)
	}
	packages := []byte("Package: alpha\nVersion: 1.0\nArchitecture: amd64\n" +
		"Filename: pool/alpha.deb\nSize: 4\nSHA256: " + strings.Repeat("a", 64) + "\n\n")
	compressed, err := debian.GzipIndex(packages)
	if err != nil {
		t.Fatal(err)
	}
	upload, err = w.blobs.BeginUpload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upload.Append(t.Context(), strings.NewReader(string(compressed))); err != nil {
		t.Fatal(err)
	}
	info, err = upload.Commit(t.Context(), blob.Digests{})
	if err != nil {
		t.Fatal(err)
	}
	it.Actual = kernel.Checksums{SHA256: info.Digests.SHA256}
	it.Size = info.Size
	if _, err := w.db.Items().Put(t.Context(), it); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(t.Context(), model.Permission{
		Name: "snapshot-metadata-only",
		Resources: map[api.ResourceType]model.Resource{
			api.ResourceArtifact: {
				Actions: model.Grants{Users: map[string]api.Actions{"user": api.ActionRead}},
				Targets: map[string]model.Target{
					"deb-snapshot-target": {Include: []string{"snapshots/first/" + rel}},
					"deb-snapshot-source": {Include: []string{rel}},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if rec := w.do(http.MethodGet, url, "user", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("snapshot index disclosed an unreadable package: %d %s", rec.Code, rec.Body)
	}
	if rec := w.do(http.MethodGet, url, "admin", ""); rec.Code != http.StatusOK {
		t.Fatalf("admin could not read valid snapshot index: %d %s", rec.Code, rec.Body)
	}
}
