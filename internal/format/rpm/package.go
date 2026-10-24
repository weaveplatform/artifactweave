// Package rpm reads bounded RPM v4 headers for native repository metadata.
package rpm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

const (
	leadSize       = 96
	headerLimit    = 16 << 20
	headerEntries  = 100000
	headerIntroLen = 16
	indexEntryLen  = 16
)

var (
	errInvalidPackage = errors.New("rpm: invalid package")
	namePart          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]*$`)
	headerMagic       = []byte{0x8e, 0xad, 0xe8, 0x01, 0, 0, 0, 0}
)

// Package is the identity and descriptive metadata read from a genuine RPM
// header. Path and checksums are supplied by the content-addressed item.
type Package struct {
	Name, Version, Release, Architecture string
	Summary, Description, License, URL   string
	Path                                 string
	Epoch                                uint32
	InstalledSize                        uint64
	BuildTime                            uint32
	ArchiveSize                          int64
	SHA256                               string
}

type headerEntry struct {
	typ, offset, count uint32
}

type rpmHeader struct {
	entries map[uint32]headerEntry
	data    []byte
}

// ReadPackage validates the lead, signature and main header bounds, the
// package's header identity against its path, and the compressed payload
// digest when the RPM supplies one. The reader is the stored blob itself.
func ReadPackage(ctx context.Context, body io.ReadSeeker, size int64, p string) (Package, error) {
	if size < leadSize+2*headerIntroLen+1 || !validPath(p) {
		return Package{}, fmt.Errorf("%w: path or size", errInvalidPackage)
	}
	var lead [leadSize]byte
	if _, err := io.ReadFull(body, lead[:]); err != nil {
		return Package{}, fmt.Errorf("rpm: read lead: %w", err)
	}
	if !bytes.Equal(lead[:4], []byte{0xed, 0xab, 0xee, 0xdb}) || lead[4] < 3 || lead[4] > 4 {
		return Package{}, fmt.Errorf("%w: lead magic or version", errInvalidPackage)
	}
	signature, end, err := readHeader(body, leadSize, size)
	if err != nil {
		return Package{}, fmt.Errorf("rpm: signature header: %w", err)
	}
	mainOffset := (end + 7) &^ 7
	main, payloadOffset, err := readHeader(body, mainOffset, size)
	if err != nil {
		return Package{}, fmt.Errorf("rpm: package header: %w", err)
	}
	if payloadOffset >= size {
		return Package{}, fmt.Errorf("%w: missing payload", errInvalidPackage)
	}
	name, err := main.stringTag(1000)
	if err != nil {
		return Package{}, err
	}
	version, err := main.stringTag(1001)
	if err != nil {
		return Package{}, err
	}
	release, err := main.stringTag(1002)
	if err != nil {
		return Package{}, err
	}
	architecture, err := main.stringTag(1022)
	if err != nil {
		return Package{}, err
	}
	if !namePart.MatchString(name) || !namePart.MatchString(version) ||
		!namePart.MatchString(release) || !namePart.MatchString(architecture) ||
		path.Base(p) != name+"-"+version+"-"+release+"."+architecture+".rpm" {
		return Package{}, fmt.Errorf("%w: header identity differs from path", errInvalidPackage)
	}
	pkg := Package{
		Name: name, Version: version, Release: release, Architecture: architecture,
		Path: p, ArchiveSize: size,
	}
	for _, field := range []struct {
		tag uint32
		out *string
	}{{1004, &pkg.Summary}, {1005, &pkg.Description}, {1014, &pkg.License}, {1020, &pkg.URL}} {
		if _, exists := main.entries[field.tag]; exists {
			*field.out, err = main.stringTag(field.tag)
			if err != nil {
				return Package{}, err
			}
		}
	}
	if pkg.Epoch, err = main.uint32Tag(1003); err != nil {
		return Package{}, err
	}
	if pkg.BuildTime, err = main.uint32Tag(1006); err != nil {
		return Package{}, err
	}
	if installed, installedErr := main.uint32Tag(1009); installedErr != nil {
		return Package{}, installedErr
	} else {
		pkg.InstalledSize = uint64(installed)
	}
	if err := verifyPayload(ctx, body, payloadOffset, size, main, signature); err != nil {
		return Package{}, err
	}
	return pkg, nil
}

func validPath(p string) bool {
	if p == "" || p != path.Clean(p) || strings.HasPrefix(p, "/") ||
		strings.HasPrefix(p, "../") || !strings.HasSuffix(p, ".rpm") {
		return false
	}
	for _, segment := range strings.Split(p, "/") {
		if !namePart.MatchString(segment) || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func readHeader(body io.ReadSeeker, offset, size int64) (rpmHeader, int64, error) {
	if offset < 0 || offset+headerIntroLen > size {
		return rpmHeader{}, 0, fmt.Errorf("%w: truncated header intro", errInvalidPackage)
	}
	if _, err := body.Seek(offset, io.SeekStart); err != nil {
		return rpmHeader{}, 0, fmt.Errorf("rpm: seek header: %w", err)
	}
	var intro [headerIntroLen]byte
	if _, err := io.ReadFull(body, intro[:]); err != nil {
		return rpmHeader{}, 0, fmt.Errorf("rpm: read header intro: %w", err)
	}
	if !bytes.Equal(intro[:8], headerMagic) {
		return rpmHeader{}, 0, fmt.Errorf("%w: header magic", errInvalidPackage)
	}
	count := int64(binary.BigEndian.Uint32(intro[8:12]))
	dataLen := int64(binary.BigEndian.Uint32(intro[12:16]))
	if count < 1 || count > headerEntries || dataLen < 1 ||
		count*indexEntryLen+dataLen > headerLimit ||
		offset+headerIntroLen+count*indexEntryLen+dataLen > size {
		return rpmHeader{}, 0, fmt.Errorf("%w: header bounds", errInvalidPackage)
	}
	raw := make([]byte, count*indexEntryLen+dataLen)
	if _, err := io.ReadFull(body, raw); err != nil {
		return rpmHeader{}, 0, fmt.Errorf("rpm: read header: %w", err)
	}
	data := raw[count*indexEntryLen:]
	entries := make(map[uint32]headerEntry, count)
	for i := int64(0); i < count; i++ {
		entry := raw[i*indexEntryLen : (i+1)*indexEntryLen]
		tag := binary.BigEndian.Uint32(entry[:4])
		e := headerEntry{
			typ:    binary.BigEndian.Uint32(entry[4:8]),
			offset: binary.BigEndian.Uint32(entry[8:12]),
			count:  binary.BigEndian.Uint32(entry[12:16]),
		}
		if _, duplicate := entries[tag]; duplicate || int64(e.offset) >= int64(len(data)) ||
			e.count == 0 {
			return rpmHeader{}, 0, fmt.Errorf("%w: header entry", errInvalidPackage)
		}
		entries[tag] = e
	}
	return rpmHeader{entries: entries, data: data},
		offset + headerIntroLen + count*indexEntryLen + dataLen, nil
}

func (h rpmHeader) stringTag(tag uint32) (string, error) {
	e, exists := h.entries[tag]
	if !exists || e.typ != 6 && e.typ != 9 || e.count != 1 {
		return "", fmt.Errorf("%w: missing or invalid string tag %d", errInvalidPackage, tag)
	}
	raw := h.data[e.offset:]
	end := bytes.IndexByte(raw, 0)
	if end < 1 || end > 1<<20 {
		return "", fmt.Errorf("%w: string tag %d bounds", errInvalidPackage, tag)
	}
	return string(raw[:end]), nil
}

func (h rpmHeader) uint32Tag(tag uint32) (uint32, error) {
	e, exists := h.entries[tag]
	if !exists {
		return 0, nil
	}
	if e.typ != 4 || e.count != 1 || int(e.offset)+4 > len(h.data) {
		return 0, fmt.Errorf("%w: integer tag %d", errInvalidPackage, tag)
	}
	return binary.BigEndian.Uint32(h.data[e.offset : e.offset+4]), nil
}

func verifyPayload(
	ctx context.Context, body io.ReadSeeker, offset, size int64,
	main, _ rpmHeader,
) error {
	entry, exists := main.entries[5092] // RPMTAG_PAYLOADSHA256
	if !exists {
		return nil
	}
	if entry.typ != 8 || entry.count != 1 {
		return fmt.Errorf("%w: payload SHA-256 tag", errInvalidPackage)
	}
	want := main.data[entry.offset:]
	end := bytes.IndexByte(want, 0)
	if end != sha256.Size*2 {
		return fmt.Errorf("%w: payload SHA-256 length", errInvalidPackage)
	}
	wantDigest, err := hex.DecodeString(string(want[:end]))
	if err != nil {
		return fmt.Errorf("%w: payload SHA-256 encoding: %w", errInvalidPackage, err)
	}
	if _, err := body.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("rpm: seek payload: %w", err)
	}
	h := sha256.New()
	if _, err := io.Copy(
		h,
		&contextReader{ctx: ctx, reader: io.LimitReader(body, size-offset)},
	); err != nil {
		return fmt.Errorf("rpm: hash payload: %w", err)
	}
	if !bytes.Equal(h.Sum(nil), wantDigest) {
		return fmt.Errorf("%w: payload checksum mismatch", errInvalidPackage)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("rpm: canceled payload read: %w", err)
	}
	return r.reader.Read(p) //nolint:wrapcheck // reader is the caller's blob stream
}
