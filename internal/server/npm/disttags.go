package npm

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"

	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// maxTagBody bounds a dist-tag PUT's body, which is one version as a JSON
// string.
const maxTagBody = 64 << 10

// tagPath reads the path of npm's dist-tag operations,
// /-/package/<name>/dist-tags[/<tag>] (R25 2.2); tag is "" for the list.
// The route matched its first two segments.
func tagPath(r *http.Request) (name, tag string, err error) {
	p, err := pkgroute.ParsePath(pkgroute.RequestFrom(r).Rest)
	if err != nil {
		return "", "", err //nolint:wrapcheck // the kit's 400
	}
	name, rest, ok := splitName(p.Segments[2:])
	switch {
	case !ok || len(rest) == 0 || rest[0] != "dist-tags" || len(rest) > 2:
		return "", "", notFound(r)
	case len(rest) == 2:
		return name, rest[1], nil
	}
	return name, "", nil
}

// listTags answers npm dist-tag ls: the dist-tags the packument shows the
// caller.
func (h handler) listTags(w http.ResponseWriter, r *http.Request) {
	name, tag, err := tagPath(r)
	switch {
	case err != nil:
		h.k.Error(w, r, err)
		return
	case tag != "":
		h.k.Error(w, r, notFound(r))
		return
	}
	p, ok := h.load(w, r, name)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, jsonType, p.distTags())
}

// addTag answers npm dist-tag add: the body names a version as a JSON
// string, whose tarball gains npm.disttag=<tag>; the npm format takes the
// tag from the version that had it. Both tarballs need Annotate.
func (h handler) addTag(w http.ResponseWriter, r *http.Request) {
	name, tag, ok := h.tagged(w, r)
	if !ok {
		return
	}
	var spec string
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTagBody)).Decode(&spec) != nil {
		h.k.Error(w, r, badRequest("the body must be the version to tag, as a JSON string"))
		return
	}
	versions, ok := h.indexedOr404(w, r, name)
	if !ok {
		return
	}
	version, valid := npmfmt.CleanVersion(spec)
	if !valid || !slices.ContainsFunc(versions, func(v indexedVersion) bool {
		return v.number == version
	}) {
		h.k.Error(w, r, apierror.New(http.StatusNotFound,
			fmt.Sprintf("version not found: %s@%s", name, spec)))
		return
	}
	for _, v := range versions {
		if v.number != version && slices.Contains(v.tags, tag) &&
			!h.k.Require(w, r, npmfmt.CanonicalPath(name, v.number), types.ActionAnnotate) {
			return
		}
	}
	h.retag(w, r, name, map[string]func([]string) []string{version: func(tags []string) []string {
		if slices.Contains(tags, tag) {
			return tags
		}
		return append(tags, tag)
	}})
}

// removeTag answers npm dist-tag rm: the tag is taken off the tarball
// that carries it, which needs Annotate. latest taken off goes to the
// version that outranks the others, as the npm format settles it, which
// may be the version it was taken from.
func (h handler) removeTag(w http.ResponseWriter, r *http.Request) {
	name, tag, ok := h.tagged(w, r)
	if !ok {
		return
	}
	versions, ok := h.indexedOr404(w, r, name)
	if !ok {
		return
	}
	drop := map[string]func([]string) []string{}
	for _, v := range versions {
		if slices.Contains(v.tags, tag) {
			drop[v.number] = func(tags []string) []string {
				return slices.DeleteFunc(tags, func(t string) bool { return t == tag })
			}
		}
	}
	if len(drop) == 0 {
		h.k.Error(w, r, apierror.New(http.StatusNotFound,
			fmt.Sprintf("%s is not a dist-tag on %s", tag, name)))
		return
	}
	h.retag(w, r, name, drop)
}

// tagged reads a dist-tag change's path, refusing a tag npm would take for
// a version range and a caller who may annotate nothing in the package.
// A PUT of the whole map, which npm never sends, is not served.
func (h handler) tagged(w http.ResponseWriter, r *http.Request) (name, tag string, ok bool) {
	name, tag, err := tagPath(r)
	switch {
	case err != nil:
		h.k.Error(w, r, err)
		return "", "", false
	case tag == "":
		h.k.Error(w, r, apierror.New(http.StatusMethodNotAllowed,
			"method not allowed: "+r.Method+" "+r.URL.Path))
		return "", "", false
	// npm makes the same check before sending (R25 2.2), with its words.
	case validRange(tag):
		h.k.Error(w, r, badRequest("Tag name must not be a valid SemVer range: "+tag))
		return "", "", false
	}
	return name, tag, h.k.RequireFolder(w, r, name, types.ActionAnnotate)
}

// indexedVersion is one indexed version with the state npm changes after
// a publish: the dist-tags and the deprecation its tarball carries.
type indexedVersion struct {
	number     string
	tags       []string
	deprecated string
}

// indexedOr404 is every version of the package called name, whatever the
// caller may read, having answered 404 when there is none.
func (h handler) indexedOr404(
	w http.ResponseWriter,
	r *http.Request,
	name string,
) ([]indexedVersion, bool) {
	versions, ok, err := h.indexed(r, name)
	switch {
	case err != nil:
		h.k.Error(w, r, err)
		return nil, false
	case !ok:
		h.k.Error(w, r, apierror.New(http.StatusNotFound, "package not found: "+name))
		return nil, false
	}
	out := make([]indexedVersion, 0, len(versions))
	for _, v := range versions {
		m, err := npmfmt.DecodeMetadata(v.Metadata)
		if err != nil {
			h.k.Error(w, r, fmt.Errorf("npm: %s@%s: %w", name, v.Version, err))
			return nil, false
		}
		out = append(out, indexedVersion{number: v.Version, tags: m.Tags, deprecated: m.Deprecated})
	}
	return out, true
}

// retag changes the npm.disttag of each version change names, by the
// function it gives, in one transaction, and answers the dist-tags the
// caller then sees, as the npm registry answers a dist-tag change. Each
// tarball needs Annotate, which is checked before anything changes.
func (h handler) retag(
	w http.ResponseWriter,
	r *http.Request,
	name string,
	change map[string]func([]string) []string,
) {
	var edits []annotation
	for _, version := range slices.Sorted(maps.Keys(change)) {
		it, err := h.tarball(r, name, version)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if !h.k.Require(w, r, it.FullPath(), types.ActionAnnotate) {
			return
		}
		props, err := h.k.DB().Items().Properties(r.Context(), it.ID)
		if err != nil {
			h.k.Error(w, r, fmt.Errorf("npm: properties of %s: %w", it.FullPath(), err))
			return
		}
		tags := change[version](slices.DeleteFunc(slices.Clone(props[npmfmt.PropDistTag]),
			func(t string) bool { return t == "" }))
		edits = append(edits, annotation{it: it, key: npmfmt.PropDistTag, values: tags})
	}
	if err := h.annotate(r, edits); err != nil {
		h.k.Error(w, r, err)
		return
	}
	p, ok, err := h.readable(r, name)
	switch {
	case err != nil:
		h.k.Error(w, r, err)
	case !ok:
		writeJSON(w, http.StatusOK, jsonType, map[string]string{})
	default:
		writeJSON(w, http.StatusOK, jsonType, p.distTags())
	}
}

// SemVer ranges as npm's semver.validRange reads them without its loose
// option: a range is ranges joined by ||, each a hyphen range or
// comparators separated by spaces, and a comparator an X-range after <, >,
// <=, >= or =, or a tilde, caret or X-range, where a version may be
// partial, start with v or =, and use x, X or * for a number.
var (
	xrangeID    = `(?:0|[1-9]\d*|[xX*])`
	preID       = `(?:0|[1-9]\d*|\d*[a-zA-Z-][a-zA-Z0-9-]*)`
	xrangePlain = `[v=\s]*` + xrangeID + `(?:\.` + xrangeID + `(?:\.` + xrangeID +
		`(?:-` + preID + `(?:\.` + preID + `)*)?(?:\+[a-zA-Z0-9-]+(?:\.[a-zA-Z0-9-]+)*)?)?)?`
	rangeComparator = regexp.MustCompile(
		`^(?:[<>]?=?` + xrangePlain + `|~>?` + xrangePlain + `|\^` + xrangePlain + `)$`)
	hyphenRange = regexp.MustCompile(`^` + xrangePlain + `\s+-\s+` + xrangePlain + `$`)
	// rangeOperator is an operator with the space semver lets follow it.
	rangeOperator = regexp.MustCompile(`(<=|>=|<|>|=|~>|~|\^)\s+`)
	rangeOr       = regexp.MustCompile(`\s*\|\|\s*`)
)

// validRange reports whether s reads as a SemVer range, which npm refuses
// as a dist-tag since a tag and a range could not be told apart.
func validRange(s string) bool {
	for _, part := range rangeOr.Split(strings.TrimSpace(s), -1) {
		if part == "" || hyphenRange.MatchString(part) {
			continue
		}
		for _, c := range strings.Fields(rangeOperator.ReplaceAllString(part, "$1")) {
			if !rangeComparator.MatchString(c) {
				return false
			}
		}
	}
	return true
}
