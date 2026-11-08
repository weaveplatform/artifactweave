// Package debian implements Debian package and repository metadata.
package debian

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

const (
	maxControlMember = 16 << 20
	maxControlTar    = 32 << 20
	maxControlFile   = 1 << 20
	maxDataTar       = 4 << 30
	maxBinarySize    = 8 << 30
)

var (
	errInvalidBinary = errors.New("debian: invalid binary package")
	packageName      = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
	architecture     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

// Binary is the metadata in a validated .deb control archive. Control holds
// the original deb822 fields for the Packages index, including continuation
// lines, so package-specific fields are not silently discarded.
type Binary struct {
	Name, Version, Architecture string
	Control                     []byte
}

type arMember struct {
	name   string
	offset int64
	size   int64
}

// ReadBinary validates the ordered ar members, bounded control metadata and
// the data archive before any uploaded package can enter an apt index.
func ReadBinary(ctx context.Context, body io.ReaderAt, size int64) (Binary, error) {
	if size < 8 || size > maxBinarySize {
		return Binary{}, fmt.Errorf("%w: archive size", errInvalidBinary)
	}
	magic := make([]byte, 8)
	if _, err := body.ReadAt(magic, 0); err != nil || string(magic) != "!<arch>\n" {
		return Binary{}, fmt.Errorf("%w: ar magic", errInvalidBinary)
	}
	var members []arMember
	for offset := int64(8); offset < size; {
		if err := ctx.Err(); err != nil {
			return Binary{}, fmt.Errorf("debian: parse canceled: %w", err)
		}
		m, next, err := readARMember(body, offset, size)
		if err != nil {
			return Binary{}, err
		}
		offset = next
		if strings.HasPrefix(m.name, "_") {
			continue // reserved extension members are ignored by dpkg
		}
		members = append(members, m)
		if len(members) > 3 {
			break // dpkg permits future members after data.tar
		}
	}
	if len(members) < 3 || members[0].name != "debian-binary" ||
		!strings.HasPrefix(members[1].name, "control.tar") ||
		!strings.HasPrefix(members[2].name, "data.tar") {
		return Binary{}, fmt.Errorf("%w: required ar members or order", errInvalidBinary)
	}
	if members[0].size > 128 {
		return Binary{}, fmt.Errorf("%w: version member exceeds limit", errInvalidBinary)
	}
	version := make([]byte, members[0].size)
	if _, err := body.ReadAt(version, members[0].offset); err != nil ||
		!bytes.HasPrefix(version, []byte("2.")) || bytes.IndexByte(version, '\n') < 0 {
		return Binary{}, fmt.Errorf("%w: unsupported archive version", errInvalidBinary)
	}
	if members[1].size > maxControlMember {
		return Binary{}, fmt.Errorf("%w: control member exceeds limit", errInvalidBinary)
	}
	control, err := readControl(ctx, body, members[1])
	if err != nil {
		return Binary{}, err
	}
	if err := validateData(ctx, body, members[2]); err != nil {
		return Binary{}, err
	}
	fields, err := parseControl(control)
	if err != nil {
		return Binary{}, err
	}
	name, versionString, arch := fields["Package"], fields["Version"], fields["Architecture"]
	if !packageName.MatchString(name) || versionString == "" ||
		strings.ContainsAny(versionString, " \t\r\n/") ||
		!architecture.MatchString(arch) || fields["Maintainer"] == "" ||
		fields["Description"] == "" {
		return Binary{}, fmt.Errorf("%w: mandatory control fields", errInvalidBinary)
	}
	return Binary{Name: name, Version: versionString, Architecture: arch, Control: control}, nil
}

func readARMember(body io.ReaderAt, offset, size int64) (arMember, int64, error) {
	if size-offset < 60 {
		return arMember{}, 0, fmt.Errorf("%w: truncated ar header", errInvalidBinary)
	}
	h := make([]byte, 60)
	if _, err := body.ReadAt(h, offset); err != nil || string(h[58:]) != "`\n" {
		return arMember{}, 0, fmt.Errorf("%w: malformed ar header", errInvalidBinary)
	}
	name := strings.TrimSpace(string(h[:16]))
	name = strings.TrimSuffix(name, "/")
	if name == "" || strings.ContainsAny(name, " \t\\\x00") {
		return arMember{}, 0, fmt.Errorf("%w: invalid ar member name", errInvalidBinary)
	}
	count, err := strconv.ParseInt(strings.TrimSpace(string(h[48:58])), 10, 64)
	if err != nil || count < 0 || count > size-offset-60 {
		return arMember{}, 0, fmt.Errorf("%w: ar member size", errInvalidBinary)
	}
	start := offset + 60
	next := start + count + count%2
	if next > size {
		return arMember{}, 0, fmt.Errorf("%w: ar member padding", errInvalidBinary)
	}
	return arMember{name: name, offset: start, size: count}, next, nil
}

func unpackMember(body io.ReaderAt, m arMember) (io.Reader, func(), error) {
	r := io.NewSectionReader(body, m.offset, m.size)
	switch {
	case strings.HasSuffix(m.name, ".gz"):
		z, err := gzip.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: gzip member: %w", errInvalidBinary, err)
		}
		return z, func() { _ = z.Close() }, nil
	case strings.HasSuffix(m.name, ".xz"):
		z, err := xz.NewReader(r)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: xz member: %w", errInvalidBinary, err)
		}
		return z, func() {}, nil
	case strings.HasSuffix(m.name, ".zst"):
		limit := uint64(maxControlTar)
		if strings.HasPrefix(m.name, "data.tar") {
			limit = maxDataTar
		}
		z, err := zstd.NewReader(r, zstd.WithDecoderMaxMemory(limit))
		if err != nil {
			return nil, nil, fmt.Errorf("%w: zstd member: %w", errInvalidBinary, err)
		}
		return z, z.Close, nil
	case m.name == "control.tar" || m.name == "data.tar":
		return r, func() {}, nil
	default:
		return nil, nil, fmt.Errorf("%w: unsupported tar compression", errInvalidBinary)
	}
}

func readControl(ctx context.Context, body io.ReaderAt, m arMember) ([]byte, error) {
	r, closeReader, err := unpackMember(body, m)
	if err != nil {
		return nil, err
	}
	defer closeReader()
	bounded := &io.LimitedReader{R: r, N: maxControlTar + 1}
	t := tar.NewReader(bounded)
	var control []byte
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("debian: control parse canceled: %w", err)
		}
		h, err := t.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || h == nil || h.Size < 0 || h.Size > maxControlFile {
			return nil, fmt.Errorf("%w: unsafe control tar", errInvalidBinary)
		}
		name := strings.TrimPrefix(h.Name, "./")
		if name == "control" {
			if control != nil || h.Typeflag != tar.TypeReg {
				return nil, fmt.Errorf("%w: duplicate or invalid control file", errInvalidBinary)
			}
			control = make([]byte, h.Size)
			if _, err := io.ReadFull(t, control); err != nil {
				return nil, fmt.Errorf("%w: truncated control file", errInvalidBinary)
			}
		}
	}
	if _, err := io.Copy(io.Discard, bounded); err != nil || bounded.N <= 0 || len(control) == 0 {
		return nil, fmt.Errorf("%w: missing or excessive control data", errInvalidBinary)
	}
	return control, nil
}

func validateData(ctx context.Context, body io.ReaderAt, m arMember) error {
	r, closeReader, err := unpackMember(body, m)
	if err != nil {
		return err
	}
	defer closeReader()
	bounded := &io.LimitedReader{R: r, N: maxDataTar + 1}
	t := tar.NewReader(bounded)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("debian: data parse canceled: %w", err)
		}
		if _, err := t.Next(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("%w: malformed data tar: %w", errInvalidBinary, err)
		}
	}
	if _, err := io.Copy(io.Discard, bounded); err != nil || bounded.N <= 0 {
		return fmt.Errorf("%w: data tar exceeds limit", errInvalidBinary)
	}
	return nil
}

func parseControl(data []byte) (map[string]string, error) {
	if len(data) == 0 || len(data) > maxControlFile || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("%w: control size or contents", errInvalidBinary)
	}
	fields := map[string]string{}
	var current string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			return nil, fmt.Errorf("%w: extra control stanza", errInvalidBinary)
		}
		if line[0] == ' ' || line[0] == '\t' {
			if current == "" {
				return nil, fmt.Errorf("%w: orphan continuation", errInvalidBinary)
			}
			fields[current] += "\n" + line
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		_, duplicate := fields[key]
		if !ok || key == "" || strings.TrimSpace(value) == "" || duplicate {
			return nil, fmt.Errorf("%w: malformed or duplicate control field", errInvalidBinary)
		}
		fields[key] = strings.TrimLeft(value, " ")
		current = key
	}
	return fields, nil
}
