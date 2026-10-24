package debian

import (
	"bytes"
	"compress/gzip"
	"crypto/md5" //nolint:gosec // Debian Release and Packages define MD5 as a wire checksum.
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	PropDistribution = "deb.distribution"
	PropComponent    = "deb.component"
	PropArchitecture = "deb.architecture"
	PropName         = "deb.name"
	PropVersion      = "deb.version"
)

// IndexedBinary binds a validated control record to the exact stored item
// and its server-computed digests. The upload cannot supply these values.
type IndexedBinary struct {
	Binary    Binary
	Path      string
	Size      int64
	Checksums types.Checksums
}

func (b IndexedBinary) record() (string, error) {
	if b.Size <= 0 || !validPackagePath(b.Path) ||
		len(b.Checksums.SHA256) != sha256.Size*2 || len(b.Checksums.MD5) != md5.Size*2 {
		return "", fmt.Errorf("%w: package path or digest", errInvalidBinary)
	}
	if _, err := hex.DecodeString(b.Checksums.SHA256); err != nil {
		return "", fmt.Errorf("%w: SHA256 digest: %w", errInvalidBinary, err)
	}
	if _, err := hex.DecodeString(b.Checksums.MD5); err != nil {
		return "", fmt.Errorf("%w: MD5 digest: %w", errInvalidBinary, err)
	}
	var out strings.Builder
	skip := false
	for _, line := range strings.Split(strings.TrimSuffix(string(b.Binary.Control), "\n"), "\n") {
		if line == "" {
			return "", fmt.Errorf("%w: extra package control stanza", errInvalidBinary)
		}
		if line[0] != ' ' && line[0] != '\t' {
			key, _, _ := strings.Cut(line, ":")
			switch strings.ToLower(key) {
			case "filename", "size", "sha256", "sha1", "md5sum":
				skip = true
			default:
				skip = false
			}
		}
		if !skip {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	out.WriteString("Filename: ")
	out.WriteString(b.Path)
	out.WriteString("\nSize: ")
	out.WriteString(strconv.FormatInt(b.Size, 10))
	out.WriteString("\nMD5sum: ")
	out.WriteString(b.Checksums.MD5)
	out.WriteString("\nSHA256: ")
	out.WriteString(b.Checksums.SHA256)
	out.WriteString("\n\n")
	return out.String(), nil
}

// Package index filenames are literal Debian control-file values. Reject any
// path that could add a field, split a stanza or escape the repository root.
func validPackagePath(p string) bool {
	return validIndexPath(p) && isBinary(p)
}

func validIndexPath(p string) bool {
	return p != "" && path.Clean(p) == p && !strings.HasPrefix(p, "/") &&
		!strings.HasPrefix(p, "../") && !strings.Contains(p, "\\") &&
		strings.IndexFunc(p, func(r rune) bool {
			return unicode.IsControl(r) || unicode.IsSpace(r)
		}) < 0
}

// Packages writes one binary-architecture index in stable package order.
// Architecture-independent packages belong in binary-all and in each
// architecture supported by the distribution.
func Packages(entries []IndexedBinary) ([]byte, error) {
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b IndexedBinary) int {
		if n := strings.Compare(a.Binary.Name, b.Binary.Name); n != 0 {
			return n
		}
		if n := strings.Compare(a.Binary.Version, b.Binary.Version); n != 0 {
			return n
		}
		return strings.Compare(a.Path, b.Path)
	})
	var out strings.Builder
	for _, entry := range entries {
		record, err := entry.record()
		if err != nil {
			return nil, err
		}
		out.WriteString(record)
	}
	return []byte(out.String()), nil
}

// GzipIndex compresses apt index text with a stable header for reproducible
// Release checksums and by-hash URLs.
func GzipIndex(body []byte) ([]byte, error) {
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	z.ModTime = time.Time{}
	if _, err := z.Write(body); err != nil {
		return nil, fmt.Errorf("debian: gzip index: %w", err)
	}
	if err := z.Close(); err != nil {
		return nil, fmt.Errorf("debian: finish gzip index: %w", err)
	}
	return out.Bytes(), nil
}

// ReleaseEntry is a path under dists/<distribution> and its generated body.
type ReleaseEntry struct {
	Path string
	Body []byte
}

// Release builds the checksummed metadata apt verifies before accepting a
// Packages index. Signing this body produces InRelease and Release.gpg.
func Release(
	distribution string,
	components, architectures []string,
	generated []ReleaseEntry,
	at time.Time,
) ([]byte, error) {
	if distribution == "" || len(components) == 0 || len(architectures) == 0 ||
		len(generated) == 0 {
		return nil, fmt.Errorf("%w: incomplete Release coordinates", errInvalidBinary)
	}
	components, architectures = slices.Clone(components), slices.Clone(architectures)
	slices.Sort(components)
	slices.Sort(architectures)
	generated = slices.Clone(generated)
	slices.SortFunc(
		generated,
		func(a, b ReleaseEntry) int { return strings.Compare(a.Path, b.Path) },
	)
	var out strings.Builder
	fmt.Fprintf(
		&out,
		"Origin: artifactweave\nLabel: artifactweave\nSuite: %s\nCodename: %s\n",
		distribution,
		distribution,
	)
	fmt.Fprintf(
		&out,
		"Date: %s\nArchitectures: %s\nComponents: %s\nAcquire-By-Hash: yes\n",
		at.UTC().
			Format(time.RFC1123),
		strings.Join(architectures, " "),
		strings.Join(components, " "),
	)
	for _, algorithm := range []string{"MD5Sum", "SHA256"} {
		out.WriteString(algorithm)
		out.WriteString(":\n")
		for _, file := range generated {
			if file.Path == "" || path.Clean(file.Path) != file.Path ||
				strings.HasPrefix(file.Path, "/") || strings.HasPrefix(file.Path, "../") {
				return nil, fmt.Errorf("%w: unsafe Release member", errInvalidBinary)
			}
			switch algorithm {
			case "MD5Sum":
				sum := md5.Sum(file.Body) //nolint:gosec // Debian Release wire checksum.
				fmt.Fprintf(&out, " %x %16d %s\n", sum, len(file.Body), file.Path)
			case "SHA256":
				sum := sha256.Sum256(file.Body)
				fmt.Fprintf(&out, " %x %16d %s\n", sum, len(file.Body), file.Path)
			}
		}
	}
	return []byte(out.String()), nil
}
