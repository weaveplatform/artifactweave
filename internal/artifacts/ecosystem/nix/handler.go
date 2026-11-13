package nix

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

// Handler is the format of Nix repositories, which the item service calls
// on every write and delete in them, whichever API carried it. It admits
// nix-cache-info, narinfo files and the archives they name only once it
// has read them whole, keeps a published store path's signed identity and
// an archive's bytes immutable, and indexes a store path only while its
// narinfo and archive are both stored and agree.
//
// A hosted repository keeps each store path's narinfo and archive in its
// folder under BinaryCache, as 7.x does, and holds an archive the client
// uploads to nar/<file> there only until the narinfo that names it is
// published, since the archive alone does not say which store path it
// belongs to. A remote's cache keeps the paths the client asks for.
type Handler struct{ generic.Handler }

// Serves reports whether t is the Nix package type.
func Serves(t repo.PackageType) bool { return t == repo.Nix }

type inspection struct {
	sha     string
	info    *Info
	archive *Archive
}

// Inspect reads a write's content before the write's transaction, so Admit
// and Deployed decide without parsing under the write lock: nix-cache-info
// must be exactly CacheInfo, a narinfo at an InfoItem must parse against
// its path, a channel snapshot's tarball (IsChannelPath) must pass
// ReadChannel, and any other path must be an ArchiveItem, or in a hosted
// repository an archive path where the client uploads an archive, whose
// content passes ReadArchive. It answers nil for a checksum write.
// Content that fails is refused with 400, as is any other path, such as a
// narinfo at the root of a hosted repository; an archive whose SHA-256 is
// not the one its path names is refused with 409, and a blob that cannot
// be opened is an error.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum {
		return nil, nil //nolint:nilnil // checksum sidecars do not replace stored content
	}
	rc, bi, err := env.Blobs.Open(ctx, w.Checksums.SHA256)
	if err != nil {
		return nil, fmt.Errorf("nix: open upload: %w", err)
	}
	defer rc.Close()
	out := inspection{sha: w.Checksums.SHA256}
	cached := cacheLayout(c, w.Cached)
	switch {
	case w.Path == "nix-cache-info":
		data, err := io.ReadAll(io.LimitReader(rc, 4097))
		if err != nil || string(data) != CacheInfo {
			return nil, format.Rejectf(400, "invalid Nix cache configuration")
		}
	case IsInfoItem(w.Path, cached):
		data, err := io.ReadAll(io.LimitReader(rc, MaxMetadataBytes+1))
		if err != nil {
			return nil, fmt.Errorf("nix: read metadata: %w", err)
		}
		info, err := Parse(data, w.Path)
		if err != nil {
			return nil, format.Rejectf(400, "invalid Nix narinfo")
		}
		out.info = &info
	case IsChannelPath(w.Path):
		// The status and message are inferred: the JFrog documentation
		// enforces the xz tarball without saying how it answers one that
		// is not (#216).
		if err := ReadChannel(ctx, rc, bi.Size); err != nil {
			return nil, format.Rejectf(400, "invalid Nix channel archive")
		}
	default:
		compression, ok := IsArchiveItem(w.Path, cached)
		if !ok && !cached {
			compression, ok = ArchivePath(w.Path)
		}
		if !ok {
			return nil, format.Rejectf(400, "invalid Nix archive path")
		}
		a, err := ReadArchive(ctx, rc, bi.Size, compression)
		if err != nil {
			return nil, format.Rejectf(400, "invalid Nix archive")
		}
		filename := strings.Split(w.Path[strings.LastIndexByte(w.Path, '/')+1:], ".nar")[0]
		expected, _ := parseHash("sha256:" + filename)
		if a.FileHash != expected {
			return nil, format.Rejectf(409, "Nix archive checksum does not match its path")
		}
		out.archive = &a
	}
	return out, nil
}

// cacheLayout reports whether a write of c's, cached or not, uses a
// remote's cache layout. A remote's own non-cached write is refused by
// Admit, which inspects it in that layout so the refusal stays its class's.
func cacheLayout(c repo.Config, cached bool) bool {
	return cached || c.Class == repo.Remote
}

func inspected(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (inspection, error) {
	if in, ok := env.Inspection.(inspection); ok && in.sha == w.Checksums.SHA256 {
		return in, nil
	}
	value, err := (Handler{}).Inspect(ctx, env, c, w)
	if err != nil {
		return inspection{}, err
	}
	in, _ := value.(inspection)
	return in, nil
}

// Admit applies the generic deploy policy, then refuses (400) a write to
// a repository that is not local or federated, unless it is a remote's
// cached write, and passes a checksum write. Any other write's content is
// checked as Inspect checks it, reusing Inspect's result for the same
// content. A narinfo a client deploys needs its archive already stored at
// its ArchiveItem and matching (409), and a write over an existing item
// may change neither an archive's bytes nor a store path's signed
// fingerprint (409).
func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := (generic.Handler{}).Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("nix: admission: %w", err)
	}
	if c.Class != repo.Local && c.Class != repo.Federated && (c.Class != repo.Remote || !w.Cached) {
		return w, format.Rejectf(400, "unsupported Nix repository class")
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	in, err := inspected(ctx, env, c, w)
	if err != nil {
		return w, err
	}
	if in.info != nil && !w.Cached && w.Op == format.OpDeploy {
		if _, err := ReferencedArchive(ctx, env, c, *in.info, false); err != nil {
			return w, format.Rejectf(409, "Nix narinfo requires its matching archive")
		}
	}
	if err := checkExisting(ctx, env, c, w, in); err != nil {
		return w, err
	}
	return w, nil
}

func checkExisting(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
	in inspection,
) error {
	old, err := env.Store.Items().Get(ctx, c.Key, w.Cached, w.Path)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("nix: existing item: %w", err)
	}
	if in.archive != nil && old.Actual.SHA256 != w.Checksums.SHA256 {
		return format.Rejectf(409, "Nix archives are immutable")
	}
	if in.info != nil {
		prior, err := ReadInfo(ctx, env, old)
		if err != nil {
			return err
		}
		if !bytes.Equal(prior.Fingerprint(), in.info.Fingerprint()) {
			return format.Rejectf(409, "Nix store-path identity is immutable")
		}
	}
	return nil
}

// GuardWrite serializes competing first publications at the same store hash.
// A transfer is admitted while planning, so repeat the fingerprint check under
// the transaction lock before its destination is replaced.
func (Handler) GuardWrite(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) error {
	if _, ok := infoHash(w.Path); w.Op == format.OpChecksum || !ok {
		return nil
	}
	in, err := inspected(ctx, env, c, w)
	if err != nil {
		return err
	}
	name := "nix.write/" + c.Key + "/" + w.Path
	holder, now := rand.Text(), time.Now()
	// Acquire and Release join the item transaction. PostgreSQL keeps the
	// unique-index lock until commit even though no lease row survives it.
	took, err := env.Leases.Acquire(ctx, work.Lease{
		Name: name, Holder: holder, Task: "Nix publication", Acquired: now,
		Expires: time.Unix(1, 0),
	}, now)
	if err != nil {
		return fmt.Errorf("nix: publication lock: %w", err)
	}
	if !took {
		return format.Rejectf(409, "Nix publication is already locked")
	}
	if err = env.Leases.Release(ctx, name, holder); err != nil {
		return fmt.Errorf("nix: publication unlock: %w", err)
	}
	return checkExisting(ctx, env, c, w, in)
}

// ReferencedArchive verifies the complete compressed and uncompressed identity.
// It returns the archive item info names in repository c, at its
// ArchiveItem (in its cache when cached), once its size and SHA-256 match
// and its content, read again with ReadArchive, matches info. A missing
// item is the store's error, wrapped, so callers can tell it from a
// mismatch, which is ErrInvalid.
func ReferencedArchive(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	info Info,
	cached bool,
) (model.Item, error) {
	it, err := env.Store.Items().Get(ctx, c.Key, cached, ArchiveItem(info.Hash(), info.URL, cached))
	if err != nil {
		return model.Item{}, fmt.Errorf("nix: archive item: %w", err)
	}
	if it.Folder || it.Size != info.FileSize || it.Actual.SHA256 != info.FileHash {
		return model.Item{}, ErrInvalid
	}
	rc, bi, err := env.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return model.Item{}, fmt.Errorf("nix: archive blob: %w", err)
	}
	defer rc.Close()
	a, err := ReadArchive(ctx, rc, bi.Size, info.Compression)
	if err != nil || !a.Matches(info) {
		return model.Item{}, ErrInvalid
	}
	return it, nil
}

// ReadInfo parses the narinfo stored as item it, against its full path, as
// Parse does. A blob that cannot be opened or read is a wrapped error, and
// content that does not parse is ErrInvalid.
func ReadInfo(ctx context.Context, env format.Env, it model.Item) (Info, error) {
	rc, _, err := env.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return Info{}, fmt.Errorf("nix: open narinfo: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, MaxMetadataBytes+1))
	if err != nil {
		return Info{}, fmt.Errorf("nix: read narinfo: %w", err)
	}
	return Parse(data, it.FullPath())
}

// Deployed checks a stored file's content as Inspect does, then indexes
// each store path the file completes: its own narinfo once the archive it
// names is stored and matches, or, for an archive, the narinfo that names
// it. A version is the store hash, under a package named for the rest of
// the store path's name, with the narinfo's Info as JSON metadata and both
// items as its files. Folders are skipped, and nix-cache-info, a channel
// snapshot's tarball and an archive a hosted repository holds until its
// narinfo is published are checked but not indexed. A narinfo published in
// a hosted repository takes the place of the archive held for it: that
// copy, whose content is now stored at the narinfo's ArchiveItem, goes.
func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it model.Item) error {
	if it.Folder {
		return nil
	}
	if _, err := inspected(
		ctx,
		env,
		c,
		format.Write{
			Op:        format.OpDeploy,
			Path:      it.FullPath(),
			Cached:    it.Cached,
			Size:      it.Size,
			Checksums: it.Actual,
		},
	); err != nil {
		return err
	}
	path := it.FullPath()
	// A channel snapshot is no store path, so it adds no version; the JFrog
	// documentation groups packages from narinfo metadata alone.
	if path == "nix-cache-info" || IsChannelPath(path) {
		return nil
	}
	if cacheLayout(c, it.Cached) {
		// A copy or restore can bring metadata before its archive. Only
		// publish a version after the pair verifies; an archive arrival
		// visits metadata only, without decompressing unrelated NARs.
		return visitInfo(ctx, env, c, func(meta model.Item, info Info) error {
			if meta.ID != it.ID && info.URL != path {
				return nil
			}
			_, err := index(ctx, env, c, meta, info, it.Cached)
			return err
		})
	}
	// Only the narinfo in an archive's own folder can name it, so a hosted
	// repository reads that one narinfo, not every narinfo it stores.
	hash, ok := infoHash(path)
	meta := it
	if !ok {
		if _, held := ArchivePath(path); held {
			return nil
		}
		hash = path[len(BinaryCache) : len(BinaryCache)+32]
		var err error
		meta, err = env.Store.Items().Get(ctx, c.Key, false, InfoItem(hash, false))
		if errors.Is(err, kernel.ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("nix: narinfo item: %w", err)
		}
	}
	info, err := ReadInfo(ctx, env, meta)
	if err != nil {
		return err
	}
	if meta.ID != it.ID && ArchiveItem(hash, info.URL, false) != path {
		return nil
	}
	indexed, err := index(ctx, env, c, meta, info, false)
	if err != nil || !indexed || meta.ID != it.ID {
		return err
	}
	return release(ctx, env, c, info)
}

// index adds the store path of the narinfo meta, whose content is info, to
// c's package index once its archive is stored and matches, and reports
// whether it did.
func index(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	meta model.Item,
	info Info,
	cached bool,
) (bool, error) {
	archive, err := ReferencedArchive(ctx, env, c, info, cached)
	if errors.Is(err, kernel.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	name := strings.TrimPrefix(info.StorePath, "/nix/store/")[33:]
	pkg, err := env.Store.Packages().
		Ensure(ctx, model.Package{RepoKey: c.Key, Type: c.PackageType, Name: name, Created: meta.Created})
	if err != nil {
		return false, fmt.Errorf("nix: package: %w", err)
	}
	data, _ := json.Marshal(info) // Info contains only JSON-safe fields.
	_, err = pkgindex.Sync(
		ctx,
		env.Store,
		pkg,
		pkgindex.IndexedVersion{
			Version:  info.Hash(),
			Created:  meta.Created,
			Metadata: data,
			Files:    []int64{meta.ID, archive.ID},
		},
	)
	if err != nil {
		return false, fmt.Errorf("nix: version: %w", err)
	}
	return true, nil
}

// release removes the archive c holds at info.URL, where the client
// uploaded it, once the narinfo info is published with its archive stored
// and verified at its ArchiveItem. The copy goes as Underive removes a
// derived file, past the trash can and without hooks: nothing indexes it,
// and a restore would bring back content the stored archive keeps. The nar
// folder goes with it once empty, as a delete prunes the folders it
// empties. A copy whose content differs is another upload's, and stays.
func release(ctx context.Context, env format.Env, c repo.Config, info Info) error {
	held, err := env.Store.Items().Get(ctx, c.Key, false, info.URL)
	switch {
	case errors.Is(err, kernel.ErrNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("nix: held archive: %w", err)
	case held.Folder || held.Actual.SHA256 != info.FileHash:
		return nil
	}
	if err = env.Underive(ctx, c, false, info.URL); err != nil {
		return fmt.Errorf("nix: release held archive: %w", err)
	}
	rest, _, err := env.Store.Items().Children(ctx, c.Key, false, "nar", kernel.Page{Limit: 1})
	if err != nil || len(rest) > 0 {
		return wrapNotFound(err)
	}
	_, err = env.Store.Items().Delete(ctx, c.Key, false, "nar")
	return wrapNotFound(err)
}

// wrapNotFound passes a missing nar folder, which another release pruned
// first, as no error.
func wrapNotFound(err error) error {
	if err == nil || errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("nix: held archives: %w", err)
}

// Deleted rebuilds the repository's index with Reindex, so a store path
// whose narinfo or archive was removed leaves it.
func (Handler) Deleted(ctx context.Context, env format.Env, c repo.Config, _ []model.Item) error {
	return (Handler{}).Reindex(ctx, env, c)
}

// Resolve guards a generic download without rewriting its path. It applies
// the repository's download policy and refuses every remote read (400),
// since only the native endpoint verifies upstream content. For a narinfo
// it also requires that the caller may read its archive (404 otherwise)
// and that the archive is stored and matches.
func (Handler) Resolve(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	d format.Download,
) (string, error) {
	if err := generic.CheckDownload(c, d.Path, false); err != nil {
		return "", fmt.Errorf("nix: download: %w", err)
	}
	if c.Class == repo.Remote {
		return "", format.Rejectf(
			http.StatusBadRequest,
			"remote Nix reads require the native binary-cache endpoint",
		)
	}
	if !IsInfoItem(d.Path, d.Cached) {
		return d.Path, nil
	}
	it, err := env.Store.Items().Get(ctx, c.Key, d.Cached, d.Path)
	if err != nil {
		return "", fmt.Errorf("nix: download: %w", err)
	}
	info, err := ReadInfo(ctx, env, it)
	if err != nil {
		return "", fmt.Errorf("nix: download: %w", err)
	}
	if d.Readable != nil && !d.Readable(ArchiveItem(info.Hash(), info.URL, d.Cached)) {
		return "", format.Rejectf(http.StatusNotFound, "Nix archive not found")
	}
	if _, err = ReferencedArchive(ctx, env, c, info, d.Cached); err != nil {
		return "", fmt.Errorf("nix: download: %w", err)
	}
	return d.Path, nil
}

// Classify reports narinfo files, at the root or an InfoItem, and
// nix-cache-info as metadata, since a narinfo's signatures, deriver and
// archive encoding may be replaced under the same path, and every archive
// and other file as unique.
func (Handler) Classify(_ repo.Config, path string) format.Class {
	if _, ok := infoHash(path); ok || path == "nix-cache-info" {
		return format.ClassMetadata
	}
	return format.ClassUnique
}

// Reindex rebuilds the package index of repository c from its stored
// narinfo files (a remote's cached ones), skipping a store path whose
// archive is missing; a narinfo or archive that no longer verifies fails
// the reindex. It checks env.CheckReindex, when set, before it writes, and
// then reconciles the index to the store paths it found.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	byName := map[string]*pkgindex.Indexed{}
	cached := c.Class == repo.Remote
	err := visitInfo(ctx, env, c, func(it model.Item, info Info) error {
		archive, err := ReferencedArchive(ctx, env, c, info, cached)
		if errors.Is(err, kernel.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(info.StorePath, "/nix/store/")[33:]
		p := byName[name]
		if p == nil {
			p = &pkgindex.Indexed{Name: name, Created: it.Created}
			byName[name] = p
		}
		data, _ := json.Marshal(info) // Info contains only JSON-safe fields.
		p.Versions = append(
			p.Versions,
			pkgindex.IndexedVersion{
				Version:  info.Hash(),
				Created:  it.Created,
				Metadata: data,
				Files:    []int64{it.ID, archive.ID},
			},
		)
		return nil
	})
	if err != nil {
		return err
	}
	if env.CheckReindex != nil {
		if err := env.CheckReindex(ctx); err != nil {
			return fmt.Errorf("nix: reindex authority: %w", err)
		}
	}
	wanted := make([]pkgindex.Indexed, 0, len(byName))
	for _, p := range byName {
		wanted = append(wanted, *p)
	}
	if err := pkgindex.Reconcile(ctx, env.Store, c, wanted); err != nil {
		return fmt.Errorf("nix: reconcile: %w", err)
	}
	return nil
}

func visitInfo(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	visit func(model.Item, Info) error,
) error {
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		items, next, err := env.Store.Items().
			Descendants(ctx, c.Key, c.Class == repo.Remote, "", page)
		if err != nil {
			return fmt.Errorf("nix: list metadata: %w", err)
		}
		for _, it := range items {
			if it.Folder || !IsInfoItem(it.FullPath(), c.Class == repo.Remote) {
				continue
			}
			info, err := ReadInfo(ctx, env, it)
			if err != nil {
				return err
			}
			if err = visit(it, info); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		page.After = next
	}
}

var (
	_ format.Handler    = Handler{}
	_ format.Inspector  = Handler{}
	_ format.Admitter   = Handler{}
	_ format.WriteGuard = Handler{}
	_ format.Resolver   = Handler{}
	_ format.Reindexer  = Handler{}
)
