// Package item writes and removes items: the deploy flow of decision 0004
// (content into the blob store, then the item and its blob reference in one
// transaction), checksum deploy, folders, client checksums and deletion into
// the trash can (decision 0010). Every write calls the repository format's
// hooks (internal/format) inside the same transaction; a format indexes what
// it deploys, and a delete prunes the package index for every format.
package item

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Errors the HTTP layer maps onto statuses.
var (
	// ErrChecksumMismatch is a declared checksum that differs from the
	// content under the client-checksums policy (409).
	ErrChecksumMismatch = errors.New("item: checksum mismatch")
	// ErrBadChecksum is a declared checksum that is not hex of a known
	// length (400).
	ErrBadChecksum = errors.New("item: malformed checksum")
	// ErrNoBlob is a checksum deploy for content the server does not hold
	// (404, after which the client uploads the content).
	ErrNoBlob = errors.New("item: no stored content has the checksum")
	// ErrConflict is a file where a folder must go, or the reverse (409).
	ErrConflict = errors.New("item: path conflict")
	// ErrCollected is content garbage collection removed while it was being
	// deployed: nothing was stored, and the deploy must be repeated (503,
	// which the JFrog CLI retries).
	ErrCollected = errors.New(
		"item: the content was removed by garbage collection while it was deployed; deploy it again",
	)
	// ErrNotFound is a missing item.
	ErrNotFound = store.ErrNotFound
)

// Service writes items into a store and its blob store.
type Service struct {
	Store store.Store
	Blobs blob.Store
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

// Target is where a write lands: a repository (or its remote cache) and a
// repository-relative path without a leading slash.
type Target struct {
	Repo   repo.Config
	Cached bool
	Path   string
}

// Deploy is a file write.
type Deploy struct {
	Target
	// Body is streamed into the blob store; a checksum deploy ignores it.
	Body io.Reader
	// Declared holds the checksums the client sent.
	Declared types.Checksums
	// Properties are set on the item (matrix parameters).
	Properties types.Properties
	By         string
}

// ParseChecksums checks declared checksums: lowercase hex of the length of
// their algorithm, or empty.
func ParseChecksums(c types.Checksums) (types.Checksums, error) {
	out := types.Checksums{
		SHA256: strings.ToLower(strings.TrimSpace(c.SHA256)),
		SHA1:   strings.ToLower(strings.TrimSpace(c.SHA1)),
		MD5:    strings.ToLower(strings.TrimSpace(c.MD5)),
	}
	for _, f := range []struct {
		v    string
		size int
	}{{out.SHA256, 64}, {out.SHA1, 40}, {out.MD5, 32}} {
		if f.v == "" {
			continue
		}
		if _, err := hex.DecodeString(f.v); err != nil || len(f.v) != f.size {
			return out, fmt.Errorf("%w: %q", ErrBadChecksum, f.v)
		}
	}
	return out, nil
}

// clientPolicy reports whether declared checksums must match: everywhere but
// a local repository set to server-generated-checksums, where a mismatch is
// recorded for the bad-checksum search instead (decision 0004).
func clientPolicy(c repo.Config) bool {
	return c.ChecksumPolicyType != repo.ServerGeneratedChecksums
}

// Deploy streams d.Body into the blob store and stores the item.
func (s *Service) Deploy(ctx context.Context, d Deploy) (types.Item, error) {
	declared, err := ParseChecksums(d.Declared)
	if err != nil {
		return types.Item{}, err
	}
	// A declared SHA-256 that the repository enforces lets the object stores
	// write the content key directly: S3 and Azure then skip the copy out of
	// a temporary object. Under server-generated checksums a mismatch is only
	// recorded, so there the digest must not become a commit condition.
	var opts []blob.UploadOption
	if clientPolicy(d.Repo) && declared.SHA256 != "" {
		opts = append(opts, blob.WithExpectedSHA256(declared.SHA256))
	}
	u, err := s.Blobs.BeginUpload(ctx, opts...)
	if err != nil {
		return types.Item{}, fmt.Errorf("item: begin upload: %w", err)
	}
	if _, err := u.Append(ctx, d.Body); err != nil {
		_ = u.Abort(context.WithoutCancel(ctx))
		return types.Item{}, fmt.Errorf("item: upload: %w", err)
	}
	var want blob.Digests
	if clientPolicy(d.Repo) {
		want = blob.Digests{SHA256: declared.SHA256, SHA1: declared.SHA1, MD5: declared.MD5}
	}
	info, err := u.Commit(ctx, want)
	if errors.Is(err, blob.ErrDigestMismatch) {
		return types.Item{}, ErrChecksumMismatch
	}
	if err != nil {
		_ = u.Abort(context.WithoutCancel(ctx))
		return types.Item{}, fmt.Errorf("item: commit upload: %w", err)
	}
	actual := types.Checksums{
		SHA256: info.Digests.SHA256, SHA1: info.Digests.SHA1, MD5: info.Digests.MD5,
	}
	stored, err := s.storeFile(ctx, d, actual, info.Size, declared)
	if errors.Is(err, store.ErrDeleting) {
		s.finishCollection(ctx, actual.SHA256)
	}
	return stored, err
}

// finishCollection completes a garbage collection that is removing sha's
// content, as the collector would. A deleting row has no references and can
// gain none, so whoever finishes the removal is safe; doing it here means a
// collector that crashed halfway refuses deploys of this content once rather
// than until its next pass. Best effort: the collector finishes what fails.
func (s *Service) finishCollection(ctx context.Context, sha string) {
	ctx = context.WithoutCancel(ctx)
	if err := s.Blobs.Delete(ctx, sha); err != nil && !errors.Is(err, blob.ErrNotFound) {
		return
	}
	_ = s.Store.Blobs().FinishDelete(ctx, sha, s.now())
}

// DeployByChecksum stores the item from content the server already holds,
// found by the strongest declared checksum. Every other declared checksum
// must match too; otherwise, and when the content is gone, ErrNoBlob.
func (s *Service) DeployByChecksum(ctx context.Context, d Deploy) (types.Item, error) {
	declared, err := ParseChecksums(d.Declared)
	if err != nil {
		return types.Item{}, err
	}
	if declared == (types.Checksums{}) {
		return types.Item{}, fmt.Errorf("%w: a checksum deploy needs a checksum", ErrBadChecksum)
	}
	b, err := s.Store.Blobs().Find(ctx, declared)
	if errors.Is(err, store.ErrNotFound) {
		return types.Item{}, ErrNoBlob
	}
	if err != nil {
		return types.Item{}, fmt.Errorf("item: find blob: %w", err)
	}
	for _, pair := range [][2]string{
		{declared.SHA256, b.Checksums.SHA256},
		{declared.SHA1, b.Checksums.SHA1},
		{declared.MD5, b.Checksums.MD5},
	} {
		if pair[0] != "" && pair[0] != pair[1] {
			return types.Item{}, ErrNoBlob
		}
	}
	// The index can name content that garbage collection already removed.
	if ok, err := s.Blobs.Exists(ctx, b.Checksums.SHA256); err != nil {
		return types.Item{}, fmt.Errorf("item: blob exists: %w", err)
	} else if !ok {
		return types.Item{}, ErrNoBlob
	}
	stored, err := s.storeFile(ctx, d, b.Checksums, b.Size, declared)
	if errors.Is(err, ErrCollected) {
		// Collected since it was found: a miss, so the client uploads it.
		return types.Item{}, ErrNoBlob
	}
	return stored, err
}

// storeFile writes the item for stored content in one transaction (putFile).
func (s *Service) storeFile(
	ctx context.Context,
	d Deploy,
	actual types.Checksums,
	size int64,
	declared types.Checksums,
) (types.Item, error) {
	var stored types.Item
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		var err error
		stored, err = s.putFile(ctx, d, actual, size, declared, s.now())
		return err
	})
	if err != nil {
		return types.Item{}, err //nolint:wrapcheck // the transaction returns ours
	}
	return stored, nil
}

// putFile writes, inside the caller's transaction, the item for stored
// content: parent folders, the item (replacing a file at the path), the blob
// reference (and the release of an overwritten file's), the properties and
// the format hook.
func (s *Service) putFile(
	ctx context.Context,
	d Deploy,
	actual types.Checksums,
	size int64,
	declared types.Checksums,
	now time.Time,
) (types.Item, error) {
	full := strings.Trim(d.Path, "/")
	if err := s.ensureFolders(ctx, d.Target, path.Dir(full), d.By, now); err != nil {
		return types.Item{}, err
	}
	items := s.Store.Items()
	it := types.Item{
		RepoKey: d.Repo.Key, Cached: d.Cached, Name: full,
		Size: size, Actual: actual, Original: declared, MimeType: MimeType(full),
		Created: now, CreatedBy: d.By, Modified: now, ModifiedBy: d.By, Updated: now,
	}
	old, err := items.Get(ctx, d.Repo.Key, d.Cached, full)
	switch {
	case err == nil && old.Folder:
		return types.Item{}, fmt.Errorf("%w: %s is a folder", ErrConflict, full)
	case err == nil:
		it.Created, it.CreatedBy = old.Created, old.CreatedBy
	case !errors.Is(err, store.ErrNotFound):
		return types.Item{}, fmt.Errorf("item: get %s: %w", full, err)
	}
	stored, err := items.Put(ctx, it)
	if err != nil {
		return types.Item{}, fmt.Errorf("item: put %s: %w", full, err)
	}
	ref, err := s.Store.Blobs().Ref(ctx, types.Blob{
		Checksums: actual, Size: size, Created: now,
	}, 1)
	if errors.Is(err, store.ErrDeleting) {
		return types.Item{}, fmt.Errorf("%w: %w", ErrCollected, err)
	}
	if err != nil {
		return types.Item{}, fmt.Errorf("item: reference blob: %w", err)
	}
	// Content garbage collection removed recently may have gone after this
	// deploy wrote it: the row only says when it went. Checked inside the
	// transaction, so a miss rolls the whole deploy back; content never
	// collected costs nothing here.
	if !ref.Deleted.IsZero() {
		ok, err := s.Blobs.Exists(ctx, actual.SHA256)
		if err != nil {
			return types.Item{}, fmt.Errorf("item: blob exists: %w", err)
		}
		if !ok {
			return types.Item{}, ErrCollected
		}
	}
	if old.ID != 0 && old.Actual.SHA256 != "" {
		if _, err := s.Store.Blobs().Unref(ctx, old.Actual.SHA256, 1); err != nil {
			return types.Item{}, fmt.Errorf("item: release overwritten blob: %w", err)
		}
	}
	if len(d.Properties) > 0 {
		if err := items.PatchProperties(ctx, stored.ID, d.Properties, nil); err != nil {
			return types.Item{}, fmt.Errorf("item: properties of %s: %w", full, err)
		}
	}
	if err := format.For(d.Repo.PackageType).Deployed(ctx, s.Store, d.Repo, stored); err != nil {
		return types.Item{}, err //nolint:wrapcheck // the format's own error
	}
	return stored, nil
}

// MakeFolder creates the folder at t.Path and its parents; an existing
// folder is returned as it is, with props added.
func (s *Service) MakeFolder(
	ctx context.Context,
	t Target,
	props types.Properties,
	by string,
) (types.Item, error) {
	now := s.now()
	full := strings.Trim(t.Path, "/")
	var folder types.Item
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		if err := s.ensureFolders(ctx, t, full, by, now); err != nil {
			return err
		}
		var err error
		if folder, err = s.Store.Items().Get(ctx, t.Repo.Key, t.Cached, full); err != nil {
			return fmt.Errorf("item: get %s: %w", full, err)
		}
		if len(props) > 0 {
			if err := s.Store.Items().PatchProperties(ctx, folder.ID, props, nil); err != nil {
				return fmt.Errorf("item: properties of %s: %w", full, err)
			}
		}
		return nil
	})
	if err != nil {
		return types.Item{}, err //nolint:wrapcheck // the transaction returns ours
	}
	return folder, nil
}

// ensureFolders creates every missing folder from the root down to dir
// ("." or "" is the root, which is not an item).
func (s *Service) ensureFolders(
	ctx context.Context,
	t Target,
	dir, by string,
	now time.Time,
) error {
	dir = strings.Trim(dir, "/")
	if dir == "" || dir == "." {
		return nil
	}
	segments := strings.Split(dir, "/")
	for i := range segments {
		p := strings.Join(segments[:i+1], "/")
		it, err := s.Store.Items().Get(ctx, t.Repo.Key, t.Cached, p)
		switch {
		case err == nil && !it.Folder:
			return fmt.Errorf("%w: %s is a file", ErrConflict, p)
		case err == nil:
			continue
		case !errors.Is(err, store.ErrNotFound):
			return fmt.Errorf("item: get %s: %w", p, err)
		}
		if _, err := s.Store.Items().Put(ctx, types.Item{
			RepoKey: t.Repo.Key, Cached: t.Cached, Name: p, Folder: true,
			Created: now, CreatedBy: by, Modified: now, ModifiedBy: by, Updated: now,
		}); err != nil {
			return fmt.Errorf("item: create folder %s: %w", p, err)
		}
	}
	return nil
}

// Algorithm names a checksum sidecar's digest.
type Algorithm string

// Sidecar algorithms, by file extension. SHA-512 is not recorded, so its
// sidecar is not served.
const (
	SHA1   Algorithm = "sha1"
	SHA256 Algorithm = "sha256"
	MD5    Algorithm = "md5"
)

// Sidecar splits a checksum sidecar path ("a/b.jar.sha1") into the
// artifact's path and the algorithm; ok is false for any other path.
func Sidecar(p string) (artifact string, a Algorithm, ok bool) {
	for _, alg := range []Algorithm{SHA1, SHA256, MD5} {
		if base, found := strings.CutSuffix(p, "."+string(alg)); found && base != "" &&
			!strings.HasSuffix(base, "/") {
			return base, alg, true
		}
	}
	return "", "", false
}

// Of returns the digest a of c.
func (a Algorithm) Of(c types.Checksums) string {
	switch a {
	case SHA1:
		return c.SHA1
	case SHA256:
		return c.SHA256
	default:
		return c.MD5
	}
}

func (a Algorithm) set(c *types.Checksums, v string) {
	switch a {
	case SHA1:
		c.SHA1 = v
	case SHA256:
		c.SHA256 = v
	default:
		c.MD5 = v
	}
}

// RecordChecksum handles a checksum sidecar upload for the file at t.Path:
// the client's value is recorded as the file's original checksum, and under
// the client-checksums policy it must equal the content's (decision 0004).
func (s *Service) RecordChecksum(
	ctx context.Context,
	t Target,
	a Algorithm,
	value string,
) (types.Item, error) {
	var declared types.Checksums
	a.set(&declared, value)
	declared, err := ParseChecksums(declared)
	if err != nil {
		return types.Item{}, err
	}
	value = a.Of(declared)
	var stored types.Item
	err = s.Store.Tx(ctx, func(ctx context.Context) error {
		it, err := s.Store.Items().Get(ctx, t.Repo.Key, t.Cached, t.Path)
		if err != nil {
			return fmt.Errorf("item: get %s: %w", t.Path, err)
		}
		if it.Folder {
			return fmt.Errorf("%w: %s is a folder", ErrConflict, t.Path)
		}
		if value != a.Of(it.Actual) && clientPolicy(t.Repo) {
			return ErrChecksumMismatch
		}
		a.set(&it.Original, value)
		if stored, err = s.Store.Items().Put(ctx, it); err != nil {
			return fmt.Errorf("item: put %s: %w", t.Path, err)
		}
		return nil
	})
	if err != nil {
		return types.Item{}, err //nolint:wrapcheck // the transaction returns ours
	}
	return stored, nil
}

// TrashEnabled reads the trash can setting; unset means on, as in
// Artifactory.
func TrashEnabled(ctx context.Context, st store.Store) (bool, error) {
	var t types.TrashcanSettings
	err := st.Settings().Get(ctx, types.SettingTrashcan, &t)
	if errors.Is(err, store.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("item: trash can setting: %w", err)
	}
	return t.Enabled, nil
}

// Delete removes the item at t.Path and everything below it ("" is the
// repository root). Files of local repositories go to the trash can when it
// is on, keeping their blob references for a restore; otherwise, and for
// remote caches, the references are released for garbage collection.
func (s *Service) Delete(ctx context.Context, t Target, by string) (store.DeleteResult, error) {
	full := strings.Trim(t.Path, "/")
	trash := !t.Cached
	if trash {
		on, err := TrashEnabled(ctx, s.Store)
		if err != nil {
			return store.DeleteResult{}, err
		}
		trash = on
	}
	now := s.now()
	var res store.DeleteResult
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		removed, err := s.affected(ctx, t, full)
		if err != nil {
			return err
		}
		hooks := format.For(t.Repo.PackageType)
		var versions []types.PackageVersion
		for _, it := range removed {
			if !it.Folder {
				if err := hooks.Deleting(ctx, s.Store, t.Repo, it); err != nil {
					return err //nolint:wrapcheck // the format's own error
				}
				pv, ok, err := pkgindex.VersionOf(ctx, s.Store, it.ID)
				if err != nil {
					return err //nolint:wrapcheck // pkgindex wraps its own
				}
				if ok {
					versions = append(versions, pv)
				}
			}
			if !trash {
				continue
			}
			props, err := s.Store.Items().Properties(ctx, it.ID)
			if err != nil {
				return fmt.Errorf("item: properties of %s: %w", it.FullPath(), err)
			}
			if _, err := s.Store.Trash().Add(ctx, types.TrashEntry{
				Record:    types.TrashRecord{Item: it, Properties: props},
				DeletedAt: now, DeletedBy: by,
			}); err != nil {
				return fmt.Errorf("item: trash %s: %w", it.FullPath(), err)
			}
		}
		if res, err = s.Store.Items().Delete(ctx, t.Repo.Key, t.Cached, full); err != nil {
			return fmt.Errorf("item: delete %s: %w", full, err)
		}
		// Deleting an item drops its version links; versions left empty go.
		if err := pkgindex.Prune(ctx, s.Store, versions); err != nil {
			return err //nolint:wrapcheck // pkgindex wraps its own
		}
		if trash {
			return nil
		}
		for sha, n := range res.BlobRefs {
			if _, err := s.Store.Blobs().Unref(ctx, sha, n); err != nil {
				return fmt.Errorf("item: release blob %s: %w", sha, err)
			}
		}
		return nil
	})
	if err != nil {
		return store.DeleteResult{}, err //nolint:wrapcheck // the transaction returns ours
	}
	return res, nil
}

// Tree lists the item at t.Path (none for the repository root) and every
// item below it, parents before their children.
func (s *Service) Tree(ctx context.Context, t Target) ([]types.Item, error) {
	return s.affected(ctx, t, strings.Trim(t.Path, "/"))
}

// affected lists the item at full (none for the root) and every item below.
func (s *Service) affected(ctx context.Context, t Target, full string) ([]types.Item, error) {
	var out []types.Item
	if full != "" {
		it, err := s.Store.Items().Get(ctx, t.Repo.Key, t.Cached, full)
		if err != nil {
			return nil, fmt.Errorf("item: get %s: %w", full, err)
		}
		out = append(out, it)
		if !it.Folder {
			return out, nil
		}
	}
	page := store.Page{Limit: store.MaxLimit}
	for {
		batch, next, err := s.Store.Items().Descendants(ctx, t.Repo.Key, t.Cached, full, page)
		if err != nil {
			return nil, fmt.Errorf("item: list below %s: %w", full, err)
		}
		out = append(out, batch...)
		if next == "" {
			return out, nil
		}
		page.After = next
	}
}

// mimeTypes is a fixed table rather than mime.TypeByExtension, whose answer
// depends on the host's mime.types and would differ between servers.
var mimeTypes = map[string]string{
	".jar":  "application/java-archive",
	".war":  "application/java-archive",
	".pom":  "application/x-maven-pom+xml",
	".xml":  "application/xml",
	".json": "application/json",
	".txt":  "text/plain",
	".html": "text/html",
	".zip":  "application/zip",
	".gz":   "application/x-gzip",
	".tgz":  "application/x-gzip",
	".tar":  "application/x-tar",
}

// MimeType is the media type recorded for a file name.
func MimeType(name string) string {
	if t, ok := mimeTypes[strings.ToLower(path.Ext(name))]; ok {
		return t
	}
	return "application/octet-stream"
}
