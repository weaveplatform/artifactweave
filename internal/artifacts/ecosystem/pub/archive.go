package pub

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
	"gopkg.in/yaml.v3"
)

const (
	// MaxArchiveBytes bounds an archive, uploaded or fetched, at 64 MiB
	// compressed.
	MaxArchiveBytes = 64 << 20
	// MaxUnpackedBytes bounds an archive's decompressed TAR stream at
	// 128 MiB, so a small archive cannot expand without limit.
	MaxUnpackedBytes = 128 << 20
	// MaxManifestBytes bounds an archive's pubspec.yaml at 1 MiB.
	MaxManifestBytes = 1 << 20
	// MaxFiles bounds an archive at 4,096 TAR entries, folders included.
	MaxFiles = 4096
)

var (
	// ErrArchive is what ReadArchive returns for every archive it refuses;
	// the hooks wrap it, and wrap it too when a stored archive no longer
	// matches its path.
	ErrArchive  = errors.New("pub: invalid dart pub archive")
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
)

// Metadata is what the package index keeps for a version, as JSON, and what
// the protocol answers from: the package's name and version, its pubspec,
// and the archive's item path and SHA-256. ReadArchive fills the name,
// version and pubspec, and Deployed and Reindex add the path and SHA-256
// of the stored item; a remote's come from its upstream version document.
type Metadata struct {
	Name    string         `json:"name"`
	Version string         `json:"version"`
	Pubspec map[string]any `json:"pubspec"`
	Path    string         `json:"path"`
	SHA256  string         `json:"sha256"`
}

// ValidName reports whether name is a pub package name the registry
// accepts: a lowercase letter followed by up to 127 lowercase letters,
// digits and underscores.
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

// Path is the item path of the archive of package name at version:
// <name>/<name>-<version>.tar.gz.
func Path(name, version string) string {
	return name + "/" + name + "-" + version + ".tar.gz"
}

// ParsePath splits an item path into the package name and version whose
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

// ReadArchive validates the entire gzip and TAR stream, not only pubspec.yaml.
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
	manifest, err := readTarManifest(plain)
	if err != nil {
		return Metadata{}, err
	}
	var node yaml.Node
	if yaml.Unmarshal(manifest, &node) != nil || !safeYAML(&node, 0) {
		return Metadata{}, ErrArchive
	}
	var pubspec map[string]any
	if yaml.Unmarshal(manifest, &pubspec) != nil || pubspec == nil {
		return Metadata{}, ErrArchive
	}
	name, nameOK := pubspec["name"].(string)
	version, versionOK := pubspec["version"].(string)
	if !nameOK || !versionOK || !ValidName(name) || !ValidVersion(version) {
		return Metadata{}, ErrArchive
	}
	if _, err := json.Marshal(pubspec); err != nil {
		return Metadata{}, ErrArchive
	}
	return Metadata{Name: name, Version: version, Pubspec: pubspec}, nil
}

func readTarManifest(plain []byte) ([]byte, error) {
	content := bytes.NewReader(plain)
	archive := tar.NewReader(content)
	seen := map[string]bool{}
	var manifest []byte
	var err error
	for count := 0; ; count++ {
		entry, nextErr := archive.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil || count >= MaxFiles || !validEntry(entry, seen) {
			return nil, ErrArchive
		}
		if entry.Typeflag == tar.TypeDir {
			continue
		}
		if entry.Name == "pubspec.yaml" {
			if entry.Size <= 0 || entry.Size > MaxManifestBytes {
				return nil, ErrArchive
			}
			manifest, err = io.ReadAll(io.LimitReader(archive, MaxManifestBytes+1))
			if err != nil || int64(len(manifest)) != entry.Size {
				return nil, ErrArchive
			}
		}
	}
	if len(manifest) == 0 {
		return nil, ErrArchive
	}
	// TAR terminators and padding may be zero-filled; any other data after
	// the first terminator is an unvalidated second archive.
	for content.Len() > 0 {
		b, _ := content.ReadByte()
		if b != 0 {
			return nil, ErrArchive
		}
	}
	return manifest, nil
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

func safeYAML(node *yaml.Node, depth int) bool {
	if node == nil || depth > 64 || node.Kind == yaml.AliasNode || node.Anchor != "" {
		return false
	}
	if node.Kind == yaml.MappingNode {
		if len(node.Content)%2 != 0 {
			return false
		}
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Kind != yaml.ScalarNode ||
				node.Content[i].Tag != "!!str" {
				return false
			}
		}
	}
	for _, child := range node.Content {
		if !safeYAML(child, depth+1) {
			return false
		}
	}
	return true
}
