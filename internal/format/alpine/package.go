// Package alpine implements Alpine APK v2 package and index metadata.
package alpine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // APKINDEX v2 requires SHA-1 of the compressed control stream.
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxControlPrefix = 8 << 20
	maxSegmentBytes  = 4 << 20
	maxInfoBytes     = 1 << 20
	maxDataEntries   = 200000
	maxExpandedBytes = 4 << 30
	legacyRegular    = byte(0) // POSIX tar's legacy regular-file type.
)

var (
	errInvalidPackage = errors.New("alpine: invalid apk package")
	pathPart          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
)

// Package is the native metadata extracted from a validated APK v2 archive.
type Package struct {
	Name, Version, Architecture  string
	Branch, Component            string
	Path                         string
	ArchiveSize                  int64
	InstalledSize                int64
	Description, URL, License    string
	Origin, Maintainer, Commit   string
	BuildTime                    int64
	Depends, Provides, InstallIf []string
	ControlSHA1                  string
	IndexRaw                     string
}

// ParsePath accepts the branch/component/architecture/name-version.apk layout
// documented by Artifactory. It does not infer identity from a filename alone.
func ParsePath(p string) (branch, component, arch, file string, ok bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 4 || !strings.HasSuffix(parts[3], ".apk") {
		return "", "", "", "", false
	}
	for _, part := range parts {
		if !pathPart.MatchString(part) || part == "." || part == ".." {
			return "", "", "", "", false
		}
	}
	return parts[0], parts[1], parts[2], parts[3], true
}

// ReadPackage validates the bounded control streams and the optional
// datahash, then extracts only metadata that belongs in APKINDEX. The caller
// provides the stored blob, so the same check can guard local and remote data.
func ReadPackage(ctx context.Context, body io.ReadSeeker, size int64, p string) (Package, error) {
	branch, component, arch, file, ok := ParsePath(p)
	if !ok || size <= 0 {
		return Package{}, fmt.Errorf("%w: path or size", errInvalidPackage)
	}
	limit := min(size, maxControlPrefix)
	prefix := make([]byte, limit)
	if _, err := io.ReadFull(body, prefix); err != nil {
		return Package{}, fmt.Errorf("alpine: read control prefix: %w", err)
	}
	r := bytes.NewReader(prefix)
	first, err := readSegment(ctx, r)
	if err != nil {
		return Package{}, err
	}
	controlStart, control := int64(0), first
	if _, hasInfo := first[".PKGINFO"]; !hasInfo {
		if !hasSignature(first) {
			return Package{}, fmt.Errorf("%w: no control or signature stream", errInvalidPackage)
		}
		controlStart = int64(len(prefix) - r.Len())
		control, err = readSegment(ctx, r)
		if err != nil {
			return Package{}, err
		}
	}
	controlEnd := int64(len(prefix) - r.Len())
	info, ok := control[".PKGINFO"]
	if !ok {
		return Package{}, fmt.Errorf("%w: missing .PKGINFO", errInvalidPackage)
	}
	fields, err := parseInfo(info)
	if err != nil {
		return Package{}, err
	}
	name, version, declaredArch := single(
		fields,
		"pkgname",
	), single(
		fields,
		"pkgver",
	), single(
		fields,
		"arch",
	)
	if !pathPart.MatchString(name) || !pathPart.MatchString(version) ||
		declaredArch != arch || file != name+"-"+version+".apk" {
		return Package{}, fmt.Errorf("%w: package identity does not match path", errInvalidPackage)
	}
	installed, err := strconv.ParseInt(single(fields, "size"), 10, 64)
	if err != nil || installed < 0 {
		return Package{}, fmt.Errorf("%w: invalid installed size", errInvalidPackage)
	}
	hash := single(fields, "datahash")
	if len(hash) != sha256.Size*2 {
		return Package{}, fmt.Errorf("%w: missing or invalid datahash", errInvalidPackage)
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return Package{}, fmt.Errorf("%w: invalid datahash: %w", errInvalidPackage, err)
	}
	if _, err := body.Seek(controlEnd, io.SeekStart); err != nil {
		return Package{}, fmt.Errorf("alpine: seek data stream: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, body); err != nil {
		return Package{}, fmt.Errorf("alpine: hash data stream: %w", err)
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), hash) {
		return Package{}, fmt.Errorf("%w: datahash mismatch", errInvalidPackage)
	}
	if _, err := body.Seek(controlEnd, io.SeekStart); err != nil {
		return Package{}, fmt.Errorf("alpine: seek data archive: %w", err)
	}
	if err := validateData(ctx, body, size-controlEnd); err != nil {
		return Package{}, err
	}
	// The v2 C field is the SHA-1 of compressed control bytes, not the
	// archive's checksum. apk-tools uses this exact value to bind a package.
	//nolint:gosec // APKINDEX v2 requires SHA-1 of the compressed control stream.
	controlHash := sha1.Sum(prefix[controlStart:controlEnd])
	return Package{
		Name: name, Version: version, Architecture: arch,
		Branch: branch, Component: component, Path: p,
		ArchiveSize: size, InstalledSize: installed,
		Description: single(fields, "pkgdesc"), URL: single(fields, "url"),
		License: single(fields, "license"), Origin: single(fields, "origin"),
		Maintainer: single(fields, "maintainer"), Commit: single(fields, "commit"),
		BuildTime: parseBuildTime(single(fields, "builddate")),
		Depends:   fields["depend"], Provides: fields["provides"], InstallIf: fields["install_if"],
		ControlSHA1: base64.StdEncoding.EncodeToString(controlHash[:]),
	}, nil
}

func hasSignature(files map[string][]byte) bool {
	for name := range files {
		if strings.HasPrefix(name, ".SIGN.RSA.") || strings.HasPrefix(name, ".SIGN.RSA256.") {
			return true
		}
	}
	return false
}

func readSegment(ctx context.Context, r *bytes.Reader) (map[string][]byte, error) {
	z, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("%w: gzip stream: %w", errInvalidPackage, err)
	}
	defer func() { _ = z.Close() }()
	z.Multistream(false)
	raw, err := io.ReadAll(io.LimitReader(z, maxSegmentBytes+1))
	if err != nil || len(raw) > maxSegmentBytes || len(raw)%512 != 0 ||
		len(raw) >= 1024 && bytes.Equal(raw[len(raw)-1024:], make([]byte, 1024)) {
		return nil, fmt.Errorf("%w: malformed or uncut control tar", errInvalidPackage)
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	files := map[string][]byte{}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("alpine: parse canceled: %w", err)
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || h == nil || h.Typeflag != tar.TypeReg || h.Size < 0 ||
			h.Size > maxInfoBytes {
			return nil, fmt.Errorf("%w: unsafe tar entry", errInvalidPackage)
		}
		total += h.Size
		if total > maxSegmentBytes || len(files) >= 32 {
			return nil, fmt.Errorf("%w: control stream limit", errInvalidPackage)
		}
		if _, exists := files[h.Name]; exists || !strings.HasPrefix(h.Name, ".") {
			return nil, fmt.Errorf("%w: invalid or duplicate control entry", errInvalidPackage)
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxInfoBytes+1))
		if err != nil || int64(len(data)) != h.Size {
			return nil, fmt.Errorf("%w: truncated control entry", errInvalidPackage)
		}
		files[h.Name] = data
	}
	return files, nil
}

// validateData mirrors the archive constraints apk applies after trusting an
// index: a complete final gzip stream, explicit parent directories and a
// digest on every file. The read is streaming and has bounded expansion.
func validateData(ctx context.Context, body io.Reader, compressedSize int64) error {
	r := &countedReader{source: body}
	z, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("%w: data gzip: %w", errInvalidPackage, err)
	}
	defer func() { _ = z.Close() }()
	z.Multistream(false)
	tr := tar.NewReader(z)
	dirs := map[string]bool{".": true}
	var expanded int64
	for entries := 0; ; entries++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("alpine: parse canceled: %w", err)
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || h == nil || entries >= maxDataEntries || h.Size < 0 ||
			h.Size > maxExpandedBytes-expanded {
			return fmt.Errorf("%w: data tar limit or entry", errInvalidPackage)
		}
		expanded += h.Size
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || path.Clean(name) != name || strings.HasPrefix(name, "/") ||
			name == ".." || strings.HasPrefix(name, "../") || strings.ContainsRune(name, '\\') ||
			!dirs[path.Dir(name)] {
			return fmt.Errorf("%w: unsafe data path or missing directory", errInvalidPackage)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			dirs[name] = true
		case tar.TypeReg, legacyRegular:
			want := h.PAXRecords["APK-TOOLS.checksum.SHA1"]
			if len(want) != sha1.Size*2 {
				return fmt.Errorf("%w: missing file checksum", errInvalidPackage)
			}
			sum := sha1.New() //nolint:gosec // apk v2 per-file wire checksum.
			if _, err := io.CopyN(sum, tr, h.Size); err != nil ||
				!strings.EqualFold(hex.EncodeToString(sum.Sum(nil)), want) {
				return fmt.Errorf("%w: file checksum mismatch", errInvalidPackage)
			}
		case tar.TypeSymlink, tar.TypeLink:
			if h.Linkname == "" {
				return fmt.Errorf("%w: empty link target", errInvalidPackage)
			}
		default:
			return fmt.Errorf("%w: unsupported data tar entry", errInvalidPackage)
		}
	}
	// Draining through a bound verifies the gzip trailer and refuses
	// decompressed bytes after the tar end beyond the package limit.
	if _, err := io.CopyN(io.Discard, z, maxExpandedBytes+1); !errors.Is(err, io.EOF) ||
		r.count != compressedSize {
		return fmt.Errorf("%w: data stream trailer", errInvalidPackage)
	}
	return nil
}

type countedReader struct {
	source io.Reader
	count  int64
}

func (r *countedReader) Read(p []byte) (int, error) {
	n, err := r.source.Read(p)
	r.count += int64(n)
	return n, err //nolint:wrapcheck // This method transparently implements io.Reader.
}

func (r *countedReader) ReadByte() (byte, error) {
	var one [1]byte
	_, err := io.ReadFull(r, one[:])
	return one[0], err //nolint:wrapcheck // This method transparently implements io.ByteReader.
}

func parseInfo(data []byte) (map[string][]string, error) {
	if len(data) == 0 || len(data) > maxInfoBytes || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("%w: .PKGINFO size or contents", errInvalidPackage)
	}
	fields := map[string][]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, " = ")
		if !ok || key == "" || value == "" || strings.ContainsAny(value, "\r\x00") {
			return nil, fmt.Errorf("%w: malformed .PKGINFO field", errInvalidPackage)
		}
		fields[key] = append(fields[key], value)
	}
	for _, key := range []string{"pkgname", "pkgver", "arch", "size", "datahash"} {
		if len(fields[key]) > 1 {
			return nil, fmt.Errorf("%w: duplicate %s", errInvalidPackage, key)
		}
	}
	return fields, nil
}

func single(fields map[string][]string, key string) string {
	if len(fields[key]) == 0 {
		return ""
	}
	return fields[key][0]
}

func parseBuildTime(s string) int64 {
	t, _ := strconv.ParseInt(s, 10, 64)
	return t
}
