package rpm

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const maxMetadataBytes = 36 << 20

// DecodeRepomd validates the published primary reference. The referenced
// primary is fetched and checked separately, before any virtual composition.
func DecodeRepomd(body []byte, root string) (string, string, error) {
	if len(body) == 0 || len(body) > maxMetadataBytes {
		return "", "", fmt.Errorf("%w: repomd size", errInvalidPackage)
	}
	var doc repoDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return "", "", fmt.Errorf("rpm: repomd xml: %w", err)
	}
	if doc.Common != "http://linux.duke.edu/metadata/repo" {
		return "", "", fmt.Errorf("%w: repomd namespace", errInvalidPackage)
	}
	for _, data := range doc.Data {
		if data.Type != "primary" {
			continue
		}
		p := data.Location.Href
		if data.Checksum.Type != "sha256" || len(data.Checksum.Value) != 64 ||
			!hexDigest(data.Checksum.Value) || !strings.HasPrefix(p, "repodata/") ||
			path.Clean(p) != p || !IsPrimaryMetadataPath(p) ||
			!strings.HasPrefix(path.Base(p), data.Checksum.Value+"-") {
			return "", "", fmt.Errorf("%w: unsafe primary reference", errInvalidPackage)
		}
		return root + p, data.Checksum.Value, nil
	}
	return "", "", fmt.Errorf("%w: missing primary reference", errInvalidPackage)
}

// IsPrimaryMetadataPath recognizes the checksum-named primary indexes
// published by createrepo_c and older Yum tooling.
func IsPrimaryMetadataPath(p string) bool {
	return strings.HasSuffix(p, "-primary.xml.gz") ||
		strings.HasSuffix(p, "-primary.xml.zst")
}

// DecodePrimary validates a checksum-addressed, bounded primary index and
// returns packages with repository-relative paths for native selection.
func DecodePrimary(compressed []byte, root, wantSHA string) ([]Package, error) {
	if len(compressed) == 0 || len(compressed) > maxMetadataBytes ||
		fmt.Sprintf("%x", sha256.Sum256(compressed)) != wantSHA {
		return nil, fmt.Errorf("%w: primary checksum or size", errInvalidPackage)
	}
	var reader io.Reader
	var closeReader func() error
	switch {
	case bytes.HasPrefix(compressed, []byte{0x1f, 0x8b}):
		gz, err := gzip.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return nil, fmt.Errorf("rpm: primary gzip: %w", err)
		}
		reader, closeReader = gz, gz.Close
	case bytes.HasPrefix(compressed, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		z, err := zstd.NewReader(
			bytes.NewReader(compressed),
			zstd.WithDecoderMaxMemory(maxMetadataBytes),
		)
		if err != nil {
			return nil, fmt.Errorf("rpm: primary zstd: %w", err)
		}
		reader, closeReader = z, func() error { z.Close(); return nil }
	default:
		return nil, fmt.Errorf("%w: unsupported primary compression", errInvalidPackage)
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxMetadataBytes+1))
	if err != nil {
		_ = closeReader()
		return nil, fmt.Errorf("rpm: primary decompress: %w", err)
	}
	if err := closeReader(); err != nil {
		return nil, fmt.Errorf("rpm: primary close: %w", err)
	}
	if len(raw) > maxMetadataBytes {
		return nil, fmt.Errorf("%w: primary decompressed size", errInvalidPackage)
	}
	var doc primaryDoc
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("rpm: primary xml: %w", err)
	}
	if doc.Common != "http://linux.duke.edu/metadata/common" ||
		doc.Packages != len(doc.Items) || len(doc.Items) > maxPackages {
		return nil, fmt.Errorf("%w: primary namespace or count", errInvalidPackage)
	}
	out := make([]Package, 0, len(doc.Items))
	seen := map[string]bool{}
	for _, entry := range doc.Items {
		p := root + entry.Location.Href
		if entry.Type != "rpm" || entry.Checksum.Type != "sha256" ||
			!hexDigest(entry.Checksum.Value) || !validPath(p) ||
			path.Base(
				p,
			) != entry.Name+"-"+entry.Version.Ver+"-"+entry.Version.Rel+"."+entry.Arch+".rpm" ||
			seen[p] {
			return nil, fmt.Errorf("%w: primary package identity", errInvalidPackage)
		}
		seen[p] = true
		out = append(out, Package{
			Name: entry.Name, Version: entry.Version.Ver, Release: entry.Version.Rel,
			Architecture: entry.Arch, Path: p, SHA256: entry.Checksum.Value,
			Summary: entry.Summary, Description: entry.Description, URL: entry.URL,
			License: entry.Format.License, ArchiveSize: entry.Size.Package,
			Provides:  decodeDependencies(entry.Format.Provides.Entries),
			Requires:  decodeDependencies(entry.Format.Requires.Entries),
			Conflicts: decodeDependencies(entry.Format.Conflicts.Entries),
			Obsoletes: decodeDependencies(entry.Format.Obsoletes.Entries),
		})
		current := &out[len(out)-1]
		if epoch, err := strconv.ParseUint(entry.Version.Epoch, 10, 32); err == nil {
			current.Epoch = uint32(epoch)
		}
		if entry.Size.Installed <= 1<<63-1 {
			current.InstalledSize = entry.Size.Installed
		}
	}
	return out, nil
}

func decodeDependencies(entries []provideXML) []Dependency {
	out := make([]Dependency, 0, len(entries))
	for _, entry := range entries {
		dep := Dependency{Name: entry.Name}
		if entry.Ver != "" {
			dep.Version = entry.Ver
			if entry.Rel != "" {
				dep.Version += "-" + entry.Rel
			}
			if entry.Epoch != "" && entry.Epoch != "0" {
				dep.Version = entry.Epoch + ":" + dep.Version
			}
			switch entry.Flags {
			case "LT":
				dep.Flags = 2
			case "GT":
				dep.Flags = 4
			case "EQ":
				dep.Flags = 8
			case "LE":
				dep.Flags = 10
			case "GE":
				dep.Flags = 12
			}
		}
		out = append(out, dep)
	}
	return out
}

func hexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == strings.ToLower(s)
}
