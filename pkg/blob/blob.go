// Package blob is the content-addressed binary store behind every repository
// (decision record 0004). Blobs are keyed by their lowercase hex SHA-256; the
// SHA-1 and MD5 that Artifactory clients also send are computed during the
// upload and returned by Commit so the caller can record them in the item
// index, which is where checksum deploy and checksum search look them up.
//
// Uploads are sessions rather than single calls because two clients need to
// append across HTTP requests: Docker chunked uploads (PATCH then PUT) and the
// JFrog CLI's multipart upload. A session is identified by an opaque ID that
// ResumeUpload accepts in a later request. One writer per session is assumed;
// the HTTP layer serialises requests for the same upload.
//
// Backends: blob/fs (local disk) and blob/s3 (S3-compatible object storage).
// Both are held to the same behaviour by blob/blobtest.
package blob

import (
	"context"
	"crypto/md5" //nolint:gosec // MD5 is a wire checksum clients send, not a security primitive
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // SHA-1 is a wire checksum clients send, not a security primitive
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"regexp"
	"time"
)

// Sentinel errors. Backends wrap them, so test with errors.Is.
var (
	// ErrNotFound reports a blob that is not stored.
	ErrNotFound = errors.New("blob: not found")
	// ErrDigestMismatch reports content whose digest differs from the one the
	// client declared. The upload is aborted; nothing is committed.
	ErrDigestMismatch = errors.New("blob: digest mismatch")
	// ErrUploadUnknown reports an upload ID that does not name a live session:
	// never issued, already committed or aborted, or malformed.
	ErrUploadUnknown = errors.New("blob: unknown upload")
	// ErrInvalidDigest reports a digest that is not lowercase hex of the right
	// length for its algorithm.
	ErrInvalidDigest = errors.New("blob: invalid digest")
)

// Digests are lowercase hex checksums. In Commit's want argument an empty
// field means "not declared"; in a returned Info an empty SHA1 or MD5 means
// the backend does not keep it (the item index does).
type Digests struct {
	SHA256 string
	SHA1   string
	MD5    string
}

// Info describes a stored blob.
type Info struct {
	Size    int64
	Digests Digests
}

// Store is the content-addressed blob store.
type Store interface {
	// BeginUpload starts an upload session.
	BeginUpload(ctx context.Context, opts ...UploadOption) (Upload, error)
	// ResumeUpload reopens a session started earlier, possibly by another
	// request. It returns ErrUploadUnknown for an ID that is not live.
	ResumeUpload(ctx context.Context, id string) (Upload, error)
	// Open returns the content for random access, so HTTP handlers can serve
	// ranges with http.ServeContent. The caller closes it. Info carries Size
	// and Digests.SHA256; SHA1 and MD5 may be empty.
	Open(ctx context.Context, sha256 string) (io.ReadSeekCloser, Info, error)
	// Stat returns a blob's Info without opening it.
	Stat(ctx context.Context, sha256 string) (Info, error)
	// Exists reports whether a blob is stored.
	Exists(ctx context.Context, sha256 string) (bool, error)
	// Delete removes a blob. Deleting a missing blob succeeds, because garbage
	// collection and a concurrent delete can race and both mean "gone".
	Delete(ctx context.Context, sha256 string) error
	// Walk calls fn for every stored blob in an unspecified order and stops at
	// the first error fn returns, which Walk returns. Upload sessions are not
	// visited. It exists for garbage collection and storage accounting.
	Walk(ctx context.Context, fn func(Info) error) error
}

// Upload is one upload session.
type Upload interface {
	// ID names the session for ResumeUpload.
	ID() string
	// Size is the number of bytes appended so far.
	Size() int64
	// Append writes r to the end of the upload and returns how many bytes it
	// stored. On error the bytes counted are stored and hashed, so the session
	// stays consistent and the client may continue from Size.
	Append(ctx context.Context, r io.Reader) (int64, error)
	// Commit finishes the upload. Every non-empty field of want is compared
	// with the computed digest; a mismatch aborts the session and returns
	// ErrDigestMismatch. Content that is already stored is not written again
	// and the session is discarded, so committing the same bytes twice, or
	// from two sessions at once, succeeds both times.
	Commit(ctx context.Context, want Digests) (Info, error)
	// Abort discards the session. Aborting a session that is already gone
	// succeeds.
	Abort(ctx context.Context) error
}

// Presigner is implemented by backends that can hand a client a time-limited
// URL for a blob, used when a repository sets downloadRedirect.
type Presigner interface {
	PresignGet(ctx context.Context, sha256 string, ttl time.Duration) (string, error)
}

// PartSizer is implemented by backends that write an upload in parts of a
// fixed size. Bytes short of a whole part stay pending in the upload's
// session, which is saved after every Append, so a caller that appends in
// multiples of PartSize keeps that saved state small. The mirror sizes its
// chunks by it.
type PartSizer interface {
	PartSize() int64
}

// UploadOption configures BeginUpload.
type UploadOption func(*UploadOptions)

// UploadOptions is the resolved form of the options, exported for backends.
type UploadOptions struct {
	// ExpectedSHA256 is the digest the client declared before sending the
	// body (Docker's monolithic PUT ?digest=, X-Checksum-Sha256). Backends
	// that cannot rename use it to write straight to the final key.
	ExpectedSHA256 string
}

// WithExpectedSHA256 declares the content digest up front. Commit still
// verifies it.
func WithExpectedSHA256(sha256 string) UploadOption {
	return func(o *UploadOptions) { o.ExpectedSHA256 = sha256 }
}

// ResolveOptions applies opts and validates them.
func ResolveOptions(opts ...UploadOption) (UploadOptions, error) {
	var o UploadOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.ExpectedSHA256 != "" {
		if err := ValidateSHA256(o.ExpectedSHA256); err != nil {
			return UploadOptions{}, err
		}
	}
	return o, nil
}

var (
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sha1Pattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	md5Pattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	uploadPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// ValidateSHA256 rejects anything but 64 lowercase hex characters. Backends
// build file and object names from the digest, so this is also the guard
// against path traversal.
func ValidateSHA256(s string) error {
	if !sha256Pattern.MatchString(s) {
		return fmt.Errorf("%w: sha256 %q", ErrInvalidDigest, s)
	}
	return nil
}

// ValidateDigests checks every non-empty field of d.
func ValidateDigests(d Digests) error {
	if d.SHA256 != "" {
		if err := ValidateSHA256(d.SHA256); err != nil {
			return err
		}
	}
	if d.SHA1 != "" && !sha1Pattern.MatchString(d.SHA1) {
		return fmt.Errorf("%w: sha1 %q", ErrInvalidDigest, d.SHA1)
	}
	if d.MD5 != "" && !md5Pattern.MatchString(d.MD5) {
		return fmt.Errorf("%w: md5 %q", ErrInvalidDigest, d.MD5)
	}
	return nil
}

// ValidUploadID reports whether id has the shape NewUploadID produces.
// Backends use upload IDs in file and object names.
func ValidUploadID(id string) bool { return uploadPattern.MatchString(id) }

// Hasher computes all three digests in one pass and can save and restore its
// state, so a resumed upload continues hashing instead of re-reading data.
type Hasher struct {
	sha256 hash.Hash
	sha1   hash.Hash
	md5    hash.Hash
}

// NewHasher returns a Hasher at the start of the stream.
func NewHasher() *Hasher {
	return &Hasher{
		sha256: sha256.New(),
		sha1:   sha1.New(), //nolint:gosec // wire checksum
		md5:    md5.New(),  //nolint:gosec // wire checksum
	}
}

// Write feeds p to every digest. It never fails.
func (h *Hasher) Write(p []byte) (int, error) {
	_, _ = h.sha256.Write(p)
	_, _ = h.sha1.Write(p)
	_, _ = h.md5.Write(p)
	return len(p), nil
}

// Sum returns the digests of everything written so far.
func (h *Hasher) Sum() Digests {
	return Digests{
		SHA256: hex.EncodeToString(h.sha256.Sum(nil)),
		SHA1:   hex.EncodeToString(h.sha1.Sum(nil)),
		MD5:    hex.EncodeToString(h.md5.Sum(nil)),
	}
}

// ErrHasherState reports a saved hasher state that cannot be restored.
var ErrHasherState = errors.New("blob: invalid hasher state")

// MarshalBinary saves the state of all three digests.
func (h *Hasher) MarshalBinary() ([]byte, error) {
	var out []byte
	for _, d := range []hash.Hash{h.sha256, h.sha1, h.md5} {
		// The standard library digests all implement BinaryMarshaler; the
		// assertion cannot fail for the types NewHasher creates.
		b, err := d.(encoding.BinaryMarshaler).MarshalBinary()
		if err != nil {
			return nil, fmt.Errorf("blob: save hasher: %w", err)
		}
		out = append(out, byte(len(b)>>8), byte(len(b)))
		out = append(out, b...)
	}
	return out, nil
}

// UnmarshalBinary restores a state saved by MarshalBinary.
func (h *Hasher) UnmarshalBinary(b []byte) error {
	fresh := NewHasher()
	for _, d := range []hash.Hash{fresh.sha256, fresh.sha1, fresh.md5} {
		if len(b) < 2 {
			return ErrHasherState
		}
		n := int(b[0])<<8 | int(b[1])
		b = b[2:]
		if len(b) < n {
			return ErrHasherState
		}
		if err := d.(encoding.BinaryUnmarshaler).UnmarshalBinary(b[:n]); err != nil {
			return fmt.Errorf("%w: %w", ErrHasherState, err)
		}
		b = b[n:]
	}
	if len(b) != 0 {
		return ErrHasherState
	}
	*h = *fresh
	return nil
}

// Verify compares every non-empty field of want with got.
func Verify(want, got Digests) error {
	pairs := []struct{ name, want, got string }{
		{"sha256", want.SHA256, got.SHA256},
		{"sha1", want.SHA1, got.SHA1},
		{"md5", want.MD5, got.MD5},
	}
	for _, p := range pairs {
		if p.want != "" && p.want != p.got {
			return fmt.Errorf(
				"%w: %s declared %s, computed %s",
				ErrDigestMismatch,
				p.name,
				p.want,
				p.got,
			)
		}
	}
	return nil
}

// CopyHashed copies r to w in chunks, feeding h only with bytes w accepted,
// so a short or failed write leaves the hash state matching what is stored.
// It returns the number of bytes stored.
func CopyHashed(w io.Writer, r io.Reader, h *Hasher) (int64, error) {
	buf := make([]byte, 256<<10)
	var total int64
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			wn, werr := w.Write(buf[:n])
			_, _ = h.Write(buf[:wn])
			total += int64(wn)
			if werr != nil {
				return total, fmt.Errorf("blob: write: %w", werr)
			}
			if wn < n {
				return total, fmt.Errorf("blob: write: %w", io.ErrShortWrite)
			}
		}
		if errors.Is(rerr, io.EOF) {
			return total, nil
		}
		if rerr != nil {
			return total, fmt.Errorf("blob: read: %w", rerr)
		}
	}
}

// randReader supplies upload IDs. It is a variable so tests can make it fail.
var randReader io.Reader = rand.Reader

// NewUploadID returns a random 32-character hex upload ID. IDs are
// unguessable because a session accepts appends from whoever presents it.
func NewUploadID() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(randReader, b[:]); err != nil {
		return "", fmt.Errorf("blob: upload id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
