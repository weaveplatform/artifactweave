// Package composer validates hosted Composer archives and their package identity.
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

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const ArchiveLimit = 128 << 20
const MetadataLimit = 1 << 20

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*/[a-z0-9][a-z0-9_.-]*$`)
var versionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

func Serves(t repo.PackageType) bool   { return t == repo.Composer }
func ValidName(name string) bool       { return nameRE.MatchString(name) && len(name) <= 255 }
func ValidVersion(version string) bool { return versionRE.MatchString(version) }

// Package is the validated composer.json of one archive. Extra Composer fields
// are retained so the solver sees require, autoload, conflicts and stability.
type Package struct {
	Name    string
	Version string
	Fields  map[string]json.RawMessage
}

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

func ParseArchive(ctx context.Context, r io.Reader, size int64, override string) (Package, error) {
	if size <= 0 || size > ArchiveLimit || override != "" && !ValidVersion(override) {
		return Package{}, errors.New("invalid Composer archive size or version")
	}
	data, err := io.ReadAll(io.LimitReader(r, ArchiveLimit+1))
	if err != nil || int64(len(data)) != size {
		return Package{}, errors.New("incomplete Composer archive")
	}
	z, err := zip.NewReader(bytes.NewReader(data), size)
	if err != nil || len(z.File) == 0 || len(z.File) > 4096 {
		return Package{}, errors.New("invalid Composer zip archive")
	}
	var out Package
	var found bool
	var inflated int64
	for _, f := range z.File {
		if err := ctx.Err(); err != nil {
			return Package{}, err
		}
		if f.UncompressedSize64 > ArchiveLimit || inflated > 2*ArchiveLimit-int64(f.UncompressedSize64) {
			return Package{}, errors.New("Composer archive decompression limit")
		}
		inflated += int64(f.UncompressedSize64)
		if strings.HasPrefix(f.Name, "/") || strings.Contains(f.Name, "\\") ||
			strings.Contains("/"+f.Name, "/../") || strings.Contains("/"+f.Name, "/./") {
			return Package{}, errors.New("unsafe Composer archive path")
		}
		body, err := f.Open()
		if err != nil {
			return Package{}, errors.New("invalid Composer archive entry")
		}
		if f.Name == "composer.json" || strings.Count(strings.Trim(f.Name, "/"), "/") == 1 && strings.HasSuffix(f.Name, "/composer.json") {
			if found || f.UncompressedSize64 > MetadataLimit {
				_ = body.Close()
				return Package{}, errors.New("ambiguous Composer metadata")
			}
			metadata, err := io.ReadAll(io.LimitReader(body, MetadataLimit+1))
			if err != nil || len(metadata) > MetadataLimit || json.Unmarshal(metadata, &out.Fields) != nil || out.Fields == nil {
				_ = body.Close()
				return Package{}, errors.New("invalid composer.json")
			}
			if json.Unmarshal(out.Fields["name"], &out.Name) != nil || !ValidName(out.Name) {
				_ = body.Close()
				return Package{}, errors.New("invalid Composer package name")
			}
			if raw := out.Fields["version"]; len(raw) != 0 && json.Unmarshal(raw, &out.Version) != nil {
				_ = body.Close()
				return Package{}, errors.New("invalid Composer version")
			}
			found = true
		} else if _, err := io.Copy(io.Discard, io.LimitReader(body, ArchiveLimit+1)); err != nil {
			_ = body.Close()
			return Package{}, errors.New("corrupt Composer archive entry")
		}
		if err := body.Close(); err != nil {
			return Package{}, errors.New("corrupt Composer archive entry")
		}
	}
	if !found || override != "" && out.Version != "" && override != out.Version {
		return Package{}, errors.New("Composer metadata missing or version mismatch")
	}
	if out.Version == "" {
		out.Version = override
	}
	if !ValidVersion(out.Version) {
		return Package{}, errors.New("Composer version required")
	}
	return out, nil
}

// HostedPath binds the embedded package identity to the upload path. The
// final filename may differ from the metadata version when a matrix version
// supplies it, as documented by Artifactory's Composer deployment guide.
func HostedPath(path, name string) bool {
	return ValidName(name) && strings.HasPrefix(path, name+"/") &&
		!strings.Contains(strings.TrimPrefix(path, name+"/"), "/") && strings.HasSuffix(path, ".zip")
}

type Handler struct{ generic.Handler }
type inspection struct {
	sha string
	pkg Package
}

func (Handler) Inspect(ctx context.Context, env format.Env, _ repo.Config, w format.Write) (any, error) {
	if w.Cached || !strings.HasSuffix(w.Path, ".zip") {
		return nil, nil
	}
	return inspect(ctx, env, w)
}

func inspect(ctx context.Context, env format.Env, w format.Write) (inspection, error) {
	if w.Checksums.SHA256 == "" {
		return inspection{}, format.Reject(http.StatusBadRequest, "Composer archive content required")
	}
	r, info, err := env.Blobs.Open(ctx, w.Checksums.SHA256)
	if err != nil {
		return inspection{}, fmt.Errorf("open Composer archive: %w", err)
	}
	defer r.Close()
	version := ""
	if values := w.Properties["composer.version"]; len(values) == 1 {
		version = values[0]
	} else if len(values) > 1 {
		return inspection{}, format.Reject(http.StatusBadRequest, "one composer.version required")
	}
	p, err := ParseArchive(ctx, r, info.Size, version)
	if err != nil || !HostedPath(w.Path, p.Name) {
		return inspection{}, format.Reject(http.StatusBadRequest, "invalid Composer archive identity or metadata")
	}
	return inspection{w.Checksums.SHA256, p}, nil
}

func (Handler) Admit(ctx context.Context, env format.Env, c repo.Config, w format.Write) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, err
	}
	if w.Cached || w.Op == format.OpChecksum {
		return w, nil
	}
	if !strings.HasSuffix(w.Path, ".zip") || c.Class != repo.Local && c.Class != repo.Federated {
		return w, format.Reject(http.StatusBadRequest, "Composer publication requires a hosted zip archive")
	}
	if in, ok := env.Inspection.(inspection); !ok || in.sha != w.Checksums.SHA256 {
		if _, err := inspect(ctx, env, w); err != nil {
			return w, err
		}
	}
	return w, nil
}

func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	if c.Class != repo.Local && c.Class != repo.Federated {
		return format.Reject(http.StatusBadRequest, "Composer reindex requires hosted repository")
	}
	items, cursor, err := env.Store.Items().Descendants(ctx, c.Key, false, "", store.Page{Limit: 500})
	for err == nil {
		for _, it := range items {
			if it.Folder {
				continue
			}
			properties, e := env.Store.Items().Properties(ctx, it.ID)
			if e != nil {
				return e
			}
			_, e = inspect(ctx, env, format.Write{Path: it.FullPath(), Checksums: it.Actual, Properties: properties})
			if e != nil {
				return e
			}
		}
		if cursor == "" {
			return nil
		}
		items, cursor, err = env.Store.Items().Descendants(ctx, c.Key, false, "", store.Page{Limit: 500, After: cursor})
	}
	return err
}

var _ format.Inspector = Handler{}
var _ format.Admitter = Handler{}
var _ format.Reindexer = Handler{}
