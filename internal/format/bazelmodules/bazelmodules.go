// Package bazelmodules validates Bazel module registry uploads.
package bazelmodules

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const maxModuleBytes = 1 << 20

var (
	errInvalidModuleBytes    = errors.New("invalid MODULE.bazel size or bytes")
	errMissingModuleCall     = errors.New("expected one literal module declaration")
	errInvalidModuleArgs     = errors.New("module name and version must be literal and unique")
	errInvalidIdentity       = errors.New("invalid module name or version")
	errInvalidPath           = errors.New("invalid Bazel Modules path")
	errModuleTooLarge        = errors.New("MODULE.bazel too large")
	errIdentityMismatch      = errors.New("module identity differs from path")
	errArchiveTypeMismatch   = errors.New("archive type differs from content")
	errUnexpectedPublication = errors.New("unexpected Bazel Modules publication")
	errInvalidPublication    = errors.New("invalid Bazel Modules publication")
	errInvalidSHA256         = errors.New("invalid sha256")
	nameRE                   = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,127}$`)
	versionRE                = regexp.MustCompile(`^[0-9][a-zA-Z0-9._+-]{0,127}$`)
	moduleCallRE             = regexp.MustCompile(`(?ms)^\s*module\s*\((.*?)\)`)
	nameArgRE                = regexp.MustCompile(`\bname\s*=\s*["']([^"']+)["']`)
	versionArgRE             = regexp.MustCompile(`\bversion\s*=\s*["']([^"']+)["']`)
)

func Serves(t repo.PackageType) bool { return t == repo.BazelModules }

// ValidName and ValidVersion are deliberately narrower than arbitrary item
// names. An ambiguous registry path cannot become an upload or a fetch target.
func ValidName(name string) bool { return nameRE.MatchString(name) && name != "." && name != ".." }

func ValidVersion(version string) bool {
	return versionRE.MatchString(version) && version != "." && version != ".."
}

// File identifies a native local publication file.
type File struct{ Name, Version, Kind, ArchiveType string }

func ParseFile(path string) (File, bool) {
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[0] != "modules" || !ValidName(parts[1]) || !ValidVersion(parts[2]) {
		return File{}, false
	}
	f := File{Name: parts[1], Version: parts[2]}
	if parts[3] == "MODULE.bazel" {
		f.Kind = "module"
		return f, true
	}
	if parts[3] == "source.json" || parts[3] == "metadata.json" {
		return File{}, false
	}
	for _, suffix := range []string{".tar.gz", ".tgz", ".tar", ".zip"} {
		if strings.HasSuffix(parts[3], suffix) && len(parts[3]) > len(suffix) &&
			!strings.ContainsAny(parts[3], "\\%") {
			f.Kind, f.ArchiveType = "archive", strings.TrimPrefix(suffix, ".")
			if f.ArchiveType == "tgz" {
				f.ArchiveType = "tar.gz"
			}
			return f, true
		}
	}
	return File{}, false
}

// ParseModule reads literal module name/version attributes. Dynamic module
// declarations and duplicate attributes are rejected because identity would
// otherwise depend on executing arbitrary Starlark at publication time.
func ParseModule(body []byte) (string, string, error) {
	if len(body) == 0 || len(body) > maxModuleBytes || strings.ContainsRune(string(body), '\x00') {
		return "", "", errInvalidModuleBytes
	}
	calls := moduleCallRE.FindAllSubmatch(body, 2)
	if len(calls) != 1 {
		return "", "", errMissingModuleCall
	}
	names := nameArgRE.FindAllSubmatch(calls[0][1], 2)
	versions := versionArgRE.FindAllSubmatch(calls[0][1], 2)
	if len(names) != 1 || len(versions) != 1 {
		return "", "", errInvalidModuleArgs
	}
	name, version := string(names[0][1]), string(versions[0][1])
	if !ValidName(name) || !ValidVersion(version) {
		return "", "", errInvalidIdentity
	}
	return name, version, nil
}

type (
	Handler    struct{ generic.Handler }
	inspection struct {
		sha string
		err error
	}
)

func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Cached || w.Op == format.OpChecksum {
		return nil, nil //nolint:nilnil // no inspection needed for cache or checksum operations
	}
	f, ok := ParseFile(w.Path)
	if !ok {
		return &inspection{
			sha: w.Checksums.SHA256,
			err: errInvalidPath,
		}, nil
	}
	rc, info, err := env.Blobs.Open(ctx, w.Checksums.SHA256)
	if err != nil {
		return nil, fmt.Errorf("bazelmodules: open upload: %w", err)
	}
	defer rc.Close()
	if f.Kind == "module" {
		if info.Size > maxModuleBytes {
			return &inspection{
				sha: w.Checksums.SHA256,
				err: errModuleTooLarge,
			}, nil
		}
		body, e := io.ReadAll(io.LimitReader(rc, maxModuleBytes+1))
		if e == nil {
			var name, version string
			name, version, e = ParseModule(body)
			if e == nil && (name != f.Name || version != f.Version) {
				e = errIdentityMismatch
			}
		}
		return &inspection{sha: w.Checksums.SHA256, err: e}, nil
	}
	kind, ok := archive.ParseKind(f.ArchiveType)
	if !ok || archive.KindOf(archive.ReaderAt(rc), w.Path) != kind {
		return &inspection{
			sha: w.Checksums.SHA256,
			err: errArchiveTypeMismatch,
		}, nil
	}
	limit := archive.Limits{Entries: 10000, Inflated: 512 << 20, File: 512 << 20}
	var total int64
	err = archive.Scan(
		archive.ReaderAt(rc),
		info.Size,
		kind,
		limit,
		func(entry archive.Entry) error {
			if _, e := archive.Clean(entry.Name); e != nil || !entry.Regular && !entry.Dir {
				return archive.ErrUnusable
			}
			if !entry.Regular {
				return nil
			}
			data, e := archive.ReadEntry(entry, limit)
			total += int64(len(data))
			if e != nil || total > limit.Inflated {
				return archive.ErrUnusable
			}
			return nil
		},
	)
	return &inspection{sha: w.Checksums.SHA256, err: err}, nil
}

func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	if !w.Cached && w.Op != format.OpChecksum {
		if _, ok := ParseFile(w.Path); !ok {
			return w, format.Rejectf(http.StatusConflict, "invalid Bazel Modules publication path")
		}
		in, ok := env.Inspection.(*inspection)
		if !ok || in.sha != w.Checksums.SHA256 || in.err != nil {
			return w, format.Rejectf(
				http.StatusConflict,
				"invalid Bazel Modules manifest or archive",
			)
		}
	}
	admitted, err := (generic.Handler{}).Admit(ctx, env, c, w)
	if err != nil {
		return admitted, fmt.Errorf("bazelmodules: admit publication: %w", err)
	}
	return admitted, nil
}

// GuardWrite prevents two archives from claiming one module version. Bazel's
// source.json can name only one archive, so ambiguity must fail at upload.
func (Handler) GuardWrite(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) error {
	if w.Cached || w.Op == format.OpChecksum {
		return nil
	}
	f, ok := ParseFile(w.Path)
	if !ok || f.Kind != "archive" {
		return nil
	}
	folder := "modules/" + f.Name + "/" + f.Version
	items, _, err := env.Store.Items().
		Children(ctx, c.Key, false, folder, store.Page{Limit: store.MaxLimit})
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("bazelmodules: list version archives: %w", err)
	}
	for _, it := range items {
		other, valid := ParseFile(it.FullPath())
		if valid && other.Kind == "archive" && it.FullPath() != w.Path {
			return format.Rejectf(
				http.StatusConflict,
				"Bazel Modules version already has an archive",
			)
		}
	}
	return nil
}

// The native HTTP view is derived from current stored items. Reindex validates
// every publication rather than maintaining a second mutable metadata copy.
func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	page := store.Page{Limit: store.MaxLimit}
	for {
		items, next, err := env.Store.Items().Descendants(ctx, c.Key, false, "modules", page)
		if err != nil {
			return fmt.Errorf("bazelmodules: list publications: %w", err)
		}
		for _, it := range items {
			if it.Folder {
				continue
			}
			f, ok := ParseFile(it.FullPath())
			if !ok {
				return fmt.Errorf("%w: %s", errUnexpectedPublication, it.FullPath())
			}
			in, err := (Handler{}).Inspect(
				ctx,
				env,
				c,
				format.Write{
					Path:      it.FullPath(),
					Checksums: it.Actual,
					Size:      it.Size,
					Op:        format.OpDeploy,
				},
			)
			if err != nil {
				return err
			}
			if checked, ok := in.(*inspection); !ok || checked.err != nil {
				return fmt.Errorf("%w: %s", errInvalidPublication, f.Kind)
			}
		}
		if next == "" {
			return nil
		}
		page.After = next
	}
}

// Integrity is Bazel's Subresource Integrity encoding of a stored SHA-256.
func Integrity(hexDigest string) (string, error) {
	digest, err := hex.DecodeString(hexDigest)
	if err != nil || len(digest) != 32 {
		return "", errInvalidSHA256
	}
	return "sha256-" + base64.StdEncoding.EncodeToString(digest), nil
}

// ArchiveStripPrefix returns a single enclosing directory shared by every
// regular archive entry, the layout Bazel's archive repository must strip.
// A flat or mixed archive is already rooted and needs no prefix.
func ArchiveStripPrefix(body io.ReadSeeker, size int64, name string) (string, error) {
	kind := archive.KindOf(archive.ReaderAt(body), name)
	if kind == archive.None {
		return "", archive.ErrUnusable
	}
	common := ""
	flat := false
	err := archive.Scan(
		archive.ReaderAt(body),
		size,
		kind,
		archive.Limits{Entries: 10000, Inflated: 512 << 20, File: 512 << 20},
		func(entry archive.Entry) error {
			if !entry.Regular {
				return nil
			}
			clean, e := archive.Clean(entry.Name)
			if e != nil {
				return fmt.Errorf("bazelmodules: unsafe archive entry: %w", e)
			}
			first, rest, hasSlash := strings.Cut(clean, "/")
			if !hasSlash || rest == "" {
				flat = true
				return nil
			}
			if common == "" {
				common = first
			} else if common != first {
				flat = true
			}
			return nil
		},
	)
	if err != nil {
		return "", fmt.Errorf("bazelmodules: inspect archive layout: %w", err)
	}
	if flat {
		return "", nil
	}
	return common, nil
}

var (
	_ format.Admitter   = Handler{}
	_ format.Inspector  = Handler{}
	_ format.Reindexer  = Handler{}
	_ format.WriteGuard = Handler{}
	_ format.Handler    = Handler{}
)
