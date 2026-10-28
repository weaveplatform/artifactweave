package nix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type Handler struct{ generic.Handler }

func Serves(t repo.PackageType) bool { return t == repo.Nix }

type inspection struct {
	sha     string
	info    *Info
	archive *Archive
}

func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum {
		return nil, nil //nolint:nilnil // checksum linking reuses an already validated object
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
	if env.Store != nil {
		old, err := env.Store.Items().Get(ctx, c.Key, w.Cached, w.Path)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return w, fmt.Errorf("nix: existing item: %w", err)
		}
		if err == nil && old.Actual.SHA256 != w.Checksums.SHA256 && in.archive != nil {
			return w, format.Rejectf(409, "Nix archives are immutable")
		}
	}
	return w, nil
}

// ReferencedArchive verifies the complete compressed and uncompressed identity.
func ReferencedArchive(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	info Info,
	cached bool,
) (types.Item, error) {
	it, err := env.Store.Items().Get(ctx, c.Key, cached, info.URL)
	if err != nil {
		return types.Item{}, fmt.Errorf("nix: archive item: %w", err)
	}
	if it.Folder || it.Size != info.FileSize || it.Actual.SHA256 != info.FileHash {
		return types.Item{}, ErrInvalid
	}
	rc, bi, err := env.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		return types.Item{}, fmt.Errorf("nix: archive blob: %w", err)
	}
	defer rc.Close()
	a, err := ReadArchive(rc, bi.Size, info.Compression)
	if err != nil || !a.Matches(info) {
		return types.Item{}, ErrInvalid
	}
	return it, nil
}

func ReadInfo(ctx context.Context, env format.Env, it types.Item) (Info, error) {
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

func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
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
	return visitInfo(ctx, env, c, func(meta types.Item, info Info) error {
		if meta.ID != it.ID && info.URL != it.FullPath() {
			return nil
		}
		archive, err := ReferencedArchive(ctx, env, c, info, it.Cached)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(info.StorePath, "/nix/store/")[33:]
		pkg, err := env.Store.Packages().
			Ensure(ctx, types.Package{RepoKey: c.Key, Type: c.PackageType, Name: name, Created: meta.Created})
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

func (Handler) Deleted(ctx context.Context, env format.Env, c repo.Config, _ []types.Item) error {
	return (Handler{}).Reindex(ctx, env, c)
}

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

func (Handler) Classify(_ repo.Config, path string) format.Class {
	if IsInfoPath(path) || path == "nix-cache-info" {
		return format.ClassMetadata
	}
	return format.ClassUnique
}

func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	byName := map[string]*pkgindex.Indexed{}
	cached := c.Class == repo.Remote
	err := visitInfo(ctx, env, c, func(it types.Item, info Info) error {
		archive, err := ReferencedArchive(ctx, env, c, info, cached)
		if errors.Is(err, store.ErrNotFound) {
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
	visit func(types.Item, Info) error,
) error {
	page := store.Page{Limit: store.MaxLimit}
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
	_ format.Handler   = Handler{}
	_ format.Inspector = Handler{}
	_ format.Admitter  = Handler{}
	_ format.Resolver  = Handler{}
	_ format.Reindexer = Handler{}
)
