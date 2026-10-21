package item

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Remote ingestion errors are source failures, never negative cache entries.
var (
	ErrRemoteLength           = errors.New("item: remote response length mismatch")
	ErrRemoteMIME             = errors.New("item: remote response MIME mismatch")
	ErrRemoteLimit            = errors.New("item: remote response exceeds limit")
	ErrRemoteChecksumRequired = errors.New("item: upstream checksum required")
)

// CacheUpload is a complete upstream response awaiting validation/publication.
// Token was obtained before network I/O. CheckCurrent rechecks authorization and
// effective proxy/certificate revision without network I/O in the transaction.
type CacheUpload struct {
	Token      types.RemoteCacheToken
	Repo       repo.Config
	Body       io.Reader
	Kind       types.RemoteKind
	Declared   types.Checksums
	Properties types.Properties
	Length     int64 // -1 means unknown; zero requires an empty body
	// ExpectedContentType is a trusted format-adapter hint for raw metadata
	// whose route has no extension (for example a PyPI HTML simple index).
	// Never derive it from the upstream Content-Type or arbitrary caller Accept.
	ExpectedContentType string
	ContentType         string
	ETag                string
	LastModified        string
	CacheControl        string
	Vary                string
	By                  string
	NoStore             bool
	// MaxBytes bounds transient storage and parsed metadata, zero means the
	// default 64MiB for metadata and no additional artifact size limit.
	MaxBytes     int64
	CheckCurrent func(context.Context, store.Store) error
}

// CacheResult is either committed content or an owned transient reader. Close
// Body after a no-store response; closing removes its temporary file.
type CacheResult struct {
	Entry types.RemoteCacheEntry
	Item  types.Item
	Info  blob.Info
	Body  io.ReadSeekCloser
}

// Cache streams and validates outside SQL, then atomically publishes item/index
// hooks and raw cache state. A failed refresh never changes the former entry.
func (s *Service) Cache(ctx context.Context, u CacheUpload) (CacheResult, error) {
	var out CacheResult
	if err := u.validate(); err != nil {
		return out, err
	}
	body := bufio.NewReader(u.Body)
	if err := remoteMIME(u, body); err != nil {
		return out, err
	}
	d, err := remoteDeploy(u)
	if err != nil {
		return out, err
	}
	if u.MaxBytes == 0 && u.Kind == types.RemoteMetadata {
		u.MaxBytes = 64 << 20
	}
	d.Body = body
	if u.MaxBytes > 0 {
		d.Body = io.LimitReader(body, u.MaxBytes+1)
	}
	if u.NoStore || u.Repo.StoreArtifactsLocally != nil && !*u.Repo.StoreArtifactsLocally ||
		strings.Contains(strings.ToLower(u.CacheControl), "no-store") {
		return s.cacheTransient(ctx, u, d)
	}
	info, err := s.Upload(ctx, d)
	if err != nil {
		return out, err
	}
	if err = remoteLength(u, info.Size); err != nil {
		return out, err
	}
	actual := digestsOf(info)
	var envInspection func(context.Context) (types.Item, error)
	if u.Kind != types.RemoteMetadata {
		env, err := s.inspected(ctx, d, actual, info.Size)
		if err != nil {
			return out, err
		}
		envInspection = func(ctx context.Context) (types.Item, error) {
			return s.putFile(ctx, env, d, actual, info.Size, d.Declared, s.now())
		}
	}
	err = s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := s.Store.RemoteCache().Check(ctx, u.Token); err != nil {
			return fmt.Errorf("cache fence: %w", err)
		}
		if u.CheckCurrent != nil {
			if err := u.CheckCurrent(ctx, s.Store); err != nil {
				return fmt.Errorf("cache current policy: %w", err)
			}
		}
		now := s.now()
		e := types.RemoteCacheEntry{
			Kind:         u.Kind,
			Blob:         types.Blob{Checksums: actual, Size: info.Size, Created: now},
			ETag:         u.ETag,
			LastModified: u.LastModified,
			ContentType:  u.ContentType,
			CacheControl: u.CacheControl,
			Vary:         u.Vary,
			FetchedAt:    now,
			ValidatedAt:  now,
			LastUsed:     now,
		}
		if envInspection != nil {
			it, err := envInspection(ctx)
			if err != nil {
				return err
			}
			out.Item = it
			e.ItemID = it.ID
		}
		var err error
		out.Entry, err = s.Store.RemoteCache().Put(ctx, u.Token, e)
		if err != nil {
			return fmt.Errorf("cache publish: %w", err)
		}
		if e.ItemID == 0 {
			return s.checkRawCacheBlob(ctx, actual.SHA256)
		}
		return nil
	})
	if err != nil {
		return CacheResult{}, fmt.Errorf("cache transaction: %w", err)
	}
	out.Info = info
	return out, nil
}

// Match item publication's GC fence. A revived tombstone can mean collection
// removed bytes after Upload committed its candidate. Only this exceptional
// path checks blob storage while SQL holds the new reference.
func (s *Service) checkRawCacheBlob(ctx context.Context, sha string) error {
	ref, err := s.Store.Blobs().Get(ctx, sha)
	if err != nil {
		return fmt.Errorf("cache blob reference: %w", err)
	}
	if ref.Deleted.IsZero() {
		return nil
	}
	exists, err := s.Blobs.Exists(ctx, sha)
	if err != nil {
		return fmt.Errorf("cache blob exists: %w", err)
	}
	if !exists {
		return ErrCollected
	}
	return nil
}

func (u CacheUpload) validate() error {
	if err := store.ValidateRemoteKey(u.Token.Key); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if u.Repo.Class != repo.Remote || u.Repo.Key != u.Token.Key.RepoKey || u.Body == nil ||
		u.Length < -1 ||
		u.MaxBytes < 0 {
		return fmt.Errorf("cache target: %w", store.ErrInvalid)
	}
	if err := store.ValidateRemoteEntry(types.RemoteCacheEntry{Kind: u.Kind}); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if u.Kind == "" || u.Kind == types.RemoteDerived {
		return fmt.Errorf("cache upstream kind: %w", store.ErrInvalid)
	}
	return nil
}

func remoteDeploy(u CacheUpload) (Deploy, error) {
	c := u.Repo
	declared := u.Declared
	switch c.RemoteRepoChecksumPolicyType {
	case "", repo.GenerateIfAbsent, repo.Fail:
		c.ChecksumPolicyType = repo.ClientChecksums
		if c.RemoteRepoChecksumPolicyType == repo.Fail && declared == (types.Checksums{}) {
			return Deploy{}, ErrRemoteChecksumRequired
		}
	case repo.IgnoreAndGenerate:
		c.ChecksumPolicyType = repo.ServerGeneratedChecksums
		declared = types.Checksums{}
	case repo.PassThru:
		c.ChecksumPolicyType = repo.ServerGeneratedChecksums
	default:
		return Deploy{}, fmt.Errorf("remote checksum policy: %w", store.ErrInvalid)
	}
	parsed, err := ParseChecksums(declared)
	if err != nil {
		return Deploy{}, err
	}
	return Deploy{
		Target: Target{Repo: c, Cached: true, Path: u.Token.Key.Path}, Declared: parsed,
		Properties: u.Properties, By: u.By, Trusted: parsed == (types.Checksums{}),
	}, nil
}

func remoteLength(u CacheUpload, n int64) error {
	if u.MaxBytes > 0 && n > u.MaxBytes {
		return ErrRemoteLimit
	}
	if u.Length >= 0 && n != u.Length {
		return ErrRemoteLength
	}
	return nil
}

func remoteMIME(u CacheUpload, body *bufio.Reader) error {
	if u.Repo.BlockMismatchingMimeTypes != nil && !*u.Repo.BlockMismatchingMimeTypes {
		return nil
	}
	expected, _, _ := mime.ParseMediaType(MimeType(u.Token.Key.Path))
	if u.ExpectedContentType != "" {
		var err error
		expected, _, err = mime.ParseMediaType(u.ExpectedContentType)
		if u.Kind != types.RemoteMetadata || err != nil || !strings.Contains(expected, "/") ||
			strings.Contains(expected, "*") {
			return ErrRemoteMIME
		}
	}
	got := u.ContentType
	if got != "" {
		var err error
		got, _, err = mime.ParseMediaType(got)
		if err != nil || !strings.Contains(got, "/") {
			return ErrRemoteMIME
		}
	}
	prefix, err := body.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("cache sniff: %w", err)
	}
	sniff, _, _ := mime.ParseMediaType(http.DetectContentType(prefix))
	for _, typ := range []string{got, sniff} {
		if typ == "" || typ == expected || typ == "application/octet-stream" {
			continue
		}
		allowed := false
		for _, override := range strings.Split(u.Repo.MismatchingMimeTypesOverrideList, ",") {
			if strings.TrimSpace(override) == typ {
				allowed = true
			}
		}
		if allowed {
			continue
		}
		// HTML error pages must never become archives, even when an upstream
		// incorrectly labels them application/octet-stream or application/zip.
		if typ == "text/html" || typ == "application/xhtml+xml" {
			return ErrRemoteMIME
		}
		if typ == got && expected != "application/octet-stream" &&
			!strings.HasPrefix(typ, "text/plain") &&
			!sameArchiveMIME(typ, expected) {
			return ErrRemoteMIME
		}
	}
	return nil
}

func sameArchiveMIME(a, b string) bool {
	return (a == "application/zip" || a == "application/java-archive") &&
		(b == "application/zip" || b == "application/java-archive") ||
		(a == "application/gzip" || a == "application/x-gzip") &&
			(b == "application/gzip" || b == "application/x-gzip")
}

type transientCache struct{ *os.File }

func (t transientCache) Close() error { return errors.Join(t.File.Close(), os.Remove(t.Name())) }

func (s *Service) cacheTransient(
	ctx context.Context,
	u CacheUpload,
	d Deploy,
) (CacheResult, error) {
	var out CacheResult
	f, err := os.CreateTemp("", "artifactweave-remote-*")
	if err != nil {
		return out, fmt.Errorf("cache temporary body: %w", err)
	}
	body := transientCache{f}
	keep := false
	defer func() {
		if !keep {
			_ = body.Close()
		}
	}()
	h := blob.NewHasher()
	n, err := io.Copy(io.MultiWriter(f, h), d.Body)
	if err != nil {
		return out, fmt.Errorf("cache transient copy: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return out, fmt.Errorf("cache transient: %w", err)
	}
	if err = remoteLength(u, n); err != nil {
		return out, err
	}
	digests := h.Sum()
	if clientPolicy(d.Repo) {
		if err = blob.Verify(
			blob.Digests{
				SHA256: d.Declared.SHA256,
				SHA512: d.Declared.SHA512,
				SHA1:   d.Declared.SHA1,
				MD5:    d.Declared.MD5,
			},
			digests,
		); err != nil {
			return out, ErrChecksumMismatch
		}
	}
	if u.CheckCurrent != nil {
		if err = u.CheckCurrent(ctx, s.Store); err != nil {
			return out, fmt.Errorf("cache transient policy: %w", err)
		}
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return out, fmt.Errorf("cache transient rewind: %w", err)
	}
	keep = true
	out.Body = body
	out.Info = blob.Info{Digests: digests, Size: n}
	return out, nil
}
