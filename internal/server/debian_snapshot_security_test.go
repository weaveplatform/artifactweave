package server

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format/debian"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
				it, err := w.db.Items().Put(t.Context(), types.Item{
					RepoKey: "deb-snapshot-target", Name: "snapshots/first", Folder: !tc.markerFile,
					Created: time.Now().UTC(), CreatedBy: "admin",
				})
				if err != nil {
					t.Fatal(err)
				}
				if tc.sourceProperty {
					if err := w.db.Items().SetProperties(t.Context(), it.ID,
						types.Properties{
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
	marker, err := w.db.Items().Put(t.Context(), types.Item{
		RepoKey: "deb-snapshot-target", Name: "snapshots/first", Folder: true,
		Created: time.Now().UTC(), CreatedBy: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.Items().SetProperties(t.Context(), marker.ID,
		types.Properties{
			"deb.snapshot.source": {"deb-snapshot-source"},
		}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	const rel = "dists/stable/main/binary-amd64/Packages.gz"
	it, err := w.db.Items().Put(t.Context(), types.Item{
		RepoKey: "deb-snapshot-target", Name: "snapshots/first/" + rel,
		Actual: types.Checksums{SHA256: strings.Repeat("a", 64)}, Size: 9,
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
	it.Actual = types.Checksums{SHA256: info.Digests.SHA256}
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
	it.Actual = types.Checksums{SHA256: info.Digests.SHA256}
	it.Size = info.Size
	if _, err := w.db.Items().Put(t.Context(), it); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "snapshot-metadata-only",
		Resources: map[types.ResourceType]types.Resource{
			types.ResourceArtifact: {
				Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
				Targets: map[string]types.Target{
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

func TestUnitDebianSnapshotRejectsUnboundMetadata(t *testing.T) {
	for _, tc := range []struct {
		name      string
		index     string
		size      int64
		digest    string
		wantError string
	}{
		{name: "missing declared index", size: 4, digest: strings.Repeat("a", 64), wantError: "snapshot index"},
		{name: "oversized declaration", size: debianIndexLimit + 1, digest: strings.Repeat("a", 64), wantError: "exceeds limit"},
		{name: "digest mismatch", index: "bad!", size: 4, digest: strings.Repeat("a", 64), wantError: "digest"},
		{name: "malformed index", index: "bad!", size: 4, digest: fmt.Sprintf("%x", sha256.Sum256([]byte("bad!"))), wantError: "Packages index"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newItemWorld(t, false)
			c := *repo.Defaults(&repo.Config{
				Key: "deb-source", Class: repo.Local, PackageType: repo.Debian,
			})
			if err := w.db.Repos().Create(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			put := func(path, body string) {
				t.Helper()
				upload, err := w.blobs.BeginUpload(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := upload.Append(t.Context(), strings.NewReader(body)); err != nil {
					t.Fatal(err)
				}
				info, err := upload.Commit(t.Context(), blob.Digests{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := w.db.Items().Put(t.Context(), types.Item{
					RepoKey: c.Key, Name: path,
					Actual: types.Checksums{SHA256: info.Digests.SHA256}, Size: info.Size,
					Created: time.Now().UTC(), CreatedBy: "admin",
				}); err != nil {
					t.Fatal(err)
				}
			}
			const index = "dists/stable/main/binary-amd64/Packages"
			release := fmt.Sprintf(
				"Suite: stable\nArchitectures: amd64\nComponents: main\nSHA256:\n %s %d main/binary-amd64/Packages\n",
				tc.digest,
				tc.size,
			)
			put("dists/stable/Release", release)
			if tc.index != "" {
				put(index, tc.index)
			}
			p := auth.Principal{Name: "admin", Admin: true}
			acc, err := accessFor(t.Context(), w.db, p)
			if err != nil {
				t.Fatal(err)
			}
			_, err = (artifactoryService{cfg: Config{Store: w.db, Blobs: w.blobs}}).
				debianSnapshotFiles(t.Context(), w.db, c, "stable", p, acc)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("unbound snapshot metadata: %v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestAccDebianSnapshotClaimCannotBeReusedByAnotherSource(t *testing.T) {
	w := newItemWorld(t, false)
	for _, key := range []string{"deb-source-one", "deb-source-two", "deb-target"} {
		if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
			Key: key, Class: repo.Local, PackageType: repo.Debian,
		})); err != nil {
			t.Fatal(err)
		}
	}
	p := auth.Principal{Name: "admin", Admin: true}
	job := debianSnapshotRequest{
		Source: "deb-source-one", Target: "deb-target", Tag: "first", Distribution: "stable",
	}
	if err := claimDebianSnapshot(t.Context(), w.db, job, p); err != nil {
		t.Fatal(err)
	}
	body := `{"tag":"first","targetRepo":"deb-target","distribution":"stable"}`
	url := "/artifactory/api/deb/deb-source-two/snapshot"
	if rec := w.do(http.MethodPost, url, "admin", body,
		"Content-Type", "application/json"); rec.Code != http.StatusConflict {
		t.Fatalf("another source claimed the snapshot tag: %d %s", rec.Code, rec.Body)
	}
	url = "/artifactory/api/deb/deb-source-one/snapshot"
	if rec := w.do(http.MethodPost, url, "admin", body,
		"Content-Type", "application/json"); rec.Code == http.StatusOK {
		t.Fatalf("missing source Release completed the snapshot: %d %s", rec.Code, rec.Body)
	}
	marker := w.item("deb-target", false, snapshotRoot(job))
	props, err := w.db.Items().Properties(t.Context(), marker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(props["deb.snapshot.source"]) != 0 ||
		len(props["deb.snapshot.pending.source"]) != 1 ||
		props["deb.snapshot.pending.source"][0] != job.Source {
		t.Fatalf("failed snapshot changed its owner: %v", props)
	}
}
