package puppet

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	// MaxArchiveBytes bounds a module archive, uploaded or fetched, at
	// 64 MiB compressed.
	MaxArchiveBytes = 64 << 20
	// MaxUnpackedBytes bounds an archive's decompressed TAR stream at
	// 128 MiB, so a small archive cannot expand without limit.
	MaxUnpackedBytes = 128 << 20
	// MaxManifestBytes bounds an archive's metadata.json at 1 MiB.
	MaxManifestBytes = 1 << 20
	// MaxFiles bounds an archive at 4,096 TAR entries, folders included.
	MaxFiles = 4096
)

var (
	// ErrArchive is what ReadArchive and the manifest decoders return for
	// every archive or manifest they refuse; Deployed and Reindex wrap it
	// when a stored archive no longer matches its path.
	ErrArchive  = errors.New("puppet: invalid module archive")
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}-[a-z][a-z0-9_]{0,63}$`)
)

// Manifest is the client-visible, validated module identity and dependencies.
type Manifest struct {
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	Author       string       `json:"author,omitempty"`
	Summary      string       `json:"summary,omitempty"`
	License      string       `json:"license,omitempty"`
	Source       string       `json:"source,omitempty"`
	Dependencies []Dependency `json:"dependencies"`
}

// Dependency is one entry of a manifest's dependencies: the module it
// needs, named <owner>/<module> or <owner>-<module>, and the version
// requirement the Puppet client resolves.
type Dependency struct {
	Name               string `json:"name"`
	VersionRequirement string `json:"version_requirement"`
}

// Metadata is what the package index keeps for a module version, as JSON,
// and what the protocol answers from: the module's name and version, its
// validated manifest, the manifest's links, and the stored archive's item
// path, SHA-256 and size. ReadArchive fills all but the path and SHA-256,
// which Deployed and Reindex take from the stored item.
type Metadata struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Manifest Manifest `json:"manifest"`
	// ProjectPage and IssuesURL are metadata.json's project_page and
	// issues_url (ManifestLinks), which a module record answers as its
	// homepage_url and issues_url. They stay out of Manifest so that the
	// release's metadata answers as it did.
	ProjectPage string `json:"project_page,omitempty"`
	IssuesURL   string `json:"issues_url,omitempty"`
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
}

// ManifestLinks returns a metadata.json's project_page and issues_url, each
// "" unless the manifest gives it as a JSON string under exactly that key.
// The Forge answers a module's homepage_url and issues_url from them; a
// link of another type is left out rather than refusing the archive, since
// the Puppet client reads neither.
func ManifestLinks(manifest []byte) (string, string) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(manifest, &fields) != nil {
		return "", ""
	}
	text := func(key string) string {
		var s string
		if json.Unmarshal(fields[key], &s) != nil {
			return ""
		}
		return s
	}
	return text("project_page"), text("issues_url")
}

// ValidateManifest rejects invalid identities and dependency constraints before
// metadata can enter the native index. Puppet resolves the validated constraints.
func ValidateManifest(m Manifest) bool {
	if !ValidName(m.Name) || !ValidVersion(m.Version) || len(m.Dependencies) > 256 {
		return false
	}
	seen := map[string]bool{}
	for _, d := range m.Dependencies {
		name := strings.ReplaceAll(d.Name, "/", "-")
		if !ValidName(name) || name == m.Name || seen[name] ||
			!validRequirement(d.VersionRequirement) {
			return false
		}
		seen[name] = true
	}
	return true
}

// The client accepts partial versions, wildcards, intersections, OR clauses,
// and hyphen ranges. Validation bounds the syntax; Puppet selects versions.
const (
	requirementNumber  = `(?:0|[1-9][0-9]*|[xX*])`
	requirementLabel   = `[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*`
	requirementVersion = requirementNumber + `(?:\.` + requirementNumber + `(?:\.` + requirementNumber + `(?:-` + requirementLabel + `)?(?:\+` + requirementLabel + `)?)?)?`
)

var (
	operatorSpace      = regexp.MustCompile(`([<>=~^])(?:\s+|\s*v)`)
	requirementPattern = regexp.MustCompile(
		`^(?:<=|>=|~>|~=|[<>=~^])?` + requirementVersion + `$`,
	)
	partialRequirementPattern = regexp.MustCompile(`^` + requirementVersion + `$`)
)

func validRequirement(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	value = operatorSpace.ReplaceAllString(value, "$1")
	total := 0
	for _, clause := range strings.Split(value, "||") {
		fields := strings.Fields(clause)
		if len(fields) == 0 {
			return false
		}
		total += len(fields)
		if total > 8 {
			return false
		}
		if len(fields) == 3 && fields[1] == "-" {
			if !partialRequirementPattern.MatchString(fields[0]) ||
				!partialRequirementPattern.MatchString(fields[2]) {
				return false
			}
			continue
		}
		for _, field := range fields {
			if !requirementPattern.MatchString(field) {
				return false
			}
		}
	}
	return true
}

// ValidName reports whether name is a Puppet module name the registry
// accepts: <owner>-<module>, each a lowercase letter followed by up to 63
// lowercase letters, digits and underscores.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// ValidVersion reports whether version is a full semantic version of at
// most 128 bytes, major.minor.patch with an optional pre-release and build,
// written without a leading v.
func ValidVersion(version string) bool {
	core, _, _ := strings.Cut(version, "-")
	core, _, _ = strings.Cut(core, "+")
	return len(version) <= 128 && strings.Count(core, ".") == 2 &&
		semver.IsValid("v"+version) && !strings.HasPrefix(version, "v")
}

// Path is the item path of the archive of module name at version:
// <name>/<name>-<version>.tar.gz.
func Path(name, version string) string {
	return name + "/" + name + "-" + version + ".tar.gz"
}

// ParsePath splits an item path into the module name and version whose
// archive it holds, and reports ok only when p is exactly Path(name,
// version) with a valid name and version.
func ParsePath(p string) (name, version string, ok bool) {
	name, file, found := strings.Cut(p, "/")
	if !found || !ValidName(name) || !strings.HasPrefix(file, name+"-") ||
		!strings.HasSuffix(file, ".tar.gz") {
		return "", "", false
	}
	version = strings.TrimSuffix(strings.TrimPrefix(file, name+"-"), ".tar.gz")
	return name, version, ValidVersion(version) && p == Path(name, version)
}

// ReadArchive validates the entire gzip and TAR stream, including metadata.json.
// It rejects duplicate and unsafe entries, links, trailing data, oversized
// decompression, and a manifest whose declared identity is invalid.
func ReadArchive(r io.Reader, size int64) (Metadata, error) {
	if size <= 0 || size > MaxArchiveBytes {
		return Metadata{}, ErrArchive
	}
	compressed, err := io.ReadAll(io.LimitReader(r, MaxArchiveBytes+1))
	if err != nil || int64(len(compressed)) != size || len(compressed) > MaxArchiveBytes {
		return Metadata{}, ErrArchive
	}
	input := bytes.NewReader(compressed)
	gz, err := gzip.NewReader(input)
	if err != nil {
		return Metadata{}, ErrArchive
	}
	gz.Multistream(false)
	plain, err := io.ReadAll(io.LimitReader(gz, MaxUnpackedBytes+1))
	closeErr := gz.Close()
	if err != nil || closeErr != nil || len(plain) > MaxUnpackedBytes || input.Len() != 0 {
		return Metadata{}, ErrArchive
	}
	manifest, root, err := readTarManifest(plain)
	if err != nil {
		return Metadata{}, err
	}
	var m Manifest
	if json.Unmarshal(manifest, &m) != nil || !ValidateManifest(m) {
		return Metadata{}, ErrArchive
	}
	if root != m.Name+"-"+m.Version {
		return Metadata{}, ErrArchive
	}
	page, issues := ManifestLinks(manifest)
	return Metadata{
		Name: m.Name, Version: m.Version, Manifest: m,
		ProjectPage: page, IssuesURL: issues, Size: size,
	}, nil
}

func readTarManifest(plain []byte) ([]byte, string, error) {
	content := bytes.NewReader(plain)
	archive := tar.NewReader(content)
	seen := map[string]bool{}
	var manifest []byte
	var root string
	var err error
	for count := 0; ; count++ {
		entry, nextErr := archive.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil || count >= MaxFiles || !validEntry(entry, seen) {
			return nil, "", ErrArchive
		}
		part, _, _ := strings.Cut(strings.TrimSuffix(entry.Name, "/"), "/")
		if root == "" {
			root = part
		}
		if part != root {
			return nil, "", ErrArchive
		}
		if entry.Typeflag == tar.TypeDir {
			continue
		}
		if entry.Name == root+"/metadata.json" {
			if entry.Size <= 0 || entry.Size > MaxManifestBytes {
				return nil, "", ErrArchive
			}
			manifest, err = io.ReadAll(io.LimitReader(archive, MaxManifestBytes+1))
			if err != nil || int64(len(manifest)) != entry.Size {
				return nil, "", ErrArchive
			}
		}
	}
	if len(manifest) == 0 {
		return nil, "", ErrArchive
	}
	// TAR terminators and padding may be zero-filled; any other data after
	// the first terminator is an unvalidated second archive.
	for content.Len() > 0 {
		b, _ := content.ReadByte()
		if b != 0 {
			return nil, "", ErrArchive
		}
	}
	return manifest, root, nil
}

func validEntry(entry *tar.Header, seen map[string]bool) bool {
	if entry == nil || entry.Name == "" || strings.HasPrefix(entry.Name, "/") ||
		strings.ContainsAny(entry.Name, "\\\x00") ||
		path.Clean(entry.Name) != strings.TrimSuffix(entry.Name, "/") ||
		strings.HasPrefix(entry.Name, "../") || entry.Name == ".." ||
		seen[entry.Name] || entry.Size < 0 {
		return false
	}
	seen[entry.Name] = true
	// The zero type flag is the historical regular-file encoding in USTAR.
	return entry.Typeflag == tar.TypeReg || entry.Typeflag == 0 ||
		entry.Typeflag == tar.TypeDir
}

// UnmarshalJSON rejects duplicate identity/dependency fields, including case
// aliases, so the archive and a client's JSON parser cannot disagree.
func (m *Manifest) UnmarshalJSON(data []byte) error {
	if err := uniqueFields(
		data,
		"name",
		"version",
		"author",
		"summary",
		"license",
		"source",
		"dependencies",
	); err != nil {
		return err
	}
	type plain Manifest
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return ErrArchive
	}
	*m = Manifest(value)
	return nil
}

// UnmarshalJSON decodes a dependency, refusing a repeated or case-aliased
// key or invalid JSON with ErrArchive, so the registry and the Puppet
// client cannot read different values from one entry.
func (d *Dependency) UnmarshalJSON(data []byte) error {
	if err := uniqueFields(data, "name", "version_requirement"); err != nil {
		return err
	}
	type plain Dependency
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return ErrArchive
	}
	*d = Dependency(value)
	return nil
}

func uniqueFields(data []byte, known ...string) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return ErrArchive
	}
	seen := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return ErrArchive
		}
		name, ok := key.(string)
		if !ok || seen[strings.ToLower(name)] {
			return ErrArchive
		}
		// encoding/json folds struct field names, including Unicode aliases.
		// Puppet reads exact JSON keys, so known fields must use their tag spelling.
		for _, field := range known {
			if name != field && strings.EqualFold(name, field) {
				return ErrArchive
			}
		}
		seen[strings.ToLower(name)] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return ErrArchive
		}
	}
	if _, err = dec.Token(); err != nil {
		return ErrArchive
	}
	if _, err = dec.Token(); !errors.Is(err, io.EOF) {
		return ErrArchive
	}
	return nil
}
