package item

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
)

// Remote ingestion errors are source failures, never negative cache entries.
var (
	ErrRemoteLength           = errors.New("item: remote response length mismatch")
	ErrRemoteMIME             = errors.New("item: remote response mime mismatch")
	ErrRemoteLimit            = errors.New("item: remote response exceeds limit")
	ErrRemoteChecksumRequired = errors.New("item: upstream checksum required")
)

// CacheUpload is a complete upstream response awaiting validation/publication.
// Token was obtained before network I/O. CheckCurrent rechecks authorization and
// effective proxy/certificate revision without network I/O in the transaction.
type CacheUpload struct {
	// SmartProperties is a complete validated peer snapshot. Merge it inside
	// the publication transaction so a concurrent local annotation wins.
	SmartProperties        *model.Properties
	SmartPropertiesStarted time.Time
	// Expected comes from authoritative protocol metadata, not optional remote
	// headers. It is enforced even when their checksum policy says ignore.
	Expected kernel.Checksums
	// Validate inspects complete bytes outside SQL, before either publication
	// or transient delivery. It must not retain or close its reader.
	Validate   func(context.Context, io.ReadSeeker, int64) error
	Token      model.RemoteCacheToken
	Repo       repo.Config
	Body       io.Reader
	Kind       model.RemoteKind
	Declared   kernel.Checksums
	Properties model.Properties
	// PublishMetadata keeps protocol metadata in its native item/index layout
	// while retaining metadata expiry and zap behavior. Ordinary raw upstream
	// metadata deliberately stays outside the derived package tree.
	PublishMetadata bool
	Length          int64 // -1 means unknown; zero requires an empty body
	// ExpectedContentType is a trusted format-adapter hint for raw metadata
	// whose route has no extension (for example a PyPI HTML simple index).
	// Never derive it from the upstream Content-Type or arbitrary caller Accept.
	ExpectedContentType  string
	ExpectedContentTypes []string
	RemoteURL            string // sanitized origin, without userinfo or query
	ContentType          string
	ETag                 string
	LastModified         string
	CacheControl         string
	Vary                 string
	By                   string
	NoStore              bool
	// MaxBytes bounds transient storage and parsed metadata, zero means the
	// default 64MiB for metadata and no additional artifact size limit.
	MaxBytes     int64
	CheckCurrent func(context.Context, Store) error
	// ClientAccept is the client's Accept values for a file with no format
	// hint, which 7.x's MIME check takes the requested types from
	// (HttpRepo.getAcceptableHeaders); nil, as for a format route, takes
	// the path's type. Only that check reads it: it is not part of the
	// cache identity.
	ClientAccept []string
}

// CacheResult is either committed content or an owned transient reader. Close
// Body after a no-store response; closing removes its temporary file.
type CacheResult struct {
	Declared kernel.Checksums
	Entry    model.RemoteCacheEntry
	Item     model.Item
	Info     blob.Info
	Body     io.ReadSeekCloser
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
	if u.MaxBytes == 0 && u.Kind == model.RemoteMetadata {
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
	if err = verifyExpected(u.Expected, info.Digests); err != nil {
		return out, err
	}
	if err = s.validateCachedBody(ctx, u, info); err != nil {
		return out, err
	}
	actual := digestsOf(info)
	var envInspection func(context.Context) (model.Item, error)
	if u.Kind != model.RemoteMetadata || u.PublishMetadata {
		env, err := s.inspectedCopy(ctx, d, actual, info.Size)
		if err != nil {
			return out, err
		}
		envInspection = func(ctx context.Context) (model.Item, error) {
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
		e := model.RemoteCacheEntry{
			OriginalChecksums: d.Declared,
			Kind:              u.Kind,
			Blob:              blob.Blob{Checksums: actual, Size: info.Size, Created: now},
			ETag:              u.ETag,
			LastModified:      u.LastModified,
			ContentType:       u.ContentType,
			CacheControl:      u.CacheControl,
			Vary:              u.Vary,
			FetchedAt:         now,
			ValidatedAt:       now,
			LastUsed:          now,
		}
		if u.Kind == model.RemoteMetadata {
			e.MetadataBaseURL = u.RemoteURL
		}
		if envInspection != nil {
			var err error
			out.Item, err = s.cacheItem(ctx, u, &d, &e, envInspection)
			if err != nil {
				return err
			}
		}

		var err error
		out.Entry, err = s.Store.RemoteCache().Put(ctx, u.Token, e)
		if err != nil {
			return fmt.Errorf("cache publish: %w", err)
		}
		if err = s.PropagateRemotePropertyState(ctx, out.Entry); err != nil {
			return err
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

func (s *Service) cacheItem(
	ctx context.Context,
	u CacheUpload,
	d *Deploy,
	e *model.RemoteCacheEntry,
	inspect func(context.Context) (model.Item, error),
) (model.Item, error) {
	if u.SmartProperties != nil {
		d.shouldReplaceProperties = true
		var err error
		d.Properties, e.Smart, err = s.initialRemoteProperties(ctx, u)
		if err != nil {
			return model.Item{}, err
		}
	}

	it, err := inspect(ctx)
	if err != nil {
		return model.Item{}, err
	}
	it.RemoteURL = u.RemoteURL
	if modified, parseErr := http.ParseTime(u.LastModified); parseErr == nil {
		it.Modified = modified
	}
	it, err = s.Store.Items().Put(ctx, it)
	if err != nil {
		return model.Item{}, fmt.Errorf("cache origin: %w", err)
	}
	e.ItemID = it.ID
	if e.Smart == nil {
		old, readErr := s.Store.RemoteCache().Read(ctx, u.Token.Key)
		if readErr != nil && !errors.Is(readErr, kernel.ErrNotFound) {
			return model.Item{}, fmt.Errorf("cache smart state: %w", readErr)
		}
		if readErr == nil && old.Token.Source == u.Token.Source && old.ItemID == it.ID &&
			old.Blob.Checksums.SHA256 == e.Blob.Checksums.SHA256 {
			e.Smart = old.Smart
		}
	}
	return it, nil
}

func (s *Service) validateCachedBody(ctx context.Context, u CacheUpload, info blob.Info) error {
	if u.Validate == nil {
		return nil
	}
	r, _, err := s.Blobs.Open(ctx, info.Digests.SHA256)
	if err != nil {
		err = fmt.Errorf("cache validation body: %w", err)
		return MarkStorageFault(ctx, err, err)
	}
	defer r.Close()
	return checkCopy(ctx, u.Validate, r, info.Size)
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
	if len(u.ExpectedContentTypes) > 0 {
		if u.Kind != model.RemoteMetadata || u.ExpectedContentType != "" || u.Validate == nil ||
			len(u.ExpectedContentTypes) > 16 {
			return ErrRemoteMIME
		}
		seen := map[string]bool{}
		for _, raw := range u.ExpectedContentTypes {
			mt, params, err := mime.ParseMediaType(raw)
			if err != nil || mt != raw || len(params) != 0 || strings.Count(mt, "/") != 1 ||
				strings.Contains(mt, "*") ||
				seen[mt] {
				return ErrRemoteMIME
			}
			seen[mt] = true
		}
	}

	if err := port.ValidateRemoteKey(u.Token.Key); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if u.Repo.Class != repo.Remote || u.Repo.Key != u.Token.Key.RepoKey || u.Body == nil ||
		u.Length < -1 ||
		u.MaxBytes < 0 {
		return fmt.Errorf("cache target: %w", kernel.ErrInvalid)
	}
	if err := port.ValidateRemoteEntry(model.RemoteCacheEntry{Kind: u.Kind}); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if u.Kind == "" || u.Kind == model.RemoteDerived {
		return fmt.Errorf("cache upstream kind: %w", kernel.ErrInvalid)
	}
	return nil
}

func remoteDeploy(u CacheUpload) (Deploy, error) {
	c := u.Repo
	declared := u.Declared
	switch c.RemoteRepoChecksumPolicyType {
	case "", repo.GenerateIfAbsent, repo.Fail:
		c.ChecksumPolicyType = repo.ClientChecksums
		if c.RemoteRepoChecksumPolicyType == repo.Fail && declared == (kernel.Checksums{}) {
			return Deploy{}, ErrRemoteChecksumRequired
		}
	case repo.IgnoreAndGenerate:
		c.ChecksumPolicyType = repo.ServerGeneratedChecksums
		declared = kernel.Checksums{}
	case repo.PassThru:
		c.ChecksumPolicyType = repo.ServerGeneratedChecksums
	default:
		return Deploy{}, fmt.Errorf("remote checksum policy: %w", kernel.ErrInvalid)
	}
	parsed, err := ParseChecksums(declared)
	if err != nil {
		return Deploy{}, err
	}
	return Deploy{
		Target: Target{Repo: c, Cached: true, Path: u.Token.Key.Path}, Declared: parsed,
		Properties: u.Properties, By: u.By, Trusted: parsed == (kernel.Checksums{}),
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
	if len(u.ExpectedContentTypes) > 0 {
		return remoteAlternativeMIME(u, body)
	}
	return remoteExpectedMIME(u, body)
}

// defaultBlockedMIME is 7.x's repo.remote.blockedMismatchingMimeTypes
// (ConstantValues, the same in 6.20 and the 7.161.26 sources): the labels a
// remote refuses on a mismatch when its mismatchingMimeTypesOverrideList is
// blank.
const defaultBlockedMIME = "text/html,application/xhtml+xml"

// blockedMIME is 7.x's HttpRepo.blockMismatchingMimeTypes (7.161.26
// sources, unchanged since 6.20): an upstream's label refuses a file only
// when it is none of the requested types and is on the block list. The
// body is not read for its type, so a file an upstream labels as an archive
// is cached whatever its bytes; an archive format's own validator still
// reads them. Nothing is refused when the label is absent, */* or
// application/octet-stream, or the requested types include
// application/octet-stream, which no label can be said to mismatch.
//
// The override list is the block list itself, as the 7.x configuration
// schema describes mismatchingMimeTypesOverrideList ("mime types that will
// be blocked by this repo if the original request was for a different mime
// type"), not types let through despite it.
func blockedMIME(u CacheUpload, requested []string) error {
	got := mediaTypeOf(u.ContentType)
	if got == "" || got == "*/*" || got == "application/octet-stream" ||
		slices.Contains(requested, "application/octet-stream") || slices.Contains(requested, got) {
		return nil
	}
	blocked := u.Repo.MismatchingMimeTypesOverrideList
	if strings.TrimSpace(blocked) == "" {
		blocked = defaultBlockedMIME
	}
	for _, entry := range strings.Split(blocked, ",") {
		if mediaTypeOf(entry) == got {
			return &MIMERefusal{Requested: requested, Label: got}
		}
	}
	return nil
}

// MIMERefusal is ErrRemoteMIME with what 7.x's refusal names: the requested
// types, in the order its message lists them, and the upstream's label.
type MIMERefusal struct {
	Requested []string
	Label     string
}

func (e *MIMERefusal) Error() string { return ErrRemoteMIME.Error() }

// Is makes a refusal ErrRemoteMIME, which a format route answers as before.
func (e *MIMERefusal) Is(target error) bool { return target == ErrRemoteMIME }

// Message is HttpRepo.blockMismatchingMimeTypes's text for a refusal of the
// file at url. The list is a Java Set's toString, "[a, b]".
func (e *MIMERefusal) Message(url string) string {
	return "Error fetching " + url + ": Mismatching mime types found in request: [" +
		strings.Join(e.Requested, ", ") + "] and response: " + e.Label +
		", and this repo blocks " + e.Label + " on mismatch"
}

// RefuseLabel is the check Cache makes of a file with no format hint, for
// an upstream answer whose body is not cached, such as a HEAD's: a
// *MIMERefusal when c refuses label for path under the client's accept
// values, nil otherwise.
func RefuseLabel(c repo.Config, path, label string, accept []string) error {
	if c.BlockMismatchingMimeTypes != nil && !*c.BlockMismatchingMimeTypes {
		return nil
	}
	u := CacheUpload{Repo: c, ContentType: label, ClientAccept: accept}
	u.Token.Key.Path = path
	return blockedMIME(u, requestedMIME(u))
}

// requestedMIME is the requested types of 7.x's getAcceptableMimeTypes:
// every Accept value split on "," as Java's String.split splits it, without
// parameters, trimmed and lowercased, or the path's type when the client
// sent no Accept. */* is dropped first, so a client that accepts anything
// is checked against the path's type as one that sends no Accept is. 7.x
// skips the check for it instead, which JFrog's Known Issues page lists as
// RTDEV-46375, a fault this does not mirror [I].
//
// The types are in the order 7.x's message lists them: each Accept value's
// types are collected into a Java HashSet, and those into one more.
func requestedMIME(u CacheUpload) []string {
	all := make([]string, 0, len(u.ClientAccept))
	wildcard := false
	for _, value := range u.ClientAccept {
		var types []string
		for _, part := range javaSplitComma(value) {
			media, _, _ := strings.Cut(part, ";")
			media = strings.ToLower(strings.TrimSpace(media))
			if media == "*/*" {
				wildcard = true
				continue
			}
			types = append(types, media)
		}
		all = append(all, javaHashSetOrder(types)...)
	}
	if len(u.ClientAccept) == 0 || len(all) == 0 && wildcard {
		return []string{mediaTypeOf(MimeType(u.Token.Key.Path))}
	}
	return javaHashSetOrder(all)
}

// javaSplitComma is Java's value.split(","): a value without a comma is
// itself, and the empty strings at the end of a split are dropped.
func javaSplitComma(value string) []string {
	parts := strings.Split(value, ",")
	if len(parts) == 1 {
		return parts
	}
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// javaHashSetOrder is the order a default java.util.HashSet iterates
// values added in this order, without repeats: by bucket, the spread
// String.hashCode masked by a table that starts at 16 and doubles past a
// load of 0.75, and in insertion order within a bucket, which a resize
// keeps.
func javaHashSetOrder(values []string) []string {
	var unique []string
	table, size := uint32(16), uint32(0)
	for _, v := range values {
		if slices.Contains(unique, v) {
			continue
		}
		unique = append(unique, v)
		// HashMap.putVal: if (++size > threshold) resize().
		if size++; size > table/4*3 {
			table *= 2
		}
	}
	bucket := func(v string) uint32 {
		var h int32
		for _, unit := range utf16.Encode([]rune(v)) {
			h = 31*h + int32(unit)
		}
		spread := uint32(h) ^ uint32(h)>>16
		return spread & (table - 1)
	}
	slices.SortStableFunc(
		unique,
		func(a, b string) int { return cmp.Compare(bucket(a), bucket(b)) },
	)
	return unique
}

// mediaTypeOf is a Content-Type value's media type as 7.x reads a label or
// a block-list entry: what comes before any ";", lowercased. 7.x trims a
// block-list entry but not a label; both are trimmed here, since a label
// with a space before its parameters names the same type.
func mediaTypeOf(value string) string {
	media, _, _ := strings.Cut(value, ";")
	return strings.ToLower(strings.TrimSpace(media))
}

// remoteExpectedMIME holds a file with no format hint to 7.x's rule with
// the types requestedMIME reads from the client's Accept, or the path's
// type. A format route passes no Accept, so its files are checked against
// their path's type [I].
//
// Raw metadata whose format adapter expects a type (ExpectedContentType)
// is held to 7.x's rule with that type requested, and also to the
// adapter's stricter check, which sniffs the body: the adapter parses the
// bytes as that type, so an error page under any label stays out.
func remoteExpectedMIME(u CacheUpload, body *bufio.Reader) error {
	if u.ExpectedContentType == "" {
		return blockedMIME(u, requestedMIME(u))
	}
	expected, _, err := mime.ParseMediaType(u.ExpectedContentType)
	if u.Kind != model.RemoteMetadata || err != nil || !strings.Contains(expected, "/") ||
		strings.Contains(expected, "*") {
		return ErrRemoteMIME
	}
	if err := blockedMIME(u, []string{expected}); err != nil {
		return err
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
		if typ == "" || typ == expected || typ == "application/octet-stream" ||
			(expected == "application/json" && strings.HasPrefix(typ, "application/") && strings.HasSuffix(typ, "+json")) {
			continue
		}
		// An HTML error page must never become metadata the adapter does not
		// read as HTML, whatever the upstream labels it.
		if typ == "text/html" || typ == "application/xhtml+xml" {
			return ErrRemoteMIME
		}
		if typ == got && expected != "application/octet-stream" &&
			!strings.HasPrefix(typ, "text/plain") && !sameXMLMIME(typ, expected) {
			return ErrRemoteMIME
		}
	}
	return nil
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
	if err = verifyExpected(u.Expected, digests); err != nil {
		return out, err
	}
	if u.Validate != nil {
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return out, fmt.Errorf("cache validation rewind: %w", err)
		}
		if err = checkCopy(ctx, u.Validate, f, n); err != nil {
			return out, err
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

func verifyExpected(expected kernel.Checksums, actual blob.Digests) error {
	parsed, err := ParseChecksums(expected)
	if err != nil {
		return err
	}
	if err = blob.Verify(
		blob.Digests{
			SHA256: parsed.SHA256,
			SHA512: parsed.SHA512,
			SHA1:   parsed.SHA1,
			MD5:    parsed.MD5,
		},
		actual,
	); err != nil {
		return ErrChecksumMismatch
	}
	return nil
}

func sameXMLMIME(a, b string) bool {
	xml := func(v string) bool {
		return v == "application/xml" || v == "text/xml" || v == "application/x-maven-pom+xml"
	}
	return xml(a) && xml(b)
}

// Negotiated metadata uses an exact adapter allowlist. Sniffing JSON reports
// text/plain, so the complete adapter validator still decides its schema.
func remoteAlternativeMIME(u CacheUpload, body *bufio.Reader) error {
	got, _, err := mime.ParseMediaType(u.ContentType)
	if u.ContentType != "" && (err != nil || !slices.Contains(u.ExpectedContentTypes, got)) {
		return ErrRemoteMIME
	}
	prefix, err := body.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("cache sniff: %w", err)
	}
	sniff, _, _ := mime.ParseMediaType(http.DetectContentType(prefix))
	if slices.Contains(u.ExpectedContentTypes, sniff) {
		if sniff == "text/html" && got != "" && got != "text/html" &&
			!strings.HasSuffix(got, "+html") {
			return ErrRemoteMIME
		}
		return nil
	}
	if sniff == "text/plain" && (got == "application/json" || strings.HasSuffix(got, "+json")) {
		return nil
	}
	return ErrRemoteMIME
}
