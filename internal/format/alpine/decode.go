package alpine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

var errInvalidIndex = errors.New("alpine: invalid APK index")

// ParseIndexPath recognizes the branch/component/architecture APKINDEX layout.
func ParseIndexPath(p string) (branch, component, arch string, ok bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 4 || parts[3] != IndexName {
		return "", "", "", false
	}
	for _, part := range parts[:3] {
		if !pathPart.MatchString(part) || part == "." || part == ".." {
			return "", "", "", false
		}
	}
	return parts[0], parts[1], parts[2], true
}

// DecodeIndex validates an apk v2 index and returns the exact package records.
// Source records remain byte-for-byte intact when a virtual index is rebuilt.
func DecodeIndex(ctx context.Context, body []byte, indexPath string) ([]Package, error) {
	branch, component, arch, ok := ParseIndexPath(indexPath)
	if !ok || len(body) == 0 || len(body) > maxIndexBytes+maxSegmentBytes {
		return nil, fmt.Errorf("%w: invalid index path or size", errInvalidIndex)
	}
	r := bytes.NewReader(body)
	raw, err := indexSegment(ctx, r)
	if err != nil {
		return nil, err
	}
	first, err := indexTar(raw)
	if err != nil {
		return nil, err
	}
	if len(first) == 1 {
		for name := range first {
			if !strings.HasPrefix(name, ".SIGN.RSA.") &&
				!strings.HasPrefix(name, ".SIGN.RSA256.") {
				return nil, fmt.Errorf("%w: invalid index signature entry", errInvalidIndex)
			}
		}
		raw, err = indexSegment(ctx, r)
		if err != nil {
			return nil, err
		}
		first, err = indexTar(raw)
		if err != nil {
			return nil, err
		}
	}
	if r.Len() != 0 || len(first) != 2 || first["DESCRIPTION"] == nil ||
		first["APKINDEX"] == nil {
		return nil, fmt.Errorf("%w: incomplete index", errInvalidIndex)
	}
	return decodeRecords(first["APKINDEX"], branch, component, arch)
}

func indexSegment(ctx context.Context, r *bytes.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("alpine: index parse canceled: %w", err)
	}
	z, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("alpine: index gzip: %w", err)
	}
	defer func() { _ = z.Close() }()
	z.Multistream(false)
	data, err := io.ReadAll(io.LimitReader(z, maxIndexBytes+1))
	if err != nil || len(data) > maxIndexBytes {
		return nil, fmt.Errorf("%w: index segment exceeds limit", errInvalidIndex)
	}
	return data, nil
}

func indexTar(raw []byte) (map[string][]byte, error) {
	if len(raw)%512 != 0 {
		return nil, fmt.Errorf("%w: truncated index tar", errInvalidIndex)
	}
	tr := tar.NewReader(bytes.NewReader(raw))
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || h == nil || h.Typeflag != tar.TypeReg || h.Size < 0 ||
			h.Size > maxIndexBytes || len(files) >= 2 {
			return nil, fmt.Errorf("%w: invalid index tar entry", errInvalidIndex)
		}
		if _, exists := files[h.Name]; exists || h.Name != "DESCRIPTION" &&
			h.Name != "APKINDEX" && !strings.HasPrefix(h.Name, ".SIGN.") {
			return nil, fmt.Errorf("%w: invalid index tar name", errInvalidIndex)
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxIndexBytes+1))
		if err != nil || int64(len(data)) != h.Size {
			return nil, fmt.Errorf("%w: truncated index tar entry", errInvalidIndex)
		}
		files[h.Name] = data
	}
	return files, nil
}

func decodeRecords(data []byte, branch, component, arch string) ([]Package, error) {
	if len(data) > maxIndexBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("%w: malformed index text", errInvalidIndex)
	}
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n\n")) {
		return nil, fmt.Errorf("%w: unterminated index record", errInvalidIndex)
	}
	text := strings.TrimSuffix(string(data), "\n\n")
	if text == "" {
		return nil, nil
	}
	var packages []Package
	seen := map[string]bool{}
	for _, record := range strings.Split(text, "\n\n") {
		if len(packages) >= maxIndexEntries || record == "" {
			return nil, fmt.Errorf("%w: malformed index record", errInvalidIndex)
		}
		fields := map[byte]string{}
		for _, line := range strings.Split(record, "\n") {
			if len(line) < 3 || line[1] != ':' || line[2] == '\r' ||
				strings.ContainsAny(line, "\r\x00") {
				return nil, fmt.Errorf("%w: malformed index field", errInvalidIndex)
			}
			if _, duplicate := fields[line[0]]; duplicate {
				return nil, fmt.Errorf("%w: duplicate index field", errInvalidIndex)
			}
			fields[line[0]] = line[2:]
		}
		checksum := strings.TrimPrefix(fields['C'], "Q1")
		digest, err := base64.StdEncoding.DecodeString(checksum)
		if err != nil || !strings.HasPrefix(fields['C'], "Q1") || len(digest) != 20 ||
			!pathPart.MatchString(fields['P']) || !pathPart.MatchString(fields['V']) ||
			fields['A'] != arch {
			return nil, fmt.Errorf("%w: invalid package identity in index", errInvalidIndex)
		}
		archiveSize, err := strconv.ParseInt(fields['S'], 10, 64)
		if err != nil || archiveSize <= 0 {
			return nil, fmt.Errorf("%w: invalid package archive size", errInvalidIndex)
		}
		installed, err := strconv.ParseInt(fields['I'], 10, 64)
		if err != nil || installed < 0 {
			return nil, fmt.Errorf("%w: invalid installed size", errInvalidIndex)
		}
		p := Package{
			Name: fields['P'], Version: fields['V'], Architecture: arch,
			Branch: branch, Component: component,
			ArchiveSize: archiveSize, InstalledSize: installed, ControlSHA1: checksum,
			IndexRaw: record + "\n\n",
		}
		p.Path = branch + "/" + component + "/" + arch + "/" + p.Name + "-" + p.Version + ".apk"
		if seen[p.Path] {
			return nil, fmt.Errorf("%w: duplicate package in index", errInvalidIndex)
		}
		seen[p.Path] = true
		packages = append(packages, p)
	}
	return packages, nil
}
