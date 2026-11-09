package rpm

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	primaryName = "primary.xml.gz"
	maxPackages = 100000
)

// Metadata is a complete depth-root repodata publication. Files are keyed
// relative to that root, so all metadata paths sit inside repodata/.
type Metadata struct {
	Files map[string][]byte
}

type primaryDoc struct {
	XMLName  xml.Name         `xml:"metadata"`
	Common   string           `xml:"xmlns,attr"`
	RPM      string           `xml:"xmlns:rpm,attr"`
	Packages int              `xml:"packages,attr"`
	Items    []primaryPackage `xml:"package"`
}

type primaryPackage struct {
	Type        string        `xml:"type,attr"`
	Name        string        `xml:"name"`
	Arch        string        `xml:"arch"`
	Version     versionXML    `xml:"version"`
	Checksum    checksumXML   `xml:"checksum"`
	Summary     string        `xml:"summary"`
	Description string        `xml:"description"`
	URL         string        `xml:"url"`
	Time        timeXML       `xml:"time"`
	Size        sizeXML       `xml:"size"`
	Location    locationXML   `xml:"location"`
	Format      packageFormat `xml:"format"`
}

type versionXML struct {
	Epoch string `xml:"epoch,attr"`
	Ver   string `xml:"ver,attr"`
	Rel   string `xml:"rel,attr"`
}

type checksumXML struct {
	Type  string `xml:"type,attr"`
	PkgID string `xml:"pkgid,attr,omitempty"`
	Value string `xml:",chardata"`
}

type timeXML struct {
	File  int64  `xml:"file,attr"`
	Build uint32 `xml:"build,attr"`
}

type sizeXML struct {
	Package   int64  `xml:"package,attr"`
	Installed uint64 `xml:"installed,attr"`
	Archive   int64  `xml:"archive,attr"`
}

type locationXML struct {
	Href string `xml:"href,attr"`
}

type packageFormat struct {
	License   string      `xml:"rpm:license"`
	Provides  providesXML `xml:"rpm:provides"`
	Requires  providesXML `xml:"rpm:requires"`
	Conflicts providesXML `xml:"rpm:conflicts"`
	Obsoletes providesXML `xml:"rpm:obsoletes"`
}

// encoding/xml resolves prefixes to namespace URIs when reading. Keep the
// prefixed tags above for native repodata output, but match the RPM namespace
// when consuming an upstream primary index (regardless of its chosen prefix).
func (f *packageFormat) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type dependencies struct {
		Entries []provideXML `xml:"http://linux.duke.edu/metadata/rpm entry"`
	}
	var decoded struct {
		License   string       `xml:"http://linux.duke.edu/metadata/rpm license"`
		Provides  dependencies `xml:"http://linux.duke.edu/metadata/rpm provides"`
		Requires  dependencies `xml:"http://linux.duke.edu/metadata/rpm requires"`
		Conflicts dependencies `xml:"http://linux.duke.edu/metadata/rpm conflicts"`
		Obsoletes dependencies `xml:"http://linux.duke.edu/metadata/rpm obsoletes"`
	}
	if err := d.DecodeElement(&decoded, &start); err != nil {
		return fmt.Errorf("rpm: decode package format: %w", err)
	}
	f.License = decoded.License
	f.Provides.Entries = decoded.Provides.Entries
	f.Requires.Entries = decoded.Requires.Entries
	f.Conflicts.Entries = decoded.Conflicts.Entries
	f.Obsoletes.Entries = decoded.Obsoletes.Entries
	return nil
}

type providesXML struct {
	Entries []provideXML `xml:"rpm:entry"`
}

type provideXML struct {
	Name  string `xml:"name,attr"`
	Flags string `xml:"flags,attr"`
	Epoch string `xml:"epoch,attr"`
	Ver   string `xml:"ver,attr"`
	Rel   string `xml:"rel,attr"`
}

type repoDoc struct {
	XMLName  xml.Name   `xml:"repomd"`
	Common   string     `xml:"xmlns,attr"`
	RPM      string     `xml:"xmlns:rpm,attr"`
	Revision int64      `xml:"revision"`
	Data     []repoData `xml:"data"`
}

type repoData struct {
	Type         string      `xml:"type,attr"`
	Checksum     checksumXML `xml:"checksum"`
	OpenChecksum checksumXML `xml:"open-checksum"`
	Location     locationXML `xml:"location"`
	Timestamp    int64       `xml:"timestamp"`
	Size         int         `xml:"size"`
	OpenSize     int         `xml:"open-size"`
}

// EncodeMetadata constructs a checksum-addressed primary index and repomd
// from validated RPM packages. It uses SHA-256 for both metadata and RPMs.
func EncodeMetadata(packages []Package, root string, now time.Time) (Metadata, error) {
	if len(packages) > maxPackages {
		return Metadata{}, fmt.Errorf("%w: too many packages", errInvalidPackage)
	}
	packages = slices.Clone(packages)
	slices.SortFunc(packages, func(a, b Package) int {
		return strings.Compare(a.Path, b.Path)
	})
	primary := primaryDoc{
		Common: "http://linux.duke.edu/metadata/common",
		RPM:    "http://linux.duke.edu/metadata/rpm", Packages: len(packages),
	}
	for _, pkg := range packages {
		if !validPath(pkg.Path) || len(pkg.SHA256) != sha256.Size*2 ||
			!strings.HasPrefix(pkg.Path, root) {
			return Metadata{}, fmt.Errorf("%w: invalid package metadata", errInvalidPackage)
		}
		rel := strings.TrimPrefix(pkg.Path, root)
		if rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "../") {
			return Metadata{}, fmt.Errorf("%w: package outside metadata root", errInvalidPackage)
		}
		primary.Items = append(primary.Items, primaryPackage{
			Type: "rpm",
			Name: pkg.Name,
			Arch: pkg.Architecture,
			Version: versionXML{
				strconv.FormatUint(uint64(pkg.Epoch), 10),
				pkg.Version,
				pkg.Release,
			},
			Checksum:    checksumXML{Type: "sha256", PkgID: "YES", Value: pkg.SHA256},
			Summary:     pkg.Summary,
			Description: pkg.Description,
			URL:         pkg.URL,
			Time:        timeXML{File: now.Unix(), Build: pkg.BuildTime},
			Size:        sizeXML{Package: pkg.ArchiveSize, Installed: pkg.InstalledSize},
			Location:    locationXML{Href: rel},
			Format: packageFormat{
				License:   pkg.License,
				Provides:  providesXML{Entries: encodeDependencies(pkg.Provides)},
				Requires:  providesXML{Entries: encodeDependencies(pkg.Requires)},
				Conflicts: providesXML{Entries: encodeDependencies(pkg.Conflicts)},
				Obsoletes: providesXML{Entries: encodeDependencies(pkg.Obsoletes)},
			},
		})
	}
	raw, err := xml.MarshalIndent(primary, "", "  ")
	if err != nil {
		return Metadata{}, fmt.Errorf("rpm: marshal primary index: %w", err)
	}
	raw = append([]byte(xml.Header), raw...)
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(raw); err != nil {
		return Metadata{}, fmt.Errorf("rpm: gzip primary index: %w", err)
	}
	if err := gz.Close(); err != nil {
		return Metadata{}, fmt.Errorf("rpm: close primary index: %w", err)
	}
	compressedBytes := compressed.Bytes()
	compressedSHA := fmt.Sprintf("%x", sha256.Sum256(compressedBytes))
	openSHA := fmt.Sprintf("%x", sha256.Sum256(raw))
	primaryPath := "repodata/" + compressedSHA + "-" + primaryName
	repomd := repoDoc{
		Common: "http://linux.duke.edu/metadata/repo",
		RPM:    "http://linux.duke.edu/metadata/rpm", Revision: now.Unix(),
		Data: []repoData{{
			Type:         "primary",
			Checksum:     checksumXML{Type: "sha256", Value: compressedSHA},
			OpenChecksum: checksumXML{Type: "sha256", Value: openSHA},
			Location:     locationXML{Href: primaryPath},
			Timestamp:    now.Unix(), Size: len(compressedBytes), OpenSize: len(raw),
		}},
	}
	manifest, err := xml.MarshalIndent(repomd, "", "  ")
	if err != nil {
		return Metadata{}, fmt.Errorf("rpm: marshal repomd: %w", err)
	}
	return Metadata{Files: map[string][]byte{
		primaryPath:           compressedBytes,
		"repodata/repomd.xml": append([]byte(xml.Header), manifest...),
	}}, nil
}

func encodeDependencies(deps []Dependency) []provideXML {
	out := make([]provideXML, 0, len(deps))
	for _, dep := range deps {
		entry := provideXML{Name: dep.Name}
		if dep.Version != "" {
			switch dep.Flags & 0xe { // RPMSENSE_LESS | GREATER | EQUAL
			case 2:
				entry.Flags = "LT"
			case 4:
				entry.Flags = "GT"
			case 8:
				entry.Flags = "EQ"
			case 10:
				entry.Flags = "LE"
			case 12:
				entry.Flags = "GE"
			}
			version := dep.Version
			if epoch, rest, ok := strings.Cut(version, ":"); ok {
				entry.Epoch = epoch
				version = rest
			} else {
				entry.Epoch = "0"
			}
			entry.Ver, entry.Rel, _ = strings.Cut(version, "-")
		}
		out = append(out, entry)
	}
	return out
}
