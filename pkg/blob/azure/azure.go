// Package azure is the Azure Blob Storage blob store (decision 0027). Blobs
// live at <prefix>sha256/<ab>/<hex> in one container.
//
// Azure block blobs are written as staged blocks that a later Put Block List
// commits, and staged blocks are server-side state any process can commit, so
// an upload session maps onto them directly:
//
//   - Appended bytes accumulate in a pending buffer of at most PartSize; each
//     full part is staged as one block. A blob that never fills a part is
//     written with a single Put Blob at commit.
//   - Block IDs are derived from the upload ID and the block number, so the
//     session only records how many blocks it staged, and re-staging a block
//     after a failure is idempotent.
//   - Staged blocks belong to one blob name and the namespace has no rename.
//     When the client declared the digest up front the blocks are staged on
//     the content key and commit needs no copy. Otherwise they are staged on
//     a temporary blob, which commit finalises and then copies into the
//     content key with parallel Put Block From URL ranges and one atomic
//     Put Block List. Asynchronous Copy Blob is never used: it shows a
//     zero-length blob at the destination while it runs, which would answer
//     Exists for content that is not there.
//   - Every commit to a content key carries If-None-Match: *, so a committed
//     blob is never overwritten; losing that race to an identical upload is
//     success.
package azure

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/cdn"
)

// Props are a committed blob's properties.
type Props struct {
	Size int64
	ETag string
	MD5  []byte
}

// Item is one listed blob.
type Item struct {
	Name string
	Size int64
}

// API is the blob-level surface the store uses, keyed by blob name within
// the store's container. NewAPI implements it over the Azure SDK; tests use
// a fake. Implementations report a missing blob as blob.ErrNotFound, an
// If-None-Match or If-Match failure as ErrPrecondition, a block list naming
// a block that is not staged as ErrBlockList, and a missing container as
// ErrConfig.
type API interface {
	// StageBlock stages data as block id of name.
	StageBlock(ctx context.Context, name, id string, data []byte) error
	// StageBlockFromURL stages bytes [offset, offset+count) of the committed
	// blob src as block id of dst, provided src still has srcETag.
	StageBlockFromURL(
		ctx context.Context,
		dst, id, src string,
		offset, count int64,
		srcETag string,
	) error
	// CommitBlockList commits name from the staged blocks ids in order,
	// storing md5 as its content MD5; with ifNoneMatch it fails when name is
	// already committed. It returns the new ETag.
	CommitBlockList(
		ctx context.Context,
		name string,
		ids []string,
		md5 []byte,
		ifNoneMatch bool,
	) (string, error)
	// Upload writes name in one request; with ifNoneMatch it fails when name
	// is already committed.
	Upload(ctx context.Context, name string, data []byte, ifNoneMatch bool) error
	// Properties returns a committed blob's properties.
	Properties(ctx context.Context, name string) (Props, error)
	// Download streams count bytes of name from offset (0 means to the end),
	// failing if name no longer has etag.
	Download(
		ctx context.Context,
		name string,
		offset, count int64,
		etag string,
	) (io.ReadCloser, error)
	// Delete removes name, committed or only staged, with its snapshots.
	Delete(ctx context.Context, name string) error
	// List returns one page of committed blobs under prefix after marker,
	// and the marker of the next page ("" on the last).
	List(ctx context.Context, prefix, marker string) ([]Item, string, error)
	// SignRead returns a URL that reads name without credentials, as o
	// describes.
	SignRead(ctx context.Context, name string, o ReadSAS) (string, error)
	// Container names the container every name lives in, the first segment
	// of a blob's path on the service.
	Container() string
}

// ReadSAS describes a read SAS: how long it lasts and the response
// overrides (rscd, rsct, rscc) it carries, empty when not wanted.
type ReadSAS struct {
	TTL                time.Duration
	ContentDisposition string
	ContentType        string
	CacheControl       string
	// Start and Expiry, when set, fix the SAS's validity instead of now and
	// now plus TTL; the start is still backdated against clock skew. A CDN
	// URL fixes both to its window, so every SAS for a blob in that window
	// is the same string and the edge's cache, keyed on it, stays useful.
	Start  time.Time
	Expiry time.Time
}

const (
	// DefaultPartSize keeps the pending tail a session stores small while
	// allowing blobs of about 390 GiB within Azure's 50,000 blocks.
	DefaultPartSize = 8 << 20
	// DefaultCopyRangeSize is the range each Put Block From URL copies: the
	// service's limit. The documentation gives both 100 MiB and 4,000 MiB;
	// the real service accepts 4,000 MiB (about 14 s server-side) and refuses
	// 4,001 MiB (research 18 §4), so a commit makes as few calls as it can.
	DefaultCopyRangeSize = 4000 << 20
	// DefaultConcurrency bounds the parallel Put Block From URL calls of one
	// commit.
	DefaultConcurrency = 8
)

// Azure limits, as variables so tests can reach them without huge blobs.
var (
	// maxBlocks is the number of committed blocks a block blob may have.
	maxBlocks = 50_000
	// maxBlockSize is the largest block for service version 2019-12-12 on.
	maxBlockSize int64 = 4000 << 20
)

// Sentinel errors. Wrapped errors keep the API error; test with errors.Is.
var (
	// ErrConfig reports an unusable store configuration, including a
	// container that does not exist.
	ErrConfig = errors.New("blob/azure: invalid configuration")
	// ErrPrecondition reports a failed If-None-Match or If-Match condition:
	// at commit, the content key is already committed.
	ErrPrecondition = errors.New("blob/azure: precondition failed")
	// ErrBlockList reports a block list naming a block that is not staged,
	// which happens at commit when a concurrent commit of the same content
	// key discarded this session's blocks.
	ErrBlockList = errors.New("blob/azure: invalid block list")
	// ErrTooManyBlocks reports an upload that would need more blocks than
	// Azure allows; a larger PartSize admits it.
	ErrTooManyBlocks = errors.New("blob/azure: too many blocks")
)

// Options configures New.
type Options struct {
	// Prefix is prepended to every blob name; a trailing slash is added.
	Prefix string
	// PartSize is the block size; zero means DefaultPartSize.
	PartSize int64
	// CopyRangeSize is the range one Put Block From URL copies; zero means
	// DefaultCopyRangeSize.
	CopyRangeSize int64
	// Concurrency bounds the parallel copies of one commit; zero means
	// DefaultConcurrency.
	Concurrency int
	// Sessions holds upload sessions between requests; session.InUploads
	// with a backend name such as "azure:<store name>" keeps them durable.
	Sessions session.Store
	// Presign makes the store sign storage URLs (blob.Signer). Set it from
	// ClientConfig.CanSign: a store on a pre-issued SAS token cannot sign.
	Presign bool
	// FrontDoor, when set with Presign, makes the store sign CDN URLs: a
	// window-aligned read SAS placed on the Front Door endpoint (decision
	// 0029).
	FrontDoor *cdn.FrontDoor
	// Now is the clock CDN windows are cut from; nil means time.Now.
	Now func() time.Time
}

// Store is a blob.Store on an Azure Blob Storage container.
type Store struct {
	api         API
	prefix      string
	partSize    int64
	copyRange   int64
	concurrency int
	sessions    session.Store
	canPresign  bool
	frontDoor   *cdn.FrontDoor
	now         func() time.Time
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
	if o.CopyRangeSize == 0 {
		o.CopyRangeSize = DefaultCopyRangeSize
	}
	if o.Concurrency == 0 {
		o.Concurrency = DefaultConcurrency
	}
	switch {
	case o.PartSize < 1 || o.PartSize > maxBlockSize:
		return nil, fmt.Errorf(
			"%w: part size %d is outside 1 to %d bytes",
			ErrConfig,
			o.PartSize,
			maxBlockSize,
		)
	case o.CopyRangeSize < 1 || o.CopyRangeSize > maxBlockSize:
		return nil, fmt.Errorf("%w: copy range size %d is outside 1 to %d bytes",
			ErrConfig, o.CopyRangeSize, maxBlockSize)
	case o.Concurrency < 1:
		return nil, fmt.Errorf("%w: concurrency %d is below 1", ErrConfig, o.Concurrency)
	}
	prefix := strings.TrimPrefix(o.Prefix, "/")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if o.FrontDoor != nil {
		if err := o.FrontDoor.Covers(api.Container() + "/" + prefix); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrConfig, err)
		}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Store{
		api: api, prefix: prefix, partSize: o.PartSize, copyRange: o.CopyRangeSize,
		concurrency: o.Concurrency, sessions: o.Sessions, canPresign: o.Presign,
		frontDoor: o.FrontDoor, now: o.Now,
	}, nil
}

// PartSize returns the size of the blocks an upload is written in.
func (s *Store) PartSize() int64 { return s.partSize }

func (s *Store) blobKey(sha string) string { return s.prefix + "sha256/" + sha[:2] + "/" + sha }

// ObjectKey is the blob name a blob is stored under in the container,
// prefix included, for signers that address it directly, such as a CDN.
// sha must be a valid digest (blob.ValidateSHA256).
func (s *Store) ObjectKey(sha string) string { return s.blobKey(sha) }
func (s *Store) tempKey(id string) string    { return s.prefix + "_uploads/" + id }

// blockID derives a block ID from the upload ID, a separator and the block
// number. The format is frozen: Azure requires every block ID of one blob to
// have the same length, and blocks staged on a content key by different
// sessions (declared digests, the copy phase) share that blob. Upload IDs are
// 32 hex characters, so every ID is 39 bytes before base64. The separator
// keeps a session's copy blocks ('+') apart from its staged blocks ('-').
func blockID(uploadID string, sep byte, n int) string {
	return base64.StdEncoding.EncodeToString(fmt.Appendf(nil, "%s%c%06d", uploadID, sep, n))
}

// blockIDs returns the IDs of blocks 1 to n.
func blockIDs(uploadID string, sep byte, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = blockID(uploadID, sep, i+1)
	}
	return ids
}

// Session phases.
const (
	phaseStaging = ""
	// phaseCopying means the temporary blob is committed and the content is
	// being copied into the content key; a retried Commit resumes the copy.
	phaseCopying = "copying"
)

// state is the azure backend's part of a session. The json tags are the
// storage format.
type state struct {
	// Name is the blob the blocks are staged on: the content key when the
	// digest was declared, otherwise the temporary blob. Empty until the
	// first block.
	Name string `json:"name,omitempty"`
	// Blocks counts the staged blocks.
	Blocks int `json:"blocks,omitempty"`
	// Phase is phaseStaging or phaseCopying.
	Phase string `json:"phase,omitempty"`
	// TempETag pins the committed temporary blob the copy reads.
	TempETag string `json:"temp_etag,omitempty"`
}

type upload struct {
	s  *Store
	ss *session.Session[state]
}

// BeginUpload starts a session.
func (s *Store) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	ss, err := session.Begin[state](ctx, s.sessions, opts...)
	if err != nil {
		return nil, fmt.Errorf("blob/azure: %w", err)
	}
	return &upload{s: s, ss: ss}, nil
}

// ResumeUpload reopens a session.
func (s *Store) ResumeUpload(ctx context.Context, id string) (blob.Upload, error) {
	ss, err := session.Resume[state](ctx, s.sessions, id)
	if err != nil {
		return nil, fmt.Errorf("blob/azure: %w", err)
	}
	return &upload{s: s, ss: ss}, nil
}

func (u *upload) ID() string  { return u.ss.ID }
func (u *upload) Size() int64 { return u.ss.Size }

// Append buffers r and stages every full part.
func (u *upload) Append(ctx context.Context, r io.Reader) (int64, error) {
	n, err := u.ss.Append(ctx, r, u.s.partSize, u.stage)
	if err != nil {
		return n, fmt.Errorf("blob/azure: append: %w", err)
	}
	return n, nil
}

// stage stages data as the next block.
func (u *upload) stage(ctx context.Context, data []byte) error {
	st := &u.ss.Data
	if st.Blocks >= maxBlocks {
		return fmt.Errorf("%w: %d blocks of %d bytes", ErrTooManyBlocks, maxBlocks, u.s.partSize)
	}
	if st.Name == "" {
		st.Name = u.s.tempKey(u.ss.ID)
		if u.ss.Expected != "" {
			// The digest is declared, so the blocks can be staged on the
			// content key and commit needs no copy. Commit verifies before
			// committing, so a wrong declaration never commits that key.
			st.Name = u.s.blobKey(u.ss.Expected)
		}
	}
	id := blockID(u.ss.ID, '-', st.Blocks+1)
	if err := u.s.api.StageBlock(ctx, st.Name, id, data); err != nil {
		return fmt.Errorf("blob/azure: stage block %d: %w", st.Blocks+1, err)
	}
	st.Blocks++
	return nil
}

// Commit finishes the session.
func (u *upload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if err := ctx.Err(); err != nil {
		return blob.Info{}, fmt.Errorf("blob/azure: commit: %w", err)
	}
	if err := blob.ValidateDigests(want); err != nil {
		return blob.Info{}, err
	}
	if err := u.ss.Live(ctx); err != nil {
		return blob.Info{}, fmt.Errorf("blob/azure: %w", err)
	}
	got, err := u.ss.Verify(want)
	if err != nil {
		_ = u.Abort(ctx)
		return blob.Info{}, err
	}
	info := blob.Info{Size: u.ss.Size, Digests: got}
	target := u.s.blobKey(got.SHA256)
	exists, err := u.s.exists(ctx, target)
	if err != nil {
		return blob.Info{}, err
	}
	if exists {
		return info, u.Abort(ctx)
	}
	md5, err := hex.DecodeString(got.MD5)
	if err != nil {
		return blob.Info{}, fmt.Errorf("blob/azure: md5: %w", err)
	}
	st := &u.ss.Data
	if st.Blocks == 0 {
		err := u.s.api.Upload(ctx, target, u.ss.Pending, true)
		if err := u.s.settle(ctx, target, info.Size, err); err != nil {
			return blob.Info{}, err
		}
		return info, u.forget(ctx)
	}
	if err := u.ss.Flush(ctx, u.stage); err != nil {
		return blob.Info{}, err
	}
	if err := u.ss.Save(ctx); err != nil {
		return blob.Info{}, fmt.Errorf("blob/azure: %w", err)
	}
	if st.Name == target {
		_, err := u.s.api.CommitBlockList(ctx, target, blockIDs(u.ss.ID, '-', st.Blocks), md5, true)
		if err := u.s.settle(ctx, target, info.Size, err); err != nil {
			return blob.Info{}, err
		}
		return info, u.forget(ctx)
	}
	if err := u.copyIntoPlace(ctx, target, md5, info.Size); err != nil {
		return blob.Info{}, err
	}
	if err := u.s.api.Delete(ctx, st.Name); err != nil && !errors.Is(err, blob.ErrNotFound) {
		return blob.Info{}, fmt.Errorf("blob/azure: remove temporary %s: %w", st.Name, err)
	}
	return info, u.forget(ctx)
}

// copyIntoPlace commits the temporary blob, unless a previous attempt did,
// and copies it into target.
func (u *upload) copyIntoPlace(ctx context.Context, target string, md5 []byte, size int64) error {
	st := &u.ss.Data
	if st.Phase != phaseCopying {
		etag, err := u.s.api.CommitBlockList(
			ctx,
			st.Name,
			blockIDs(u.ss.ID, '-', st.Blocks),
			md5,
			false,
		)
		if err != nil {
			return fmt.Errorf("blob/azure: commit temporary %s: %w", st.Name, err)
		}
		st.Phase, st.TempETag = phaseCopying, etag
		if err := u.ss.Save(ctx); err != nil {
			return fmt.Errorf("blob/azure: %w", err)
		}
	}
	n := int((size + u.s.copyRange - 1) / u.s.copyRange)
	if n > maxBlocks {
		return fmt.Errorf(
			"%w: copying %d bytes in ranges of %d",
			ErrTooManyBlocks,
			size,
			u.s.copyRange,
		)
	}
	ids := blockIDs(u.ss.ID, '+', n)
	if err := u.s.copyRanges(ctx, st.Name, target, st.TempETag, size, ids); err != nil {
		return err
	}
	_, err := u.s.api.CommitBlockList(ctx, target, ids, md5, true)
	return u.s.settle(ctx, target, size, err)
}

// copyRanges stages the ranges of src as the blocks ids of dst, at most
// s.concurrency at a time, and stops starting new ranges after a failure.
func (s *Store) copyRanges(
	ctx context.Context,
	src, dst, srcETag string,
	size int64,
	ids []string,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, s.concurrency)
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for i, id := range ids {
		off := int64(i) * s.copyRange
		count := min(s.copyRange, size-off)
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			if err := s.api.StageBlockFromURL(ctx, dst, id, src, off, count, srcETag); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("copy range at %d: %w", off, err))
				mu.Unlock()
				cancel()
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("blob/azure: copy into place: %w", err)
	}
	return nil
}

// settle interprets the outcome of a write to the content key name: a
// precondition or block-list failure means an identical upload committed it
// first, which is success once the stored size confirms it; otherwise the
// stored size must match what was written.
func (s *Store) settle(ctx context.Context, name string, size int64, err error) error {
	if err != nil && !errors.Is(err, ErrPrecondition) && !errors.Is(err, ErrBlockList) {
		return fmt.Errorf("blob/azure: commit %s: %w", name, err)
	}
	p, perr := s.api.Properties(ctx, name)
	if perr != nil {
		return fmt.Errorf("blob/azure: confirm %s: %w", name, errors.Join(err, perr))
	}
	if p.Size != size {
		return fmt.Errorf(
			"blob/azure: %s holds %d bytes, want %d: %w",
			name,
			p.Size,
			size,
			errors.Join(err, ErrPrecondition),
		)
	}
	return nil
}

func (u *upload) forget(ctx context.Context) error {
	if err := u.ss.Forget(ctx); err != nil {
		return fmt.Errorf("blob/azure: %w", err)
	}
	return nil
}

// Abort discards the session and its temporary blob. Blocks staged on a
// content key are left for Azure to discard after a week: deleting that
// name could remove another session's blocks or a committed blob.
func (u *upload) Abort(ctx context.Context) error {
	st := u.ss.Data
	if st.Name != "" && st.Name == u.s.tempKey(u.ss.ID) {
		if err := u.s.api.Delete(ctx, st.Name); err != nil && !errors.Is(err, blob.ErrNotFound) {
			return fmt.Errorf("blob/azure: remove temporary %s: %w", st.Name, err)
		}
	}
	return u.forget(ctx)
}

func (s *Store) exists(ctx context.Context, name string) (bool, error) {
	_, err := s.api.Properties(ctx, name)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, blob.ErrNotFound) {
		return false, nil
	}
	return false, fmt.Errorf("blob/azure: properties %s: %w", name, err)
}

// Stat returns a blob's size and digest.
func (s *Store) Stat(ctx context.Context, sha string) (blob.Info, error) {
	_, info, err := s.stat(ctx, sha)
	return info, err
}

func (s *Store) stat(ctx context.Context, sha string) (Props, blob.Info, error) {
	if err := ctx.Err(); err != nil {
		return Props{}, blob.Info{}, fmt.Errorf("blob/azure: stat: %w", err)
	}
	if err := blob.ValidateSHA256(sha); err != nil {
		return Props{}, blob.Info{}, err //nolint:wrapcheck // already a blob sentinel
	}
	p, err := s.api.Properties(ctx, s.blobKey(sha))
	if err != nil {
		return Props{}, blob.Info{}, fmt.Errorf("blob/azure: stat %s: %w", sha, err)
	}
	return p, blob.Info{Size: p.Size, Digests: blob.Digests{SHA256: sha}}, nil
}

// Exists reports whether the blob is stored.
func (s *Store) Exists(ctx context.Context, sha string) (bool, error) {
	_, err := s.Stat(ctx, sha)
	if errors.Is(err, blob.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Open returns a reader that fetches ranges on demand, pinned to the blob's
// ETag.
func (s *Store) Open(ctx context.Context, sha string) (io.ReadSeekCloser, blob.Info, error) {
	p, info, err := s.stat(ctx, sha)
	if err != nil {
		return nil, blob.Info{}, err
	}
	return &reader{ctx: ctx, s: s, name: s.blobKey(sha), size: p.Size, etag: p.ETag}, info, nil
}

// Delete removes the blob; deleting a missing blob succeeds.
func (s *Store) Delete(ctx context.Context, sha string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("blob/azure: delete: %w", err)
	}
	if err := blob.ValidateSHA256(sha); err != nil {
		return err //nolint:wrapcheck // already a blob sentinel
	}
	if err := s.api.Delete(ctx, s.blobKey(sha)); err != nil && !errors.Is(err, blob.ErrNotFound) {
		return fmt.Errorf("blob/azure: delete %s: %w", sha, err)
	}
	return nil
}

// Walk lists every blob under the prefix.
func (s *Store) Walk(ctx context.Context, fn func(blob.Info) error) error {
	prefix := s.prefix + "sha256/"
	marker := ""
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("blob/azure: walk: %w", err)
		}
		items, next, err := s.api.List(ctx, prefix, marker)
		if err != nil {
			return fmt.Errorf("blob/azure: walk: %w", err)
		}
		for _, it := range items {
			name := it.Name[strings.LastIndex(it.Name, "/")+1:]
			if blob.ValidateSHA256(name) != nil {
				continue
			}
			if err := fn(
				blob.Info{Size: it.Size, Digests: blob.Digests{SHA256: name}},
			); err != nil {
				return fmt.Errorf("blob/azure: walk: %w", err)
			}
		}
		if next == "" {
			return nil
		}
		marker = next
	}
}

// Signs reports whether the store signs URLs for via: storage URLs when
// Options.Presign was set, and CDN URLs when it also has a Front Door. A
// store on a pre-issued SAS signs neither: Front Door passes the SAS through
// to storage, so a CDN URL needs a SAS the store can mint.
func (s *Store) Signs(via blob.Via) bool {
	if via == blob.ViaCDN {
		return s.canPresign && s.frontDoor != nil
	}
	return s.canPresign
}

// SignURL returns a time-limited read URL, carrying o's filename and media
// type as response overrides.
func (s *Store) SignURL(ctx context.Context, sha string, o blob.SignOptions) (string, error) {
	if err := blob.ValidateSHA256(sha); err != nil {
		return "", err //nolint:wrapcheck // already a blob sentinel
	}
	if !s.Signs(o.Via) {
		return "", fmt.Errorf("blob/azure: sign %s via %s: %w", sha, o.Via, blob.ErrCannotSign)
	}
	if o.TTL <= 0 {
		return "", fmt.Errorf("%w: a signed URL must last more than zero, not %s", ErrConfig, o.TTL)
	}
	rs := ReadSAS{TTL: o.TTL, ContentType: o.ContentType}
	if o.Filename != "" {
		rs.ContentDisposition = blob.Disposition(o.Filename)
	}
	if o.Via == blob.ViaCDN {
		return s.signFrontDoor(ctx, sha, rs)
	}
	signed, err := s.api.SignRead(ctx, s.blobKey(sha), rs)
	if err != nil {
		return "", fmt.Errorf("blob/azure: sign %s: %w", sha, err)
	}
	return signed, nil
}

// signFrontDoor signs a read SAS fixed to the current TTL-long window and
// moves it onto the Front Door endpoint, so every client in one window gets
// the same URL for a blob and the edge serves it from its cache (decision
// 0029). The SAS lives until the window ends plus the TTL; an edge may keep
// serving a copy it cached just before then for up to its max-age, or the
// route's cap if that is shorter.
func (s *Store) signFrontDoor(ctx context.Context, sha string, rs ReadSAS) (string, error) {
	start, expiry, err := cdn.Window(s.now(), rs.TTL)
	if err != nil {
		return "", fmt.Errorf("blob/azure: sign %s via front_door: %w", sha, err)
	}
	rs.Start, rs.Expiry = start, expiry
	// Without a Cache-Control of its own an object stays in Front Door's
	// cache for one to three days; max-age bounds it at the TTL even where
	// the route's cap is missing. A fixed value, not the time left in the
	// window, so the window's SAS stays one string.
	rs.CacheControl = "public, max-age=" + strconv.FormatInt(int64(rs.TTL/time.Second), 10)
	key := s.blobKey(sha)
	raw, err := s.api.SignRead(ctx, key, rs)
	if err != nil {
		return "", fmt.Errorf("blob/azure: sign %s via front_door: %w", sha, err)
	}
	signed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("blob/azure: sign %s via front_door: %w", sha, err)
	}
	u, err := s.frontDoor.URL(s.api.Container()+"/"+key, signed.RawQuery)
	if err != nil {
		return "", fmt.Errorf("blob/azure: sign %s via front_door: %w", sha, err)
	}
	return u, nil
}

// reader serves a blob through ranged GETs. It opens the body lazily and
// reopens it after a Seek, so http.ServeContent's seek-to-end-then-start
// probe costs no request and a Range request costs exactly one. Reads at the
// end return io.EOF without a request, because Azure answers a range past the
// end with 416.
type reader struct {
	ctx  context.Context //nolint:containedctx // io.Reader has no context parameter
	s    *Store
	name string
	size int64
	etag string
	off  int64
	body io.ReadCloser
}

// ErrSeek reports an invalid Seek.
var ErrSeek = errors.New("blob/azure: invalid seek")

func (r *reader) Read(p []byte) (int, error) {
	if r.off >= r.size {
		return 0, io.EOF
	}
	if r.body == nil {
		body, err := r.s.api.Download(r.ctx, r.name, r.off, 0, r.etag)
		if err != nil {
			return 0, fmt.Errorf("blob/azure: get %s: %w", r.name, err)
		}
		r.body = body
	}
	n, err := r.body.Read(p)
	r.off += int64(n)
	if errors.Is(err, io.EOF) {
		_ = r.body.Close()
		r.body = nil
		if r.off < r.size {
			return n, fmt.Errorf("blob/azure: get %s: %w", r.name, io.ErrUnexpectedEOF)
		}
		return n, io.EOF
	}
	if err != nil {
		return n, fmt.Errorf("blob/azure: read %s: %w", r.name, err)
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
		return fmt.Errorf("blob/azure: close %s: %w", r.name, err)
	}
	return nil
}
