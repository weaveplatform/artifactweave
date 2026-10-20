package nuget

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Metadata is a version's document in the package index
// (PackageVersion.Metadata, decision 0038): what the v3 and v2 feeds are
// built from, so a read needs neither the package nor its properties.
// Everything in it follows from the version's files and their stored
// checksums, so a reindex writes the same bytes.
//
// The package fields describe the version's .nupkg. A version whose only
// file is a .snupkg, pushed before its package, has none of them: Path is
// empty, and the feeds show no such version.
type Metadata struct {
	// ID is the nuspec's id with its case.
	ID string `json:"id,omitempty"`
	// Version is the normalised version with its label's case;
	// OriginalVersion is the nuspec's as written.
	Version         string `json:"version,omitempty"`
	OriginalVersion string `json:"original_version,omitempty"`
	Title           string `json:"title,omitempty"`
	// Authors and Owners are the nuspec's comma-separated strings, which
	// the search answers split.
	Authors      string `json:"authors,omitempty"`
	Owners       string `json:"owners,omitempty"`
	Description  string `json:"description,omitempty"`
	Summary      string `json:"summary,omitempty"`
	ReleaseNotes string `json:"release_notes,omitempty"`
	Copyright    string `json:"copyright,omitempty"`
	Language     string `json:"language,omitempty"`
	// Tags is the nuspec's space-separated string.
	Tags                     string            `json:"tags,omitempty"`
	ProjectURL               string            `json:"project_url,omitempty"`
	IconURL                  string            `json:"icon_url,omitempty"`
	LicenseURL               string            `json:"license_url,omitempty"`
	LicenseExpression        string            `json:"license_expression,omitempty"`
	RequireLicenseAcceptance bool              `json:"require_license_acceptance,omitempty"`
	MinClientVersion         string            `json:"min_client_version,omitempty"`
	Readme                   string            `json:"readme,omitempty"`
	DependencyGroups         []DependencyGroup `json:"dependency_groups,omitempty"`
	PackageTypes             []PackageType     `json:"package_types,omitempty"`
	// Nuspec is the .nuspec as the package holds it, which the flat
	// container serves.
	Nuspec string `json:"nuspec,omitempty"`
	// Path is the .nupkg's path in the repository, which every download
	// route serves (packageContent and the v2 Download).
	Path string `json:"path,omitempty"`
	// SHA512 is the .nupkg's SHA-512 in base64, the v2 feed's PackageHash.
	SHA512 string `json:"sha512,omitempty"`
	Size   int64  `json:"size,omitempty"`
	// SemVer2 is whether only SemVer 2.0.0 clients may see the version
	// (Nuspec.IsSemVer2).
	SemVer2 bool `json:"semver2,omitempty"`
	// Published is when the .nupkg was last deployed, in UTC to the
	// microsecond the stores keep.
	Published time.Time `json:"published,omitzero"`
	// Symbols is the path of the version's .snupkg, empty without one.
	Symbols string `json:"symbols,omitempty"`
}

// HasPackage reports whether a .nupkg backs the version.
func (m Metadata) HasPackage() bool { return m.Path != "" }

// IsPrerelease reports a version with a pre-release label. A normalised
// version's numeric parts hold no hyphen, so the first one starts it.
func (m Metadata) IsPrerelease() bool { return strings.Contains(m.Version, "-") }

// DecodeMetadata reads a version's Metadata.
func DecodeMetadata(b []byte) (Metadata, error) {
	var m Metadata
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("nuget: version metadata: %w", err)
	}
	return m, nil
}

// describe is the Metadata of the package n describes, stored as it.
func describe(n *Nuspec, it types.Item) Metadata {
	return Metadata{
		ID: n.ID, Version: n.Version.Normalized(), OriginalVersion: n.Version.Original,
		Title: n.Title, Authors: n.Authors, Owners: n.Owners, Description: n.Description,
		Summary: n.Summary, ReleaseNotes: n.ReleaseNotes, Copyright: n.Copyright,
		Language: n.Language, Tags: n.Tags, ProjectURL: n.ProjectURL, IconURL: n.IconURL,
		LicenseURL: n.LicenseURL, LicenseExpression: n.LicenseExpression,
		RequireLicenseAcceptance: n.RequireLicenseAcceptance != nil && *n.RequireLicenseAcceptance,
		MinClientVersion:         n.MinClientVersion, Readme: n.Readme,
		DependencyGroups: n.DependencyGroups, PackageTypes: n.PackageTypes,
		Nuspec: string(n.Raw), Path: it.FullPath(), SHA512: digest(it.Actual.SHA512),
		Size: it.Size, SemVer2: n.IsSemVer2(), Published: published(it),
	}
}

// encode is m as stored: the same Metadata always gives the same bytes,
// which is what lets a reindex leave the index unchanged. HTML is not
// escaped, so the nuspec keeps its angle brackets.
func (m Metadata) encode() []byte {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	// A Metadata holds strings, numbers and a time, which always marshal.
	_ = e.Encode(m)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// published is when the file it was last deployed. A move keeps it and a
// copy is a deploy at its target.
func published(it types.Item) time.Time {
	return it.Modified.UTC().Truncate(time.Microsecond)
}

// digest is a stored SHA-512, in hex, as NuGet's base64 PackageHash. Every
// upload path stores one (#296); without one it is empty.
func digest(sha512 string) string {
	raw, _ := hex.DecodeString(sha512)
	return base64.StdEncoding.EncodeToString(raw)
}
