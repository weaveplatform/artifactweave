package agentplugins

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	// MaxArchiveBytes bounds a plugin bundle, and the total its files
	// inflate to, at 64 MiB.
	MaxArchiveBytes = 64 << 20
	// MaxManifestBytes bounds each harness manifest and a root plugin.json
	// at 1 MiB.
	MaxManifestBytes = 1 << 20
	// MaxFiles bounds a bundle at 512 ZIP entries, folders included.
	MaxFiles = 512
)

// StandardSchema is the $schema a root plugin.json sets to declare the
// Agent Plugins Standard 1.0.0, the only version 7.173.8 indexes.
const StandardSchema = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"

var (
	// ErrArchive is what ReadArchive returns for every bundle it refuses;
	// Deployed and Reindex wrap it for a stored bundle that has neither a
	// harness manifest nor a conforming root plugin.json.
	ErrArchive   = errors.New("agentplugins: invalid agent plugin bundle")
	namePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	harnessPaths = map[string]string{
		"claude": ".claude-plugin/plugin.json",
		"cursor": ".cursor-plugin/plugin.json",
		"codex":  ".codex-plugin/plugin.json",
	}
	// standardName is the schema's name pattern without its lookahead, which
	// Go's regexp lacks; conformsToStandard refuses "--" and ".." itself.
	standardName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
)

// Metadata records only bounded manifest data. Path identity replaces any
// conflicting manifest name or version before it enters the package index.
type Metadata struct {
	Name      string                     `json:"name"`
	Version   string                     `json:"version"`
	Path      string                     `json:"path"`
	SHA256    string                     `json:"sha256"`
	SHA1      string                     `json:"sha1,omitempty"`
	Harnesses []string                   `json:"harnesses"`
	Manifests map[string]json.RawMessage `json:"manifests"`
	// Root is set for any root plugin.json with a name, Standard only for
	// one that conforms to the Agent Plugins Standard 1.0.0. A harness
	// bundle may carry a root plugin.json of its own that predates the
	// standard; 7.x discovers the standard layout only from a conforming
	// one, so that bundle keeps its harness-only listing.
	Root     bool            `json:"root"`
	Standard json.RawMessage `json:"standard,omitempty"`
}

// Indexed reports whether the bundle has a layout 7.x indexes: a harness
// manifest or a conforming root plugin.json.
func (m Metadata) Indexed() bool { return len(m.Harnesses) > 0 || len(m.Standard) > 0 }

// Types is the multi-valued agentplugins.type property: the harnesses whose
// manifests the bundle carries and, for a conforming root plugin.json,
// generic, vscode and cursor, the values the Agent Plugins Repositories
// docs give for the standard layout. A bundle with both layouts takes the
// union [I]; the values are sorted, as the harnesses always were.
func (m Metadata) Types() []string {
	types := slices.Clone(m.Harnesses)
	if len(m.Standard) > 0 {
		types = append(types, "generic", "vscode", "cursor")
	}
	slices.Sort(types)
	return slices.Compact(types)
}

// ValidName reports whether v is a plugin name the registry accepts: a
// letter or digit followed by up to 127 letters, digits, dots, underscores
// and hyphens.
func ValidName(v string) bool { return namePattern.MatchString(v) }

// ValidVersion reports whether v is a full semantic version of at most 128
// bytes, major.minor.patch with an optional pre-release and build, written
// without a leading v.
func ValidVersion(v string) bool {
	core, _, _ := strings.Cut(v, "-")
	core, _, _ = strings.Cut(core, "+")
	return len(v) <= 128 && !strings.HasPrefix(v, "v") &&
		strings.Count(core, ".") == 2 && semver.IsValid("v"+v)
}

// Path is the canonical item path of the bundle of plugin name at version,
// <name>/<version>/<name>-<version>.zip, where jf agent plugins publish
// uploads it.
func Path(name, version string) string {
	return name + "/" + version + "/" + name + "-" + version + ".zip"
}

// ParsePath splits a canonical bundle path into the plugin name and
// version, and reports ok only when p is exactly Path(name, version) with a
// valid name and version.
func ParsePath(p string) (name, version string, ok bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 3 || !ValidName(parts[0]) || !ValidVersion(parts[1]) ||
		p != Path(parts[0], parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// ReadArchive checks every ZIP member, including CRC and total decompressed
// size, and reads the harness manifests and the root plugin.json. Each must
// be a JSON object with a name, or the bundle is refused; the root one is
// kept as Standard only when it conforms to the Agent Plugins Standard
// 1.0.0 (self-managed 7.173.8). A bundle with neither kind of manifest is
// refused.
func ReadArchive(r io.Reader, size int64) (Metadata, error) {
	if size < 0 || size > MaxArchiveBytes {
		return Metadata{}, ErrArchive
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxArchiveBytes+1))
	if err != nil || len(data) > MaxArchiveBytes || int64(len(data)) != size {
		return Metadata{}, ErrArchive
	}
	z, err := zip.NewReader(bytes.NewReader(data), size)
	if err != nil || len(z.File) == 0 || len(z.File) > MaxFiles {
		return Metadata{}, ErrArchive
	}
	meta := Metadata{Manifests: map[string]json.RawMessage{}}
	seen := map[string]bool{}
	var total uint64
	for _, f := range z.File {
		if !safeName(f.Name) || seen[f.Name] || (!f.Mode().IsRegular() && !f.FileInfo().IsDir()) {
			return Metadata{}, ErrArchive
		}
		seen[f.Name] = true
		if f.FileInfo().IsDir() {
			continue
		}
		total += f.UncompressedSize64
		if total > MaxArchiveBytes {
			return Metadata{}, ErrArchive
		}
		if err := validateEntry(f); err != nil {
			return Metadata{}, ErrArchive
		}
		for harness, p := range harnessPaths {
			if f.Name != p {
				continue
			}
			content, err := readEntry(f)
			if err != nil || !validManifest(content) {
				return Metadata{}, ErrArchive
			}
			meta.Manifests[harness] = content
			meta.Harnesses = append(meta.Harnesses, harness)
		}
		if f.Name == "plugin.json" {
			content, err := readEntry(f)
			if err != nil || !validManifest(content) {
				return Metadata{}, ErrArchive
			}
			meta.Root = true
			if conformsToStandard(content) {
				meta.Standard = content
			}
		}
	}
	if len(meta.Harnesses) == 0 && !meta.Root {
		return Metadata{}, ErrArchive
	}
	slices.Sort(meta.Harnesses)
	return meta, nil
}

func validateEntry(f *zip.File) error {
	if f.UncompressedSize64 > MaxArchiveBytes {
		return ErrArchive
	}
	rc, err := f.Open()
	if err != nil {
		return ErrArchive
	}
	n, readErr := io.Copy(io.Discard, io.LimitReader(rc, MaxArchiveBytes+1))
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil || n > MaxArchiveBytes ||
		uint64(n) != f.UncompressedSize64 { //nolint:gosec // n cannot be negative
		return ErrArchive
	}
	return nil
}

func readEntry(f *zip.File) (json.RawMessage, error) {
	if f.UncompressedSize64 > MaxManifestBytes {
		return nil, ErrArchive
	}
	rc, err := f.Open()
	if err != nil {
		return nil, ErrArchive
	}
	data, readErr := io.ReadAll(io.LimitReader(rc, MaxManifestBytes+1))
	closeErr := rc.Close()
	if readErr != nil || closeErr != nil || len(data) > MaxManifestBytes ||
		uint64(len(data)) != f.UncompressedSize64 {
		return nil, ErrArchive
	}
	return data, nil
}

func validManifest(data []byte) bool {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return false
	}
	name, ok := m["name"].(string)
	return ok && strings.TrimSpace(name) != ""
}

// conformsToStandard reports whether a root plugin.json validates against
// the Agent Plugins 1.0.0 schema (schemas/1.0.0/plugin.schema.json in
// agentplugins/agent-plugins-spec): $schema set to StandardSchema, a name
// the spec's section 5.5 allows, the typed metadata fields of section 5.4,
// an extensions object of objects, and no other field. The JFrog docs index
// only a "conforming" root plugin.json. The spec lets a loading client
// ignore an unknown field or a malformed extensions field, but says such a
// manifest "does not conform to the schema", so the registry, which
// validates rather than loads, does not index it as the standard [I].
func conformsToStandard(data []byte) bool {
	m, ok := object(data)
	if !ok {
		return false
	}
	var schema, name string
	if json.Unmarshal(m["$schema"], &schema) != nil || schema != StandardSchema ||
		json.Unmarshal(m["name"], &name) != nil || len(name) > 64 ||
		!standardName.MatchString(name) ||
		strings.Contains(name, "--") || strings.Contains(name, "..") {
		return false
	}
	for key, raw := range m {
		switch key {
		case "$schema", "name":
			ok = true
		case "version", "description", "homepage", "repository", "license":
			ok = isString(raw)
		case "author":
			ok = objectOf(raw, func(k string, v json.RawMessage) bool {
				return (k == "name" || k == "email" || k == "url") && isString(v)
			})
		case "keywords":
			var items []json.RawMessage
			ok = !isNull(raw) && json.Unmarshal(raw, &items) == nil &&
				!slices.ContainsFunc(items, func(v json.RawMessage) bool { return !isString(v) })
		case "extensions":
			ok = objectOf(raw, func(_ string, v json.RawMessage) bool {
				_, isObject := object(v)
				return isObject
			})
		default:
			ok = false
		}
		if !ok {
			return false
		}
	}
	return true
}

// object decodes raw as a JSON object; encoding/json would take null for
// an empty map, which the schema does not.
func object(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if isNull(raw) || json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	return m, true
}

func objectOf(raw json.RawMessage, member func(string, json.RawMessage) bool) bool {
	m, ok := object(raw)
	if !ok {
		return false
	}
	for k, v := range m {
		if !member(k, v) {
			return false
		}
	}
	return true
}

// isString reports whether raw is a JSON string; encoding/json would take
// null for an empty one.
func isString(raw json.RawMessage) bool {
	var s string
	return !isNull(raw) && json.Unmarshal(raw, &s) == nil
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func safeName(name string) bool {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") ||
		strings.ContainsRune(name, 0) || path.Clean(name) != strings.TrimSuffix(name, "/") {
		return false
	}
	for part := range strings.SplitSeq(strings.TrimSuffix(name, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
