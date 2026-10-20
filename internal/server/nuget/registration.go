package nuget

import (
	"compress/gzip"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// hive is a registration hive (decision 0038). Microsoft defines
// RegistrationsBaseUrl, with its 3.0.0-beta, 3.0.0-rc and 3.4.0 aliases,
// without SemVer 2.0.0 versions, and 3.6.0 with them (research 29
// section 5); the paths are 7.x's.
type hive struct {
	path    string
	semver2 bool
}

// hives are the two hives, the one without SemVer 2.0.0 versions first.
var hives = []hive{{path: "registration"}, {path: "registration-semver2", semver2: true}}

// shows reports whether the hive lists the version m describes.
func (hv hive) shows(m nugetfmt.Metadata) bool { return hv.semver2 || !m.SemVer2 }

// pageSize is how many versions a registration page holds. [I] 7.x's page
// size is not shown by any source; 128 is the most nuget.org inlines.
const pageSize = 128

// registration answers a hive (decision 0038): a package's index, a page
// for any bounds, a version's leaf, and Download/{id}/{version}, the
// packageContent URL 7.x's leaves carry. The answer is computed on read
// from the index for the caller and the request's base URL, so nothing is
// stored or queued, and it is gzipped when the request accepts it, which
// covers the 3.4.0 alias: the client accepts gzip on every request.
func (h handler) registration(hv hive) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		segs, err := segments(r, hv.path)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		switch {
		case len(segs) == 3 && segs[0] == "Download":
			h.download(w, r, segs[1], segs[2])
		case len(segs) == 2 && segs[1] == "index.json":
			h.registrationIndex(w, r, hv, segs[0])
		case len(segs) == 2 && strings.HasSuffix(segs[1], ".json"):
			h.registrationLeaf(w, r, hv, segs[0], strings.TrimSuffix(segs[1], ".json"))
		case len(segs) == 4 && segs[1] == "page" && strings.HasSuffix(segs[3], ".json"):
			h.registrationPage(w, r, hv, segs[0], segs[2], strings.TrimSuffix(segs[3], ".json"))
		default:
			h.k.Error(w, r, notFound(r))
		}
	}
}

// download serves the .nupkg of id at version through the item routes'
// download, refusing a caller who may not read it as they do.
func (h handler) download(w http.ResponseWriter, r *http.Request, id, ver string) {
	key, ok := versionKey(ver)
	if !ok {
		h.k.Error(w, r, notFound(r))
		return
	}
	v, ok, err := h.indexed(r, id, key)
	switch {
	case err != nil:
		h.k.Error(w, r, err)
	case !ok:
		h.k.Error(w, r, notFound(r))
	default:
		h.k.ServeItem(w, r, v.meta.Path)
	}
}

// hiveURLs builds the URLs a hive's documents carry.
type hiveURLs struct{ base string }

func urlsOf(r *http.Request, hv hive) hiveURLs {
	return hiveURLs{base: v3Base(r) + "/" + hv.path + "/"}
}

func (u hiveURLs) index(name string) string {
	return u.base + url.PathEscape(name) + "/index.json"
}

// leaf is a version's leaf: the version lower-cased, as the client builds
// the URL itself (RegistrationResourceV3.GetUri).
func (u hiveURLs) leaf(name, key string) string {
	return u.base + url.PathEscape(name) + "/" + url.PathEscape(key) + ".json"
}

func (u hiveURLs) page(name, lower, upper string) string {
	return u.base + url.PathEscape(name) + "/page/" + url.PathEscape(lower) + "/" +
		url.PathEscape(upper) + ".json"
}

// content is 7.x's packageContent, the id in its nuspec's case.
func (u hiveURLs) content(m nugetfmt.Metadata) string {
	return u.base + "Download/" + url.PathEscape(m.ID) + "/" + url.PathEscape(m.Version)
}

// registrationIndex is a package's registration index, in 7.x's order
// (RTFACT-30380).
//
//nolint:tagliatelle // NuGet's wire names
type registrationIndex struct {
	Count int        `json:"count"`
	Items []pageLink `json:"items"`
	ID    string     `json:"@id"`
}

// pageLink is a page the index links rather than inlines, as 7.x does even
// for one version (RTFACT-30380); the client fetches each from its @id.
//
//nolint:tagliatelle // NuGet's wire names
type pageLink struct {
	Count int    `json:"count"`
	Lower string `json:"lower"`
	Upper string `json:"upper"`
	ID    string `json:"@id"`
}

// registrationIndex lists the package's versions the caller may read and
// the hive shows, in pages of pageSize in NuGet order. A package with none
// answers 404, which the client reads as "no such package".
func (h handler) registrationIndex(w http.ResponseWriter, r *http.Request, hv hive, id string) {
	p, _, err := h.load(r, id)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	p = p.only(hv.shows)
	if len(p.versions) == 0 {
		h.k.Error(w, r, notFound(r))
		return
	}
	u := urlsOf(r, hv)
	out := registrationIndex{ID: u.index(p.name)}
	for start := 0; start < len(p.versions); start += pageSize {
		page := p.versions[start:min(start+pageSize, len(p.versions))]
		lower, upper := page[0].meta.Version, page[len(page)-1].meta.Version
		out.Items = append(out.Items, pageLink{
			Count: len(page), Lower: lower, Upper: upper, ID: u.page(p.name, lower, upper),
		})
	}
	out.Count = len(out.Items)
	writeCompressed(w, r, out)
}

// registrationPage is a page, in the order of 7.x's fields (RTFACT-30380).
//
//nolint:tagliatelle // NuGet's wire names
type registrationPage struct {
	Count  int    `json:"count"`
	Lower  string `json:"lower"`
	Parent string `json:"parent"`
	Upper  string `json:"upper"`
	Items  []leaf `json:"items"`
	ID     string `json:"@id"`
}

// leaf is a version in a page.
//
//nolint:tagliatelle // NuGet's wire names
type leaf struct {
	CatalogEntry   catalogEntry `json:"catalogEntry"`
	PackageContent string       `json:"packageContent"`
	Registration   string       `json:"registration"`
	ID             string       `json:"@id"`
}

// catalogEntry has 7.x's fields (RTFACT-30380), with @id, which Microsoft
// requires, the nuspec's other fields when it has them, and published,
// which 7.x leaves out though JFrog tracks it as missing (RTFACT-27465):
// Microsoft's schema has it, so filling it is no deviation. listed is
// always true: there is no unlisted state.
//
//nolint:tagliatelle // NuGet's wire names
type catalogEntry struct {
	ID                       string            `json:"@id"`
	PackageID                string            `json:"id"`
	Version                  string            `json:"version"`
	Title                    string            `json:"title,omitempty"`
	Authors                  string            `json:"authors,omitempty"`
	Summary                  string            `json:"summary,omitempty"`
	Description              string            `json:"description,omitempty"`
	Tags                     []string          `json:"tags,omitempty"`
	IconURL                  string            `json:"iconUrl,omitempty"`
	LicenseURL               string            `json:"licenseUrl,omitempty"`
	LicenseExpression        string            `json:"licenseExpression,omitempty"`
	ProjectURL               string            `json:"projectUrl,omitempty"`
	Language                 string            `json:"language,omitempty"`
	MinClientVersion         string            `json:"minClientVersion,omitempty"`
	RequireLicenseAcceptance bool              `json:"requireLicenseAcceptance"`
	Listed                   bool              `json:"listed"`
	Published                string            `json:"published"`
	PackageContent           string            `json:"packageContent"`
	DependencyGroups         []dependencyGroup `json:"dependencyGroups,omitempty"`
}

//nolint:tagliatelle // NuGet's wire names
type dependencyGroup struct {
	TargetFramework string       `json:"targetFramework,omitempty"`
	Dependencies    []dependency `json:"dependencies,omitempty"`
}

// dependency carries its range as the nuspec wrote it, as 7.x does, and
// the dependency's index in the same hive.
type dependency struct {
	ID           string `json:"id"`
	Range        string `json:"range,omitempty"`
	Registration string `json:"registration"`
}

// registrationPage answers every version the caller may read and the hive
// shows from lower to upper inclusive, compared as NuGet versions ignoring
// case, for any bounds, as 7.x answers a page it did not list
// (RTFACT-30380). Bounds that are not versions, or that hold no such
// version, answer 404.
func (h handler) registrationPage(
	w http.ResponseWriter,
	r *http.Request,
	hv hive,
	id, lower, upper string,
) {
	lo, okLo := nugetfmt.NormalizeVersion(lower)
	hi, okHi := nugetfmt.NormalizeVersion(upper)
	if !okLo || !okHi {
		h.k.Error(w, r, notFound(r))
		return
	}
	p, _, err := h.load(r, id)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	p = p.only(func(m nugetfmt.Metadata) bool {
		return hv.shows(m) && types.CompareNuGet(m.Version, lo) >= 0 &&
			types.CompareNuGet(m.Version, hi) <= 0
	})
	if len(p.versions) == 0 {
		h.k.Error(w, r, notFound(r))
		return
	}
	u := urlsOf(r, hv)
	out := registrationPage{
		Count: len(p.versions), Lower: lo, Upper: hi, Parent: u.index(p.name),
		ID: u.page(p.name, lo, hi), Items: make([]leaf, 0, len(p.versions)),
	}
	for _, v := range p.versions {
		out.Items = append(out.Items, leaf{
			CatalogEntry: u.entry(p.name, v), PackageContent: u.content(v.meta),
			Registration: u.index(p.name), ID: u.leaf(p.name, v.pv.Version),
		})
	}
	writeCompressed(w, r, out)
}

// entry is v's catalog entry.
func (u hiveURLs) entry(name string, v version) catalogEntry {
	m := v.meta
	e := catalogEntry{
		ID: u.leaf(name, v.pv.Version), PackageID: m.ID, Version: m.Version, Title: m.Title,
		Authors: m.Authors, Summary: m.Summary, Description: m.Description,
		Tags: strings.Fields(m.Tags), IconURL: m.IconURL, LicenseURL: m.LicenseURL,
		LicenseExpression: m.LicenseExpression, ProjectURL: m.ProjectURL, Language: m.Language,
		MinClientVersion: m.MinClientVersion, RequireLicenseAcceptance: m.RequireLicenseAcceptance,
		Listed: true, Published: stamp(m.Published), PackageContent: u.content(m),
	}
	for _, g := range m.DependencyGroups {
		out := dependencyGroup{TargetFramework: g.TargetFramework}
		for _, d := range g.Dependencies {
			out.Dependencies = append(out.Dependencies, dependency{
				ID: d.ID, Range: d.Range, Registration: u.index(nugetfmt.PackageName(d.ID)),
			})
		}
		e.DependencyGroups = append(e.DependencyGroups, out)
	}
	return e
}

// leafDocument is a version's leaf as 7.x answers it (RTFACT-27465), with
// published.
//
//nolint:tagliatelle // NuGet's wire names
type leafDocument struct {
	ID             string `json:"@id"`
	Listed         bool   `json:"listed"`
	PackageContent string `json:"packageContent"`
	Registration   string `json:"registration"`
	Published      string `json:"published"`
}

// registrationLeaf answers one version the caller may read and the hive
// shows; 404 for any other.
func (h handler) registrationLeaf(
	w http.ResponseWriter,
	r *http.Request,
	hv hive,
	id, ver string,
) {
	key, ok := versionKey(ver)
	if !ok {
		h.k.Error(w, r, notFound(r))
		return
	}
	p, _, err := h.load(r, id)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	v, ok := p.only(hv.shows).find(key)
	if !ok {
		h.k.Error(w, r, notFound(r))
		return
	}
	u := urlsOf(r, hv)
	writeCompressed(w, r, leafDocument{
		ID: u.leaf(p.name, v.pv.Version), Listed: true, PackageContent: u.content(v.meta),
		Registration: u.index(p.name), Published: stamp(v.meta.Published),
	})
}

// stamp is a time as the registration writes it: ISO 8601 in UTC, to the
// millisecond.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// writeCompressed answers 200 with v, gzipped when the request accepts
// gzip.
func writeCompressed(w http.ResponseWriter, r *http.Request, v any) {
	body := append(encode(v), '\n')
	w.Header().Set("Content-Type", jsonType)
	w.Header().Add("Vary", "Accept-Encoding")
	if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		_, _ = w.Write(body)
		return
	}
	w.Header().Set("Content-Encoding", "gzip")
	gz := gzip.NewWriter(w)
	_, _ = gz.Write(body)
	_ = gz.Close()
}

// acceptsGzip reports whether an Accept-Encoding header accepts gzip.
// An explicit gzip preference takes precedence over the wildcard, so
// "gzip;q=0, *" cannot send a client an encoding it explicitly refused.
func acceptsGzip(h string) bool {
	wildcard := false
	for entry := range strings.SplitSeq(h, ",") {
		name, params, _ := strings.Cut(entry, ";")
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "gzip" && name != "*" {
			continue
		}
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				q = f
			}
		}
		if name == "gzip" {
			return q > 0
		}
		wildcard = q > 0
	}
	return wildcard
}
