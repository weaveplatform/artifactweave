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
// must be exactly CacheInfo, a narinfo must parse against its path, and
// any other path must be an archive path whose content passes ReadArchive.
// It answers nil for a checksum write. Content that fails is refused with
// 400, an archive whose SHA-256 is not the one its path names with 409,
// and a blob that cannot be opened is an error.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
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
	switch {
	case w.Path == "nix-cache-info":
		data, err := io.ReadAll(io.LimitReader(rc, 4097))
		if err != nil || string(data) != CacheInfo {
			return nil, format.Rejectf(400, "invalid Nix cache configuration")
		}
	case IsInfoPath(w.Path):
		data, err := io.ReadAll(io.LimitReader(rc, MaxMetadataBytes+1))
		if err != nil {
			return nil, fmt.Errorf("nix: read metadata: %w", err)
		}
		info, err := Parse(data, w.Path)
		if err != nil {
			return nil, format.Rejectf(400, "invalid Nix narinfo")
		}
		out.info = &info
	default:
		compression, ok := ArchivePath(w.Path)
		if !ok {
			return nil, format.Rejectf(400, "invalid Nix archive path")
		}
		a, err := ReadArchive(rc, bi.Size, compression)
		if err != nil {
			return nil, format.Rejectf(400, "invalid Nix archive")
		}
		filename := strings.Split(strings.TrimPrefix(w.Path, "nar/"), ".nar")[0]
		expected, _ := parseHash("sha256:" + filename)
		if a.FileHash != expected {
			return nil, format.Rejectf(409, "Nix archive checksum does not match its path")
		}
		out.archive = &a
	}
	return out, nil
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
// content. A narinfo a client deploys needs its archive already stored and
// matching (409), and a write over an existing item may change neither an
// archive's bytes nor a store path's signed fingerprint (409).
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
	if w.Op == format.OpChecksum || !IsInfoPath(w.Path) {
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
// It returns the archive item info names in repository c (in its cache
// when cached) once its size and SHA-256 match and its content, read again
// with ReadArchive, matches info. A missing item is the store's error,
// wrapped, so callers can tell it from a mismatch, which is ErrInvalid.
func ReferencedArchive(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	info Info,
	cached bool,
) (model.Item, error) {
	it, err := env.Store.Items().Get(ctx, c.Key, cached, info.URL)
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
	a, err := ReadArchive(rc, bi.Size, info.Compression)
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
// names is stored and matches, or, for an archive, every narinfo that
// names it. A version is the store hash, under a package named for the
// rest of the store path's name, with the narinfo's Info as JSON metadata
// and both items as its files. Folders are skipped, and nix-cache-info is
// checked but not indexed.
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
	if it.FullPath() == "nix-cache-info" {
		return nil
	}
	// A copy or restore can bring metadata before its archive. Only publish a
	// version after the pair verifies; an archive arrival visits metadata only,
	// without decompressing unrelated NARs in the repository.
	return visitInfo(ctx, env, c, func(meta model.Item, info Info) error {
		if meta.ID != it.ID && info.URL != it.FullPath() {
			return nil
		}
		archive, err := ReferencedArchive(ctx, env, c, info, it.Cached)
		if errors.Is(err, kernel.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(info.StorePath, "/nix/store/")[33:]
		pkg, err := env.Store.Packages().
			Ensure(ctx, model.Package{RepoKey: c.Key, Type: c.PackageType, Name: name, Created: meta.Created})
		if err != nil {
			return fmt.Errorf("nix: package: %w", err)
		}
		data, _ := json.Marshal(info) // Info contains only JSON-safe fields.
		_, err = pkgindex.Sync(
			ctx,
			env.Store,
			pkg,
			pkgindex.IndexedVersion{
				Version:  meta.Name[:32],
				Created:  meta.Created,
				Metadata: data,
				Files:    []int64{meta.ID, archive.ID},
			},
		)
		if err != nil {
			return fmt.Errorf("nix: version: %w", err)
		}
		return nil
	})
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
	if !IsInfoPath(d.Path) {
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
	if d.Readable != nil && !d.Readable(info.URL) {
		return "", format.Rejectf(http.StatusNotFound, "Nix archive not found")
	}
	if _, err = ReferencedArchive(ctx, env, c, info, d.Cached); err != nil {
		return "", fmt.Errorf("nix: download: %w", err)
	}
	return d.Path, nil
}

// Classify reports narinfo files and nix-cache-info as metadata, since a
// narinfo's signatures, deriver and archive encoding may be replaced under
// the same path, and every archive and other file as unique.
func (Handler) Classify(_ repo.Config, path string) format.Class {
	if IsInfoPath(path) || path == "nix-cache-info" {
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
				Version:  it.Name[:32],
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
			if it.Folder || !IsInfoPath(it.FullPath()) {
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
