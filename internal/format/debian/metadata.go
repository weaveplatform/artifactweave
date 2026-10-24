package debian

import (
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

const (
	maxPackagesIndex = 64 << 20
	maxPackageRows   = 100000
	maxReleaseIndex  = 4 << 20
)

// PackageRecord is a validated stanza in an upstream Packages index. Raw
// retains package fields for virtual index composition, while Filename and
// SHA256 bind the advertised download to one repository-relative artifact.
type PackageRecord struct {
	Name, Version, Architecture string
	Filename, SHA256            string
	Size                        int64
	Raw                         []byte
}

// PackagesIndexPath accepts only the apt binary-index layout this adapter
// understands. The same grammar guards remote metadata and virtual reads.
func PackagesIndexPath(p string) bool {
	parts := strings.Split(p, "/")
	if len(parts) != 5 || parts[0] != "dists" ||
		!architecture.MatchString(parts[1]) ||
		!architecture.MatchString(parts[2]) ||
		!strings.HasPrefix(parts[3], "binary-") ||
		!architecture.MatchString(strings.TrimPrefix(parts[3], "binary-")) ||
		path.Clean(p) != p {
		return false
	}
	return indexName(parts[4], "Packages")
}

// SourcesIndexPath accepts apt source indexes for one distribution/component.
func SourcesIndexPath(p string) bool {
	parts := strings.Split(p, "/")
	return len(parts) == 5 && parts[0] == "dists" &&
		architecture.MatchString(parts[1]) && architecture.MatchString(parts[2]) &&
		parts[3] == "source" && path.Clean(p) == p &&
		indexName(parts[4], "Sources")
}

func indexName(got, want string) bool {
	if got == want {
		return true
	}
	for _, ext := range []string{".gz", ".xz", ".bz2", ".zst"} {
		if got == want+ext {
			return true
		}
	}
	return false
}

// ReleaseMetadataPath names one of the metadata files apt checks before it
// accepts indexes for a distribution.
func ReleaseMetadataPath(p string) bool {
	parts := strings.Split(p, "/")
	return len(parts) == 3 && parts[0] == "dists" &&
		architecture.MatchString(parts[1]) &&
		(parts[2] == "Release" || parts[2] == "InRelease" || parts[2] == "Release.gpg")
}

// ByHashMetadataPath names a checksum-addressed Packages or Sources index.
func ByHashMetadataPath(p string) (string, bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 7 || parts[0] != "dists" ||
		!architecture.MatchString(parts[1]) || !architecture.MatchString(parts[2]) ||
		parts[4] != "by-hash" || parts[5] != "SHA256" ||
		len(parts[6]) != sha256.Size*2 ||
		(parts[3] != "source" && (!strings.HasPrefix(parts[3], "binary-") || !architecture.MatchString(strings.TrimPrefix(parts[3], "binary-")))) {
		return "", false
	}
	if _, err := hex.DecodeString(parts[6]); err != nil {
		return "", false
	}
	return strings.ToLower(parts[6]), true
}

// ValidateReleaseMetadata bounds upstream metadata before cache publication.
// Apt verifies the OpenPGP signature with its configured key; this checks the
// control structure and checksum paths so malformed bytes do not enter cache.
func ValidateReleaseMetadata(p string, body []byte) error {
	if !ReleaseMetadataPath(p) || len(body) == 0 || len(body) > maxReleaseIndex ||
		bytes.IndexByte(body, 0) >= 0 {
		return fmt.Errorf("%w: invalid Release metadata", errInvalidBinary)
	}
	if strings.HasSuffix(p, "/Release.gpg") {
		if !bytes.HasPrefix(body, []byte("-----BEGIN PGP SIGNATURE-----")) ||
			!bytes.Contains(body, []byte("-----END PGP SIGNATURE-----")) {
			return fmt.Errorf("%w: invalid Release signature armor", errInvalidBinary)
		}
		return nil
	}
	if strings.HasSuffix(p, "/InRelease") {
		block, rest := clearsign.Decode(body)
		if block == nil || len(bytes.TrimSpace(rest)) != 0 {
			return fmt.Errorf("%w: invalid InRelease armor", errInvalidBinary)
		}
		body = block.Plaintext
	}
	fields, err := controlFields(body, maxReleaseIndex)
	if err != nil || fields["suite"] == "" || fields["architectures"] == "" ||
		fields["components"] == "" || fields["sha256"] == "" {
		return fmt.Errorf("%w: incomplete Release fields", errInvalidBinary)
	}
	rows := strings.Split(fields["sha256"], "\n")
	if len(rows) < 2 || len(rows) > maxPackageRows {
		return fmt.Errorf("%w: Release checksum entries", errInvalidBinary)
	}
	for _, line := range rows[1:] {
		parts := strings.Fields(line)
		if len(parts) != 3 || len(parts[0]) != sha256.Size*2 ||
			!validIndexPath(parts[2]) {
			return fmt.Errorf("%w: Release checksum entry", errInvalidBinary)
		}
		if _, err := hex.DecodeString(parts[0]); err != nil {
			return fmt.Errorf("%w: Release SHA256", errInvalidBinary)
		}
		if size, err := strconv.ParseInt(parts[1], 10, 64); err != nil || size < 0 {
			return fmt.Errorf("%w: Release member size", errInvalidBinary)
		}
	}
	return nil
}

// PackageIndexRef binds a Packages representation to the SHA256 and size
// declared by its member Release file.
type PackageIndexRef struct {
	Path   string
	SHA256 string
	Size   int64
}

// BasePath is the uncompressed Packages or Sources path for a Release member.
func (r PackageIndexRef) BasePath() string { return indexBase(r.Path) }

// ReleasePackageIndexes selects the plain Packages index for each advertised
// component and architecture, falling back to gzip when plain is absent.
// The caller must compare the fetched bytes with each declared digest.
func ReleasePackageIndexes(distribution string, body []byte) ([]PackageIndexRef, error) {
	refs, err := ReleaseIndexRefs(distribution, body)
	if err != nil {
		return nil, err
	}
	selected := map[string]PackageIndexRef{}
	for _, candidate := range refs {
		if !PackagesIndexPath(candidate.Path) {
			continue
		}
		base := indexBase(candidate.Path)
		if current, exists := selected[base]; !exists ||
			indexRank(candidate.Path) < indexRank(current.Path) {
			selected[base] = candidate
		}
	}
	out := make([]PackageIndexRef, 0, len(selected))
	for _, ref := range selected {
		out = append(out, ref)
	}
	slices.SortFunc(out, func(a, b PackageIndexRef) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

func indexBase(p string) string {
	for _, ext := range []string{".gz", ".xz", ".bz2", ".zst"} {
		if strings.HasSuffix(p, ext) {
			return strings.TrimSuffix(p, ext)
		}
	}
	return p
}

func indexRank(p string) int {
	for i, ext := range []string{".gz", ".xz", ".bz2", ".zst"} {
		if strings.HasSuffix(p, ext) {
			return i + 1
		}
	}
	return 0
}

// ReleaseIndexRefs returns every checksum-bound metadata path in a Release.
// Snapshot creation uses these declarations to materialize an immutable view.
func ReleaseIndexRefs(distribution string, body []byte) ([]PackageIndexRef, error) {
	if !ReleaseMetadataPath("dists/"+distribution+"/Release") ||
		ValidateReleaseMetadata("dists/"+distribution+"/Release", body) != nil {
		return nil, fmt.Errorf("%w: invalid member Release", errInvalidBinary)
	}
	fields, _ := controlFields(body, maxReleaseIndex)
	out := []PackageIndexRef{}
	for _, line := range strings.Split(fields["sha256"], "\n")[1:] {
		parts := strings.Fields(line)
		indexPath := "dists/" + distribution + "/" + parts[2]
		size, _ := strconv.ParseInt(parts[1], 10, 64)
		out = append(out, PackageIndexRef{
			Path: indexPath, SHA256: strings.ToLower(parts[0]), Size: size,
		})
	}
	slices.SortFunc(out, func(a, b PackageIndexRef) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// DecodePackagesIndex accepts the plain and mandatory gzip index encodings.
// The bound applies after decompression, so a small compressed bomb cannot
// enter the remote cache or a virtual index.
func DecodePackagesIndex(body []byte) ([]PackageRecord, error) {
	var err error
	body, err = decodeIndexCompression(body)
	if err != nil {
		return nil, err
	}
	return ParsePackagesIndex(body)
}

func decodeIndexCompression(body []byte) ([]byte, error) {
	var reader io.Reader
	var closeReader func()
	switch {
	case bytes.HasPrefix(body, []byte{0x1f, 0x8b}):
		z, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("%w: gzip index: %w", errInvalidBinary, err)
		}
		reader, closeReader = z, func() { _ = z.Close() }
	case bytes.HasPrefix(body, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}):
		z, err := xz.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("%w: xz index: %w", errInvalidBinary, err)
		}
		reader = z
	case bytes.HasPrefix(body, []byte("BZh")):
		reader = bzip2.NewReader(bytes.NewReader(body))
	case bytes.HasPrefix(body, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		z, err := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderMaxMemory(maxPackagesIndex))
		if err != nil {
			return nil, fmt.Errorf("%w: zstd index: %w", errInvalidBinary, err)
		}
		reader, closeReader = z, z.Close
	default:
		return body, nil
	}
	if closeReader != nil {
		defer closeReader()
	}
	plain, err := io.ReadAll(io.LimitReader(reader, maxPackagesIndex+1))
	if err != nil {
		return nil, fmt.Errorf("%w: compressed index body: %w", errInvalidBinary, err)
	}
	return plain, nil
}

// ParsePackagesIndex checks the complete apt Packages representation before
// it can be cached or combined into a virtual index. A malformed stanza must
// not make apt resolve a package that the registry cannot safely address.
func ParsePackagesIndex(body []byte) ([]PackageRecord, error) {
	if len(body) > maxPackagesIndex || bytes.IndexByte(body, 0) >= 0 {
		return nil, fmt.Errorf("%w: Packages index size or contents", errInvalidBinary)
	}
	if len(body) == 0 {
		return nil, nil
	}
	if !bytes.HasSuffix(body, []byte("\n\n")) {
		return nil, fmt.Errorf("%w: incomplete Packages stanza", errInvalidBinary)
	}
	parts := bytes.Split(body[:len(body)-2], []byte("\n\n"))
	if len(parts) > maxPackageRows {
		return nil, fmt.Errorf("%w: too many Packages stanzas", errInvalidBinary)
	}
	out := make([]PackageRecord, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, raw := range parts {
		if len(raw) == 0 || len(raw) > maxControlFile {
			return nil, fmt.Errorf("%w: Packages stanza size", errInvalidBinary)
		}
		fields, err := parseControl(raw)
		if err != nil {
			return nil, err
		}
		size, err := strconv.ParseInt(fields["Size"], 10, 64)
		if err != nil || size <= 0 ||
			!packageName.MatchString(fields["Package"]) ||
			fields["Version"] == "" || strings.ContainsAny(fields["Version"], " \t\r\n/") ||
			!architecture.MatchString(fields["Architecture"]) ||
			!validPackagePath(fields["Filename"]) ||
			len(fields["SHA256"]) != sha256.Size*2 {
			return nil, fmt.Errorf("%w: invalid Packages coordinates", errInvalidBinary)
		}
		if _, err := hex.DecodeString(fields["SHA256"]); err != nil {
			return nil, fmt.Errorf("%w: Packages SHA256", errInvalidBinary)
		}
		if seen[fields["Filename"]] {
			return nil, fmt.Errorf("%w: duplicate Packages filename", errInvalidBinary)
		}
		seen[fields["Filename"]] = true
		out = append(out, PackageRecord{
			Name: fields["Package"], Version: fields["Version"],
			Architecture: fields["Architecture"], Filename: fields["Filename"],
			SHA256: strings.ToLower(fields["SHA256"]), Size: size,
			Raw: append(bytes.Clone(raw), '\n', '\n'),
		})
	}
	return out, nil
}
