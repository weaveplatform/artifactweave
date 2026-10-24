// Package fs is the local-disk blob store. Blobs live at
// <root>/sha256/<ab>/<cd>/<hex>; the two shard levels keep directories small
// enough for tools and filesystems that slow down on huge listings. Upload
// sessions live at <root>/_uploads/<id>/ on the same filesystem, so committing
// is an fsync and a rename rather than a copy.
package fs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

const (
	blobDir   = "sha256"
	uploadDir = "_uploads"
	dataFile  = "data"
	stateFile = "state"
)

// Operating system calls are variables so tests can inject failures on the
// paths a healthy disk never takes.
var (
	osRename    = os.Rename
	publishFile = publish
	osMkdirAll  = os.MkdirAll
	osOpenFile  = os.OpenFile
	osRemove    = os.Remove
	osRemoveAll = os.RemoveAll
	osWriteFile = os.WriteFile
	fsync       = (*os.File).Sync
	syncDir     = syncDirectory
)

// Store is a blob.Store on a local directory.
type Store struct {
	root string
}

var _ blob.Store = (*Store)(nil)

// New opens (creating if needed) a store rooted at dir.
func New(dir string) (*Store, error) {
	abs := filepath.Clean(dir)
	for _, d := range []string{filepath.Join(abs, blobDir), filepath.Join(abs, uploadDir)} {
		if err := osMkdirAll(d, 0o750); err != nil {
			return nil, fmt.Errorf("blob/fs: create %s: %w", d, err)
		}
	}
	return &Store{root: abs}, nil
}

func (s *Store) blobPath(sha string) string {
	return filepath.Join(s.root, blobDir, sha[0:2], sha[2:4], sha)
}

func (s *Store) uploadPath(id string) string {
	return filepath.Join(s.root, uploadDir, id)
}

// state is what survives between requests of one upload session. Size is
// stored next to the hasher state so a crash between writing data and
// writing state is detectable: the data file is then longer than Size and
// the session rehashes it on resume.
type state struct {
	Size     int64  `json:"size"`
	Hasher   []byte `json:"hasher"`
	Expected string `json:"expected,omitempty"`
}

type upload struct {
	s        *Store
	id       string
	dir      string
	size     int64
	h        *blob.Hasher
	expected string
}

// BeginUpload starts a session.
func (s *Store) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("blob/fs: begin: %w", err)
	}
	o, err := blob.ResolveOptions(opts...)
	if err != nil {
		return nil, err
	}
	id, err := blob.NewUploadID()
	if err != nil {
		return nil, err
	}
	u := &upload{
		s:        s,
		id:       id,
		dir:      s.uploadPath(id),
		h:        blob.NewHasher(),
		expected: o.ExpectedSHA256,
	}
	if err := osMkdirAll(u.dir, 0o750); err != nil {
		return nil, fmt.Errorf("blob/fs: begin: %w", err)
	}
	f, err := osOpenFile(filepath.Join(u.dir, dataFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		_ = os.RemoveAll(u.dir)
		return nil, fmt.Errorf("blob/fs: begin: %w", err)
	}
	_ = f.Close()
	if err := u.saveState(); err != nil {
		_ = os.RemoveAll(u.dir)
		return nil, err
	}
	return u, nil
}

// ResumeUpload reopens a session.
func (s *Store) ResumeUpload(ctx context.Context, id string) (blob.Upload, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("blob/fs: resume: %w", err)
	}
	if !blob.ValidUploadID(id) {
		return nil, fmt.Errorf("%w: %q", blob.ErrUploadUnknown, id)
	}
	u := &upload{s: s, id: id, dir: s.uploadPath(id), h: blob.NewHasher()}
	raw, err := os.ReadFile(filepath.Join(u.dir, stateFile))
	if errors.Is(err, iofs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", blob.ErrUploadUnknown, id)
	}
	if err != nil {
		return nil, fmt.Errorf("blob/fs: resume %s: %w", id, err)
	}
	var st state
	if err := json.Unmarshal(raw, &st); err != nil {
		st = state{Size: -1}
	}
	u.expected = st.Expected
	fi, err := os.Stat(filepath.Join(u.dir, dataFile))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", blob.ErrUploadUnknown, id, err)
	}
	if st.Size == fi.Size() && u.h.UnmarshalBinary(st.Hasher) == nil {
		u.size = st.Size
		return u, nil
	}
	// The saved state does not describe the data on disk (a crash between the
	// two writes, or a damaged state file): the data is authoritative, so
	// rehash it rather than lose the session.
	if err := u.rehash(); err != nil {
		return nil, err
	}
	return u, nil
}

func (u *upload) rehash() error {
	f, err := os.Open(filepath.Join(u.dir, dataFile))
	if err != nil {
		return fmt.Errorf("blob/fs: rehash %s: %w", u.id, err)
	}
	defer func() { _ = f.Close() }()
	u.h = blob.NewHasher()
	n, err := io.Copy(u.h, f)
	if err != nil {
		return fmt.Errorf("blob/fs: rehash %s: %w", u.id, err)
	}
	u.size = n
	return u.saveState()
}

func (u *upload) saveState() error {
	hs, err := u.h.MarshalBinary()
	if err != nil {
		return err
	}
	// Marshalling a struct of an int, a byte slice and a string cannot fail.
	raw, _ := json.Marshal(state{Size: u.size, Hasher: hs, Expected: u.expected})
	tmp := filepath.Join(u.dir, stateFile+".tmp")
	if err := osWriteFile(tmp, raw, 0o640); err != nil {
		return fmt.Errorf("blob/fs: state: %w", err)
	}
	if err := osRename(tmp, filepath.Join(u.dir, stateFile)); err != nil {
		return fmt.Errorf("blob/fs: state: %w", err)
	}
	return nil
}

func (u *upload) ID() string  { return u.id }
func (u *upload) Size() int64 { return u.size }

func (u *upload) live() error {
	if _, err := os.Stat(filepath.Join(u.dir, stateFile)); err != nil {
		return fmt.Errorf("%w: %s", blob.ErrUploadUnknown, u.id)
	}
	return nil
}

// Append adds r to the session.
func (u *upload) Append(ctx context.Context, r io.Reader) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("blob/fs: append: %w", err)
	}
	if err := u.live(); err != nil {
		return 0, err
	}
	f, err := osOpenFile(filepath.Join(u.dir, dataFile), os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return 0, fmt.Errorf("blob/fs: append: %w", err)
	}
	n, cerr := blob.CopyHashed(f, r, u.h)
	u.size += n
	if err := f.Close(); err != nil && cerr == nil {
		cerr = fmt.Errorf("blob/fs: append: %w", err)
	}
	// Save even after a failed copy: the counted bytes are on disk and hashed,
	// and the client resumes from Size.
	if err := u.saveState(); err != nil && cerr == nil {
		cerr = err
	}
	return n, cerr
}

// Commit finishes the session.
func (u *upload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if err := ctx.Err(); err != nil {
		return blob.Info{}, fmt.Errorf("blob/fs: commit: %w", err)
	}
	if err := blob.ValidateDigests(want); err != nil {
		return blob.Info{}, err
	}
	if err := u.live(); err != nil {
		return blob.Info{}, err
	}
	if want.SHA256 == "" {
		want.SHA256 = u.expected
	}
	got := u.h.Sum()
	if err := blob.Verify(want, got); err != nil {
		_ = u.Abort(ctx)
		return blob.Info{}, err
	}
	info := blob.Info{Size: u.size, Digests: got}
	target := u.s.blobPath(got.SHA256)
	if _, err := os.Stat(target); err == nil {
		return info, u.discard()
	}
	data := filepath.Join(u.dir, dataFile)
	if err := syncFile(data); err != nil {
		return blob.Info{}, err
	}
	if err := osMkdirAll(filepath.Dir(target), 0o750); err != nil {
		return blob.Info{}, fmt.Errorf("blob/fs: commit: %w", err)
	}
	if err := publishFile(data, target); err != nil {
		// Another session may already have published this immutable blob.
		// Windows publication never replaces that winner: replacement can
		// temporarily deny readers access even after their commit succeeded.
		if _, serr := os.Stat(target); serr != nil {
			return blob.Info{}, fmt.Errorf("blob/fs: commit: %w", err)
		}
	}
	if err := syncDir(filepath.Dir(target)); err != nil {
		return blob.Info{}, err
	}
	return info, u.discard()
}

func (u *upload) discard() error {
	if err := osRemoveAll(u.dir); err != nil {
		return fmt.Errorf("blob/fs: discard upload %s: %w", u.id, err)
	}
	return nil
}

// Abort discards the session.
func (u *upload) Abort(context.Context) error { return u.discard() }

func syncFile(path string) error {
	f, err := osOpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("blob/fs: sync: %w", err)
	}
	serr := fsync(f)
	_ = f.Close()
	if serr != nil {
		return fmt.Errorf("blob/fs: sync: %w", serr)
	}
	return nil
}

// syncDirectory makes the rename durable. Windows cannot fsync a directory
// handle and its rename is already durable when it returns, so it is skipped.
func syncDirectory(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("blob/fs: sync dir: %w", err)
	}
	serr := fsync(d)
	_ = d.Close()
	if serr != nil {
		return fmt.Errorf("blob/fs: sync dir: %w", serr)
	}
	return nil
}

// Open returns the blob for random access.
func (s *Store) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	info, err := s.Stat(ctx, sha)
	if err != nil {
		return nil, blob.Info{}, err
	}
	f, err := openBlobFile(ctx, s.blobPath(sha))
	if err != nil {
		return nil, blob.Info{}, notFound(sha, err)
	}
	return f, info, nil
}

// Stat returns a blob's size and digest.
func (s *Store) Stat(ctx context.Context, sha string) (blob.Info, error) {
	if err := ctx.Err(); err != nil {
		return blob.Info{}, fmt.Errorf("blob/fs: stat: %w", err)
	}
	if err := blob.ValidateSHA256(sha); err != nil {
		return blob.Info{}, err
	}
	fi, err := os.Stat(s.blobPath(sha))
	if err != nil {
		return blob.Info{}, notFound(sha, err)
	}
	return blob.Info{Size: fi.Size(), Digests: blob.Digests{SHA256: sha}}, nil
}

// Exists reports whether the blob is stored.
func (s *Store) Exists(ctx context.Context, sha string) (bool, error) {
	_, err := s.Stat(ctx, sha)
	if errors.Is(err, blob.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Delete removes the blob.
func (s *Store) Delete(ctx context.Context, sha string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("blob/fs: delete: %w", err)
	}
	if err := blob.ValidateSHA256(sha); err != nil {
		return err
	}
	if err := osRemove(s.blobPath(sha)); err != nil && !errors.Is(err, iofs.ErrNotExist) {
		return fmt.Errorf("blob/fs: delete %s: %w", sha, err)
	}
	return nil
}

// Walk visits every stored blob.
func (s *Store) Walk(ctx context.Context, fn func(blob.Info) error) error {
	err := filepath.WalkDir(
		filepath.Join(s.root, blobDir),
		func(path string, d iofs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if d.IsDir() || blob.ValidateSHA256(d.Name()) != nil {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				// Deleted between listing and stat: not stored any more.
				return nil //nolint:nilerr // a vanished blob is skipped, not an error
			}
			return fn(blob.Info{Size: fi.Size(), Digests: blob.Digests{SHA256: d.Name()}})
		},
	)
	if err != nil {
		return fmt.Errorf("blob/fs: walk: %w", err)
	}
	return nil
}

func notFound(sha string, err error) error {
	if errors.Is(err, iofs.ErrNotExist) {
		return fmt.Errorf("%w: %s", blob.ErrNotFound, sha)
	}
	return fmt.Errorf("blob/fs: %s: %w", sha, err)
}
