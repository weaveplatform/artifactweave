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
//     state are saved through a session.Store after every append, so a
//     session resumes in a later request (decision 0027; session.InUploads
//     keeps it in the metadata database).
//   - At commit the content key is known. If the client declared the digest
//     up front the multipart upload already targets the content key; otherwise
//     it targets a temporary key and is copied into place, with a multipart
//     copy above S3's 5 GiB single-copy limit.
//   - Every write of a content key is a must-not-exist write (If-None-Match:
//     *, decision 0027), as the GCS and Azure backends write, so a blob that
//     is already stored is never written again and two uploads of one blob
//     cannot race. S3 answers 412 for a taken key, and the existing object is
//     adopted. There is no HeadObject before the write.
package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/blob/session"
	"github.com/weaveplatform/artifactweave/pkg/cdn"
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
	// maxSignedTTL is the longest validity SigV4 allows a presigned URL.
	maxSignedTTL = 7 * 24 * time.Hour
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

// errPrecondition is S3's 412 PreconditionFailed: a must-not-exist write
// (If-None-Match: *) found its key taken.
var errPrecondition = errors.New("blob/s3: key already stored")

// Options configures New.
type Options struct {
	// Bucket must already exist.
	Bucket string
	// Prefix is prepended to every key; a trailing slash is added if missing.
	Prefix string
	// PartSize is the multipart part size; zero means DefaultPartSize. S3
	// rejects parts under 5 MiB, so smaller values are refused.
	PartSize int64
	// Sessions holds upload sessions between requests; session.InUploads
	// with a backend name such as "s3:<store name>" keeps them durable.
	Sessions session.Store
	// Presign signs storage URLs when set (blob.Signer); NewClient always
	// returns one.
	Presign PresignAPI
	// CDN signs CDN URLs when set, such as a cdn.CloudFront in front of the
	// bucket (decision 0029).
	CDN cdn.Signer
}

// Store is a blob.Store on an S3 bucket.
type Store struct {
	api      API
	presign  PresignAPI
	cdn      cdn.Signer
	bucket   string
	prefix   string
	partSize int64
	sessions session.Store
	// unconditional is set once the store has refused If-None-Match as
	// NotImplemented; writes then go without it (see writeOnce).
	unconditional atomic.Bool
}

var (
	_ blob.Store     = (*Store)(nil)
	_ blob.Signer    = (*Store)(nil)
	_ blob.PartSizer = (*Store)(nil)
)

// New returns a store over api.
func New(api API, o Options) (*Store, error) {
	if api == nil || o.Bucket == "" || o.Sessions == nil {
		return nil, fmt.Errorf("%w: client, bucket and session store are required", ErrConfig)
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
	if o.CDN != nil {
		if err := o.CDN.Covers(prefix); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrConfig, err)
		}
	}
	return &Store{
		api: api, presign: o.Presign, cdn: o.CDN, bucket: o.Bucket, prefix: prefix,
		partSize: o.PartSize, sessions: o.Sessions,
	}, nil
}

// PartSize returns the size of the parts an upload is written in.
func (s *Store) PartSize() int64 { return s.partSize }

func (s *Store) blobKey(sha string) string { return s.prefix + "sha256/" + sha[:2] + "/" + sha }

// ObjectKey is the key a blob is stored under, prefix included, for signers
// that address the bucket directly, such as a CDN. sha must be a valid
// digest (blob.ValidateSHA256).
func (s *Store) ObjectKey(sha string) string { return s.blobKey(sha) }
func (s *Store) tempKey(id string) string    { return s.prefix + "_uploads/" + id }

// multipart is the s3 backend's part of a session: the multipart upload
// once the first full part starts it. The json tags are the storage format.
type multipart struct {
	// Key is the object the multipart upload writes; empty until it starts.
	Key string `json:"key,omitempty"`
	// UploadID is S3's multipart upload ID; empty until the first full part.
	UploadID string `json:"upload_id,omitempty"`
	Parts    []part `json:"parts,omitempty"`
}

// part is one uploaded part.
type part struct {
	Number int32  `json:"number"`
	ETag   string `json:"etag"`
}

type upload struct {
	s  *Store
	ss *session.Session[multipart]
}

// BeginUpload starts a session.
func (s *Store) BeginUpload(ctx context.Context, opts ...blob.UploadOption) (blob.Upload, error) {
	ss, err := session.Begin[multipart](ctx, s.sessions, opts...)
	if err != nil {
		return nil, fmt.Errorf("blob/s3: %w", err)
	}
	return &upload{s: s, ss: ss}, nil
}

// ResumeUpload reopens a session.
func (s *Store) ResumeUpload(ctx context.Context, id string) (blob.Upload, error) {
	ss, err := session.Resume[multipart](ctx, s.sessions, id)
	if err != nil {
		return nil, fmt.Errorf("blob/s3: %w", err)
	}
	return &upload{s: s, ss: ss}, nil
}

func (u *upload) ID() string  { return u.ss.ID }
func (u *upload) Size() int64 { return u.ss.Size }

// Append buffers r and uploads every full part.
func (u *upload) Append(ctx context.Context, r io.Reader) (int64, error) {
	n, err := u.ss.Append(ctx, r, u.s.partSize, u.flushPart)
	if err != nil {
		return n, fmt.Errorf("blob/s3: append: %w", err)
	}
	return n, nil
}

// flushPart uploads data as the next part, starting the multipart upload on
// the first one.
func (u *upload) flushPart(ctx context.Context, data []byte) error {
	mp := &u.ss.Data
	if mp.UploadID == "" {
		key := u.s.tempKey(u.ss.ID)
		if u.ss.Expected != "" {
			// The digest is declared, so the parts can target the content key
			// and commit needs no copy. Commit verifies before completing, so
			// a wrong declaration never reaches that key.
			key = u.s.blobKey(u.ss.Expected)
		}
		out, err := u.s.api.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
			Bucket: &u.s.bucket, Key: &key,
		})
		if err != nil {
			return fmt.Errorf("blob/s3: start multipart: %w", err)
		}
		mp.Key, mp.UploadID = key, aws.ToString(out.UploadId)
		if err := u.ss.Save(ctx); err != nil {
			return err //nolint:wrapcheck // names the session
		}
	}
	num := int32(len(mp.Parts) + 1) // #nosec G115 -- S3 caps uploads at 10,000 parts
	out, err := u.s.api.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: &u.s.bucket, Key: &mp.Key, UploadId: &mp.UploadID,
		PartNumber: &num, Body: bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	})
	if err != nil {
		return fmt.Errorf("blob/s3: upload part %d: %w", num, translate(err))
	}
	mp.Parts = append(mp.Parts, part{Number: num, ETag: aws.ToString(out.ETag)})
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
	if err := u.ss.Live(ctx); err != nil {
		return blob.Info{}, fmt.Errorf("blob/s3: %w", err)
	}
	got, err := u.ss.Verify(want)
	if err != nil {
		_ = u.Abort(ctx)
		return blob.Info{}, err
	}
	mp := &u.ss.Data
	info := blob.Info{Size: u.ss.Size, Digests: got}
	target := u.s.blobKey(got.SHA256)
	if mp.UploadID == "" {
		_, err := u.s.writeOnce(ctx, target, u.ss.Size, func(ifNoneMatch *string) error {
			_, err := u.s.api.PutObject(ctx, &s3.PutObjectInput{
				Bucket: &u.s.bucket, Key: &target, Body: bytes.NewReader(u.ss.Pending),
				ContentLength: aws.Int64(int64(len(u.ss.Pending))), IfNoneMatch: ifNoneMatch,
			})
			return err //nolint:wrapcheck // writeOnce names the write
		})
		if err != nil {
			return blob.Info{}, fmt.Errorf("blob/s3: put %s: %w", got.SHA256, err)
		}
		return info, u.forget(ctx)
	}
	if len(u.ss.Pending) > 0 {
		if err := u.ss.Flush(ctx, u.flushPart); err != nil {
			return blob.Info{}, err
		}
		if err := u.ss.Save(ctx); err != nil {
			return blob.Info{}, fmt.Errorf("blob/s3: %w", err)
		}
	}
	if mp.Key == target {
		// The parts target the content key, so completing them is the write.
		adopted, err := u.s.writeOnce(ctx, target, u.ss.Size, func(ifNoneMatch *string) error {
			return u.complete(ctx, ifNoneMatch)
		})
		if err != nil {
			return blob.Info{}, err
		}
		if adopted {
			// A refused completion leaves the upload open; its parts are
			// not needed.
			return info, u.Abort(ctx)
		}
		return info, u.forget(ctx)
	}
	if err := u.complete(ctx, nil); err != nil {
		return blob.Info{}, err
	}
	if _, err := u.s.writeOnce(ctx, target, u.ss.Size, func(ifNoneMatch *string) error {
		return u.s.copy(ctx, mp.Key, target, u.ss.Size, ifNoneMatch)
	}); err != nil {
		return blob.Info{}, err
	}
	if _, err := u.s.api.DeleteObject(
		ctx,
		&s3.DeleteObjectInput{Bucket: &u.s.bucket, Key: &mp.Key},
	); err != nil {
		return blob.Info{}, fmt.Errorf("blob/s3: remove temporary %s: %w", mp.Key, err)
	}
	return info, u.forget(ctx)
}

// writeOnce runs write as a must-not-exist write (If-None-Match: *) of
// key and reports whether key was already taken, in which case the object
// there is adopted when it is size bytes long.
//
// Keys are content addressed, so the condition never guards correctness,
// only work and races: a store that ignores it (MinIO does on CopyObject)
// overwrites with the same bytes, and a store that refuses it as
// NotImplemented is written once more without it and never asked again.
func (s *Store) writeOnce(
	ctx context.Context,
	key string,
	size int64,
	write func(ifNoneMatch *string) error,
) (bool, error) {
	cond := aws.String("*")
	if s.unconditional.Load() {
		cond = nil
	}
	err := write(cond)
	if cond != nil && isNotImplemented(err) {
		s.unconditional.Store(true)
		err = write(nil)
	}
	switch {
	case err == nil:
		return false, nil
	case errors.Is(translate(err), errPrecondition):
		return true, s.adopt(ctx, key, size)
	default:
		return false, err
	}
}

// adopt accepts the object already stored under key when it is size bytes
// long. Its key is its SHA-256, and an object reaches a content key only
// after that digest has been verified, so the size is what is left to tell
// a whole blob from a damaged one.
func (s *Store) adopt(ctx context.Context, key string, size int64) error {
	out, err := s.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &s.bucket, Key: &key})
	if err != nil {
		return fmt.Errorf("blob/s3: check existing %s: %w", key, translate(err))
	}
	if got := aws.ToInt64(out.ContentLength); got != size {
		return fmt.Errorf(
			"blob/s3: %s exists with other content (%d bytes, want %d): %w",
			key,
			got,
			size,
			errPrecondition,
		)
	}
	return nil
}

// complete completes the multipart upload, as a must-not-exist write when
// ifNoneMatch is set.
func (u *upload) complete(ctx context.Context, ifNoneMatch *string) error {
	mp := &u.ss.Data
	parts := make([]types.CompletedPart, len(mp.Parts))
	for i, p := range mp.Parts {
		parts[i] = types.CompletedPart{PartNumber: aws.Int32(p.Number), ETag: aws.String(p.ETag)}
	}
	_, err := u.s.api.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: &u.s.bucket, Key: &mp.Key, UploadId: &mp.UploadID,
		MultipartUpload: &types.CompletedMultipartUpload{Parts: parts},
		IfNoneMatch:     ifNoneMatch,
	})
	if err != nil {
		return fmt.Errorf("blob/s3: complete multipart: %w", translate(err))
	}
	return nil
}

func (u *upload) forget(ctx context.Context) error {
	if err := u.ss.Forget(ctx); err != nil {
		return fmt.Errorf("blob/s3: %w", err)
	}
	return nil
}

// Abort discards the session and any uploaded parts.
func (u *upload) Abort(ctx context.Context) error {
	if mp := u.ss.Data; mp.UploadID != "" {
		_, err := u.s.api.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
			Bucket: &u.s.bucket, Key: &mp.Key, UploadId: &mp.UploadID,
		})
		if err != nil && !errors.Is(translate(err), blob.ErrUploadUnknown) {
			return fmt.Errorf("blob/s3: abort multipart: %w", err)
		}
	}
	return u.forget(ctx)
}

// copy moves src to dst, in one CopyObject when S3 allows it and as a
// multipart copy above the single-copy limit, as a must-not-exist write of
// dst when ifNoneMatch is set.
func (s *Store) copy(ctx context.Context, src, dst string, size int64, ifNoneMatch *string) error {
	source := s.bucket + "/" + src
	if size <= copyLimit {
		_, err := s.api.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket: &s.bucket, Key: &dst, CopySource: &source, IfNoneMatch: ifNoneMatch,
		})
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
		IfNoneMatch:     ifNoneMatch,
	})
	if err != nil {
		// A refused or failed completion leaves the copy's parts behind.
		_, _ = s.api.AbortMultipartUpload(
			ctx,
			&s3.AbortMultipartUploadInput{Bucket: &s.bucket, Key: &dst, UploadId: id},
		)
		return fmt.Errorf("blob/s3: complete multipart copy: %w", err)
	}
	return nil
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

// Signs reports whether the store signs URLs for via: storage URLs when it
// has a presign client, CDN URLs when it has a CDN signer.
func (s *Store) Signs(via blob.Via) bool {
	if via == blob.ViaCDN {
		return s.cdn != nil
	}
	return s.presign != nil
}

// SignURL returns a time-limited GET URL, carrying o's filename and media
// type as response overrides.
func (s *Store) SignURL(ctx context.Context, sha string, o blob.SignOptions) (string, error) {
	if err := blob.ValidateSHA256(sha); err != nil {
		return "", err //nolint:wrapcheck // already a blob sentinel
	}
	if !s.Signs(o.Via) {
		return "", fmt.Errorf("blob/s3: sign %s via %s: %w", sha, o.Via, blob.ErrCannotSign)
	}
	if o.Via == blob.ViaCDN {
		u, err := s.cdn.Sign(s.blobKey(sha), cdn.Request{
			TTL: o.TTL, Filename: o.Filename, ContentType: o.ContentType,
		})
		if err != nil {
			return "", fmt.Errorf("blob/s3: sign %s via %s: %w", sha, s.cdn.Provider(), err)
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
	key := s.blobKey(sha)
	in := &s3.GetObjectInput{Bucket: &s.bucket, Key: &key}
	if o.Filename != "" {
		in.ResponseContentDisposition = aws.String(blob.Disposition(o.Filename))
	}
	if o.ContentType != "" {
		in.ResponseContentType = aws.String(o.ContentType)
	}
	req, err := s.presign.PresignGetObject(ctx, in,
		func(p *s3.PresignOptions) { p.Expires = o.TTL })
	if err != nil {
		return "", fmt.Errorf("blob/s3: sign %s: %w", sha, err)
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
	case "PreconditionFailed":
		return fmt.Errorf("%w: %w", errPrecondition, err)
	}
	return err
}

// isNotImplemented reports a store refusing a request it does not support,
// as an S3-compatible store without conditional writes may refuse
// If-None-Match.
func isNotImplemented(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "NotImplemented"
}
