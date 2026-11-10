package server

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/blob"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitDebianSnapshotRejectsUnboundMetadata(t *testing.T) {
	for _, tc := range []struct {
		name          string
		index         string
		size          int64
		digest        string
		expectedError string
	}{
		{name: "missing declared index", size: 4, digest: strings.Repeat("a", 64), expectedError: "snapshot index"},
		{name: "oversized declaration", size: debianIndexLimit + 1, digest: strings.Repeat("a", 64), expectedError: "exceeds limit"},
		{name: "digest mismatch", index: "bad!", size: 4, digest: strings.Repeat("a", 64), expectedError: "digest"},
		{name: "malformed index", index: "bad!", size: 4, digest: fmt.Sprintf("%x", sha256.Sum256([]byte("bad!"))), expectedError: "Packages index"},
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
			p := kernel.Principal{Name: "admin", Admin: true}
			acc, err := itemkit.AccessFor(t.Context(), authorizerOver(w.db), p, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = newArtifactoryService(Config{Store: w.db, Blobs: w.blobs}).
				debianSnapshotFiles(t.Context(), w.db, c, "stable", p, acc)
			if err == nil || !strings.Contains(err.Error(), tc.expectedError) {
				t.Fatalf("unbound snapshot metadata: %v, want %q", err, tc.expectedError)
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
	p := kernel.Principal{Name: "admin", Admin: true}
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
