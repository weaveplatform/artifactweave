// Package opkg reads bounded IPK archives and their control metadata.
package opkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxPackageSize = 1 << 30
	maxControlSize = 1 << 20
	maxArchiveSize = 2 << 30
)

var (
	errInvalidPackage = errors.New("opkg: invalid ipk package")
	pathPart          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]*$`)
)

// Package is the metadata that opkg reads from an IPK's CONTROL/control.
// Checksums and archive size come from the stored blob, never from the control.
type Package struct {
	Name, Version, Architecture            string
	Description, Maintainer                string
	Depends, Provides, Conflicts, Replaces string
	Section, Priority, Source, License     string
	Path, SHA256, MD5                      string
	Size                                   int64
}

// ValidPath accepts feed-relative IPK names. Artifactory's documented upload
// example allows my-app.ipk, while canonical OpenWrt names include identity.
func ValidPath(p string) bool {
	if p == "" || p != path.Clean(p) || strings.HasPrefix(p, "/") ||
		strings.HasPrefix(p, "../") || !strings.HasSuffix(p, ".ipk") {
		return false
	}
	for _, segment := range strings.Split(p, "/") {
		if !pathPart.MatchString(segment) || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func Feed(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

func IndexPath(feed, name string) string {
	if feed == "" {
		return name
	}
	return feed + "/" + name
}

// ReadPackage accepts OpenWrt's gzip(tar) IPK and the older ar container.
// Both must contain one debian-binary, one control.tar.gz, and one data.tar.gz.
func ReadPackage(ctx context.Context, r io.ReadSeeker, size int64, p string) (Package, error) {
	if !ValidPath(p) || size <= 0 || size > maxPackageSize {
		return Package{}, fmt.Errorf("%w: path or archive size", errInvalidPackage)
	}
	var prefix [8]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return Package{}, fmt.Errorf("opkg: read archive: %w", err)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return Package{}, fmt.Errorf("opkg: seek archive: %w", err)
	}
	var members map[string][]byte
	var err error
	if bytes.Equal(prefix[:], []byte("!<arch>\n")) {
		members, err = readAr(ctx, r, size)
	} else {
		members, err = readTarGzip(ctx, io.LimitReader(r, size))
	}
	if err != nil {
		return Package{}, err
	}
	if string(members["debian-binary"]) != "2.0\n" ||
		len(members["control.tar.gz"]) == 0 || len(members["data.tar.gz"]) == 0 ||
		len(members) != 3 {
		return Package{}, fmt.Errorf("%w: required ipk members", errInvalidPackage)
	}
	control, err := readControlTar(ctx, members["control.tar.gz"])
	if err != nil {
		return Package{}, err
	}
	fields, err := parseFields(control)
	if err != nil {
		return Package{}, err
	}
	name, version, arch := fields["Package"], fields["Version"], fields["Architecture"]
	if !pathPart.MatchString(name) || !pathPart.MatchString(version) ||
		!pathPart.MatchString(arch) {
		return Package{}, fmt.Errorf("%w: control identity", errInvalidPackage)
	}
	base := path.Base(p)
	// Architectures such as x86_64 contain underscores; splitting into
	// exactly three components would silently skip their identity check.
	if strings.Count(base, "_") >= 2 &&
		base != name+"_"+version+"_"+arch+".ipk" {
		return Package{}, fmt.Errorf(
			"%w: filename differs from control identity",
			errInvalidPackage,
		)
	}
	return Package{
		Name: name, Version: version, Architecture: arch,
		Description: fields["Description"], Maintainer: fields["Maintainer"],
		Depends: fields["Depends"], Provides: fields["Provides"],
		Conflicts: fields["Conflicts"], Replaces: fields["Replaces"],
		Section: fields["Section"], Priority: fields["Priority"],
		Source: fields["Source"], License: fields["License"], Path: p,
		Size: size,
	}, nil
}

func readTarGzip(ctx context.Context, r io.Reader) (map[string][]byte, error) {
	z, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("%w: outer gzip: %w", errInvalidPackage, err)
	}
	defer func() { _ = z.Close() }()
	limited := &io.LimitedReader{R: z, N: maxArchiveSize + 1}
	files, err := readTar(ctx, limited, true)
	if err != nil {
		return nil, err
	}
	if err := finishGzip(limited); err != nil {
		return nil, err
	}
	return files, nil
}

func readAr(ctx context.Context, r io.Reader, size int64) (map[string][]byte, error) {
	if size < 8 {
		return nil, fmt.Errorf("%w: short ar archive", errInvalidPackage)
	}
	var magic [8]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil || string(magic[:]) != "!<arch>\n" {
		return nil, fmt.Errorf("%w: ar magic", errInvalidPackage)
	}
	remaining := size - 8
	files := map[string][]byte{}
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("opkg: canceled ar read: %w", err)
		}
		if remaining < 60 {
			return nil, fmt.Errorf("%w: short ar header", errInvalidPackage)
		}
		var header [60]byte
		if _, err := io.ReadFull(r, header[:]); err != nil ||
			string(header[58:]) != "`\n" {
			return nil, fmt.Errorf("%w: ar header", errInvalidPackage)
		}
		remaining -= 60
		name := strings.TrimSuffix(strings.TrimSpace(string(header[:16])), "/")
		n, err := strconv.ParseInt(strings.TrimSpace(string(header[48:58])), 10, 64)
		if err != nil || n <= 0 || n > remaining {
			return nil, fmt.Errorf("%w: ar member size", errInvalidPackage)
		}
		if _, exists := files[name]; exists || name != "debian-binary" &&
			name != "control.tar.gz" && name != "data.tar.gz" {
			return nil, fmt.Errorf("%w: ar member name", errInvalidPackage)
		}
		if name == "data.tar.gz" {
			if err := validateDataTar(ctx, io.LimitReader(r, n)); err != nil {
				return nil, err
			}
			files[name] = []byte{1}
		} else {
			if n > maxControlSize {
				return nil, fmt.Errorf("%w: ar metadata too large", errInvalidPackage)
			}
			data := make([]byte, n)
			if _, err := io.ReadFull(r, data); err != nil {
				return nil, fmt.Errorf("%w: ar member data", errInvalidPackage)
			}
			files[name] = data
		}
		remaining -= n
		if n%2 == 1 {
			var pad [1]byte
			if _, err := io.ReadFull(r, pad[:]); err != nil || pad[0] != '\n' {
				return nil, fmt.Errorf("%w: ar padding", errInvalidPackage)
			}
			remaining--
		}
	}
	return files, nil
}

func readTar(ctx context.Context, r io.Reader, outer bool) (map[string][]byte, error) {
	tr := tar.NewReader(r)
	files := map[string][]byte{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("opkg: canceled archive read: %w", err)
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || h == nil || h.Size < 0 || h.Size > maxArchiveSize ||
			h.Typeflag != tar.TypeReg && h.Typeflag != byte(0) &&
				(outer || h.Typeflag != tar.TypeDir) {
			return nil, fmt.Errorf("%w: tar entry", errInvalidPackage)
		}
		name := strings.TrimPrefix(h.Name, "./")
		if !outer && h.Typeflag == tar.TypeDir {
			continue
		}
		if _, exists := files[name]; exists {
			return nil, fmt.Errorf("%w: duplicate tar member", errInvalidPackage)
		}
		if outer && name != "debian-binary" && name != "control.tar.gz" &&
			name != "data.tar.gz" {
			return nil, fmt.Errorf("%w: unexpected tar member", errInvalidPackage)
		}
		if outer && name == "data.tar.gz" {
			if h.Size == 0 {
				return nil, fmt.Errorf("%w: empty data archive", errInvalidPackage)
			}
			if err := validateDataTar(ctx, tr); err != nil {
				return nil, err
			}
			files[name] = []byte{1}
			continue
		}
		if h.Size > maxControlSize {
			return nil, fmt.Errorf("%w: metadata member too large", errInvalidPackage)
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxControlSize+1))
		if err != nil || int64(len(data)) != h.Size {
			return nil, fmt.Errorf("%w: truncated tar member", errInvalidPackage)
		}
		files[name] = data
	}
	return files, nil
}

func validateDataTar(ctx context.Context, compressed io.Reader) error {
	z, err := gzip.NewReader(compressed)
	if err != nil {
		return fmt.Errorf("%w: data gzip", errInvalidPackage)
	}
	defer func() { _ = z.Close() }()
	limited := &io.LimitedReader{R: z, N: maxArchiveSize + 1}
	tr := tar.NewReader(limited)
	for count := 0; ; count++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("opkg: canceled data read: %w", err)
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return finishGzip(limited)
		}
		if err != nil || h == nil || count >= 100000 || h.Size < 0 ||
			h.Size > maxArchiveSize || h.Typeflag != tar.TypeReg &&
			h.Typeflag != byte(0) && h.Typeflag != tar.TypeDir &&
			h.Typeflag != tar.TypeSymlink && h.Typeflag != tar.TypeLink {
			return fmt.Errorf("%w: data tar entry", errInvalidPackage)
		}
		p := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag == tar.TypeDir {
			p = strings.TrimSuffix(p, "/")
		}
		if h.Typeflag == tar.TypeDir && (p == "" || p == ".") {
			continue
		}
		if p == "" || path.Clean(p) != p || strings.HasPrefix(p, "../") ||
			strings.HasPrefix(p, "/") {
			return fmt.Errorf("%w: data tar path", errInvalidPackage)
		}
		// gzip output is bounded by limited, including this tar entry.
		//nolint:gosec
		if _, err := io.Copy(
			io.Discard,
			tr,
		); err != nil {
			return fmt.Errorf("%w: data tar body", errInvalidPackage)
		}
	}
}

func readControlTar(ctx context.Context, compressed []byte) ([]byte, error) {
	z, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("%w: control gzip", errInvalidPackage)
	}
	defer func() { _ = z.Close() }()
	limited := &io.LimitedReader{R: z, N: maxControlSize + 1}
	files, err := readTar(ctx, limited, false)
	if err != nil {
		return nil, err
	}
	if err := finishGzip(limited); err != nil {
		return nil, err
	}
	control := files["control"]
	if len(control) == 0 {
		return nil, fmt.Errorf("%w: missing control", errInvalidPackage)
	}
	return control, nil
}

// tar.Reader stops at its end-of-archive marker. Consume the rest of the
// compressed member so gzip verifies its checksum and length trailer.
func finishGzip(r *io.LimitedReader) error {
	if _, err := io.Copy(io.Discard, r); err != nil || r.N == 0 {
		return fmt.Errorf("%w: compressed member checksum or size", errInvalidPackage)
	}
	return nil
}

func parseFields(body []byte) (map[string]string, error) {
	if len(body) == 0 || len(body) > maxControlSize || !utf8.Valid(body) ||
		bytes.IndexByte(body, 0) >= 0 || !bytes.HasSuffix(body, []byte("\n")) {
		return nil, fmt.Errorf("%w: control text", errInvalidPackage)
	}
	fields := map[string]string{}
	last := ""
	for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		if strings.HasPrefix(line, " ") {
			if last == "" {
				return nil, fmt.Errorf("%w: orphan continuation", errInvalidPackage)
			}
			fields[last] += "\n" + line
			continue
		}
		key, value, ok := strings.Cut(line, ": ")
		if !ok || key == "" || strings.ContainsAny(line, "\r\x00") ||
			len(key) > 64 || len(value) > maxControlSize {
			return nil, fmt.Errorf("%w: control field", errInvalidPackage)
		}
		if _, exists := fields[key]; exists {
			return nil, fmt.Errorf("%w: duplicate control field", errInvalidPackage)
		}
		fields[key], last = value, key
	}
	return fields, nil
}

func Digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
