package opkg

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	PackagesName     = "Packages"
	PackagesGzipName = "Packages.gz"
	ReleaseName      = "Release"
	SignatureName    = "Packages.sig"
	maxIndexBytes    = 36 << 20
	maxIndexEntries  = 100000
)

var errInvalidIndex = errors.New("opkg: invalid Packages index")

func IsIndex(p string) bool {
	switch path.Base(p) {
	case PackagesName, PackagesGzipName, ReleaseName, SignatureName:
		return true
	}
	return false
}

func FeedPath(p string) (string, bool) {
	if !IsIndex(p) {
		return "", false
	}
	feed := Feed(p)
	if feed == "" {
		return "", true
	}
	for _, part := range strings.Split(feed, "/") {
		if !pathPart.MatchString(part) || part == "." || part == ".." {
			return "", false
		}
	}
	return feed, true
}

// DecodeIndex rejects metadata that could make a client fetch a package
// outside its feed, confuse a virtual member selection, or skip integrity
// checks. gzip is selected from the requested resource, not content sniffing.
func DecodeIndex(body []byte, indexPath string) ([]Package, error) {
	feed, ok := FeedPath(indexPath)
	if !ok || len(body) == 0 || len(body) > maxIndexBytes {
		return nil, fmt.Errorf("%w: path or size", errInvalidIndex)
	}
	if path.Base(indexPath) == PackagesGzipName {
		z, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("%w: gzip", errInvalidIndex)
		}
		defer func() { _ = z.Close() }()
		body, err = io.ReadAll(io.LimitReader(z, maxIndexBytes+1))
		if err != nil || len(body) > maxIndexBytes {
			return nil, fmt.Errorf("%w: decompressed size", errInvalidIndex)
		}
	}
	if !utf8.Valid(body) || bytes.IndexByte(body, 0) >= 0 ||
		!bytes.HasSuffix(body, []byte("\n\n")) {
		return nil, fmt.Errorf("%w: index text", errInvalidIndex)
	}
	text := strings.TrimSuffix(string(body), "\n\n")
	if text == "" {
		return nil, nil
	}
	records := strings.Split(text, "\n\n")
	if len(records) > maxIndexEntries {
		return nil, fmt.Errorf("%w: too many packages", errInvalidIndex)
	}
	packages := make([]Package, 0, len(records))
	seen := map[string]bool{}
	for _, record := range records {
		fields, err := parseFields([]byte(record + "\n"))
		if err != nil {
			return nil, fmt.Errorf("%w: stanza: %w", errInvalidIndex, err)
		}
		name, version, arch := fields["Package"], fields["Version"], fields["Architecture"]
		filename := fields["Filename"]
		if !pathPart.MatchString(name) || !pathPart.MatchString(version) ||
			!pathPart.MatchString(arch) || !ValidPath(filename) ||
			strings.Contains(filename, "/") {
			return nil, fmt.Errorf("%w: package identity or filename", errInvalidIndex)
		}
		checksum := fields["SHA256sum"]
		if checksum == "" {
			checksum = fields["SHA256Sum"]
		}
		if len(checksum) != sha256.Size*2 || strings.ToLower(checksum) != checksum {
			return nil, fmt.Errorf("%w: missing SHA256sum", errInvalidIndex)
		}
		if _, err := hex.DecodeString(checksum); err != nil {
			return nil, fmt.Errorf("%w: SHA256sum", errInvalidIndex)
		}
		size, err := strconv.ParseInt(fields["Size"], 10, 64)
		if err != nil || size <= 0 || size > maxPackageSize {
			return nil, fmt.Errorf("%w: package size", errInvalidIndex)
		}
		p := IndexPath(feed, filename)
		if seen[p] {
			return nil, fmt.Errorf("%w: duplicate filename", errInvalidIndex)
		}
		seen[p] = true
		packages = append(packages, Package{
			Name: name, Version: version, Architecture: arch,
			Description: fields["Description"], Maintainer: fields["Maintainer"],
			Depends: fields["Depends"], Provides: fields["Provides"],
			Conflicts: fields["Conflicts"], Replaces: fields["Replaces"],
			Section: fields["Section"], Priority: fields["Priority"],
			Source: fields["Source"], License: fields["License"],
			Path: p, SHA256: checksum, MD5: fields["MD5Sum"], Size: size,
		})
	}
	return packages, nil
}

// EncodeIndex creates a deterministic Packages/Packages.gz pair for one feed.
func EncodeIndex(packages []Package, feed string) (map[string][]byte, error) {
	if len(packages) > maxIndexEntries {
		return nil, fmt.Errorf("%w: too many packages", errInvalidIndex)
	}
	packages = slices.Clone(packages)
	slices.SortFunc(packages, func(a, b Package) int {
		return strings.Compare(a.Path, b.Path)
	})
	var raw bytes.Buffer
	seen := map[string]bool{}
	for _, p := range packages {
		if !ValidPath(p.Path) || Feed(p.Path) != feed || seen[p.Path] ||
			!pathPart.MatchString(p.Name) || !pathPart.MatchString(p.Version) ||
			!pathPart.MatchString(p.Architecture) ||
			len(p.SHA256) != sha256.Size*2 || p.Size <= 0 || p.Size > maxPackageSize {
			return nil, fmt.Errorf("%w: package record", errInvalidIndex)
		}
		if _, err := hex.DecodeString(p.SHA256); err != nil ||
			strings.ToLower(p.SHA256) != p.SHA256 {
			return nil, fmt.Errorf("%w: package digest", errInvalidIndex)
		}
		seen[p.Path] = true
		fmt.Fprintf(
			&raw,
			"Package: %s\nVersion: %s\nArchitecture: %s\n",
			p.Name,
			p.Version,
			p.Architecture,
		)
		fmt.Fprintf(
			&raw,
			"Filename: %s\nSize: %d\nSHA256sum: %s\n",
			path.Base(p.Path),
			p.Size,
			p.SHA256,
		)
		if p.MD5 != "" {
			fmt.Fprintf(&raw, "MD5Sum: %s\n", p.MD5)
		}
		for _, field := range []struct{ name, value string }{
			{"Depends", p.Depends},
			{"Provides", p.Provides},
			{"Conflicts", p.Conflicts},
			{"Replaces", p.Replaces},
			{"Section", p.Section},
			{"Priority", p.Priority},
			{"Maintainer", p.Maintainer},
			{"Source", p.Source},
			{"License", p.License},
			{"Description", p.Description},
		} {
			if field.value != "" {
				if strings.ContainsAny(field.value, "\r\x00") ||
					strings.Contains(field.value, "\n") && field.name != "Description" {
					return nil, fmt.Errorf("%w: metadata control character", errInvalidIndex)
				}
				fmt.Fprintf(&raw, "%s: %s\n", field.name, field.value)
			}
		}
		raw.WriteByte('\n')
	}
	if raw.Len() == 0 {
		raw.WriteString("\n\n")
	}
	if raw.Len() > maxIndexBytes {
		return nil, fmt.Errorf("%w: encoded size", errInvalidIndex)
	}
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(raw.Bytes()); err != nil {
		return nil, fmt.Errorf("opkg: compress index: %w", err)
	}
	if err := z.Close(); err != nil {
		return nil, fmt.Errorf("opkg: close index: %w", err)
	}
	release := fmt.Sprintf("SHA256: %s %d Packages\nSHA256: %s %d Packages.gz\n",
		Digest(raw.Bytes()), raw.Len(), Digest(compressed.Bytes()), compressed.Len())
	return map[string][]byte{
		PackagesName: raw.Bytes(), PackagesGzipName: compressed.Bytes(),
		ReleaseName: []byte(release),
	}, nil
}

func ValidateRelease(body []byte) error {
	if len(body) == 0 || len(body) > 1<<20 || !utf8.Valid(body) ||
		bytes.IndexByte(body, 0) >= 0 || !bytes.HasSuffix(body, []byte("\n")) {
		return fmt.Errorf("%w: Release text", errInvalidIndex)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 4 || parts[0] != "SHA256:" ||
			(parts[3] != PackagesName && parts[3] != PackagesGzipName) ||
			seen[parts[3]] || len(parts[1]) != 64 {
			return fmt.Errorf("%w: Release record", errInvalidIndex)
		}
		if _, err := hex.DecodeString(parts[1]); err != nil {
			return fmt.Errorf("%w: Release digest", errInvalidIndex)
		}
		size, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || size <= 0 || size > maxIndexBytes {
			return fmt.Errorf("%w: Release size", errInvalidIndex)
		}
		seen[parts[3]] = true
	}
	if !seen[PackagesName] || !seen[PackagesGzipName] {
		return fmt.Errorf("%w: incomplete Release", errInvalidIndex)
	}
	return nil
}
