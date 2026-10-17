// Package golang is the format of Go module repositories (decision 0035).
// It reads the GOPROXY paths a request names, checks the path of every
// version file a client deploys, chooses what @v/list and @latest answer,
// and keeps the package index. In the index a module is a package, and
// each of its versions links whichever of the version's .zip, .mod and
// .info are stored.
package golang

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// Extensions of a version's files.
const (
	ExtZip  = "zip"
	ExtMod  = "mod"
	ExtInfo = "info"
)

// File is one file of a module version.
type File struct {
	Module, Version, Ext string
}

// Path is where f is stored: <module>/@v/<version>.<ext>, with the module
// path and version as written, not case-encoded, because that is what
// jf go-publish deploys and records in its build-info.
func (f File) Path() string { return f.Module + "/@v/" + f.Version + "." + f.Ext }

// Kind is the GOPROXY operation a request path names.
type Kind int

// Operations under /artifactory/api/go/{repoKey}/.
const (
	// KindList is <module>/@v/list.
	KindList Kind = iota + 1
	// KindInfo, KindMod and KindZip are <module>/@v/<version>.info, .mod and .zip.
	KindInfo
	KindMod
	KindZip
	// KindLatest is <module>/@latest, and <module>/@v/latest.info, which jf go
	// asks for.
	KindLatest
)

// Request is a GOPROXY request path after the repository key, with its
// module path and version decoded.
type Request struct {
	Kind    Kind
	Module  string
	Version string
}

// File is the version file r names; it is meaningful for KindInfo, KindMod and KindZip.
func (r Request) File() File {
	ext := map[Kind]string{KindInfo: ExtInfo, KindMod: ExtMod, KindZip: ExtZip}[r.Kind]
	return File{Module: r.Module, Version: r.Version, Ext: ext}
}

// ErrNoOperation is a path that names no GOPROXY operation. Every method
// answers it 404.
var ErrNoOperation = errors.New("golang: no Go module operation")

// Invalid is a path that names a Go operation but fails its checks. A read
// answers it 404, and a PUT 400 with the message.
type Invalid struct{ Message string }

func (e *Invalid) Error() string { return e.Message }

// ParseRequest reads a GOPROXY request path. A module path or version that
// contains "!" is case-decoded, and anything else is taken as written: the
// go command sends the encoded form and jf the plain one, and both name the
// same version. The split is at the last "/@v/", which is unambiguous
// because a module path cannot contain "@". The checks are those a PUT
// applies (decision 0035): a version file's extension is zip, mod or info,
// and its module path and version pass module.Check with the version
// canonical, which also turns a .info request for a branch name away.
func ParseRequest(p string) (Request, error) {
	if m, ok := strings.CutSuffix(p, "/@latest"); ok {
		return request(KindLatest, m, "")
	}
	m, rest, ok := split(p)
	switch {
	case !ok:
		return Request{}, fmt.Errorf("%w: %s", ErrNoOperation, p)
	case rest == "list":
		return request(KindList, m, "")
	case rest == "latest.info":
		return request(KindLatest, m, "")
	}
	v, ext, ok := cutExt(rest)
	if !ok {
		return Request{}, &Invalid{
			Message: fmt.Sprintf("File %s is not a Go module .zip, .mod or .info.", rest),
		}
	}
	return request(map[string]Kind{ExtInfo: KindInfo, ExtMod: KindMod, ExtZip: KindZip}[ext], m, v)
}

// request decodes and checks the module path and version of a request of
// kind k.
func request(k Kind, escapedModule, escapedVersion string) (Request, error) {
	if escapedModule == "" {
		return Request{}, fmt.Errorf("%w: no module path", ErrNoOperation)
	}
	m, ok := decode(escapedModule, module.UnescapePath)
	if !ok {
		return Request{}, &Invalid{
			Message: fmt.Sprintf("Module path %s is not validly case-encoded.", escapedModule),
		}
	}
	if k == KindList || k == KindLatest {
		if err := checkPath(m); err != nil {
			return Request{}, err
		}
		return Request{Kind: k, Module: m}, nil
	}
	v, ok := decode(escapedVersion, module.UnescapeVersion)
	if !ok {
		return Request{}, &Invalid{Message: fmt.Sprintf(
			"Version %s of module %s is not validly case-encoded.", escapedVersion, m)}
	}
	if err := check(m, v); err != nil {
		return Request{}, err
	}
	return Request{Kind: k, Module: m, Version: v}, nil
}

// decode case-decodes s with unescape when it holds an escape. "!" is never
// part of a valid module path or version, and upper case never part of an
// encoded one, so either spelling is accepted.
func decode(s string, unescape func(string) (string, error)) (string, bool) {
	if !strings.Contains(s, "!") {
		return s, true
	}
	out, err := unescape(s)
	return out, err == nil
}

// ParseFile reads the stored path of a version file and applies the path
// checks of decision 0035, which decide whether the file is indexed. A
// stored path is never case-decoded: a file deployed through the item
// routes at an encoded path fails module.Check, as "!" cannot appear in a
// module path, and belongs to no version.
func ParseFile(p string) (File, bool) {
	m, rest, ok := split(p)
	if !ok || m == "" {
		return File{}, false
	}
	v, ext, ok := cutExt(rest)
	if !ok || check(m, v) != nil {
		return File{}, false
	}
	return File{Module: m, Version: v, Ext: ext}, true
}

// split cuts p at its last "/@v/" into the module path and the file name
// below @v.
func split(p string) (m, rest string, ok bool) {
	i := strings.LastIndex(p, "/@v/")
	if i < 0 || strings.Contains(p[i+len("/@v/"):], "/") {
		return "", "", false
	}
	return p[:i], p[i+len("/@v/"):], true
}

// cutExt splits a version file's name into its version and an extension
// that is zip, mod or info.
func cutExt(name string) (v, ext string, ok bool) {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return "", "", false
	}
	v, ext = name[:i], name[i+1:]
	switch ext {
	case ExtZip, ExtMod, ExtInfo:
		return v, ext, true
	}
	return "", "", false
}

// check applies module.Check, which also requires a /vN module path's
// versions to be vN, and requires the version to be canonical, so one
// version has one path. It also refuses +incompatible below v2, as the go
// command does and module.Check does not: on a path without /vN, v1.0.0 and
// v1.0.0+incompatible would otherwise be two versions the package index
// orders as one.
func check(m, v string) error {
	if err := checkPath(m); err != nil {
		return err
	}
	invalid := func(why any) error {
		return &Invalid{Message: fmt.Sprintf("Version %s of module %s is invalid: %v.", v, m, why)}
	}
	if err := module.Check(m, v); err != nil {
		return invalid(cause(err))
	}
	if semver.Build(v) == "+incompatible" && semver.Compare(v, "v2.0.0") < 0 {
		return invalid("+incompatible marks only v2 and later")
	}
	if c := module.CanonicalVersion(v); c != v {
		return &Invalid{Message: fmt.Sprintf(
			"Version %s of module %s is not canonical: use %s.", v, m, c)}
	}
	return nil
}

// checkPath applies module.CheckPath.
func checkPath(m string) error {
	if err := module.CheckPath(m); err != nil {
		return &Invalid{Message: fmt.Sprintf("Module path %s is invalid: %v.", m, cause(err))}
	}
	return nil
}

// cause is the innermost error x/mod wrapped, which says what is wrong
// without repeating the module path and version the message names.
func cause(err error) error {
	for u := errors.Unwrap(err); u != nil; u = errors.Unwrap(err) {
		err = u
	}
	return err
}
