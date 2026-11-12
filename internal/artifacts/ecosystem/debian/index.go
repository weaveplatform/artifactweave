package debian

import (
	"bytes"
	"compress/gzip"
	"crypto/md5" //nolint:gosec // Debian Release and Packages define MD5 as a wire checksum.
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/dsnet/compress/bzip2"
	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"

	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// PropDistribution, PropComponent and PropArchitecture are the matrix
// parameters a client deploys a binary with, as the JFrog Debian
// documentation gives them, naming the distributions, components and
// architectures whose indexes list it; a binary deployed without them is
// stored but never indexed. PropName and PropVersion are the properties
// Deployed and Reindex set from the binary's control data. The items keep
// these names, so they never change.
const (
	PropDistribution = "deb.distribution"
	PropComponent    = "deb.component"
	PropArchitecture = "deb.architecture"
	PropName         = "deb.name"
	PropVersion      = "deb.version"
	// PropIndexStatus is the property JFrog's Debian guide names for why a
	// package failed validation and was left out of the index ("Use Debian
	// Metadata Validation"). It holds ReadBinary's reason, whose wording is
	// artifactweave's own: no 7.x source gives JFrog's [I].
	PropIndexStatus = "deb.index.status"
)

// IndexedBinary binds a validated control record to the exact stored item
// and its server-computed digests. The upload cannot supply these values.
type IndexedBinary struct {
	Binary    Binary
	Path      string
	Size      int64
	Checksums kernel.Checksums
}

func (b IndexedBinary) record() (string, error) {
	if b.Size <= 0 || !validPackagePath(b.Path) ||
		len(b.Checksums.SHA256) != sha256.Size*2 || len(b.Checksums.MD5) != md5.Size*2 {
		return "", fmt.Errorf("%w: package path or digest", errInvalidBinary)
	}
	if _, err := hex.DecodeString(b.Checksums.SHA256); err != nil {
		return "", fmt.Errorf("%w: sha256 digest: %w", errInvalidBinary, err)
	}
	if _, err := hex.DecodeString(b.Checksums.MD5); err != nil {
		return "", fmt.Errorf("%w: md5 digest: %w", errInvalidBinary, err)
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

// ValidBinaryPath reports whether a repository-relative path can safely
// appear as the Filename of a binary package in an apt index.
func ValidBinaryPath(p string) bool { return validPackagePath(p) }

// ValidSourcePath reports whether a repository-relative descriptor path can
// safely appear in a Sources index.
func ValidSourcePath(p string) bool { return validIndexPath(p) && isSourceDescriptor(p) }

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

// IndexForm is one file a repository writes for an index: the suffix after
// the index's name, empty for the plain index, and the file's bytes.
type IndexForm struct {
	Suffix string
	Body   []byte
}

// optionalIndexForms are the optionalIndexCompressionFormats values 7.x
// writes an index in besides gzip, which the JFrog Debian guide says is
// "created for every Debian repository and cannot be disabled". Its
// Compression Formats section lists Bzip2 (.bz2), LZMA (.lzma) and XZ (.xz),
// and JFrog's repository configuration JSON gives their values as bz2, lzma
// and xz.
var optionalIndexForms = []struct {
	name, suffix string
	compress     func([]byte) ([]byte, error)
}{
	{"bz2", ".bz2", bzip2Index},
	{"lzma", ".lzma", lzmaIndex},
	{"xz", ".xz", xzIndex},
}

// IndexForms returns the files a repository writes for the plain index
// body: body itself, its gzip copy, and a copy in each format formats, a
// repository's optionalIndexCompressionFormats, names. Each form comes once
// in that fixed order whatever the setting's order or repeats. A value
// other than bz2, lzma and xz adds nothing [I]: gz is written anyway, and
// 7.x offers no other.
func IndexForms(body []byte, formats []string) ([]IndexForm, error) {
	compressed, err := GzipIndex(body)
	if err != nil {
		return nil, err
	}
	out := []IndexForm{{Body: body}, {Suffix: ".gz", Body: compressed}}
	for _, form := range optionalIndexForms {
		if !slices.Contains(formats, form.name) {
			continue
		}
		compressed, err := form.compress(body)
		if err != nil {
			return nil, err
		}
		out = append(out, IndexForm{Suffix: form.suffix, Body: compressed})
	}
	return out, nil
}

// bzip2Index compresses at level 9, the bzip2 tool's default and so the
// level Debian's archive tools write Packages.bz2 at.
func bzip2Index(body []byte) ([]byte, error) {
	var out bytes.Buffer
	z, err := bzip2.NewWriter(&out, &bzip2.WriterConfig{Level: bzip2.BestCompression})
	if err != nil {
		return nil, fmt.Errorf("debian: bzip2 index: %w", err)
	}
	return finishIndex(&out, z, "bzip2", body)
}

// lzmaIndex writes the .lzma (LZMA alone) form apt reads through liblzma,
// with an unknown size and an end marker as xz --format=lzma writes it.
func lzmaIndex(body []byte) ([]byte, error) {
	var out bytes.Buffer
	z, err := lzma.NewWriter(&out)
	if err != nil {
		return nil, fmt.Errorf("debian: lzma index: %w", err)
	}
	return finishIndex(&out, z, "lzma", body)
}

func xzIndex(body []byte) ([]byte, error) {
	var out bytes.Buffer
	z, err := xz.NewWriter(&out)
	if err != nil {
		return nil, fmt.Errorf("debian: xz index: %w", err)
	}
	return finishIndex(&out, z, "xz", body)
}

func finishIndex(out *bytes.Buffer, z io.WriteCloser, name string, body []byte) ([]byte, error) {
	if _, err := z.Write(body); err != nil {
		return nil, fmt.Errorf("debian: %s index: %w", name, err)
	}
	if err := z.Close(); err != nil {
		return nil, fmt.Errorf("debian: finish %s index: %w", name, err)
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
