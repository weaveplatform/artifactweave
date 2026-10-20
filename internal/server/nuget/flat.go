package nuget

import (
	"io"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/pkg/types"
)

// flat answers the flat container, PackageBaseAddress (decision 0038),
// which restore uses whenever the service index lists it:
// {lower id}/index.json lists the versions, and
// {lower id}/{lower version}/{lower id}.{lower version}.nupkg and
// .../{lower id}.nuspec serve a version's package and manifest. The id and
// version segments match ignoring case and the version is normalised first,
// since the .NET client sends a version in the case the list gave it.
func (h handler) flat(w http.ResponseWriter, r *http.Request) {
	segs, err := segments(r, "flatcontainer")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	switch {
	case len(segs) == 2 && segs[1] == "index.json":
		h.flatVersions(w, r, segs[0])
	case len(segs) == 3:
		h.flatFile(w, r, segs[0], segs[1], segs[2])
	default:
		h.k.Error(w, r, notFound(r))
	}
}

// flatContainerVersions is the version list's body.
type flatContainerVersions struct {
	Versions []string `json:"versions"`
}

// flatVersions lists every version of id the caller may read, normalised,
// lower-cased and ascending. There is no unlisted state, so every version
// is listed. A package with none answers 404, which the client reads as
// "no such package" (IgnoreNotFounds); a caller who may read nothing of it
// gets the same, so the answer says nothing about what it may not read.
func (h handler) flatVersions(w http.ResponseWriter, r *http.Request, id string) {
	p, ok, err := h.load(r, id)
	switch {
	case err != nil:
		h.k.Error(w, r, err)
		return
	case !ok:
		h.k.Error(w, r, notFound(r))
		return
	}
	out := flatContainerVersions{Versions: make([]string, 0, len(p.versions))}
	for _, v := range p.versions {
		out.Versions = append(out.Versions, v.pv.Version)
	}
	writeJSON(w, out)
}

// flatFile serves a version's .nupkg through the item routes' download,
// or its .nuspec as the package holds it, to a caller who may read the
// .nupkg. A version that is not there, or a file name that is neither,
// answers 404.
func (h handler) flatFile(w http.ResponseWriter, r *http.Request, id, ver, file string) {
	key, ok := versionKey(ver)
	if !ok {
		h.k.Error(w, r, notFound(r))
		return
	}
	isPackage := strings.EqualFold(file, id+"."+ver+".nupkg")
	if !isPackage && !strings.EqualFold(file, id+".nuspec") {
		h.k.Error(w, r, notFound(r))
		return
	}
	v, ok, err := h.indexed(r, id, key)
	switch {
	case err != nil:
		h.k.Error(w, r, err)
	case !ok:
		h.k.Error(w, r, notFound(r))
	case isPackage:
		h.k.ServeItem(w, r, v.meta.Path)
	case h.k.Require(w, r, v.meta.Path, types.ActionRead):
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, v.meta.Nuspec)
	}
}
