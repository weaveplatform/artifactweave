// Package gcs is the Google Cloud Storage blob store (decision 0027). Blobs
// live at <prefix>sha256/<ab>/<hex>.
//
// GCS objects cannot be appended to, and the Go SDK cannot resume a
// resumable-upload session from persisted state, so an upload session is
// built from compose instead:
//
//   - Bytes accumulate in a pending buffer of at most PartSize. A blob that
//     never fills one part is written in a single request at commit.
//   - Each full part becomes its own object under <prefix>_uploads/<id>/,
//     written with a must-not-exist precondition. Part names are never
//     reused across parts, so a retry of the same part either lands or finds
//     its own earlier copy.
//   - GCS composes at most 32 sources per request, so before a 33rd source
//     the session composes what it has into a fold object and continues from
//     that. There is no limit on how many components a composite object has.
//   - At commit the content key is known; the session composes everything
//     straight onto it with a must-not-exist precondition and
//     DeleteSourceObjects, so the bytes are never copied. A 412 there means
//     the blob is already stored, which the contract treats as success.
//   - Composite objects carry a CRC32C but no MD5, so the session tracks the
//     CRC32C of everything appended and checks the result against it; the
//     blob.Hasher state still provides SHA-256, SHA-1 and MD5.
//
// The session (part objects, their generations, sizes and checksums) is
// persisted through pkg/blob/session after every append.
package gcs

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/cdn"
)

// Attrs are the object attributes the store uses.
type Attrs struct {
	Name       string
	Size       int64
	CRC32C     uint32
	Generation int64
}

// Source names one compose source at a pinned generation, so a source
// replaced since it was recorded fails the compose instead of being used.
type Source struct {
	Name       string
	Generation int64
}

// API is the subset of Cloud Storage the store uses, bound to one bucket,
// so tests can substitute a fake that models GCS and injects failures.
// NewAPI returns the implementation over the official SDK.
type API interface {
	// Write stores data as a new object, checked against crc on the server.
	// It returns ErrPrecondition when the name already exists.
	Write(ctx context.Context, name string, data []byte, crc uint32) (Attrs, error)
	// Attrs returns an object's attributes, or blob.ErrNotFound.
	Attrs(ctx context.Context, name string) (Attrs, error)
	// Read opens the object from offset to the end, or blob.ErrNotFound.
	Read(ctx context.Context, name string, offset int64) (io.ReadCloser, error)
	// Compose concatenates srcs into a new object dst, optionally deleting
	// the sources. It returns ErrPrecondition when dst already exists.
	Compose(ctx context.Context, dst string, srcs []Source, deleteSources bool) (Attrs, error)
	// Delete removes an object, only at generation when it is non-zero. It
	// returns blob.ErrNotFound when the object is missing.
	Delete(ctx context.Context, name string, generation int64) error
	// List calls fn for every object whose name starts with prefix.
	List(ctx context.Context, prefix string, fn func(Attrs) error) error
	// SignedURL returns a V4 signed GET URL for name.
	SignedURL(name string, o SignedURLOptions) (string, error)
}

// SignedURLOptions describe a signed GET URL: when it expires and the
// response overrides it carries, empty when not wanted.
type SignedURLOptions struct {
	Expires            time.Time
	ContentDisposition string
	ContentType        string
}

// ErrPrecondition reports a write or compose onto a name that exists.
var ErrPrecondition = errors.New("blob/gcs: precondition failed")

// ErrConfig reports an unusable store configuration.
var ErrConfig = errors.New("blob/gcs: invalid configuration")

const (
	// DefaultPartSize matches the SDK's own chunk size and its parallel
	// upload's part size: large enough that a 10 GiB blob costs about 660
	// Class A operations, small enough to hold in a session row.
	DefaultPartSize = 16 << 20
	// partAlign keeps parts a multiple of the resumable-upload granularity,
	// which the SDK uses for writes at or above its chunk size.
	partAlign = 256 << 10
	// maxSources is GCS's limit on sources per compose request.
	maxSources = 32
	// maxSignedTTL is the longest validity a V4 signed URL may have.
	maxSignedTTL = 7 * 24 * time.Hour
)

// minPartSize is the smallest part size accepted: below it a session pays a
// Class A operation for every few hundred KiB. A variable so tests can
// exercise many parts with small blobs.
var minPartSize int64 = 1 << 20

// castagnoli is the CRC32C table GCS checksums use.
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Options configures New.
type Options struct {
	// Prefix is prepended to every object name; a trailing slash is added
	// if missing.
	Prefix string
	// PartSize is the part size; zero means DefaultPartSize. It must be a
	// multiple of 256 KiB and at least 1 MiB.
	PartSize int64
	// Sessions holds upload sessions between requests; session.InUploads
	// with a backend name such as "gcs:<store name>" keeps them durable.
	Sessions session.Store
	// Presign makes the store sign storage URLs (blob.Signer). Set it from
	// ClientConfig.CanSign: signing needs a key, or credentials that name a
	// service account and may call IAM signBlob.
	Presign bool
	// SignsOverNetwork marks every signature as an IAM signBlob request, so
	// the store caches the URLs it signs (decision 0029). Set it from
	// ClientConfig.SignsOverNetwork.
	SignsOverNetwork bool
	// CDN signs CDN URLs when set, such as a cdn.CloudCDN in front of the
	// bucket (decision 0029). It must reach every key under Prefix.
	CDN cdn.Signer
}

// Store is a blob.Store on a GCS bucket.
type Store struct {
	api        API
	prefix     string
	partSize   int64
	sessions   session.Store
	canPresign bool
	// signed caches signed URLs when signing is a network call; nil when
	// it is local and costs nothing to repeat.
	signed *urlCache
	// cdn signs CDN URLs with a local HMAC, so they are never cached.
	cdn cdn.Signer
}

var (
	_ blob.Store     = (*Store)(nil)
	_ blob.Signer    = (*Store)(nil)
	_ blob.PartSizer = (*Store)(nil)
)

// New returns a store over api.
func New(api API, o Options) (*Store, error) {
	if api == nil || o.Sessions == nil {
		return nil, fmt.Errorf("%w: client and session store are required", ErrConfig)
	}
	if o.PartSize == 0 {
		o.PartSize = DefaultPartSize
	}
	if o.PartSize < minPartSize || o.PartSize%partAlign != 0 {
		return nil, fmt.Errorf("%w: part size %d must be a multiple of %d and at least %d",
			ErrConfig, o.PartSize, partAlign, minPartSize)
	}
	prefix := strings.TrimPrefix(o.Prefix, "/")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if o.CDN != nil {
		if err := o.CDN.Covers(prefix); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrConfig, err)
		}
	}
	s := &Store{
		api:        api,
		prefix:     prefix,
		partSize:   o.PartSize,
		sessions:   o.Sessions,
		canPresign: o.Presign,
		cdn:        o.CDN,
	}
	if o.Presign && o.SignsOverNetwork {
		s.signed = newURLCache(signedURLCacheSize)
	}
	return s, nil
}

// PartSize returns the size of the parts an upload is written in.
func (s *Store) PartSize() int64 { return s.partSize }

func (s *Store) blobName(sha string) string { return s.prefix + "sha256/" + sha[:2] + "/" + sha }

// ObjectKey is the object name a blob is stored under, prefix included, for
// signers that address the bucket directly, such as a CDN. sha must be a
// valid digest (blob.ValidateSHA256).
func (s *Store) ObjectKey(sha string) string { return s.blobName(sha) }
func (s *Store) uploadDir(id string) string  { return s.prefix + "_uploads/" + id + "/" }

// object is one part or fold object recorded in a session.
type object struct {
	Name       string `json:"name"`
	Generation int64  `json:"generation"`
	Size       int64  `json:"size"`
	CRC32C     uint32 `json:"crc32c"`
}

// parts is the gcs backend's part of a session. The json tags are the
// storage format.
type parts struct {
	// Fold is the composite of every part folded so far, nil before the
	// first fold.
	Fold *object `json:"fold,omitempty"`
	// Parts are the parts written since the last fold, in order.
	Parts []object `json:"parts,omitempty"`
	// Next numbers the next part or fold object; names are never reused.
	Next int `json:"next"`
	// CRC32C covers every byte handed to a part so far, in order.
	CRC32C uint32 `json:"crc32c"`
}

// sources returns the fold and the parts as compose sources, in order.
func (p *parts) sources() []Source {
	var out []Source
	if p.Fold != nil {
		out = append(out, Source{Name: p.Fold.Name, Generation: p.Fold.Generation})
	}
	for _, o := range p.Parts {
		out = append(out, Source{Name: o.Name, Generation: o.Generation})
	}
	return out
}

// size is the byte count of the fold and the parts.
func (p *parts) size() int64 {
	var n int64
	if p.Fold != nil {
		n = p.Fold.Size
	}
	for _, o := range p.Parts {
		n += o.Size
	}
	return n
}

type upload struct {
	s  *Store
	ss *session.Session[parts]
}

// BeginUpload starts a session.
func (s *Store) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	ss, err := session.Begin[parts](ctx, s.sessions, opts...)
	if err != nil {
		return nil, fmt.Errorf("blob/gcs: %w", err)
	}
	return &upload{s: s, ss: ss}, nil
}

// ResumeUpload reopens a session.
func (s *Store) ResumeUpload(ctx context.Context, id string) (blob.Upload, error) {
	ss, err := session.Resume[parts](ctx, s.sessions, id)
	if err != nil {
		return nil, fmt.Errorf("blob/gcs: %w", err)
	}
	return &upload{s: s, ss: ss}, nil
}

func (u *upload) ID() string  { return u.ss.ID }
func (u *upload) Size() int64 { return u.ss.Size }

// Append buffers r and writes every full part.
func (u *upload) Append(ctx context.Context, r io.Reader) (int64, error) {
	n, err := u.ss.Append(ctx, r, u.s.partSize, u.flushPart)
	if err != nil {
		return n, fmt.Errorf("blob/gcs: append: %w", err)
	}
	return n, nil
}

// flushPart writes data as the next part object, folding first when the
// session already has as many sources as one compose accepts. Folding
// before the write, rather than after it, keeps the rule that a part is
// recorded only when its flush succeeds: a part that is written but not
// recorded would be written again under the same name and adopted.
func (u *upload) flushPart(ctx context.Context, data []byte) error {
	p := &u.ss.Data
	if len(p.sources()) >= maxSources {
		if err := u.fold(ctx); err != nil {
			return err
		}
	}
	name := fmt.Sprintf("%sp-%08d", u.s.uploadDir(u.ss.ID), p.Next)
	crc := crc32.Checksum(data, castagnoli)
	attrs, err := u.s.writeOnce(ctx, name, data, crc)
	if err != nil {
		return err
	}
	p.Parts = append(
		p.Parts,
		object{Name: name, Generation: attrs.Generation, Size: attrs.Size, CRC32C: crc},
	)
	p.Next++
	p.CRC32C = crc32.Update(p.CRC32C, castagnoli, data)
	return nil
}

// writeOnce writes data under name with a must-not-exist precondition. A
// precondition failure means an earlier attempt at the same part already
// landed, possibly from a request whose response was lost; it is adopted
// when its size and CRC32C match and refused otherwise.
func (s *Store) writeOnce(
	ctx context.Context,
	name string,
	data []byte,
	crc uint32,
) (Attrs, error) {
	attrs, err := s.api.Write(ctx, name, data, crc)
	if err == nil {
		return attrs, nil
	}
	if !errors.Is(err, ErrPrecondition) {
		return Attrs{}, fmt.Errorf("blob/gcs: write %s: %w", name, err)
	}
	return s.adopt(ctx, name, int64(len(data)), crc)
}

// adopt returns the attributes of an existing object when it holds the
// expected bytes.
func (s *Store) adopt(ctx context.Context, name string, size int64, crc uint32) (Attrs, error) {
	attrs, err := s.api.Attrs(ctx, name)
	if err != nil {
		return Attrs{}, fmt.Errorf("blob/gcs: check existing %s: %w", name, err)
	}
	if attrs.Size != size || attrs.CRC32C != crc {
		return Attrs{}, fmt.Errorf(
			"blob/gcs: %s exists with other content (%d bytes, crc32c %08x): %w",
			name,
			attrs.Size,
			attrs.CRC32C,
			ErrPrecondition,
		)
	}
	return attrs, nil
}

// fold composes the fold and the parts into one new fold object and saves
// the session. A fold that already exists from an earlier attempt whose
// sources are gone is adopted the same way a part is.
func (u *upload) fold(ctx context.Context) error {
	p := &u.ss.Data
	name := fmt.Sprintf("%sc-%08d", u.s.uploadDir(u.ss.ID), p.Next)
	size := p.size()
	attrs, err := u.s.api.Compose(ctx, name, p.sources(), true)
	if err != nil {
		if !errors.Is(err, ErrPrecondition) && !errors.Is(err, blob.ErrNotFound) {
			return fmt.Errorf("blob/gcs: fold into %s: %w", name, err)
		}
		if attrs, err = u.s.adopt(ctx, name, size, p.CRC32C); err != nil {
			return err
		}
	}
	p.Fold = &object{Name: name, Generation: attrs.Generation, Size: size, CRC32C: p.CRC32C}
	p.Parts = nil
	p.Next++
	if err := u.ss.Save(ctx); err != nil {
		return fmt.Errorf("blob/gcs: %w", err)
	}
	return nil
}

// Commit finishes the session.
func (u *upload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if err := ctx.Err(); err != nil {
		return blob.Info{}, fmt.Errorf("blob/gcs: commit: %w", err)
	}
	if err := blob.ValidateDigests(want); err != nil {
		return blob.Info{}, err //nolint:wrapcheck // already a blob sentinel
	}
	if err := u.ss.Live(ctx); err != nil {
		return blob.Info{}, fmt.Errorf("blob/gcs: %w", err)
	}
	got, err := u.ss.Verify(want)
	if err != nil {
		_ = u.Abort(ctx)
		return blob.Info{}, err //nolint:wrapcheck // already a blob sentinel
	}
	info := blob.Info{Size: u.ss.Size, Digests: got}
	target := u.s.blobName(got.SHA256)
	if _, err := u.s.api.Attrs(ctx, target); err == nil {
		return info, u.finish(ctx)
	} else if !errors.Is(err, blob.ErrNotFound) {
		return blob.Info{}, fmt.Errorf("blob/gcs: check %s: %w", got.SHA256, err)
	}
	p := &u.ss.Data
	if len(p.sources()) == 0 {
		crc := crc32.Checksum(u.ss.Pending, castagnoli)
		if _, err := u.s.api.Write(
			ctx,
			target,
			u.ss.Pending,
			crc,
		); err != nil &&
			!errors.Is(err, ErrPrecondition) {
			return blob.Info{}, fmt.Errorf("blob/gcs: write %s: %w", got.SHA256, err)
		}
		return info, u.finish(ctx)
	}
	if err := u.ss.Flush(ctx, u.flushPart); err != nil {
		return blob.Info{}, fmt.Errorf("blob/gcs: commit: %w", err)
	}
	if err := u.ss.Save(ctx); err != nil {
		return blob.Info{}, fmt.Errorf("blob/gcs: %w", err)
	}
	attrs, err := u.s.api.Compose(ctx, target, p.sources(), true)
	switch {
	case errors.Is(err, ErrPrecondition):
		// Another session stored the same content since the check above.
		return info, u.finish(ctx)
	case err != nil:
		return blob.Info{}, fmt.Errorf("blob/gcs: compose %s: %w", got.SHA256, err)
	}
	if attrs.Size != u.ss.Size || attrs.CRC32C != p.CRC32C {
		// Never leave a blob under a content address it does not match. The
		// generation pins the delete to the object this compose created.
		_ = u.s.api.Delete(ctx, target, attrs.Generation)
		return blob.Info{}, fmt.Errorf(
			"blob/gcs: composed %s has %d bytes, crc32c %08x; want %d, %08x",
			got.SHA256,
			attrs.Size,
			attrs.CRC32C,
			u.ss.Size,
			p.CRC32C,
		)
	}
	return info, u.finish(ctx)
}

// finish removes the session's leftover objects and forgets the session.
func (u *upload) finish(ctx context.Context) error {
	if err := u.s.removeAll(ctx, u.s.uploadDir(u.ss.ID)); err != nil {
		return err
	}
	if err := u.ss.Forget(ctx); err != nil {
		return fmt.Errorf("blob/gcs: %w", err)
	}
	return nil
}

// removeAll deletes every object under prefix. After a successful compose
// the sources are already gone; deleting by listing also covers emulators
// that ignore DeleteSourceObjects and parts written but never recorded.
func (s *Store) removeAll(ctx context.Context, prefix string) error {
	var names []string
	if err := s.api.List(ctx, prefix, func(a Attrs) error {
		names = append(names, a.Name)
		return nil
	}); err != nil {
		return fmt.Errorf("blob/gcs: list %s: %w", prefix, err)
	}
	for _, name := range names {
		if err := s.api.Delete(ctx, name, 0); err != nil && !errors.Is(err, blob.ErrNotFound) {
			return fmt.Errorf("blob/gcs: delete %s: %w", name, err)
		}
	}
	return nil
}

// Abort discards the session and its objects.
func (u *upload) Abort(ctx context.Context) error {
	return u.finish(ctx)
}

// Stat returns a blob's size and digest.
func (s *Store) Stat(ctx context.Context, sha string) (blob.Info, error) {
	if err := ctx.Err(); err != nil {
		return blob.Info{}, fmt.Errorf("blob/gcs: stat: %w", err)
	}
	if err := blob.ValidateSHA256(sha); err != nil {
		return blob.Info{}, err //nolint:wrapcheck // already a blob sentinel
	}
	attrs, err := s.api.Attrs(ctx, s.blobName(sha))
	if err != nil {
		return blob.Info{}, fmt.Errorf("blob/gcs: stat %s: %w", sha, err)
	}
	return blob.Info{Size: attrs.Size, Digests: blob.Digests{SHA256: sha}}, nil
}

// Exists reports whether the blob is stored.
func (s *Store) Exists(ctx context.Context, sha string) (bool, error) {
	_, err := s.Stat(ctx, sha)
	if errors.Is(err, blob.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Open returns a reader that fetches ranges on demand.
func (s *Store) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	info, err := s.Stat(ctx, sha)
	if err != nil {
		return nil, blob.Info{}, err
	}
	return &reader{ctx: ctx, s: s, name: s.blobName(sha), size: info.Size}, info, nil
}

// Delete removes the blob; a missing blob is already gone.
func (s *Store) Delete(ctx context.Context, sha string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("blob/gcs: delete: %w", err)
	}
	if err := blob.ValidateSHA256(sha); err != nil {
		return err //nolint:wrapcheck // already a blob sentinel
	}
	if err := s.api.Delete(
		ctx,
		s.blobName(sha),
		0,
	); err != nil &&
		!errors.Is(err, blob.ErrNotFound) {
		return fmt.Errorf("blob/gcs: delete %s: %w", sha, err)
	}
	return nil
}

// Walk lists every blob under the prefix; upload sessions live elsewhere.
func (s *Store) Walk(ctx context.Context, fn func(blob.Info) error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("blob/gcs: walk: %w", err)
	}
	err := s.api.List(ctx, s.prefix+"sha256/", func(a Attrs) error {
		name := a.Name[strings.LastIndex(a.Name, "/")+1:]
		if blob.ValidateSHA256(name) != nil {
			return nil
		}
		return fn(blob.Info{Size: a.Size, Digests: blob.Digests{SHA256: name}})
	})
	if err != nil {
		return fmt.Errorf("blob/gcs: walk: %w", err)
	}
	return nil
}

// Signs reports whether the store signs URLs for via: storage URLs when
// Options.Presign was set, CDN URLs when it has a CDN signer.
func (s *Store) Signs(via blob.Via) bool {
	if via == blob.ViaCDN {
		return s.cdn != nil
	}
	return via == blob.ViaStorage && s.canPresign
}

// SignURL returns a time-limited GET URL, carrying o's filename and media
// type as response overrides. A store that signs over the network issues
// URLs on windows and reuses one per window (see sign).
func (s *Store) SignURL(_ context.Context, sha string, o blob.SignOptions) (string, error) {
	if err := blob.ValidateSHA256(sha); err != nil {
		return "", err //nolint:wrapcheck // already a blob sentinel
	}
	if !s.Signs(o.Via) {
		return "", fmt.Errorf("blob/gcs: sign %s via %s: %w", sha, o.Via, blob.ErrCannotSign)
	}
	if o.Via == blob.ViaCDN {
		u, err := s.cdn.Sign(s.blobName(sha), cdn.Request{
			TTL: o.TTL, Filename: o.Filename, ContentType: o.ContentType,
		})
		if err != nil {
			return "", fmt.Errorf("blob/gcs: sign %s via %s: %w", sha, s.cdn.Provider(), err)
		}
		return u, nil
	}
	if o.TTL <= 0 || o.TTL > maxSignedTTL {
		return "", fmt.Errorf(
			"%w: a signed URL must last more than zero and at most 7 days, not %s",
			ErrConfig,
			o.TTL,
		)
	}
	so := SignedURLOptions{ContentType: o.ContentType}
	if o.Filename != "" {
		so.ContentDisposition = blob.Disposition(o.Filename)
	}
	return s.sign(sha, o, so)
}

// sign signs so for sha. Signing locally, the URL expires the TTL from now.
// Signing over the network, it expires at the end of the current window plus
// the TTL, where windows are TTL long (cdn.Expiry): every request in a
// window asks for the same URL, so one signBlob request serves the window.
// A cached URL is handed out only in the window it was signed for, so it
// always has at least the TTL left. A URL can live up to twice the TTL, so a
// TTL over half the V4 limit, or under a second, is signed afresh each time.
func (s *Store) sign(sha string, o blob.SignOptions, so SignedURLOptions) (string, error) {
	now := time.Now()
	so.Expires = now.Add(o.TTL)
	var key urlKey
	cached := false
	if s.signed != nil && 2*o.TTL <= maxSignedTTL {
		if exp, err := cdn.Expiry(now, o.TTL); err == nil {
			so.Expires, cached = exp, true
			key = urlKey{
				sha:         sha,
				filename:    o.Filename,
				contentType: o.ContentType,
				expires:     exp.Unix(),
			}
			if url, ok := s.signed.get(key); ok {
				return url, nil
			}
		}
	}
	url, err := s.api.SignedURL(s.blobName(sha), so)
	if err != nil {
		return "", fmt.Errorf("blob/gcs: sign %s: %w", sha, err)
	}
	if cached {
		s.signed.put(key, url)
	}
	return url, nil
}

// reader serves a blob through ranged reads. It opens the object lazily
// and reopens it after a Seek, so http.ServeContent's seek-to-end-then-start
// probe costs no request and a Range request costs exactly one.
type reader struct {
	ctx  context.Context //nolint:containedctx // io.Reader has no context parameter
	s    *Store
	name string
	size int64
	off  int64
	body io.ReadCloser
}

// ErrSeek reports an invalid Seek.
var ErrSeek = errors.New("blob/gcs: invalid seek")

func (r *reader) Read(p []byte) (int, error) {
	if r.off >= r.size {
		return 0, io.EOF
	}
	if r.body == nil {
		body, err := r.s.api.Read(r.ctx, r.name, r.off)
		if err != nil {
			return 0, fmt.Errorf("blob/gcs: read %s: %w", r.name, err)
		}
		r.body = body
	}
	n, err := r.body.Read(p)
	r.off += int64(n)
	if errors.Is(err, io.EOF) {
		_ = r.body.Close()
		r.body = nil
		if r.off < r.size {
			return n, fmt.Errorf("blob/gcs: read %s: %w", r.name, io.ErrUnexpectedEOF)
		}
		return n, io.EOF
	}
	if err != nil {
		return n, fmt.Errorf("blob/gcs: read %s: %w", r.name, err)
	}
	return n, nil
}

func (r *reader) Seek(offset int64, whence int) (int64, error) {
	var pos int64
	switch whence {
	case io.SeekStart:
		pos = offset
	case io.SeekCurrent:
		pos = r.off + offset
	case io.SeekEnd:
		pos = r.size + offset
	default:
		return r.off, fmt.Errorf("%w: whence %d", ErrSeek, whence)
	}
	if pos < 0 {
		return r.off, fmt.Errorf("%w: negative position %d", ErrSeek, pos)
	}
	if pos != r.off && r.body != nil {
		_ = r.body.Close()
		r.body = nil
	}
	r.off = pos
	return pos, nil
}

func (r *reader) Close() error {
	if r.body == nil {
		return nil
	}
	err := r.body.Close()
	r.body = nil
	if err != nil {
		return fmt.Errorf("blob/gcs: close %s: %w", r.name, err)
	}
	return nil
}
