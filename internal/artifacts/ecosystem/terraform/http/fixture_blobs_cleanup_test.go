package terraformhttp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
)

func TestAccRemoteFixtureBlobCleanupWaitsForWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		store, err := fs.New(root)
		if err != nil {
			t.Fatal(err)
		}
		reached, release := make(chan struct{}), make(chan struct{})
		blobs := &fixtureBlobs{Store: heldCommitStore{Store: store, reached: reached, release: release}}
		upload, err := blobs.BeginUpload(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := upload.Append(t.Context(), strings.NewReader("fixture worker")); err != nil {
			t.Fatal(err)
		}
		worker := make(chan error, 1)
		go func() {
			_, err := upload.Commit(t.Context(), blob.Digests{})
			worker <- err
		}()
		<-reached
		closed := make(chan struct{})
		go func() {
			blobs.close()
			close(closed)
		}()
		// The only outstanding filesystem mutation is held at Commit. A
		// fixture must not remove its directory while that work can resume.
		synctest.Wait()
		finishedEarly := false
		select {
		case <-closed:
			finishedEarly = true
		default:
		}
		close(release)
		if err := <-worker; err != nil {
			t.Fatal(err)
		}
		<-closed
		if finishedEarly {
			t.Fatal("fixture cleanup returned while a blob commit was still in flight")
		}
		committed := 0
		if err := store.Walk(t.Context(), func(blob.Info) error { committed++; return nil }); err != nil {
			t.Fatal(err)
		}
		if committed != 1 {
			t.Fatalf("the held worker committed %d real blobs, want one", committed)
		}
		if err := os.RemoveAll(root); err != nil {
			t.Fatal(err)
		}
		checks := map[string]func() error{
			"begin":  func() error { _, err := blobs.BeginUpload(t.Context()); return err },
			"resume": func() error { _, err := blobs.ResumeUpload(t.Context(), upload.ID()); return err },
			"append": func() error { _, err := upload.Append(t.Context(), strings.NewReader("late")); return err },
			"commit": func() error { _, err := upload.Commit(t.Context(), blob.Digests{}); return err },
			"abort":  func() error { return upload.Abort(t.Context()) },
			"delete": func() error { return blobs.Delete(t.Context(), strings.Repeat("a", 64)) },
		}
		for name, run := range checks {
			if err := run(); !errors.Is(err, errFixtureBlobsClosed) {
				t.Fatalf("%s after fixture cleanup: %v, want closed fence", name, err)
			}
		}
		if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("detached work recreated the removed blob directory: %v", err)
		}
	})
}

type heldCommitStore struct {
	blob.Store
	reached chan struct{}
	release chan struct{}
}

func (s heldCommitStore) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	upload, err := s.Store.BeginUpload(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return heldCommitUpload{Upload: upload, reached: s.reached, release: s.release}, nil
}

type heldCommitUpload struct {
	blob.Upload
	reached chan struct{}
	release chan struct{}
}

func (u heldCommitUpload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	close(u.reached)
	<-u.release
	return u.Upload.Commit(ctx, want)
}
