// Package hex validates Hex tarballs and extracts metadata needed by the
// signed registry v2 protocol. It never executes metadata.config as Erlang.
package hex

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

const (
	MaxTarballBytes = 64 << 20
	maxContentBytes = 128 << 20
	maxTarEntries   = 20000
)

var (
	errTarball = errors.New("hex: invalid hex tarball")
	nameRE     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,99}$`)
	versionRE  = regexp.MustCompile(
		`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`,
	)
)

type Requirement struct {
	Package     string
	Requirement string
	App         string
	Repository  string
	Optional    bool
}

type Package struct {
	Name          string
	Version       string
	Requirements  []Requirement
	InnerChecksum [32]byte
	OuterChecksum [32]byte
	// These signed registry fields are retained when a remote view is
	// permission-filtered and re-signed. Local tarballs do not carry them.
	Retirement      []byte
	AdvisoryIndexes []uint64
	PublishedAt     []byte
}

func Serves(t repo.PackageType) bool { return t == repo.Hex }

func ValidName(name string) bool { return nameRE.MatchString(name) }

func ValidVersion(version string) bool { return versionRE.MatchString(version) }

func TarballPath(name, version string) (string, bool) {
	if !ValidName(name) || !ValidVersion(version) {
		return "", false
	}
	return "tarballs/" + name + "-" + version + ".tar", true
}

func IsTarballPath(p string) bool {
	if !strings.HasPrefix(p, "tarballs/") || !strings.HasSuffix(p, ".tar") {
		return false
	}
	base := strings.TrimSuffix(strings.TrimPrefix(p, "tarballs/"), ".tar")
	return base != "" && !strings.Contains(base, "/") && safePath(p)
}

// InspectTarball validates both tar layers and metadata before the item can
// be admitted to a repository. The source bytes remain unchanged.
func InspectTarball(r io.Reader) (Package, error) {
	var pkg Package
	body, err := io.ReadAll(io.LimitReader(r, MaxTarballBytes+1))
	if err != nil || len(body) == 0 || len(body) > MaxTarballBytes {
		return pkg, errTarball
	}
	pkg.OuterChecksum = sha256.Sum256(body)
	files, err := outerFiles(body)
	if err != nil {
		return pkg, err
	}
	if len(files) != 4 || string(files["VERSION"]) != "3" ||
		len(files["CHECKSUM"]) != 64 || len(files["metadata.config"]) > 1<<20 {
		return pkg, errTarball
	}
	inner := sha256.New()
	_, _ = inner.Write(files["VERSION"])
	_, _ = inner.Write(files["metadata.config"])
	_, _ = inner.Write(files["contents.tar.gz"])
	copy(pkg.InnerChecksum[:], inner.Sum(nil))
	if !strings.EqualFold(string(files["CHECKSUM"]), hex.EncodeToString(pkg.InnerChecksum[:])) {
		return pkg, errTarball
	}
	if err := validateContents(files["contents.tar.gz"]); err != nil {
		return pkg, err
	}
	fields, err := parseMetadata(files["metadata.config"])
	if err != nil {
		return pkg, fmt.Errorf("%w: %w", errTarball, err)
	}
	name, nameOK := fields["name"].(string)
	version, versionOK := fields["version"].(string)
	if !nameOK || !versionOK || !ValidName(name) || !ValidVersion(version) {
		return pkg, errTarball
	}
	pkg.Name, pkg.Version = name, version
	pkg.Requirements, err = parseRequirements(fields["requirements"])
	if err != nil {
		return pkg, err
	}
	return pkg, nil
}

func outerFiles(body []byte) (map[string][]byte, error) {
	tr := tar.NewReader(bytes.NewReader(body))
	files := make(map[string][]byte, 4)
	for i := 0; ; i++ {
		if i >= maxTarEntries {
			return nil, errTarball
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || hdr.Typeflag != tar.TypeReg && hdr.Typeflag != 0 ||
			hdr.Size < 0 || hdr.Size > MaxTarballBytes {
			return nil, errTarball
		}
		if hdr.Name != "VERSION" && hdr.Name != "CHECKSUM" &&
			hdr.Name != "metadata.config" && hdr.Name != "contents.tar.gz" {
			return nil, errTarball
		}
		if _, exists := files[hdr.Name]; exists {
			return nil, errTarball
		}
		b, err := io.ReadAll(io.LimitReader(tr, hdr.Size+1))
		if err != nil || int64(len(b)) != hdr.Size {
			return nil, errTarball
		}
		files[hdr.Name] = b
	}
	return files, nil
}

func parseRequirements(v any) ([]Requirement, error) {
	entries, ok := v.([]any)
	if !ok {
		return nil, errMetadata
	}
	result := make([]Requirement, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		// Hex's metadata.config uses a list of keyword lists: each
		// requirement carries its own "name" attribute. Some older archives
		// use a {name, attributes} tuple, which the same bounded term parser
		// can safely accept.
		attrs, ok := entry.([]any)
		legacyName := ""
		if !ok {
			kv, pair := entry.(tuple)
			if !pair || len(kv) != 2 {
				return nil, errMetadata
			}
			legacyName, pair = kv[0].(string)
			if !pair {
				return nil, errMetadata
			}
			attrs, ok = kv[1].([]any)
		}
		if !ok {
			return nil, errMetadata
		}
		fields := map[string]any{}
		for _, attr := range attrs {
			pair, ok := attr.(tuple)
			if !ok || len(pair) != 2 {
				return nil, errMetadata
			}
			key, ok := pair[0].(string)
			if !ok || key == "" {
				return nil, errMetadata
			}
			if _, exists := fields[key]; exists {
				return nil, errMetadata
			}
			fields[key] = pair[1]
		}
		name, nameOK := fields["name"].(string)
		if name == "" {
			name, nameOK = legacyName, legacyName != ""
		}
		if !nameOK || !ValidName(name) || seen[name] || legacyName != "" && legacyName != name {
			return nil, errMetadata
		}
		seen[name] = true
		app, a := fields["app"].(string)
		requirement, r := fields["requirement"].(string)
		optional, o := fields["optional"].(bool)
		if !a || !r || !o || app == "" || requirement == "" ||
			!nameRE.MatchString(app) || len(requirement) > 256 {
			return nil, errMetadata
		}
		repoName, _ := fields["repository"].(string)
		if repoName != "" && !ValidName(repoName) {
			return nil, errMetadata
		}
		result = append(result, Requirement{
			Package: name, Requirement: requirement, App: app,
			Repository: repoName, Optional: optional,
		})
	}
	return result, nil
}

func validateContents(body []byte) error {
	if len(body) == 0 || len(body) > MaxTarballBytes {
		return errTarball
	}
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return errTarball
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(io.LimitReader(gz, maxContentBytes+1))
	var total int64
	seen := map[string]bool{}
	for i := 0; ; i++ {
		if i >= maxTarEntries {
			return errTarball
		}
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || hdr.Size < 0 || hdr.Size > maxContentBytes ||
			!safePath(hdr.Name) || seen[hdr.Name] {
			return errTarball
		}
		seen[hdr.Name] = true
		total += hdr.Size
		if total > maxContentBytes {
			return errTarball
		}
		switch hdr.Typeflag {
		case tar.TypeReg, 0, tar.TypeDir:
		case tar.TypeSymlink:
			if !safeLink(hdr.Name, hdr.Linkname) {
				return errTarball
			}
		default:
			return errTarball
		}
		if _, err := io.CopyN(io.Discard, tr, hdr.Size); err != nil {
			return errTarball
		}
	}
	// Read through the gzip trailer so truncated or corrupt CRC data fails.
	if n, err := io.Copy(
		io.Discard,
		io.LimitReader(gz, maxContentBytes-total+1),
	); err != nil ||
		n > maxContentBytes-total {
		return errTarball
	}
	return nil
}

func safePath(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") &&
		!strings.ContainsAny(p, "\\\x00") && path.Clean(p) == strings.TrimSuffix(p, "/") &&
		p != ".." && !strings.HasPrefix(p, "../")
}

func safeLink(name, target string) bool {
	if target == "" || strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\\\x00") {
		return false
	}
	combined := path.Clean(path.Join(path.Dir(name), target))
	return combined != ".." && !strings.HasPrefix(combined, "../")
}
