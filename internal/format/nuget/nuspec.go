package nuget

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/weaveplatform/artifactweave/internal/archive"
	"github.com/weaveplatform/artifactweave/internal/format"
)

// ErrNotPackage is content the format cannot index: no zip, no single
// readable .nuspec at its root, an id NuGet would not accept, a version
// NuGet cannot parse, or more than limits allow. Decision 0038 stores no
// such .nupkg or .snupkg in a local repository.
var ErrNotPackage = errors.New("nuget: not a valid NuGet package")

// limits bound reading a package (decision 0038, as npm's are bounded): a
// zip of more than 2^20 entries, or a .nuspec over 1 MiB, is no valid
// package. A zip walk reads only the entry it opens, so Inflated bounds
// nothing here.
var limits = archive.Limits{Entries: 1 << 20, Inflated: 16 << 30, File: 1 << 20}

// MaxIDLength is the longest id NuGet accepts, in UTF-16 code units
// (PackageIdValidator.MaxPackageIdLength).
const MaxIDLength = 100

// idPattern is NuGet's ^\w+([.-]\w+)*$, with .NET's \w, which takes
// letters, combining marks, digits and connector punctuation of every
// script, not ASCII alone as Go's \w does (PackageIdValidator).
var idPattern = regexp.MustCompile(
	`^[\p{L}\p{Mn}\p{Nd}\p{Pc}]+(?:[.-][\p{L}\p{Mn}\p{Nd}\p{Pc}]+)*$`,
)

// ValidID reports whether NuGet accepts id for a package.
func ValidID(id string) bool {
	return idPattern.MatchString(id) && len(utf16.Encode([]rune(id))) <= MaxIDLength
}

// Nuspec is what the format reads from a package's .nuspec: the elements
// decision 0038 indexes, each with surrounding space trimmed, as NuGet's
// reader trims them. An element the nuspec lacks is empty.
type Nuspec struct {
	ID      string
	Version Version
	Title   string
	// Authors and Owners are the nuspec's comma-separated strings.
	Authors, Owners string
	Description     string
	Summary         string
	ReleaseNotes    string
	Copyright       string
	Language        string
	// Tags is the nuspec's space-separated string.
	Tags       string
	ProjectURL string
	IconURL    string
	LicenseURL string
	// LicenseExpression is a <license type="expression">'s SPDX expression.
	LicenseExpression string
	// RequireLicenseAcceptance is nil when the nuspec does not say.
	RequireLicenseAcceptance *bool
	// MinClientVersion is <metadata>'s minClientVersion attribute.
	MinClientVersion string
	// Readme is the path in the package of its readme.
	Readme           string
	DependencyGroups []DependencyGroup
	PackageTypes     []PackageType
	// Raw is the .nuspec as the package holds it.
	Raw []byte
}

// DependencyGroup is a nuspec's <group>, or its dependencies outside any
// group, which have no target framework.
type DependencyGroup struct {
	// TargetFramework is as the nuspec writes it.
	TargetFramework string       `json:"target_framework,omitempty"`
	Dependencies    []Dependency `json:"dependencies,omitempty"`
}

// Dependency is one <dependency>.
type Dependency struct {
	ID string `json:"id"`
	// Range is the version attribute as the nuspec writes it.
	Range string `json:"range,omitempty"`
}

// PackageType is one <packageType>.
type PackageType struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// SymbolsPackageType is the package type of a symbols package
// (NuGet.Packaging's PackageType.SymbolsPackage).
const SymbolsPackageType = "SymbolsPackage"

// IsSymbols reports a nuspec that declares the SymbolsPackage type, as a
// .snupkg's does. The push route refuses one: it belongs at the symbols
// route (decision 0038, "Push").
func (n *Nuspec) IsSymbols() bool {
	for _, t := range n.PackageTypes {
		if strings.EqualFold(t.Name, SymbolsPackageType) {
			return true
		}
	}
	return false
}

// IsSemVer2 reports a package only SemVer 2.0.0 clients read: its version
// is one, or a dependency range has a bound that is (Microsoft's
// "Semantic Versioning 2.0.0").
func (n *Nuspec) IsSemVer2() bool {
	if n.Version.IsSemVer2() {
		return true
	}
	for _, g := range n.DependencyGroups {
		for _, d := range g.Dependencies {
			if rangeIsSemVer2(d.Range) {
				return true
			}
		}
	}
	return false
}

// ReadPackage reads the package in ra, of size bytes: the one .nuspec at
// the root of its zip, its extension in any case, as NuGet's
// PackageArchiveReader finds it. ErrNotPackage when the format cannot index
// it; any other error is a failure to read the content.
func ReadPackage(ra io.ReaderAt, size int64) (*Nuspec, error) {
	src := &firstErr{ra: ra}
	// Sniffed by content alone: the name would make anything called .nupkg
	// a zip, and the refusal would then blame a broken zip for a text file.
	if archive.KindOf(src, "") != archive.KindZip {
		return nil, fmt.Errorf("%w: it is no zip", ErrNotPackage)
	}
	var (
		raw   []byte
		found int
	)
	err := archive.Scan(src, size, archive.KindZip, limits, func(e archive.Entry) error {
		n, err := archive.Clean(e.Name)
		if err != nil || !e.Regular || strings.Contains(n, "/") ||
			!strings.HasSuffix(strings.ToLower(n), ".nuspec") {
			return nil //nolint:nilerr // an unusable entry is no nuspec
		}
		if found++; found > 1 {
			return fmt.Errorf("%w: more than one .nuspec at its root", ErrNotPackage)
		}
		raw, err = archive.ReadEntry(e, limits)
		return err //nolint:wrapcheck // archive names its own
	})
	switch {
	case src.err != nil:
		return nil, fmt.Errorf("nuget: read package: %w", src.err)
	case errors.Is(err, ErrNotPackage):
		return nil, err //nolint:wrapcheck // the walk's callback's own, already ErrNotPackage
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrNotPackage, err)
	case raw == nil:
		return nil, fmt.Errorf("%w: no .nuspec at its root", ErrNotPackage)
	}
	return ParseNuspec(raw)
}

// firstErr remembers the first read error that is not the end of the
// content. Without it, a blob store failing mid-read would look like
// content that is no package, and a valid push would be refused as one.
type firstErr struct {
	ra  io.ReaderAt
	err error
}

func (r *firstErr) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.ra.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) && r.err == nil {
		r.err = err
	}
	return n, err //nolint:wrapcheck // io.ReaderAt contract
}

// ReadStored reads the stored content sha as a package; an error wrapping
// ErrNotPackage when it is none the format can index.
func ReadStored(ctx context.Context, blobs format.Blobs, sha string) (*Nuspec, error) {
	rc, info, err := blobs.Open(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("nuget: open %s: %w", sha, err)
	}
	defer func() { _ = rc.Close() }()
	return ReadPackage(archive.ReaderAt(rc), info.Size)
}

// The nuspec's elements. Tags without a namespace match an element of that
// local name in any, so every nuspec schema version reads alike, as NuGet
// reads the elements in whatever namespace <metadata> has. Names match
// with their case, as NuGet's reader matches them.
type (
	xmlPackage struct {
		XMLName  xml.Name     `xml:"package"`
		Metadata *xmlMetadata `xml:"metadata"`
	}
	xmlMetadata struct {
		MinClientVersion         string  `xml:"minClientVersion,attr"`
		ID                       string  `xml:"id"`
		Version                  string  `xml:"version"`
		Title                    string  `xml:"title"`
		Authors                  string  `xml:"authors"`
		Owners                   string  `xml:"owners"`
		Description              string  `xml:"description"`
		Summary                  string  `xml:"summary"`
		ReleaseNotes             string  `xml:"releaseNotes"`
		Copyright                string  `xml:"copyright"`
		Language                 string  `xml:"language"`
		Tags                     string  `xml:"tags"`
		ProjectURL               string  `xml:"projectUrl"`
		IconURL                  string  `xml:"iconUrl"`
		LicenseURL               string  `xml:"licenseUrl"`
		Readme                   string  `xml:"readme"`
		RequireLicenseAcceptance *string `xml:"requireLicenseAcceptance"`
		License                  struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"license"`
		PackageTypes []struct {
			Name    string `xml:"name,attr"`
			Version string `xml:"version,attr"`
		} `xml:"packageTypes>packageType"`
		Dependencies struct {
			Groups []struct {
				TargetFramework string          `xml:"targetFramework,attr"`
				Dependencies    []xmlDependency `xml:"dependency"`
			} `xml:"group"`
			Flat []xmlDependency `xml:"dependency"`
		} `xml:"dependencies"`
	}
	xmlDependency struct {
		ID      string `xml:"id,attr"`
		Version string `xml:"version,attr"`
	}
)

// utf8BOM starts some .nuspec files, which the XML reader would otherwise
// take for text before the root element.
var utf8BOM = []byte("\xef\xbb\xbf")

// ParseNuspec reads a .nuspec. ErrNotPackage when it is no XML <package>
// with a <metadata>, or names no id NuGet accepts or no version it can
// parse.
func ParseNuspec(raw []byte) (*Nuspec, error) {
	var doc xmlPackage
	d := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(raw, utf8BOM)))
	// NuGet writes UTF-8. A declaration naming another ASCII-compatible
	// encoding is read as UTF-8 rather than refused, so a package that says
	// "us-ascii" or "UTF-8" in its own case still reads.
	d.CharsetReader = func(charset string, in io.Reader) (io.Reader, error) {
		switch strings.ToLower(charset) {
		case "utf-8", "utf8", "us-ascii", "ascii":
			return in, nil
		}
		return nil, fmt.Errorf("%w: the .nuspec is encoded in %s", ErrNotPackage, charset)
	}
	if err := d.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: the .nuspec is no XML <package>: %w", ErrNotPackage, err)
	}
	m := doc.Metadata
	if m == nil {
		return nil, fmt.Errorf("%w: the .nuspec has no <metadata>", ErrNotPackage)
	}
	n := &Nuspec{ID: strings.TrimSpace(m.ID), Raw: raw}
	if !ValidID(n.ID) {
		return nil, fmt.Errorf(
			"%w: the .nuspec's id %q is not a valid NuGet id",
			ErrNotPackage,
			n.ID,
		)
	}
	v, ok := ParseVersion(m.Version)
	if !ok {
		return nil, fmt.Errorf("%w: the .nuspec's version %q is not a valid NuGet version",
			ErrNotPackage, strings.TrimSpace(m.Version))
	}
	n.Version = v
	trim := strings.TrimSpace
	n.Title, n.Authors, n.Owners = trim(m.Title), trim(m.Authors), trim(m.Owners)
	n.Description, n.Summary = trim(m.Description), trim(m.Summary)
	n.ReleaseNotes, n.Copyright, n.Language = trim(
		m.ReleaseNotes,
	), trim(
		m.Copyright,
	), trim(
		m.Language,
	)
	n.Tags, n.ProjectURL, n.IconURL = trim(m.Tags), trim(m.ProjectURL), trim(m.IconURL)
	n.LicenseURL, n.Readme = trim(m.LicenseURL), trim(m.Readme)
	n.MinClientVersion = trim(m.MinClientVersion)
	if strings.EqualFold(trim(m.License.Type), "expression") {
		n.LicenseExpression = trim(m.License.Value)
	}
	if m.RequireLicenseAcceptance != nil {
		// NuGet reads anything but "true", in any case, as false.
		b := strings.EqualFold(trim(*m.RequireLicenseAcceptance), "true")
		n.RequireLicenseAcceptance = &b
	}
	for _, t := range m.PackageTypes {
		if name := trim(t.Name); name != "" {
			n.PackageTypes = append(
				n.PackageTypes,
				PackageType{Name: name, Version: trim(t.Version)},
			)
		}
	}
	// NuGet's reader takes the groups when there are any and the
	// dependencies outside them only when there are none.
	for _, g := range m.Dependencies.Groups {
		n.DependencyGroups = append(n.DependencyGroups, DependencyGroup{
			TargetFramework: trim(g.TargetFramework), Dependencies: dependencies(g.Dependencies),
		})
	}
	if len(m.Dependencies.Groups) == 0 && len(m.Dependencies.Flat) > 0 {
		n.DependencyGroups = []DependencyGroup{{Dependencies: dependencies(m.Dependencies.Flat)}}
	}
	return n, nil
}

// dependencies are ds with their ids and ranges trimmed, less any without
// an id, which names nothing to depend on.
func dependencies(ds []xmlDependency) []Dependency {
	var out []Dependency
	for _, d := range ds {
		if id := strings.TrimSpace(d.ID); id != "" {
			out = append(out, Dependency{ID: id, Range: strings.TrimSpace(d.Version)})
		}
	}
	return out
}
