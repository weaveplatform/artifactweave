package fs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/blobtest"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestContract(t *testing.T) {
	blobtest.Run(t, func(t *testing.T) blob.Store { return newStore(t) }, blobtest.Options{})
}

// A successful commit makes the blob immediately readable, even while other
// sessions finish publishing identical content. The contract's concurrent
// commit case reads only after every writer has finished.
func TestConcurrentCommitAndRead(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	other, err := New(s.root)
	if err != nil {
		t.Fatal(err)
	}
	stores := []*Store{s, other}
	const writers = 8
	for round := range 30 {
		body := blobtest.Content(byte(round), 4096)
		uploads := make([]blob.Upload, writers)
		for i := range uploads {
			uploads[i] = begin(t, stores[i%len(stores)], body)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, u := range uploads {
			wg.Go(func() {
				<-start
				info, err := u.Commit(ctx, blob.Digests{})
				if err != nil {
					t.Errorf("round %d commit: %v", round, err)
					return
				}
				for range 10 {
					r, _, err := stores[(i+1)%len(stores)].Open(ctx, info.Digests.SHA256)
					if err != nil {
						t.Errorf("round %d open after commit: %v", round, err)
						return
					}
					got, err := io.ReadAll(r)
					_ = r.Close()
					if err != nil || !bytes.Equal(got, body) {
						t.Errorf("round %d read after commit: %v; content matches: %v",
							round, err, bytes.Equal(got, body))
						return
					}
				}
			})
		}
		close(start)
		wg.Wait()
		if t.Failed() {
			return
		}
	}
}

var errInjected = errors.New("injected")

// swap replaces a package variable for the duration of a test.
func swap[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func begin(t *testing.T, s *Store, body []byte) blob.Upload {
	t.Helper()
	u, err := s.BeginUpload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 0 {
		if _, err := u.Append(context.Background(), bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	return u
}

func TestNewFailures(t *testing.T) {
	swap(t, &osMkdirAll, func(string, os.FileMode) error { return errInjected })
	if _, err := New(t.TempDir()); !errors.Is(err, errInjected) {
		t.Fatalf("mkdir failure: %v", err)
	}
}

func TestBeginFailures(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	t.Run("mkdir", func(t *testing.T) {
		swap(t, &osMkdirAll, func(string, os.FileMode) error { return errInjected })
		if _, err := s.BeginUpload(ctx); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("create data", func(t *testing.T) {
		swap(
			t,
			&osOpenFile,
			func(string, int, os.FileMode) (*os.File, error) { return nil, errInjected },
		)
		if _, err := s.BeginUpload(ctx); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("save state", func(t *testing.T) {
		swap(t, &osRename, func(string, string) error { return errInjected })
		if _, err := s.BeginUpload(ctx); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	entries, _ := os.ReadDir(filepath.Join(s.root, uploadDir))
	if len(entries) != 0 {
		t.Fatalf("failed begins left %d sessions", len(entries))
	}
}

func TestResumeRepairsState(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	body := blobtest.Content(1, 4096)
	u := begin(t, s, body[:1000])
	dir := s.uploadPath(u.ID())
	// Simulate a crash after data was written but before state was saved:
	// more bytes on disk than the state records.
	f, err := os.OpenFile(filepath.Join(dir, dataFile), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(body[1000:2000]); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	r, err := s.ResumeUpload(ctx, u.ID())
	if err != nil || r.Size() != 2000 {
		t.Fatalf("resume after crash: size %d %v", r.Size(), err)
	}
	if _, err := r.Append(ctx, bytes.NewReader(body[2000:])); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(ctx, blobtest.Digest(body)); err != nil {
		t.Fatalf("commit after repair: %v", err)
	}

	// A damaged state file is repaired the same way.
	u = begin(t, s, body[:500])
	if err := os.WriteFile(
		filepath.Join(s.uploadPath(u.ID()), stateFile),
		[]byte("{"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	r, err = s.ResumeUpload(ctx, u.ID())
	if err != nil || r.Size() != 500 {
		t.Fatalf("resume with damaged state: %d %v", r.Size(), err)
	}
}

func TestResumeFailures(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	u := begin(t, s, []byte("abc"))
	dir := s.uploadPath(u.ID())

	t.Run("data missing", func(t *testing.T) {
		if err := os.Rename(filepath.Join(dir, dataFile), filepath.Join(dir, "moved")); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Rename(filepath.Join(dir, "moved"), filepath.Join(dir, dataFile)) }()
		if _, err := s.ResumeUpload(ctx, u.ID()); !errors.Is(err, blob.ErrUploadUnknown) {
			t.Fatal(err)
		}
	})
	t.Run("state unreadable", func(t *testing.T) {
		state := filepath.Join(dir, stateFile)
		if err := os.Rename(state, state+".bak"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(state, 0o750); err != nil {
			t.Fatal(err)
		}
		defer func() {
			_ = os.Remove(state)
			_ = os.Rename(state+".bak", state)
		}()
		if _, err := s.ResumeUpload(
			ctx,
			u.ID(),
		); err == nil ||
			errors.Is(err, blob.ErrUploadUnknown) {
			t.Fatalf("unreadable state should be an I/O error: %v", err)
		}
	})
	t.Run("rehash fails", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(dir, stateFile), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		swap(t, &osRename, func(string, string) error { return errInjected })
		if _, err := s.ResumeUpload(ctx, u.ID()); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
}

type failingReader struct{ after int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.after <= 0 {
		return 0, errInjected
	}
	n := min(len(p), r.after)
	for i := range p[:n] {
		p[i] = 'a'
	}
	r.after -= n
	return n, nil
}

func TestAppendFailures(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	t.Run("reader fails midway, counted bytes kept", func(t *testing.T) {
		u := begin(t, s, nil)
		n, err := u.Append(ctx, &failingReader{after: 10})
		if !errors.Is(err, errInjected) || n != 10 || u.Size() != 10 {
			t.Fatalf("n %d size %d err %v", n, u.Size(), err)
		}
		r, err := s.ResumeUpload(ctx, u.ID())
		if err != nil || r.Size() != 10 {
			t.Fatalf("resume after partial append: %d %v", r.Size(), err)
		}
		info, err := r.Commit(ctx, blobtest.Digest(bytes.Repeat([]byte("a"), 10)))
		if err != nil || info.Size != 10 {
			t.Fatalf("commit partial: %v", err)
		}
	})
	t.Run("open data fails", func(t *testing.T) {
		u := begin(t, s, nil)
		swap(
			t,
			&osOpenFile,
			func(string, int, os.FileMode) (*os.File, error) { return nil, errInjected },
		)
		if _, err := u.Append(ctx, bytes.NewReader([]byte("x"))); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("save state fails", func(t *testing.T) {
		u := begin(t, s, nil)
		swap(t, &osRename, func(string, string) error { return errInjected })
		if _, err := u.Append(ctx, bytes.NewReader([]byte("x"))); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
}

func TestCommitFailures(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	body := []byte("commit failure body")

	t.Run("sync open fails", func(t *testing.T) {
		u := begin(t, s, body)
		swap(
			t,
			&osOpenFile,
			func(string, int, os.FileMode) (*os.File, error) { return nil, errInjected },
		)
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("mkdir shard fails", func(t *testing.T) {
		u := begin(t, s, append(body, '1'))
		swap(t, &osMkdirAll, func(string, os.FileMode) error { return errInjected })
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("rename fails and target absent", func(t *testing.T) {
		u := begin(t, s, append(body, '2'))
		swap(t, &publishFile, func(string, string) error { return errInjected })
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("rename fails but a concurrent commit won", func(t *testing.T) {
		content := append(body, '3')
		u := begin(t, s, content)
		target := s.blobPath(blobtest.Digest(content).SHA256)
		swap(t, &publishFile, func(string, string) error {
			if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
				return err
			}
			if err := os.WriteFile(target, content, 0o600); err != nil {
				return err
			}
			return errInjected
		})
		if _, err := u.Commit(ctx, blob.Digests{}); err != nil {
			t.Fatalf("lost race must succeed: %v", err)
		}
	})
	t.Run("sync dir fails", func(t *testing.T) {
		u := begin(t, s, append(body, '4'))
		swap(t, &syncDir, func(string) error { return errInjected })
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
}

func TestSyncDirectory(t *testing.T) {
	if err := syncDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("directories are not synced on Windows, so a missing one is not detected")
	}
	if err := syncDirectory(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("syncing a missing directory must fail")
	}
}

func TestDeleteFailure(t *testing.T) {
	s := newStore(t)
	info := blobtest.Put(context.Background(), t, s, []byte("delete me"))
	swap(t, &osRemove, func(string) error { return errInjected })
	if err := s.Delete(context.Background(), info.Digests.SHA256); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
}

func TestStatAndOpenIOErrors(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	sha := blobtest.Digest([]byte("dir instead of file")).SHA256
	// A directory where the blob should be: Stat succeeds on it, but it is
	// not readable content, and Walk must not report it.
	if err := os.MkdirAll(s.blobPath(sha), 0o750); err != nil {
		t.Fatal(err)
	}
	rc, _, err := s.Open(ctx, sha)
	if err == nil {
		_, err = io.ReadAll(rc)
		_ = rc.Close()
	}
	if err == nil {
		t.Fatal("reading a directory as a blob must fail")
	}
	count := 0
	if err := s.Walk(ctx, func(blob.Info) error { count++; return nil }); err != nil || count != 0 {
		t.Fatalf("walk over a directory-shaped entry: %d %v", count, err)
	}
	if err := notFound(
		sha,
		errInjected,
	); errors.Is(err, blob.ErrNotFound) ||
		!errors.Is(err, errInjected) {
		t.Fatalf("non-ENOENT errors must pass through: %v", err)
	}
}

func TestNewRejectsUnusableRoot(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(file); err == nil {
		t.Fatal("a file is not a usable root")
	}
}

func TestMoreFailures(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	t.Run("state write fails", func(t *testing.T) {
		u := begin(t, s, nil)
		swap(t, &osWriteFile, func(string, []byte, os.FileMode) error { return errInjected })
		if _, err := u.Append(ctx, bytes.NewReader([]byte("x"))); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("discard fails", func(t *testing.T) {
		u := begin(t, s, nil)
		swap(t, &osRemoveAll, func(string) error { return errInjected })
		if err := u.Abort(ctx); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("fsync fails", func(t *testing.T) {
		u := begin(t, s, []byte("fsync"))
		swap(t, &fsync, func(*os.File) error { return errInjected })
		if _, err := u.Commit(ctx, blob.Digests{}); !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
		if err := syncDirectory(t.TempDir()); err != nil && !errors.Is(err, errInjected) {
			t.Fatal(err)
		}
	})
	t.Run("rehash of unreadable data", func(t *testing.T) {
		u := begin(t, s, []byte("abc"))
		dir := s.uploadPath(u.ID())
		data := filepath.Join(dir, dataFile)
		// A directory where the data file should be: Stat succeeds, the
		// recorded size disagrees, and reading it for the rehash fails.
		if err := os.Remove(data); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(data, 0o750); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ResumeUpload(ctx, u.ID()); err == nil {
			t.Fatal("rehashing a directory must fail")
		}
	})
	t.Run("walk over a missing tree", func(t *testing.T) {
		s := newStore(t)
		if err := os.RemoveAll(filepath.Join(s.root, blobDir)); err != nil {
			t.Fatal(err)
		}
		if err := s.Walk(ctx, func(blob.Info) error { return nil }); err == nil {
			t.Fatal("walking a missing tree must fail")
		}
	})
}

func TestUnreadableFiles(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	if filepath.Separator == '\\' {
		t.Skip("permission bits do not block reads on Windows")
	}
	ctx := context.Background()
	s := newStore(t)
	info := blobtest.Put(ctx, t, s, []byte("locked"))
	path := s.blobPath(info.Digests.SHA256)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, _, err := s.Open(
		ctx,
		info.Digests.SHA256,
	); err == nil ||
		errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("an unreadable blob is an I/O error, not missing: %v", err)
	}

	u := begin(t, s, []byte("abc"))
	data := filepath.Join(s.uploadPath(u.ID()), dataFile)
	if err := os.WriteFile(
		filepath.Join(s.uploadPath(u.ID()), stateFile),
		[]byte("{"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(data, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(data, 0o600) })
	if _, err := s.ResumeUpload(ctx, u.ID()); err == nil {
		t.Fatal("rehash of an unreadable data file must fail")
	}
}
