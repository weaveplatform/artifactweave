// Package conan owns Conan's on-disk revision layout and write policy.
package conan

import (
	"context"
	"regexp"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

const Manifest = "conanmanifest.txt"

var componentPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+\-]{0,127}$`)

// Reference is the Conan identity of a recipe. Missing user and channel are
// represented by underscores on the wire, as the Conan client does.
type Reference struct {
	Name, Version, User, Channel, Revision string
}

func (r Reference) Root() string {
	return strings.Join([]string{r.User, r.Name, r.Version, r.Channel, r.Revision}, "/")
}

func (r Reference) RecipeRoot() string { return r.Root() + "/export" }

func (r Reference) PackageRoot(id, revision string) string {
	return r.Root() + "/package/" + id + "/" + revision
}

func (r Reference) Valid() bool {
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

func (f FilePath) Root() string {
	if f.Package {
		return f.Ref.PackageRoot(f.ID, f.PRev)
	}
	return f.Ref.RecipeRoot()
}

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
	if !r.Valid() {
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

func Serves(t repo.PackageType) bool { return t == repo.Conan }

type nativeKey struct{}

// Native marks a write that entered through /api/conan. A generic item PUT
// cannot claim a Conan revision simply by choosing its storage path.
func Native(
	ctx context.Context,
) context.Context {
	return context.WithValue(ctx, nativeKey{}, true)
}

func IsNative(ctx context.Context) bool { return ctx.Value(nativeKey{}) == true }
