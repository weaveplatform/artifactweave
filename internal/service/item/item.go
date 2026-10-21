// Package item writes and removes items: the deploy flow of decision 0004
// (content into the blob store, then the item and its blob reference in one
// transaction), checksum deploy, folders, client checksums and deletion into
// the trash can (decision 0010). Every write calls the repository format's
// hooks (internal/format, decision 0032): Inspect once the content is
// stored, then Admit and the hooks inside the write's transaction. A format
// indexes what it deploys, and a delete prunes the package index for every
// format.
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
	"github.com/weaveplatform/artifactweave/internal/format/maven"
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
	// ErrExists is a deploy with IfAbsent to a path that holds an item.
	ErrExists = errors.New("item: the path already holds an item")
)

// Service writes items into a store and its blob store.
type Service struct {
	Store store.Store
	Blobs blob.Store
	// Now supplies the time; nil means time.Now.
	Now func() time.Time
	// Formats returns a package type's format; nil means formats.For.
	// Tests substitute formats that record or fail their hooks.
	Formats func(repo.PackageType) format.Handler
	// Work takes the derived work formats ask for. Nil writes it to the
	// store's queue directly, where every node's workers find it on their
	// next poll; a queue also wakes its own idle workers on commit.
	Work Enqueuer
	// Periodic names the kinds of derived work RegisterWork runs on a timer
	// rather than as soon as they are due, each with its interval
	// (work.Queue.HandleEvery).
	Periodic map[string]time.Duration
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
	// ResolvePath derives a destination from the format inspection before any
	// transaction begins, for uploads whose content names their canonical path.
	// Its error refuses the write without publishing items or package metadata.
	ResolvePath func(inspection any) (string, error)
	// Declared holds the checksums the client sent.
	Declared types.Checksums
	// Properties are set on the item (matrix parameters).
	Properties types.Properties
	By         string
	// IfAbsent stores the file only when nothing is at its path, and
	// refuses with ErrExists otherwise, for a file published once, such as
	// an npm version's tarball. The check is made in the write's
	// transaction, after the content is stored.
	IfAbsent bool
	// CanOverwritePath, when set, is the caller's right to replace the file
	// at a full path. It is asked about the path the format admits, which a
	// format may rename (Maven's unique snapshots), so a caller's check of
	// the path it sent does not cover it; an *OverwriteDeniedError naming
	// that path and By when it says no.
	CanOverwritePath func(fullPath string) bool
	// OverwriteIdentical replaces a file without CanOverwritePath's consent
	// when the deploy declares the file's content (DeclaresContentOf) and
	// stores exactly that content, as Artifactory lets a re-deploy of
	// identical content through without the delete permission. The stored
	// content is compared too, because a declaration alone is only the
	// client's claim: under server-generated-checksums nothing checks it.
	OverwriteIdentical bool

	// Trusted records the computed checksums as the client's, as
	// Artifactory does for the files it writes itself (6.20
	// InternalArtifactoryRequest.isTrustServerChecksums) and for a checksum
	// deploy. A file the server writes, such as a build-info document or a
	// registry's manifest, has no client to send its checksums, and under
	// client-checksums its checksum files would otherwise answer 404.
	Trusted bool
	// Reusable, when set, decides whether a checksum deploy may store the
	// content it found; when it says no, the deploy fails with ErrNoBlob as
	// if the content were not there, so the answer does not reveal that it
	// is. A caller's checksum deploy sets it (knowing a checksum must not
	// copy content the caller cannot read); a write that already holds the
	// content, such as a registry upload's completion, leaves it nil.
	Reusable func(ctx context.Context, b types.Blob) (bool, error)
}

// ParseChecksums checks declared checksums: lowercase hex of the length of
// their algorithm, or empty.
func ParseChecksums(c types.Checksums) (types.Checksums, error) {
	out := types.Checksums{
		SHA256: strings.ToLower(strings.TrimSpace(c.SHA256)),
		SHA512: strings.ToLower(strings.TrimSpace(c.SHA512)),
		SHA1:   strings.ToLower(strings.TrimSpace(c.SHA1)),
		MD5:    strings.ToLower(strings.TrimSpace(c.MD5)),
	}
	for _, f := range []struct {
		v    string
		size int
	}{{out.SHA256, 64}, {out.SHA512, 128}, {out.SHA1, 40}, {out.MD5, 32}} {
		if f.v == "" {
			continue
		}
		if _, err := hex.DecodeString(f.v); err != nil || len(f.v) != f.size {
			return out, fmt.Errorf("%w: %q", ErrBadChecksum, f.v)
		}
	}
	return out, nil
}

// DeclaresContentOf reports whether parsed declared checksums name f's
// content: a SHA-1 or SHA-256 that equals f's, and neither that differs.
// MD5 and SHA-512 do not count, as in Artifactory 6.20's isFileOverwrite,
// which compares the SHA-1 and SHA-256 alone.
func DeclaresContentOf(declared types.Checksums, f types.Item) bool {
	if declared.SHA1 == "" && declared.SHA256 == "" {
		return false
	}
	return (declared.SHA1 == "" || declared.SHA1 == f.Actual.SHA1) &&
		(declared.SHA256 == "" || declared.SHA256 == f.Actual.SHA256)
}

// clientPolicy reports whether declared checksums must match: everywhere but
// a local repository set to server-generated-checksums, where a mismatch is
// recorded for the bad-checksum search instead (decision 0004).
func clientPolicy(c repo.Config) bool {
	return c.ChecksumPolicyType != repo.ServerGeneratedChecksums
}

// Deploy streams d.Body into the blob store and stores the item.
func (s *Service) Deploy(ctx context.Context, d Deploy) (types.Item, error) {
	info, err := s.Upload(ctx, d)
	if err != nil {
		return types.Item{}, err
	}
	return s.DeployCommitted(ctx, d, info)
}

// Upload streams d.Body into the blob store and commits it under the
// repository's checksum policy, as Deploy does, but stores no item, for a
// caller that must see the content's digests before deciding what to
// store: DeployCommitted then stores it. Content no item comes to
// reference is collected as an orphan.
func (s *Service) Upload(ctx context.Context, d Deploy) (blob.Info, error) {
	declared, err := ParseChecksums(d.Declared)
	if err != nil {
		return blob.Info{}, err
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
		return blob.Info{}, fmt.Errorf("item: begin upload: %w", err)
	}
	if _, err := u.Append(ctx, d.Body); err != nil {
		_ = u.Abort(context.WithoutCancel(ctx))
		return blob.Info{}, fmt.Errorf("item: upload: %w", err)
	}
	var want blob.Digests
	if clientPolicy(d.Repo) {
		want = blob.Digests{
			SHA256: declared.SHA256,
			SHA512: declared.SHA512,
			SHA1:   declared.SHA1,
			MD5:    declared.MD5,
		}
	}
	info, err := u.Commit(ctx, want)
	if errors.Is(err, blob.ErrDigestMismatch) {
		return blob.Info{}, ErrChecksumMismatch
	}
	if err != nil {
		_ = u.Abort(context.WithoutCancel(ctx))
		return blob.Info{}, fmt.Errorf("item: commit upload: %w", err)
	}
	return info, nil
}

// DeployCommitted stores the item for content the caller already committed
// to the blob store, the second half of Deploy. A registry upload commits
// in a later request than it began, so it cannot hand Deploy a body.
func (s *Service) DeployCommitted(
	ctx context.Context,
	d Deploy,
	info blob.Info,
) (types.Item, error) {
	declared, err := ParseChecksums(d.Declared)
	if err != nil {
		return types.Item{}, err
	}
	return s.storeCommitted(ctx, d, info, declared)
}

func (s *Service) storeCommitted(
	ctx context.Context,
	d Deploy,
	info blob.Info,
	declared types.Checksums,
) (types.Item, error) {
	actual := digestsOf(info)
	stored, err := s.storeFile(ctx, d, actual, info.Size, declared)
	if errors.Is(err, store.ErrDeleting) {
		s.finishCollection(ctx, actual.SHA256)
	}
	return stored, err
}

// inspected returns the environment of a deploy's hooks, with what the
// format's Inspect read from the stored content.
func (s *Service) inspected(
	ctx context.Context,
	d Deploy,
	actual types.Checksums,
	size int64,
) (format.Env, error) {
	return inspect(ctx, s.format(d.Repo.PackageType), s.env(), d.Repo, format.Write{
		Op: format.OpDeploy, Path: strings.Trim(d.Path, "/"), Cached: d.Cached,
		Properties: d.Properties, Size: size, Checksums: actual,
	})
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
// must match too, and d.Reusable must allow it; otherwise, and when the
// content is gone, ErrNoBlob.
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
		{declared.SHA512, b.Checksums.SHA512},
		{declared.SHA1, b.Checksums.SHA1},
		{declared.MD5, b.Checksums.MD5},
	} {
		if pair[0] != "" && pair[0] != pair[1] {
			return types.Item{}, ErrNoBlob
		}
	}
	if d.Reusable != nil {
		ok, err := d.Reusable(ctx, b)
		if err != nil {
			return types.Item{}, fmt.Errorf("item: may reuse content: %w", err)
		}
		if !ok {
			return types.Item{}, ErrNoBlob
		}
	}
	// The index can name content that garbage collection already removed.
	if ok, err := s.Blobs.Exists(ctx, b.Checksums.SHA256); err != nil {
		return types.Item{}, fmt.Errorf("item: blob exists: %w", err)
	} else if !ok {
		return types.Item{}, ErrNoBlob
	}
	// Artifactory trusts the found content's checksums (6.20
	// UploadServiceUtils.setFileInfoChecksums); since 7.161.15 (RTDEV-80481)
	// a deploy that sent only a SHA-256 no longer lacks a client SHA-1.
	d.Trusted = true
	stored, err := s.storeFile(ctx, d, b.Checksums, b.Size, declared)
	if errors.Is(err, ErrCollected) {
		// Collected since it was found: a miss, so the client uploads it.
		return types.Item{}, ErrNoBlob
	}
	return stored, err
}

// storeFile writes the item for stored content in one transaction
// (putFile), once the format inspected it.
func (s *Service) storeFile(
	ctx context.Context,
	d Deploy,
	actual types.Checksums,
	size int64,
	declared types.Checksums,
) (types.Item, error) {
	env, err := s.inspected(ctx, d, actual, size)
	if err != nil {
		return types.Item{}, err
	}
	if d.ResolvePath != nil {
		d.Path, err = d.ResolvePath(env.Inspection)
		if err != nil {
			return types.Item{}, err
		}
	}
	var stored types.Item
	err = s.Store.Tx(ctx, func(ctx context.Context) error {
		var err error
		stored, err = s.putFile(ctx, env, d, actual, size, declared, s.now())
		return err
	})
	if err != nil {
		return types.Item{}, err //nolint:wrapcheck // the transaction returns ours
	}
	return stored, nil
}

// putFile writes, inside the caller's transaction, the item for stored
// content as the format admits it, then runs the format's hooks: Deployed,
// and Annotated for the properties the deploy set.
func (s *Service) putFile(
	ctx context.Context,
	env format.Env,
	d Deploy,
	actual types.Checksums,
	size int64,
	declared types.Checksums,
	now time.Time,
) (types.Item, error) {
	h := s.format(d.Repo.PackageType)
	// Only properties the client sent replace the file's: those a format
	// adds in Admit, such as Maven's plugin mark, join the file's, as
	// 6.20's interceptors add theirs after saveProperties.
	replace := len(d.Properties) > 0
	w := format.Write{
		Op: format.OpDeploy, Path: strings.Trim(d.Path, "/"), Cached: d.Cached,
		Properties: d.Properties, Size: size, Checksums: actual,
	}
	if err := guardWrite(ctx, h, env, d.Repo, w); err != nil {
		return types.Item{}, err
	}
	w, err := admit(ctx, h, env, d.Repo, w)
	if err != nil {
		return types.Item{}, err
	}
	d.Path, d.Properties = w.Path, w.Properties
	stored, err := s.writeFile(ctx, d, fileWrite{
		actual: actual, size: size, declared: declared, replace: replace,
	}, now)
	if err != nil {
		return types.Item{}, err
	}
	if err := h.Deployed(ctx, env, d.Repo, stored); err != nil {
		return types.Item{}, err //nolint:wrapcheck // the format's own error
	}
	if err := s.annotated(ctx, h, env, d.Repo, stored,
		format.Annotation{Set: d.Properties, At: now}); err != nil {
		return types.Item{}, err
	}
	return stored, nil
}

// fileWrite is the content writeFile stores, and how its properties land.
type fileWrite struct {
	actual types.Checksums
	size   int64
	// declared are the checksums the client sent.
	declared types.Checksums
	// replace makes the deploy's properties replace the file's rather than
	// join them: a client's matrix parameters, or a format's own set for an
	// item it derives.
	replace bool
}

// writeFile writes, inside the caller's transaction, the item for stored
// content without any hook: parent folders, the item (replacing a file at
// the path), the blob reference (and the release of an overwritten file's)
// and the properties.
func (s *Service) writeFile(
	ctx context.Context,
	d Deploy,
	fw fileWrite,
	now time.Time,
) (types.Item, error) {
	actual, size, declared := fw.actual, fw.size, fw.declared
	full := strings.Trim(d.Path, "/")
	if err := s.ensureFolders(ctx, d.Target, path.Dir(full), d.By, now); err != nil {
		return types.Item{}, err
	}
	items := s.Store.Items()
	// Otherwise Artifactory records the SHA-256 it computes as the client's
	// when the client sent none ("Identical"), so a .sha256 file is served
	// under client-checksums for every deploy (ServedChecksum).
	// The overwrite check below reads declared, what the client sent: the
	// SHA-256 filled in here is the server's own, and counting it as a
	// declaration would let any re-upload of the same bytes replace a file
	// without the delete permission.
	original := declared
	switch {
	case d.Trusted:
		original = actual
	case original.SHA256 == "":
		original.SHA256 = actual.SHA256
	}
	it := types.Item{
		RepoKey: d.Repo.Key, Cached: d.Cached, Name: full,
		Size: size, Actual: actual, Original: original, MimeType: MimeType(full),
		Created: now, CreatedBy: d.By, Modified: now, ModifiedBy: d.By, Updated: now,
	}
	old, err := items.Get(ctx, d.Repo.Key, d.Cached, full)
	switch {
	case err == nil && d.IfAbsent:
		return types.Item{}, fmt.Errorf("%w: %s", ErrExists, full)
	case err == nil && old.Folder:
		return types.Item{}, fmt.Errorf("%w: %s is a folder", ErrConflict, full)
	case err == nil && !d.mayReplace(full, old, declared, actual):
		return types.Item{}, &OverwriteDeniedError{Repo: d.Repo.Key, Path: full, User: d.By}
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
	fresh := old.ID != 0 && old.Actual.SHA256 != actual.SHA256 && !maven.IsNonUniqueSnapshot(full)
	if err := s.settle(ctx, stored, d.Properties, fresh, fw.replace, now); err != nil {
		return types.Item{}, err
	}
	return stored, nil
}

// settle gives a file just written its properties and statistics. With
// fresh, it overwrote a file of other content, which Artifactory treats as
// a new file at the old path: 6.20's DbStoringRepoMixin.clearOldFileData
// drops "all the existing metadata", every property and the download
// statistics, unless the path is a Maven non-unique snapshot, which every
// build overwrites (RTFACT-16387 shows the properties "completely gone"
// from a user's side). Otherwise a deploy's dist-tags, deprecation or
// build properties would go on describing content they were never set on.
// With replace, props then replace the file's properties, even over the
// same content, as 6.20's saveProperties sets them with setProperties,
// which clears first. [I] 7.x documents neither (#216).
func (s *Service) settle(
	ctx context.Context,
	it types.Item,
	props types.Properties,
	fresh, replace bool,
	now time.Time,
) error {
	items := s.Store.Items()
	switch {
	case fresh || replace:
		if err := items.SetProperties(ctx, it.ID, props, now); err != nil {
			return fmt.Errorf("item: properties of %s: %w", it.FullPath(), err)
		}
	case len(props) > 0:
		if err := items.PatchProperties(ctx, it.ID, props, nil, now); err != nil {
			return fmt.Errorf("item: properties of %s: %w", it.FullPath(), err)
		}
	}
	if !fresh {
		return nil
	}
	stats, err := items.Stats(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("item: statistics of %s: %w", it.FullPath(), err)
	}
	if stats == (types.ItemStats{}) {
		return nil
	}
	if err := items.PutStats(ctx, it.ID, types.ItemStats{}); err != nil {
		return fmt.Errorf("item: statistics of %s: %w", it.FullPath(), err)
	}
	return nil
}

// mayReplace reports whether d may replace old, the file at full, with
// content whose digests are actual: CanOverwritePath allows it, or
// OverwriteIdentical does and the deploy declares and stores old's own
// content.
func (d *Deploy) mayReplace(full string, old types.Item, declared, actual types.Checksums) bool {
	if d.CanOverwritePath == nil || d.CanOverwritePath(full) {
		return true
	}
	return d.OverwriteIdentical && DeclaresContentOf(declared, old) &&
		actual.SHA256 == old.Actual.SHA256
}

// MakeFolder creates the folder at t.Path and its parents, once the format
// admits it (format.FolderAdmitter); an existing folder is returned as it
// is, with props added.
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
		if a, ok := s.format(t.Repo.PackageType).(format.FolderAdmitter); ok {
			if err := a.AdmitFolder(ctx, s.env(), t.Repo, full); err != nil {
				return err //nolint:wrapcheck // the format's own error
			}
		}
		if err := s.ensureFolders(ctx, t, full, by, now); err != nil {
			return err
		}
		var err error
		if folder, err = s.Store.Items().Get(ctx, t.Repo.Key, t.Cached, full); err != nil {
			return fmt.Errorf("item: get %s: %w", full, err)
		}
		if len(props) > 0 {
			return s.Annotate(ctx, t.Repo, folder, props, nil)
		}
		return nil
	})
	if err != nil {
		return types.Item{}, err //nolint:wrapcheck // the transaction returns ours
	}
	return folder, nil
}

// EnsureRoot returns the record of the root of t's repository, or of its
// cache when t.Cached, creating it when absent. The record holds the
// properties set on the repository itself (store.ItemStore).
func (s *Service) EnsureRoot(ctx context.Context, t Target, by string) (types.Item, error) {
	now := s.now()
	it, err := s.Store.Items().EnsureRoot(ctx, types.Item{
		RepoKey: t.Repo.Key, Cached: t.Cached,
		Created: now, CreatedBy: by, Modified: now, ModifiedBy: by, Updated: now,
	})
	if err != nil {
		return types.Item{}, fmt.Errorf("item: root of %s: %w", t.Repo.Key, err)
	}
	return it, nil
}

// ensureFolders creates every missing folder from the root down to dir
// ("." or "" is the root, which holds no folder of its own).
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

// Sidecar algorithms, by file extension.
const (
	SHA256 Algorithm = "sha256"
	SHA512 Algorithm = "sha512"
	SHA1   Algorithm = "sha1"
	MD5    Algorithm = "md5"
)

// Sidecar splits a checksum sidecar path ("a/b.jar.sha1") into the
// artifact's path and the algorithm; ok is false for any other path.
func Sidecar(p string) (artifact string, a Algorithm, ok bool) {
	for _, alg := range []Algorithm{SHA256, SHA512, SHA1, MD5} {
		if base, found := strings.CutSuffix(p, "."+string(alg)); found && base != "" &&
			!strings.HasSuffix(base, "/") {
			return base, alg, true
		}
	}
	return "", "", false
}

// mavenMetadata is the name of Maven's metadata files. internal/format/maven
// names them too, but its tests import this package.
const mavenMetadata = "maven-metadata.xml"

// ServedChecksum is the value of the file it that its sidecar for algorithm
// a answers a GET with, where c is the repository that holds the file, as
// Artifactory's LocalRepoChecksumPolicy.getChecksum (6.20) chooses it.
// Under client-checksums it is the client's checksum, and empty while the
// client has sent none, so the sidecar answers 404 until one is deployed, as
// local-repositories documents for 7.x. Under server-generated-checksums,
// for a file named maven-metadata.xml in any repository (the server
// computes those checksums, and drops a client's), and in a remote cache,
// which that policy does not govern, it is the computed checksum.
func ServedChecksum(c repo.Config, it types.Item, a Algorithm) string {
	if it.Cached || !clientPolicy(c) || path.Base(it.Name) == mavenMetadata {
		return a.Of(it.Actual)
	}
	return a.Of(it.Original)
}

// Of returns the digest a of c.
func (a Algorithm) Of(c types.Checksums) string {
	switch a {
	case SHA1:
		return c.SHA1
	case SHA256:
		return c.SHA256
	case SHA512:
		return c.SHA512
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
	case SHA512:
		c.SHA512 = v
	default:
		c.MD5 = v
	}
}

// SidecarError is a checksum sidecar upload refused for its file, with
// Artifactory's answer (UploadServiceImpl.validatePathAndUploadChecksum):
// Err is store.ErrNotFound for a missing file, ErrConflict for a folder and
// ErrChecksumMismatch for a value the client-checksums policy refuses.
type SidecarError struct {
	Err     error
	Message string
}

func (e *SidecarError) Error() string { return e.Message }

func (e *SidecarError) Unwrap() error { return e.Err }

// OwnsSidecars reports whether c's format takes every upload named as a
// checksum sidecar as one, even without its file (format.SidecarOwner).
func (s *Service) OwnsSidecars(c repo.Config) bool {
	_, ok := s.format(c.PackageType).(format.SidecarOwner)
	return ok
}

// RecordChecksum is RecordSidecar for a sidecar whose length is unknown.
func (s *Service) RecordChecksum(
	ctx context.Context,
	t Target,
	a Algorithm,
	value string,
) (types.Item, error) {
	return s.RecordSidecar(ctx, t, a, value, -1)
}

// RecordSidecar handles a checksum sidecar upload of size bytes (-1 when
// the client declared none) for the file at t.Path, once the format admits
// it: the client's value is recorded as the file's original checksum, and
// under the client-checksums policy it must equal the content's (decision
// 0004).
func (s *Service) RecordSidecar(
	ctx context.Context,
	t Target,
	a Algorithm,
	value string,
	size int64,
) (types.Item, error) {
	received := value
	var stored types.Item
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		// The format answers first, as Artifactory refuses an oversized
		// sidecar and drops metadata checksums before reading the value.
		w, err := admit(ctx, s.format(t.Repo.PackageType), s.env(), t.Repo, format.Write{
			Op: format.OpChecksum, Path: strings.Trim(t.Path, "/"), Cached: t.Cached, Size: size,
		})
		if err != nil {
			return err
		}
		var declared types.Checksums
		a.set(&declared, value)
		if declared, err = ParseChecksums(declared); err != nil {
			return err
		}
		value := a.Of(declared)
		it, err := s.Store.Items().Get(ctx, t.Repo.Key, t.Cached, w.Path)
		if errors.Is(err, store.ErrNotFound) {
			return &SidecarError{Err: err, Message: fmt.Sprintf(
				"Target file to set checksum on doesn't exist: %s:%s", t.Repo.Key, w.Path)}
		}
		if err != nil {
			return fmt.Errorf("item: get %s: %w", w.Path, err)
		}
		if it.Folder {
			return &SidecarError{Err: ErrConflict, Message: fmt.Sprintf(
				"Checksum only supported for files (but found folder): %s:%s", t.Repo.Key, w.Path)}
		}
		if actual := a.Of(it.Actual); value != actual && clientPolicy(t.Repo) {
			return &SidecarError{Err: ErrChecksumMismatch, Message: fmt.Sprintf(
				"Checksum error for '%s.%s': received '%s' but actual is '%s'",
				strings.Trim(t.Path, "/"), a, received, actual)}
		}
		a.set(&it.Original, value)
		if stored, err = s.Store.Items().Put(ctx, it); err != nil {
			return fmt.Errorf("item: put %s: %w", w.Path, err)
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
// repository root), with the sidecars its format keeps with each file
// (format.SidecarKeeper). Files of local repositories go to the trash can
// when it is on, keeping their blob references for a restore; otherwise,
// and for remote caches, the references are released for garbage
// collection.
func (s *Service) Delete(ctx context.Context, t Target, by string) (store.DeleteResult, error) {
	trash := !t.Cached
	if trash {
		on, err := TrashEnabled(ctx, s.Store)
		if err != nil {
			return store.DeleteResult{}, err
		}
		trash = on
	}
	return s.remove(ctx, t, by, trash)
}

// Purge is Delete bypassing the trash can: the references are released at
// once. It is for an item whose record lives elsewhere, such as a build-info
// document (decision 0014), which a restore would bring back without its run.
func (s *Service) Purge(ctx context.Context, t Target) (store.DeleteResult, error) {
	return s.remove(ctx, t, "", false)
}

// remove deletes the item at t.Path and everything below it, moving files to
// the trash can when trash is set.
func (s *Service) remove(
	ctx context.Context,
	t Target,
	by string,
	trash bool,
) (store.DeleteResult, error) {
	full := strings.Trim(t.Path, "/")
	now := s.now()
	var res store.DeleteResult
	err := s.Store.Tx(ctx, func(ctx context.Context) error {
		invalidated, err := s.invalidateCache(ctx, t)
		if err != nil {
			return err
		}
		removed, err := s.affected(ctx, t, full)
		if errors.Is(err, store.ErrNotFound) && invalidated > 0 {
			return nil
		}
		if err != nil {
			return err
		}
		// A file's sidecars go with it (format.SidecarKeeper), through the
		// same hooks and into the trash can at the same moment, which is
		// how a restore of the file finds them again.
		sidecars, err := s.sidecarsOf(ctx, t.Repo, t.Cached, removed)
		if err != nil {
			return err
		}
		for _, sc := range sidecars {
			if _, err := s.invalidateCache(
				ctx,
				Target{Repo: t.Repo, Cached: t.Cached, Path: sc.FullPath()},
			); err != nil {
				return err
			}
		}
		removed = append(removed, sidecars...)
		hooks, env := s.format(t.Repo.PackageType), s.env()
		var versions []types.PackageVersion
		for _, it := range removed {
			if !it.Folder {
				if err := hooks.Deleting(ctx, env, t.Repo, it); err != nil {
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
				Record: types.TrashRecord{
					Item: it, Properties: props, PackageType: string(t.Repo.PackageType),
				},
				DeletedAt: now, DeletedBy: by,
			}); err != nil {
				return fmt.Errorf("item: trash %s: %w", it.FullPath(), err)
			}
		}
		if res, err = s.Store.Items().Delete(ctx, t.Repo.Key, t.Cached, full); err != nil {
			return fmt.Errorf("item: delete %s: %w", full, err)
		}
		if res, err = s.deleteSidecars(ctx, t, sidecars, res); err != nil {
			return err
		}
		// Deleting an item drops its version links; versions left empty go.
		if err := pkgindex.Prune(ctx, s.Store, versions); err != nil {
			return err //nolint:wrapcheck // pkgindex wraps its own
		}
		if err := hooks.Deleted(ctx, env, t.Repo, removed); err != nil {
			return err //nolint:wrapcheck // the format's own error
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
	".yaml": "application/x-yaml",
	".yml":  "application/x-yaml",
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
