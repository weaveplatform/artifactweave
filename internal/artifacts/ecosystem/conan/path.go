package conan

import (
	"context"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// Manifest is the file name of a revision's manifest, which the Conan
// client uploads last and which commits the revision: until it is stored,
// the revision's files are hidden from search, download and the index.
const Manifest = "conanmanifest.txt"

var componentPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+\-]{0,127}$`)

// Reference is the Conan identity of a recipe. Missing user and channel are
// represented by underscores on the wire, as the Conan client does.
type Reference struct {
	Name, Version, User, Channel, Revision string
}

// Root is the folder that holds every file of the recipe revision r,
// user/name/version/channel/revision: the conan-default layout puts the
// user first, unlike the reference's name/version@user/channel form.
func (r Reference) Root() string {
	return strings.Join([]string{r.User, r.Name, r.Version, r.Channel, r.Revision}, "/")
}

// RecipeRoot is the folder of the recipe revision's own files, the export
// folder under Root.
func (r Reference) RecipeRoot() string { return r.Root() + "/export" }

// PackageRoot is the folder of binary package id at package revision
// revision, built from the recipe revision r.
func (r Reference) PackageRoot(id, revision string) string {
	return r.Root() + "/package/" + id + "/" + revision
}

// IsValid reports whether every part of r, the revision included, is a
// valid Component.
func (r Reference) IsValid() bool {
	return Component(r.Name) && Component(r.Version) && Component(r.User) &&
		Component(r.Channel) && Component(r.Revision)
}

// Component rejects traversal and control characters before a protocol
// segment can become an item path. Conan references do not contain slashes.
func Component(s string) bool {
	return s != "." && s != ".." && componentPattern.MatchString(s)
}

// FilePath is the item-tree interpretation of a native Conan file URL.
type FilePath struct {
	Ref      Reference
	Package  bool
	ID, PRev string
	File     string
}

// Root is the folder of the revision f belongs to: the package revision's
// folder for a package file, and the recipe revision's export folder
// otherwise.
func (f FilePath) Root() string {
	if f.Package {
		return f.Ref.PackageRoot(f.ID, f.PRev)
	}
	return f.Ref.RecipeRoot()
}

// Path is f's item path: its File below Root.
func (f FilePath) Path() string { return f.Root() + "/" + f.File }

// SafeFile permits Conan's metadata subfolders but no empty, dot, encoded
// separator or exceptionally long path component.
func SafeFile(name string) bool {
	if name == "" || len(name) > 1024 || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || len(part) > 255 ||
			strings.ContainsAny(part, "\\\x00\r\n?#;%") {
			return false
		}
	}
	return true
}

// ParseStored recognizes the documented Artifactory conan-default layout:
// user/name/version/channel/recipe-revision/{export/file or
// package/id/package-revision/file}. It is deliberately independent of the
// HTTP route so restore and reindex use the same identity rules.
func ParseStored(path string) (FilePath, bool) {
	p := strings.Split(path, "/")
	if len(p) < 7 {
		return FilePath{}, false
	}
	r := Reference{User: p[0], Name: p[1], Version: p[2], Channel: p[3], Revision: p[4]}
	if !r.IsValid() {
		return FilePath{}, false
	}
	switch p[5] {
	case "export":
		file := strings.Join(p[6:], "/")
		return FilePath{Ref: r, File: file}, SafeFile(file)
	case "package":
		if len(p) < 9 || !Component(p[6]) || !Component(p[7]) {
			return FilePath{}, false
		}
		file := strings.Join(p[8:], "/")
		return FilePath{Ref: r, Package: true, ID: p[6], PRev: p[7], File: file}, SafeFile(file)
	default:
		return FilePath{}, false
	}
}

// Serves reports whether t is the Conan package type.
func Serves(t repo.PackageType) bool { return t == repo.Conan }

type nativeKey struct{}

// Native marks a write that entered through /api/conan. A generic item PUT
// cannot claim a Conan revision simply by choosing its storage path.
func Native(
	ctx context.Context,
) context.Context {
	return context.WithValue(ctx, nativeKey{}, true)
}

// IsNative reports whether ctx carries the mark Native sets, which Admit
// requires of every revision write that is not a remote's cached one.
func IsNative(ctx context.Context) bool { return ctx.Value(nativeKey{}) == true }
