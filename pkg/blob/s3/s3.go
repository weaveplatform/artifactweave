// Package s3 is the S3-compatible blob store (AWS S3, MinIO, Ceph RGW and
// similar). Blobs live at <prefix>sha256/<ab>/<hex>.
//
// Object stores cannot append or rename, so an upload session is shaped
// around those limits:
//
//   - Bytes accumulate in a pending buffer of at most PartSize. A blob that
//     never fills one part is written with a single PutObject at commit; most
//     package files are that small, and they never pay for a multipart upload.
//   - When the buffer fills, the session starts an S3 multipart upload (on the
//     first full part) and uploads the part. S3 requires every part except the
//     last to be at least 5 MiB, which is why appends are buffered rather than
//     uploaded as they arrive: Docker clients PATCH in chunks of any size.
//   - The multipart upload ID, the part list, the pending tail and the hash
//     state are saved through an UploadStateStore after every append, so a
//     session resumes in a later request (decision record 0004 puts this state
//     in the metadata database).
//   - At commit the content key is known. If the client declared the digest
//     up front the multipart upload already targets the content key; otherwise
//     it targets a temporary key and is copied into place, with a multipart
//     copy above S3's 5 GiB single-copy limit.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/weaveplatform/artifactweave/pkg/blob"
)

// API is the subset of *s3.Client the store uses, so tests can substitute a
// fake that injects failures.
type API interface {
	PutObject(
		context.Context,
		*s3.PutObjectInput,
		...func(*s3.Options),
	) (*s3.PutObjectOutput, error)
	GetObject(
		context.Context,
		*s3.GetObjectInput,
		...func(*s3.Options),
	) (*s3.GetObjectOutput, error)
	HeadObject(
		context.Context,
		*s3.HeadObjectInput,
		...func(*s3.Options),
	) (*s3.HeadObjectOutput, error)
	DeleteObject(
		context.Context,
		*s3.DeleteObjectInput,
		...func(*s3.Options),
	) (*s3.DeleteObjectOutput, error)
	CopyObject(
		context.Context,
		*s3.CopyObjectInput,
		...func(*s3.Options),
	) (*s3.CopyObjectOutput, error)
	ListObjectsV2(
		context.Context,
		*s3.ListObjectsV2Input,
		...func(*s3.Options),
	) (*s3.ListObjectsV2Output, error)
	CreateMultipartUpload(
		context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options),
	) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(
		context.Context,
		*s3.UploadPartInput,
		...func(*s3.Options),
	) (*s3.UploadPartOutput, error)
	UploadPartCopy(
		context.Context,
		*s3.UploadPartCopyInput,
		...func(*s3.Options),
	) (*s3.UploadPartCopyOutput, error)
	CompleteMultipartUpload(
		context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options),
	) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(
		context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options),
	) (*s3.AbortMultipartUploadOutput, error)
}

// PresignAPI is the subset of *s3.PresignClient the store uses.
type PresignAPI interface {
	PresignGetObject(
		context.Context, *s3.GetObjectInput, ...func(*s3.PresignOptions),
	) (*v4.PresignedHTTPRequest, error)
}

const (
	// DefaultPartSize balances request count against the memory one session
	// holds for its pending tail.
	DefaultPartSize = 8 << 20
	// copyPartSize is the range each UploadPartCopy moves above copyLimit.
	copyPartSize = 1 << 30
)

// S3 limits, as variables so tests can exercise the paths without
// gigabyte objects.
var (
	// minPartSize is S3's floor for every part but the last.
	minPartSize int64 = 5 << 20
	// copyLimit is the largest object CopyObject accepts in one call.
	copyLimit int64 = 5 << 30
	// copyChunk is the range each UploadPartCopy call moves.
	copyChunk int64 = copyPartSize
)

// ErrConfig reports an unusable store configuration.
var ErrConfig = errors.New("blob/s3: invalid configuration")

// Options configures New.
type Options struct {
	// Bucket must already exist.
	Bucket string
	// Prefix is prepended to every key; a trailing slash is added if missing.
	Prefix string
	// PartSize is the multipart part size; zero means DefaultPartSize. S3
	// rejects parts under 5 MiB, so smaller values are refused.
	PartSize int64
	// States holds upload sessions between requests.
	States UploadStateStore
	// Presign enables PresignGet when set.
	Presign PresignAPI
}

// Store is a blob.Store on an S3 bucket.
type Store struct {
	api      API
	presign  PresignAPI
	bucket   string
	prefix   string
	partSize int64
	states   UploadStateStore
}

var (
	_ blob.Store     = (*Store)(nil)
	_ blob.Presigner = (*Store)(nil)
)

// New returns a store over api.
func New(api API, o Options) (*Store, error) {
	if api == nil || o.Bucket == "" || o.States == nil {
		return nil, fmt.Errorf("%w: client, bucket and upload state store are required", ErrConfig)
	}
	if o.PartSize == 0 {
		o.PartSize = DefaultPartSize
	}
	if o.PartSize < minPartSize {
		return nil, fmt.Errorf(
			"%w: part size %d is below S3's %d byte minimum",
			ErrConfig,
			o.PartSize,
			minPartSize,
		)
	}
	prefix := strings.TrimPrefix(o.Prefix, "/")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &Store{
		api: api, presign: o.Presign, bucket: o.Bucket, prefix: prefix,
		partSize: o.PartSize, states: o.States,
	}, nil
}

func (s *Store) blobKey(sha string) string { return s.prefix + "sha256/" + sha[:2] + "/" + sha }
func (s *Store) tempKey(id string) string  { return s.prefix + "_uploads/" + id }

// UploadState is what an upload session persists between requests.
type UploadState struct {
	ID string
	// Key is the object the multipart upload writes; empty until it starts.
	Key string
	// MultipartID is S3's upload ID; empty until the first full part.
	MultipartID string
	Parts       []Part
	// Size counts every appended byte, uploaded or pending.
	Size int64
	// Hasher is the saved blob.Hasher state.
	Hasher []byte
	// Pending is the tail not yet uploaded, shorter than the part size.
	Pending  []byte
	Expected string
}

// Part is one uploaded part.
type Part struct {
	Number int32
	ETag   string
}

// UploadStateStore keeps sessions between requests. Get returns an error
// wrapping blob.ErrUploadUnknown for an unknown ID.
type UploadStateStore interface {
	Put(ctx context.Context, st UploadState) error
	Get(ctx context.Context, id string) (UploadState, error)
	Delete(ctx context.Context, id string) error
}

// MemoryStates is an UploadStateStore in process memory. Sessions do not
// survive a restart; it serves tests and runs until the metadata store
// provides a durable one.
type MemoryStates struct {
	mu sync.Mutex
	m  map[string]UploadState
}

// NewMemoryStates returns an empty MemoryStates.
func NewMemoryStates() *MemoryStates { return &MemoryStates{m: map[string]UploadState{}} }

// Put stores a copy of st.
func (m *MemoryStates) Put(_ context.Context, st UploadState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st.Parts = append([]Part(nil), st.Parts...)
	st.Pending = append([]byte(nil), st.Pending...)
	m.m[st.ID] = st
	return nil
}

// Get returns a copy of the session.
func (m *MemoryStates) Get(_ context.Context, id string) (UploadState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.m[id]
	if !ok {
		return UploadState{}, fmt.Errorf("%w: %s", blob.ErrUploadUnknown, id)
	}
	st.Parts = append([]Part(nil), st.Parts...)
	st.Pending = append([]byte(nil), st.Pending...)
	return st, nil
}

// Delete forgets a session.
func (m *MemoryStates) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.m, id)
	return nil
}

type upload struct {
	s  *Store
	st UploadState
	h  *blob.Hasher
}

// BeginUpload starts a session.
func (s *Store) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("blob/s3: begin: %w", err)
	}
	o, err := blob.ResolveOptions(opts...)
	if err != nil {
		return nil, err
	}
	id, err := blob.NewUploadID()
	if err != nil {
		return nil, err
	}
	u := &upload{s: s, st: UploadState{ID: id, Expected: o.ExpectedSHA256}, h: blob.NewHasher()}
	if err := u.save(ctx); err != nil {
		return nil, err
	}
	return u, nil
}

// ResumeUpload reopens a session.
func (s *Store) ResumeUpload(ctx context.Context, id string) (blob.Upload, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("blob/s3: resume: %w", err)
	}
	if !blob.ValidUploadID(id) {
		return nil, fmt.Errorf("%w: %q", blob.ErrUploadUnknown, id)
	}
	st, err := s.states.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("blob/s3: resume: %w", err)
	}
	u := &upload{s: s, st: st, h: blob.NewHasher()}
	if err := u.h.UnmarshalBinary(st.Hasher); err != nil {
		// Unlike the fs backend there is no local copy of the uploaded parts
		// to rehash, so a damaged state ends the session.
		return nil, fmt.Errorf("%w: %s: %w", blob.ErrUploadUnknown, id, err)
	}
	return u, nil
}

func (u *upload) save(ctx context.Context) error {
	hs, err := u.h.MarshalBinary()
	if err != nil {
		return err
	}
	u.st.Hasher = hs
	if err := u.s.states.Put(ctx, u.st); err != nil {
		return fmt.Errorf("blob/s3: save upload %s: %w", u.st.ID, err)
	}
	return nil
}

func (u *upload) live(ctx context.Context) error {
	if _, err := u.s.states.Get(ctx, u.st.ID); err != nil {
		return fmt.Errorf("blob/s3: %w", err)
	}
	return nil
}

func (u *upload) ID() string  { return u.st.ID }
func (u *upload) Size() int64 { return u.st.Size }

// Append buffers r and uploads every full part.
func (u *upload) Append(ctx context.Context, r io.Reader) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("blob/s3: append: %w", err)
	}
	if err := u.live(ctx); err != nil {
		return 0, err
	}
	var total int64
	buf := make([]byte, 256<<10)
	var appendErr error
	for appendErr == nil {
		room := int(u.s.partSize) - len(u.st.Pending)
		n, rerr := r.Read(buf[:min(len(buf), room)])
		if n > 0 {
			u.st.Pending = append(u.st.Pending, buf[:n]...)
			_, _ = u.h.Write(buf[:n])
			u.st.Size += int64(n)
			total += int64(n)
		}
		if int64(len(u.st.Pending)) >= u.s.partSize {
			// The bytes stay pending (and hashed) if the part fails, so the
			// next Append or Commit retries the same part.
			appendErr = u.flushPart(ctx)
		}
		switch {
		case errors.Is(rerr, io.EOF):
			rerr = nil
			if appendErr == nil {
				return total, u.save(ctx)
			}
		case rerr != nil && appendErr == nil:
			appendErr = fmt.Errorf("blob/s3: read: %w", rerr)
		}
	}
	if err := u.save(ctx); err != nil {
		return total, errors.Join(appendErr, err)
	}
	return total, appendErr
}

// flushPart uploads the pending buffer as the next part, starting the
// multipart upload on the first one.
func (u *upload) flushPart(ctx context.Context) error {
	if u.st.MultipartID == "" {
		key := u.s.tempKey(u.st.ID)
		if u.st.Expected != "" {
			// The digest is declared, so the parts can target the content key
			// and commit needs no copy. Commit verifies before completing, so
			// a wrong declaration never reaches that key.
			key = u.s.blobKey(u.st.Expected)
		}
		out, err := u.s.api.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
			Bucket: &u.s.bucket, Key: &key,
		})
		if err != nil {
			return fmt.Errorf("blob/s3: start multipart: %w", err)
		}
		u.st.Key, u.st.MultipartID = key, aws.ToString(out.UploadId)
		if err := u.save(ctx); err != nil {
			return err
		}
	}
	num := int32(len(u.st.Parts) + 1) // #nosec G115 -- S3 caps uploads at 10,000 parts
	out, err := u.s.api.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: &u.s.bucket, Key: &u.st.Key, UploadId: &u.st.MultipartID,
		PartNumber: &num, Body: bytes.NewReader(u.st.Pending),
		ContentLength: aws.Int64(int64(len(u.st.Pending))),
	})
	if err != nil {
		return fmt.Errorf("blob/s3: upload part %d: %w", num, translate(err))
	}
	u.st.Parts = append(u.st.Parts, Part{Number: num, ETag: aws.ToString(out.ETag)})
	u.st.Pending = u.st.Pending[:0]
	return nil
}

// Commit finishes the session.
func (u *upload) Commit(ctx context.Context, want blob.Digests) (blob.Info, error) {
	if err := ctx.Err(); err != nil {
		return blob.Info{}, fmt.Errorf("blob/s3: commit: %w", err)
	}
	if err := blob.ValidateDigests(want); err != nil {
		return blob.Info{}, err
	}
	if err := u.live(ctx); err != nil {
		return blob.Info{}, err
	}
	if want.SHA256 == "" {
		want.SHA256 = u.st.Expected
	}
	got := u.h.Sum()
	if err := blob.Verify(want, got); err != nil {
		_ = u.Abort(ctx)
		return blob.Info{}, err
	}
	info := blob.Info{Size: u.st.Size, Digests: got}
	target := u.s.blobKey(got.SHA256)
	exists, err := u.s.exists(ctx, target)
	if err != nil {
		return blob.Info{}, err
	}
	if exists {
		return info, u.Abort(ctx)
	}
	if u.st.MultipartID == "" {
		_, err := u.s.api.PutObject(ctx, &s3.PutObjectInput{
			Bucket: &u.s.bucket, Key: &target, Body: bytes.NewReader(u.st.Pending),
			ContentLength: aws.Int64(int64(len(u.st.Pending))),
		})
		if err != nil {
			return blob.Info{}, fmt.Errorf("blob/s3: put %s: %w", got.SHA256, err)
		}
		return info, u.forget(ctx)
	}
	if len(u.st.Pending) > 0 {
		if err := u.flushPart(ctx); err != nil {
			return blob.Info{}, err
		}
		if err := u.save(ctx); err != nil {
			return blob.Info{}, err
		}
	}
	if err := u.complete(ctx); err != nil {
		return blob.Info{}, err
	}
	if u.st.Key != target {
		if err := u.s.copy(ctx, u.st.Key, target, u.st.Size); err != nil {
			return blob.Info{}, err
		}
		if _, err := u.s.api.DeleteObject(
			ctx,
			&s3.DeleteObjectInput{Bucket: &u.s.bucket, Key: &u.st.Key},
		); err != nil {
			return blob.Info{}, fmt.Errorf("blob/s3: remove temporary %s: %w", u.st.Key, err)
		}
	}
	return info, u.forget(ctx)
}

func (u *upload) complete(ctx context.Context) error {
	parts := make([]types.CompletedPart, len(u.st.Parts))
	for i, p := range u.st.Parts {
		parts[i] = types.CompletedPart{PartNumber: aws.Int32(p.Number), ETag: aws.String(p.ETag)}
	}
	_, err := u.s.api.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: &u.s.bucket, Key: &u.st.Key, UploadId: &u.st.MultipartID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		return fmt.Errorf("blob/s3: complete multipart: %w", translate(err))
	}
	return nil
}

func (u *upload) forget(ctx context.Context) error {
	if err := u.s.states.Delete(ctx, u.st.ID); err != nil {
		return fmt.Errorf("blob/s3: forget upload %s: %w", u.st.ID, err)
	}
	return nil
}

// Abort discards the session and any uploaded parts.
func (u *upload) Abort(ctx context.Context) error {
	if u.st.MultipartID != "" {
		_, err := u.s.api.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket: &u.s.bucket, Key: &u.st.Key, UploadId: &u.st.MultipartID,
		})
		if err != nil && !errors.Is(translate(err), blob.ErrUploadUnknown) {
			return fmt.Errorf("blob/s3: abort multipart: %w", err)
		}
	}
	return u.forget(ctx)
}

// copy moves src to dst, in one CopyObject when S3 allows it and as a
// multipart copy above the single-copy limit.
func (s *Store) copy(ctx context.Context, src, dst string, size int64) error {
	source := s.bucket + "/" + src
	if size <= copyLimit {
		_, err := s.api.CopyObject(
			ctx,
			&s3.CopyObjectInput{Bucket: &s.bucket, Key: &dst, CopySource: &source},
		)
		if err != nil {
			return fmt.Errorf("blob/s3: copy into place: %w", err)
		}
		return nil
	}
	out, err := s.api.CreateMultipartUpload(
		ctx,
		&s3.CreateMultipartUploadInput{Bucket: &s.bucket, Key: &dst},
	)
	if err != nil {
		return fmt.Errorf("blob/s3: start multipart copy: %w", err)
	}
	id := out.UploadId
	var parts []types.CompletedPart
	for off, num := int64(0), int32(1); off < size; off, num = off+copyChunk, num+1 {
		end := min(off+copyChunk, size) - 1
		rng := "bytes=" + strconv.FormatInt(off, 10) + "-" + strconv.FormatInt(end, 10)
		pc, err := s.api.UploadPartCopy(ctx, &s3.UploadPartCopyInput{
			Bucket: &s.bucket, Key: &dst, UploadId: id, PartNumber: aws.Int32(num),
			CopySource: &source, CopySourceRange: &rng,
		})
		if err != nil {
			_, _ = s.api.AbortMultipartUpload(
				ctx,
				&s3.AbortMultipartUploadInput{Bucket: &s.bucket, Key: &dst, UploadId: id},
			)
			return fmt.Errorf("blob/s3: copy part %d: %w", num, err)
		}
		etag := ""
		if pc.CopyPartResult != nil {
			etag = aws.ToString(pc.CopyPartResult.ETag)
		}
		parts = append(
			parts,
			types.CompletedPart{PartNumber: aws.Int32(num), ETag: aws.String(etag)},
		)
	}
	_, err = s.api.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: &s.bucket, Key: &dst, UploadId: id,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		return fmt.Errorf("blob/s3: complete multipart copy: %w", err)
	}
	return nil
}

func (s *Store) exists(ctx context.Context, key string) (bool, error) {
	_, err := s.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err == nil {
		return true, nil
	}
	if errors.Is(translate(err), blob.ErrNotFound) {
		return false, nil
	}
	return false, fmt.Errorf("blob/s3: head %s: %w", key, err)
}

// Stat returns a blob's size and digest.
func (s *Store) Stat(ctx context.Context, sha string) (blob.Info, error) {
	if err := ctx.Err(); err != nil {
		return blob.Info{}, fmt.Errorf("blob/s3: stat: %w", err)
	}
	if err := blob.ValidateSHA256(sha); err != nil {
		return blob.Info{}, err
	}
	key := s.blobKey(sha)
	out, err := s.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return blob.Info{}, fmt.Errorf("blob/s3: stat %s: %w", sha, translate(err))
	}
	return blob.Info{Size: aws.ToInt64(out.ContentLength), Digests: blob.Digests{SHA256: sha}}, nil
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
	return &reader{ctx: ctx, s: s, key: s.blobKey(sha), size: info.Size}, info, nil
}

// Delete removes the blob; S3 deletes of missing keys already succeed.
func (s *Store) Delete(ctx context.Context, sha string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("blob/s3: delete: %w", err)
	}
	if err := blob.ValidateSHA256(sha); err != nil {
		return err
	}
	key := s.blobKey(sha)
	if _, err := s.api.DeleteObject(
		ctx,
		&s3.DeleteObjectInput{Bucket: &s.bucket, Key: &key},
	); err != nil {
		return fmt.Errorf("blob/s3: delete %s: %w", sha, err)
	}
	return nil
}

// Walk lists every blob under the prefix.
func (s *Store) Walk(ctx context.Context, fn func(blob.Info) error) error {
	prefix := s.prefix + "sha256/"
	var token *string
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("blob/s3: walk: %w", err)
		}
		out, err := s.api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: &s.bucket, Prefix: &prefix, ContinuationToken: token,
		})
		if err != nil {
			return fmt.Errorf("blob/s3: walk: %w", err)
		}
		for _, obj := range out.Contents {
			key := aws.ToString(obj.Key)
			name := key[strings.LastIndex(key, "/")+1:]
			if blob.ValidateSHA256(name) != nil {
				continue
			}
			if err := fn(
				blob.Info{Size: aws.ToInt64(obj.Size), Digests: blob.Digests{SHA256: name}},
			); err != nil {
				return fmt.Errorf("blob/s3: walk: %w", err)
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			return nil
		}
		token = out.NextContinuationToken
	}
}

// PresignGet returns a time-limited GET URL for downloadRedirect.
func (s *Store) PresignGet(ctx context.Context, sha string, ttl time.Duration) (string, error) {
	if err := blob.ValidateSHA256(sha); err != nil {
		return "", err
	}
	if s.presign == nil {
		return "", fmt.Errorf("%w: presigning is not configured", ErrConfig)
	}
	key := s.blobKey(sha)
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: &s.bucket, Key: &key},
		func(o *s3.PresignOptions) { o.Expires = ttl })
	if err != nil {
		return "", fmt.Errorf("blob/s3: presign %s: %w", sha, err)
	}
	return req.URL, nil
}

// reader serves a blob through ranged GETs. It opens the body lazily and
// reopens it after a Seek, so http.ServeContent's seek-to-end-then-start
// probe costs no request and a Range request costs exactly one.
type reader struct {
	ctx  context.Context //nolint:containedctx // io.Reader has no context parameter
	s    *Store
	key  string
	size int64
	off  int64
	body io.ReadCloser
}

// ErrSeek reports an invalid Seek.
var ErrSeek = errors.New("blob/s3: invalid seek")

func (r *reader) Read(p []byte) (int, error) {
	if r.off >= r.size {
		return 0, io.EOF
	}
	if r.body == nil {
		rng := "bytes=" + strconv.FormatInt(r.off, 10) + "-"
		out, err := r.s.api.GetObject(
			r.ctx,
			&s3.GetObjectInput{Bucket: &r.s.bucket, Key: &r.key, Range: &rng},
		)
		if err != nil {
			return 0, fmt.Errorf("blob/s3: get %s: %w", r.key, translate(err))
		}
		r.body = out.Body
	}
	n, err := r.body.Read(p)
	r.off += int64(n)
	if errors.Is(err, io.EOF) {
		_ = r.body.Close()
		r.body = nil
		if r.off < r.size {
			return n, fmt.Errorf("blob/s3: get %s: %w", r.key, io.ErrUnexpectedEOF)
		}
		return n, io.EOF
	}
	if err != nil {
		return n, fmt.Errorf("blob/s3: read %s: %w", r.key, err)
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
		return fmt.Errorf("blob/s3: close %s: %w", r.key, err)
	}
	return nil
}

// translate maps S3's "not there" errors onto the blob sentinels. S3 reports
// a missing key as NoSuchKey on GET and as a bare 404 NotFound on HEAD, and a
// finished or aborted multipart upload as NoSuchUpload.
func translate(err error) error {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return err
	}
	switch apiErr.ErrorCode() {
	case "NoSuchKey", "NotFound":
		return fmt.Errorf("%w: %w", blob.ErrNotFound, err)
	case "NoSuchUpload":
		return fmt.Errorf("%w: %w", blob.ErrUploadUnknown, err)
	}
	return err
}
