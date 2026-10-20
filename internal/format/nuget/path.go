package nuget

import (
	"path"
	"strings"
)

// kind is what a file's name makes it to the format.
type kind int

const (
	// other is every file the format stores as a plain item, never indexed
	// and never refused.
	other kind = iota
	// pkgFile is a .nupkg: a version's package.
	pkgFile
	// symbolsFile is a .snupkg: a version's symbols.
	symbolsFile
)

// Extensions of the files the format indexes, and of the legacy symbol
// packages it leaves alone. They are matched with their case, as NuGet
// writes them and as AQL, which a reindex finds them with, matches names.
const (
	PackageExt       = ".nupkg"
	SymbolsExt       = ".snupkg"
	LegacySymbolsExt = ".symbols.nupkg"
)

// kindOf is the kind of the file at p. A legacy .symbols.nupkg is a plain
// file: 7.x stopped indexing them in 7.90.5 (RTDEV-41067).
func kindOf(p string) kind {
	switch name := path.Base(p); {
	case strings.HasSuffix(name, LegacySymbolsExt):
		return other
	case strings.HasSuffix(name, PackageExt):
		return pkgFile
	case strings.HasSuffix(name, SymbolsExt):
		return symbolsFile
	}
	return other
}

func (k kind) ext() string {
	if k == symbolsFile {
		return SymbolsExt
	}
	return PackageExt
}

// FileName is the canonical file name of the package of id at version,
// "<id>.<version>.nupkg" with the id's own case and the version
// normalised (decision 0038). A push stores it at the repository root, or
// in the folder it names, where the JFrog CLI looks for it.
func FileName(id string, v Version) string { return id + "." + v.Normalized() + PackageExt }

// SymbolsFileName is the canonical file name of the symbols of id at
// version, "<id>.<version>.snupkg".
func SymbolsFileName(id string, v Version) string {
	return id + "." + v.Normalized() + SymbolsExt
}

// canonical reports whether p, a file of kind k, is at a canonical name of
// the package n describes: its file name compared ignoring case, in any
// folder.
func canonical(p string, k kind, n *Nuspec) bool {
	want := FileName(n.ID, n.Version)
	if k == symbolsFile {
		want = SymbolsFileName(n.ID, n.Version)
	}
	return k != other && strings.EqualFold(path.Base(p), want)
}

// PackageName is the package index's name for id: the id lower-cased, as
// NuGet compares ids ignoring case and as the flat container and the
// registration spell them.
func PackageName(id string) string { return strings.ToLower(id) }

// VersionKey is the package index's version for v: normalised and
// lower-cased, as the flat container and the registration spell it, so
// that versions NuGet takes for one, such as 1.0.0-Beta and 1.0.0-beta,
// are one version of the index. Metadata.Version keeps the case.
func VersionKey(v Version) string { return strings.ToLower(v.Normalized()) }

// versionKey names a version in the index.
type versionKey struct{ pkg, version string }

func keyOf(n *Nuspec) versionKey {
	return versionKey{pkg: PackageName(n.ID), version: VersionKey(n.Version)}
}

// keysOf are the versions whose canonical name p could be. A name such as
// Foo.Bar.1.0.0.nupkg reads as Foo.Bar 1.0.0, as Foo.Bar.1 0.0 and as
// Foo.Bar.1.0 0, since an id may end in a part of digits; the index holds
// at most the one the file's content named.
func keysOf(p string) []versionKey {
	k := kindOf(p)
	if k == other {
		return nil
	}
	base := strings.TrimSuffix(path.Base(p), k.ext())
	var out []versionKey
	for i := range len(base) - 1 {
		if base[i] != '.' || base[i+1] < '0' || base[i+1] > '9' {
			continue
		}
		id, ver := base[:i], base[i+1:]
		if v, ok := ParseVersion(ver); ok && ValidID(id) {
			out = append(out, versionKey{pkg: PackageName(id), version: VersionKey(v)})
		}
	}
	return out
}
