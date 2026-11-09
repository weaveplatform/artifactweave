package composer

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const (
	// ArchiveLimit bounds a Composer archive at 128 MiB stored, each entry
	// at 128 MiB inflated, and all its entries together at twice that.
	ArchiveLimit = 128 << 20
	// MetadataLimit bounds an archive's composer.json at 1 MiB.
	MetadataLimit = 1 << 20
)

var (
	nameRE               = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_.-]*$`)
	versionRE            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
	errArchiveSize       = errors.New("composer: invalid composer archive size or version")
	errArchiveIncomplete = errors.New("composer: incomplete composer archive")
	errArchiveZIP        = errors.New("composer: invalid composer zip archive")
	errArchiveLimit      = errors.New("composer: composer archive decompression limit")
	errArchivePath       = errors.New("composer: unsafe composer archive path")
	errArchiveEntry      = errors.New("composer: invalid composer archive entry")
	errMetadataAmbiguous = errors.New("composer: ambiguous composer metadata")
	errMetadataInvalid   = errors.New("composer: invalid composer.json")
	errPackageName       = errors.New("composer: invalid composer package name")
	errPackageVersion    = errors.New("composer: invalid composer version")
	errMetadataMismatch  = errors.New("composer: composer metadata missing or version mismatch")
	errVersionRequired   = errors.New("composer: composer version required")
)

// Serves reports whether t is the Composer package type.
func Serves(t repo.PackageType) bool { return t == repo.Composer }

// ValidName reports whether name is a Composer package name the registry
// accepts: a lowercase vendor/package of at most 255 bytes, each part a
// letter or digit followed by letters, digits, underscores, dots and
// hyphens.
func ValidName(name string) bool { return nameRE.MatchString(name) && len(name) <= 255 }

// ValidVersion reports whether version is a version the registry accepts:
// a letter or digit followed by up to 127 ASCII letters, digits, dots,
// underscores, pluses and hyphens. It checks the characters only, so a
// branch version such as dev-main passes.
func ValidVersion(version string) bool { return versionRE.MatchString(version) }

// Package is the validated composer.json of one archive. Extra Composer fields
// are retained so the solver sees require, autoload, conflicts and stability.
type Package struct {
	Name    string
	Version string
	Fields  map[string]json.RawMessage
}

// Document returns the package's composer.json as a catalog entry: every
// retained field, with name and version set to the validated identity, so
// the entry has a version even when a composer.version property supplied
// it. A package without fields yields just the name and version.
func (p Package) Document() map[string]any {
	var doc map[string]any
	data, _ := json.Marshal(p.Fields)
	_ = json.Unmarshal(data, &doc)
	if doc == nil {
		doc = map[string]any{}
	}
	doc["name"], doc["version"] = p.Name, p.Version
	return doc
}

// ParseArchive reads a whole ZIP archive of size bytes from r and returns
// its validated composer.json. The archive must hold 1 to 4,096 entries
// within ArchiveLimit, with no absolute, backslashed or dot-segment path,
// each read to its end, and exactly one composer.json, at the root or in
// one top-level folder, within MetadataLimit and with a valid name
// (ValidName). The version is the file's own, or override when the file
// has none; a non-empty override must be valid and agree with the file's
// version, and the result must have a valid version. It stops, wrapping
// ctx's error, when ctx ends.
func ParseArchive(ctx context.Context, r io.Reader, size int64, override string) (Package, error) {
	if size <= 0 || size > ArchiveLimit || override != "" && !ValidVersion(override) {
		return Package{}, errArchiveSize
	}
	data, err := io.ReadAll(io.LimitReader(r, ArchiveLimit+1))
	if err != nil || int64(len(data)) != size {
		return Package{}, errArchiveIncomplete
	}
	z, err := zip.NewReader(bytes.NewReader(data), size)
	if err != nil || len(z.File) == 0 || len(z.File) > 4096 {
		return Package{}, errArchiveZIP
	}
	var scan archiveScan
	for _, f := range z.File {
		if err := ctx.Err(); err != nil {
			return Package{}, fmt.Errorf("scan composer archive: %w", err)
		}
		if err := scan.add(f); err != nil {
			return Package{}, err
		}
	}
	if !scan.hasMetadata ||
		override != "" && scan.pkg.Version != "" && override != scan.pkg.Version {
		return Package{}, errMetadataMismatch
	}
	if scan.pkg.Version == "" {
		scan.pkg.Version = override
	}
	if !ValidVersion(scan.pkg.Version) {
		return Package{}, errVersionRequired
	}
	return scan.pkg, nil
}

type archiveScan struct {
	pkg         Package
	hasMetadata bool
	inflated    int64
}

func (s *archiveScan) add(f *zip.File) error {
	if f.UncompressedSize64 > ArchiveLimit ||
		s.inflated > 2*ArchiveLimit-int64(f.UncompressedSize64) {
		return errArchiveLimit
	}
	s.inflated += int64(f.UncompressedSize64)
	if strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, "\\") ||
		strings.Contains("/"+f.Name, "/../") || strings.Contains("/"+f.Name, "/./") {
		return errArchivePath
	}
	body, err := f.Open()
	if err != nil {
		return errArchiveEntry
	}
	metadata := f.Name == "composer.json" || strings.Count(strings.Trim(f.Name, "/"), "/") == 1 &&
		strings.HasSuffix(f.Name, "/composer.json")
	if metadata {
		err = s.readMetadata(body, f.UncompressedSize64)
	} else {
		_, err = io.Copy(io.Discard, io.LimitReader(body, ArchiveLimit+1))
		if err != nil {
			err = errArchiveEntry
		}
	}
	if closeErr := body.Close(); err == nil && closeErr != nil {
		err = errArchiveEntry
	}
	return err
}

func (s *archiveScan) readMetadata(body io.Reader, size uint64) error {
	if s.hasMetadata || size > MetadataLimit {
		return errMetadataAmbiguous
	}
	metadata, err := io.ReadAll(io.LimitReader(body, MetadataLimit+1))
	if err != nil || len(metadata) > MetadataLimit ||
		json.Unmarshal(metadata, &s.pkg.Fields) != nil || s.pkg.Fields == nil {
		return errMetadataInvalid
	}
	if json.Unmarshal(s.pkg.Fields["name"], &s.pkg.Name) != nil || !ValidName(s.pkg.Name) {
		return errPackageName
	}
	if raw := s.pkg.Fields["version"]; len(raw) != 0 && json.Unmarshal(raw, &s.pkg.Version) != nil {
		return errPackageVersion
	}
	s.hasMetadata = true
	return nil
}

// HostedPath binds the embedded package identity to the upload path. The
// final filename may differ from the metadata version when a matrix version
// supplies it, as documented by Artifactory's Composer deployment guide.
func HostedPath(path, name string) bool {
	return ValidName(name) && strings.HasPrefix(path, name+"/") &&
		!strings.Contains(
			strings.TrimPrefix(path, name+"/"),
			"/",
		) && strings.HasSuffix(path, ".zip")
}

type (
	// Handler is the Composer format's hooks: it admits a ZIP archive to a
	// local or federated repository only when its composer.json names the
	// package its path does, and rechecks every stored file on reindex. It
	// keeps no index, since the protocol reads the archives on every
	// request. It embeds the generic hooks, so every path is classed as
	// generic content is.
	Handler    struct{ generic.Handler }
	inspection struct {
		sha string
		pkg Package
	}
)

// Inspect reads and validates the stored archive of a write to a .zip path
// before the write's transaction, and returns what it found for Admit, so
// an archive is read once per write. A remote's cached content and any
// other file inspect nothing. The archive must be valid (ParseArchive),
// with the version from the write's composer.version property when it has
// one, and must sit directly under its package's name (HostedPath);
// otherwise the write is refused with 400, as is a write without content
// or with more than one composer.version. An archive that cannot be opened
// fails the write.
func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Cached || !strings.HasSuffix(w.Path, ".zip") {
		return inspection{}, nil
	}
	return inspect(ctx, env, w)
}

func inspect(ctx context.Context, env format.Env, w format.Write) (inspection, error) {
	if w.Checksums.SHA256 == "" {
		return inspection{}, format.Rejectf(
			http.StatusBadRequest,
			"Composer archive content required",
		)
	}
	r, info, err := env.Blobs.Open(ctx, w.Checksums.SHA256)
	if err != nil {
		return inspection{}, fmt.Errorf("open composer archive: %w", err)
	}
	defer r.Close()
	version := ""
	if values := w.Properties["composer.version"]; len(values) == 1 {
		version = values[0]
	} else if len(values) > 1 {
		return inspection{}, format.Rejectf(http.StatusBadRequest, "one composer.version required")
	}
	p, err := ParseArchive(ctx, r, info.Size, version)
	if err != nil || !HostedPath(w.Path, p.Name) {
		return inspection{}, format.Rejectf(
			http.StatusBadRequest,
			"invalid Composer archive identity or metadata",
		)
	}
	return inspection{w.Checksums.SHA256, p}, nil
}

// Admit applies the generic deploy policy, wrapping its refusal, then
// passes a remote's cached content and a checksum deploy. Any other write
// must be a .zip archive in a local or federated repository (400) that
// passes Inspect's checks. It reuses Inspect's reading when it covers the
// same content, and reads the stored archive itself otherwise.
func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("admit composer publication: %w", err)
	}
	if w.Cached || w.Op == format.OpChecksum {
		return w, nil
	}
	if !strings.HasSuffix(w.Path, ".zip") || c.Class != repo.Local && c.Class != repo.Federated {
		return w, format.Rejectf(
			http.StatusBadRequest,
			"Composer publication requires a hosted zip archive",
		)
	}
	if in, ok := env.Inspection.(inspection); !ok || in.sha != w.Checksums.SHA256 {
		if _, err := inspect(ctx, env, w); err != nil {
			return w, err
		}
	}
	return w, nil
}

// Reindex applies Inspect's checks to every stored file of a local or
// federated repository and stops at the first that fails (400); any other
// class is refused with 400. It writes nothing, since there is no index to
// rebuild.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	if c.Class != repo.Local && c.Class != repo.Federated {
		return format.Rejectf(http.StatusBadRequest, "Composer reindex requires hosted repository")
	}
	items, cursor, err := env.Store.Items().
		Descendants(ctx, c.Key, false, "", store.Page{Limit: 500})
	for err == nil {
		for _, it := range items {
			if it.Folder {
				continue
			}
			properties, e := env.Store.Items().Properties(ctx, it.ID)
			if e != nil {
				return fmt.Errorf("composer reindex properties: %w", e)
			}
			_, e = inspect(
				ctx,
				env,
				format.Write{Path: it.FullPath(), Checksums: it.Actual, Properties: properties},
			)
			if e != nil {
				return e
			}
		}
		if cursor == "" {
			return nil
		}
		items, cursor, err = env.Store.Items().
			Descendants(ctx, c.Key, false, "", store.Page{Limit: 500, After: cursor})
	}
	if err != nil {
		return fmt.Errorf("composer reindex descendants: %w", err)
	}
	return nil
}

var (
	_ format.Inspector = Handler{}
	_ format.Admitter  = Handler{}
	_ format.Reindexer = Handler{}
)
