// Package bower validates Bower package trees and indexes versions from
// their bower.json manifest. The item tree remains the source of truth.
package bower

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/format/generic"
	"github.com/weaveplatform/artifactweave/internal/service/pkgindex"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const MaxManifestBytes = 1 << 20

var nameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,99}$`)

var ErrManifest = errors.New("bower: invalid bower manifest")

type Manifest struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Dependencies map[string]string `json:"dependencies,omitempty"`
}

func Serves(t repo.PackageType) bool { return t == repo.Bower }

func ValidName(name string) bool { return nameRE.MatchString(name) && name != "." && name != ".." }

func ValidVersion(version string) bool {
	core := strings.SplitN(strings.SplitN(version, "+", 2)[0], "-", 2)[0]
	return strings.Count(core, ".") == 2 && semver.IsValid("v"+version)
}

// ParsePath accepts files inside a single versioned Bower package tree.
func ParsePath(p string) (name, version, file string, ok bool) {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsRune(p, '\\') ||
		strings.ContainsRune(p, 0) {
		return "", "", "", false
	}
	parts := strings.Split(p, "/")
	if len(parts) < 3 || !ValidName(parts[0]) || !ValidVersion(parts[1]) {
		return "", "", "", false
	}
	for _, part := range parts[2:] {
		if part == "" || part == "." || part == ".." || strings.Contains(part, ":") {
			return "", "", "", false
		}
	}
	return parts[0], parts[1], strings.Join(parts[2:], "/"), true
}

func ManifestPath(name, version string) string {
	return name + "/" + version + "/bower.json"
}

// DecodeManifest accepts one bounded JSON object and rejects duplicate
// security-sensitive keys, since clients can interpret duplicate keys
// differently when selecting a package identity or dependency.
func DecodeManifest(body []byte) (Manifest, error) {
	return decodeManifest(body, false)
}

// DecodeGitManifest accepts upstream Git tags whose bower.json omits version;
// Bower takes that version from the selected Git tag. A stated version must
// still agree with the tag before the archive enters the remote cache.
func DecodeGitManifest(body []byte) (Manifest, error) {
	return decodeManifest(body, true)
}

func decodeManifest(body []byte, optionalVersion bool) (Manifest, error) {
	var m Manifest
	if len(body) == 0 || len(body) > MaxManifestBytes {
		return m, ErrManifest
	}
	d := json.NewDecoder(bytes.NewReader(body))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return m, ErrManifest
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return m, ErrManifest
		}
		name, ok := key.(string)
		if !ok || fields[name] != nil {
			return m, ErrManifest
		}
		var raw json.RawMessage
		if err = d.Decode(&raw); err != nil {
			return m, ErrManifest
		}
		fields[name] = raw
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return m, ErrManifest
	}
	if _, err = d.Token(); !errors.Is(err, io.EOF) {
		return m, ErrManifest
	}
	if err = json.Unmarshal(body, &m); err != nil || !ValidName(m.Name) ||
		m.Version == "" && !optionalVersion || m.Version != "" && !ValidVersion(m.Version) ||
		len(m.Dependencies) > 1000 {
		return Manifest{}, ErrManifest
	}
	for name, target := range m.Dependencies {
		if !ValidName(name) || target == "" || len(target) > 512 {
			return Manifest{}, ErrManifest
		}
	}
	return m, nil
}

type Handler struct{ generic.Handler }

type inspection struct {
	sha, path string
	manifest  Manifest
}

func (Handler) Inspect(
	ctx context.Context,
	env format.Env,
	_ repo.Config,
	w format.Write,
) (any, error) {
	if w.Op == format.OpChecksum || !strings.HasSuffix(w.Path, "/bower.json") {
		return nil, nil //nolint:nilnil // Ordinary package files need no manifest inspection.
	}
	m, err := stored(ctx, env, w.Checksums.SHA256, w.Path)
	if err != nil {
		return nil, err
	}
	return inspection{sha: w.Checksums.SHA256, path: w.Path, manifest: m}, nil
}

func (Handler) Admit(
	ctx context.Context,
	env format.Env,
	c repo.Config,
	w format.Write,
) (format.Write, error) {
	w, err := generic.Handler{}.Admit(ctx, env, c, w)
	if err != nil {
		return w, fmt.Errorf("bower: generic admission: %w", err)
	}
	if w.Op == format.OpChecksum {
		return w, nil
	}
	name, version, file, ok := ParsePath(w.Path)
	if !ok {
		return w, format.Rejectf(http.StatusBadRequest, "invalid Bower package path")
	}
	if file != "bower.json" {
		return w, nil
	}
	if inspected, ok := env.Inspection.(inspection); ok &&
		inspected.sha == w.Checksums.SHA256 && inspected.path == w.Path &&
		inspected.manifest.Name == name && inspected.manifest.Version == version {
		return w, nil
	}
	if _, err := stored(ctx, env, w.Checksums.SHA256, w.Path); err != nil {
		return w, err
	}
	return w, nil
}

func (Handler) Deployed(ctx context.Context, env format.Env, c repo.Config, it types.Item) error {
	if it.Folder || !strings.HasSuffix(it.FullPath(), "/bower.json") {
		return nil
	}
	m, err := stored(ctx, env, it.Actual.SHA256, it.FullPath())
	if err != nil {
		return err
	}
	if err := pkgindex.Link(ctx, env.Store, pkgindex.Version{
		RepoKey: c.Key, PackageType: c.PackageType, Package: m.Name, Version: m.Version,
	}, it.ID, it.Created); err != nil {
		return fmt.Errorf("bower: index deployed manifest: %w", err)
	}
	return nil
}

func (Handler) Reindex(ctx context.Context, env format.Env, c repo.Config) error {
	storedRef := pkgindex.Stored(c)
	var wanted []pkgindex.Indexed
	byName := map[string][]pkgindex.IndexedVersion{}
	for page := (store.Page{Limit: store.MaxLimit}); ; {
		items, next, err := env.Store.Items().Descendants(ctx, c.Key, storedRef.Cached, "", page)
		if err != nil {
			return fmt.Errorf("bower: list manifests: %w", err)
		}
		for _, it := range items {
			if it.Folder || !strings.HasSuffix(it.FullPath(), "/bower.json") {
				continue
			}
			m, err := stored(ctx, env, it.Actual.SHA256, it.FullPath())
			if err != nil {
				return err
			}
			byName[m.Name] = append(byName[m.Name], pkgindex.IndexedVersion{
				Version: m.Version, Created: it.Created, Files: []int64{it.ID},
			})
		}
		if next == "" {
			break
		}
		page.After = next
	}
	for name, versions := range byName {
		slices.SortFunc(versions, func(a, b pkgindex.IndexedVersion) int {
			return strings.Compare(a.Version, b.Version)
		})
		wanted = append(wanted, pkgindex.Indexed{
			Name: name, Created: versions[0].Created, Versions: versions,
		})
	}
	if err := pkgindex.Reconcile(
		ctx,
		env.Store,
		c,
		wanted,
	); err != nil {
		return fmt.Errorf("bower: reconcile package index: %w", err)
	}
	return nil
}

func stored(ctx context.Context, env format.Env, sha, p string) (Manifest, error) {
	name, version, file, ok := ParsePath(p)
	if !ok || file != "bower.json" || sha == "" {
		return Manifest{}, format.Rejectf(http.StatusBadRequest, "invalid Bower manifest")
	}
	r, info, err := env.Blobs.Open(ctx, sha)
	if err != nil {
		return Manifest{}, fmt.Errorf("bower: open manifest: %w", err)
	}
	defer func() { _ = r.Close() }()
	if info.Size <= 0 || info.Size > MaxManifestBytes {
		return Manifest{}, format.Rejectf(http.StatusBadRequest, "invalid Bower manifest")
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("bower: read manifest: %w", err)
	}
	m, err := DecodeManifest(data)
	if err != nil || m.Name != name || m.Version != version {
		return Manifest{}, format.Rejectf(
			http.StatusBadRequest,
			"Bower manifest identity does not match path",
		)
	}
	return m, nil
}

var (
	_ format.Handler   = Handler{}
	_ format.Inspector = Handler{}
	_ format.Admitter  = Handler{}
	_ format.Reindexer = Handler{}
)
