package nuget

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	// defaultTake is how many results a search answers when take is not a
	// number: 20, what Microsoft's documentation and the client use.
	defaultTake = 20
	// maxTake caps take. [I] 7.x's cap is not shown by any source; 1,000 is
	// nuget.org's.
	maxTake = 1000
)

// listPage is how many packages one read of the index lists.
var listPage = 500

// filter is which versions a feed request may see beyond what its caller
// may read (decision 0038): pre-releases only when asked for, and SemVer
// 2.0.0 versions only for a client that says it reads them, which clients
// before NuGet 4.3.0 do not.
type filter struct {
	prerelease, semver2 bool
}

// shows reports whether the filter lets the version m describes through.
func (f filter) shows(m nugetfmt.Metadata) bool {
	return (f.prerelease || !m.IsPrerelease()) && (f.semver2 || !m.SemVer2)
}

// semVer2 reports whether a semVerLevel parameter is 2.0.0 or more; an
// absent or unreadable one is 1.0.0, as Microsoft defines it.
func semVer2(level string) bool {
	v, ok := nugetfmt.NormalizeVersion(level)
	return ok && types.CompareNuGet(v, "2.0.0") >= 0
}

// match is what a search matches a package's newest visible version
// against: every term must appear, ignoring case, in its id, title,
// description or tags, and a package type, when one is asked for, must be
// declared. [I] How 7.x matches terms is not shown by any source.
type match struct {
	// terms are lower case; none matches every package.
	terms []string
	// packageType is "" for any.
	packageType string
}

// matches reports whether m, a package's newest visible version, matches.
func (q match) matches(m nugetfmt.Metadata) bool {
	if q.packageType != "" && !declares(m, q.packageType) {
		return false
	}
	text := strings.ToLower(strings.Join([]string{m.ID, m.Title, m.Description, m.Tags}, "\n"))
	for _, t := range q.terms {
		if !strings.Contains(text, t) {
			return false
		}
	}
	return true
}

// declares reports whether m declares the package type name, ignoring
// case. [I] A package that declares none is a Dependency, NuGet's default
// type.
func declares(m nugetfmt.Metadata, name string) bool {
	if len(m.PackageTypes) == 0 {
		return strings.EqualFold(name, dependencyType)
	}
	for _, t := range m.PackageTypes {
		if strings.EqualFold(t.Name, name) {
			return true
		}
	}
	return false
}

const dependencyType = "Dependency"

// page is a request's skip and take: a value that is no number, or is
// negative, takes its default, and take is capped at maxTake.
type page struct{ skip, take int }

func pageOf(q url.Values) page {
	p := page{take: defaultTake}
	if n, err := strconv.Atoi(q.Get("skip")); err == nil && n > 0 {
		p.skip = n
	}
	if n, err := strconv.Atoi(q.Get("take")); err == nil && n >= 0 {
		p.take = min(n, maxTake)
	}
	return p
}

// eachPackage calls each with every package of the request's repository
// whose name keep accepts, in the index's order, the id lower-cased, with
// the versions the caller may read and f shows. A package left with none
// is skipped.
func (h handler) eachPackage(
	r *http.Request,
	keep func(name string) bool,
	f filter,
	each func(p pkg) error,
) error {
	key := pkgroute.RequestFrom(r).Repo.Key
	at := store.Page{Limit: listPage}
	for {
		found, next, err := h.k.DB().Packages().List(r.Context(), key, at)
		if err != nil {
			return fmt.Errorf("nuget: packages of %s: %w", key, err)
		}
		for _, fp := range found {
			if !keep(fp.Name) {
				continue
			}
			p, ok, err := h.versionsOf(r, fp)
			if err != nil {
				return err
			}
			p = p.only(f.shows)
			if !ok || len(p.versions) == 0 {
				continue
			}
			if err := each(p); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		at.After = next
	}
}

// newest is the package's newest version shown.
func (p pkg) newest() version { return p.versions[len(p.versions)-1] }

// searchAnswer is the search's body.
//
//nolint:tagliatelle // NuGet's wire names
type searchAnswer struct {
	TotalHits int            `json:"totalHits"`
	Data      []searchResult `json:"data"`
}

// searchResult is one package, its fields its newest visible version's.
// Those the nuspec lacks are left out; owners and verified are never sent,
// since nothing here records who owns a package or verifies a prefix.
//
//nolint:tagliatelle // NuGet's wire names
type searchResult struct {
	ID             string          `json:"id"`
	Version        string          `json:"version"`
	Description    string          `json:"description,omitempty"`
	Summary        string          `json:"summary,omitempty"`
	Title          string          `json:"title,omitempty"`
	IconURL        string          `json:"iconUrl,omitempty"`
	LicenseURL     string          `json:"licenseUrl,omitempty"`
	ProjectURL     string          `json:"projectUrl,omitempty"`
	Tags           []string        `json:"tags,omitempty"`
	Authors        []string        `json:"authors,omitempty"`
	Registration   string          `json:"registration"`
	TotalDownloads int64           `json:"totalDownloads"`
	PackageTypes   []packageType   `json:"packageTypes"`
	Versions       []searchVersion `json:"versions"`
}

type packageType struct {
	Name string `json:"name"`
}

//nolint:tagliatelle // NuGet's wire names
type searchVersion struct {
	Version   string `json:"version"`
	Downloads int64  `json:"downloads"`
	ID        string `json:"@id"`
}

// search answers the SearchQueryService (decision 0038) from the package
// index, which every deploy keeps current in its own transaction, so a
// package is found once its push commits. Each package is matched as its
// caller sees it, by its newest version among those the caller may read
// and the request shows. includeDelisted and supportedFramework are
// ignored: nothing is unlisted, and nothing here reads a package's
// frameworks. Results are in id order ignoring case, the index's [I], and
// 7.x's undocumented search rate limit has no equivalent.
func (h handler) search(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	q := match{
		terms:       strings.Fields(strings.ToLower(params.Get("q"))),
		packageType: strings.TrimSpace(params.Get("packageType")),
	}
	f := filter{
		prerelease: strings.EqualFold(params.Get("prerelease"), "true"),
		semver2:    semVer2(params.Get("semVerLevel")),
	}
	at := pageOf(params)
	hv := hives[0]
	if f.semver2 {
		hv = hives[1]
	}
	u := urlsOf(r, hv)
	answer := searchAnswer{Data: []searchResult{}}
	err := h.eachPackage(r, func(string) bool { return true }, f, func(p pkg) error {
		if !q.matches(p.newest().meta) {
			return nil
		}
		answer.TotalHits++
		if answer.TotalHits <= at.skip || len(answer.Data) == at.take {
			return nil
		}
		res, err := h.result(r, u, p)
		if err != nil {
			return err
		}
		answer.Data = append(answer.Data, res)
		return nil
	})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, answer)
}

// result is p as a search result, its registration in the hive the
// request's semVerLevel names.
func (h handler) result(r *http.Request, u hiveURLs, p pkg) (searchResult, error) {
	m := p.newest().meta
	res := searchResult{
		ID: m.ID, Version: m.Version, Description: m.Description, Summary: m.Summary,
		Title: m.Title, IconURL: m.IconURL, LicenseURL: m.LicenseURL, ProjectURL: m.ProjectURL,
		Tags: strings.Fields(m.Tags), Authors: splitAuthors(m.Authors),
		Registration: u.index(p.name), Versions: make([]searchVersion, 0, len(p.versions)),
	}
	for _, t := range m.PackageTypes {
		res.PackageTypes = append(res.PackageTypes, packageType{Name: t.Name})
	}
	if len(res.PackageTypes) == 0 {
		res.PackageTypes = []packageType{{Name: dependencyType}}
	}
	for _, v := range p.versions {
		n, err := h.downloads(r, v.meta)
		if err != nil {
			return searchResult{}, err
		}
		res.TotalDownloads += n
		res.Versions = append(res.Versions, searchVersion{
			Version: v.meta.Version, Downloads: n, ID: u.leaf(p.name, v.pv.Version),
		})
	}
	return res, nil
}

// splitAuthors is the nuspec's comma-separated authors as a list.
func splitAuthors(s string) []string {
	var out []string
	for a := range strings.SplitSeq(s, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// downloads is how often the .nupkg m describes was downloaded, from the
// item's download statistics, which every download route counts.
func (h handler) downloads(r *http.Request, m nugetfmt.Metadata) (int64, error) {
	ctx := r.Context()
	st := h.k.DB()
	key := pkgroute.RequestFrom(r).Repo.Key
	it, err := st.Items().Get(ctx, key, false, m.Path)
	if err != nil {
		return 0, fmt.Errorf("nuget: %s/%s: %w", key, m.Path, err)
	}
	s, err := st.Items().Stats(ctx, it.ID)
	if err != nil {
		return 0, fmt.Errorf("nuget: statistics of %s/%s: %w", key, m.Path, err)
	}
	return s.DownloadCount, nil
}

// autocompleteAnswer is the autocomplete's body: ids, or one id's versions.
//
//nolint:tagliatelle // NuGet's wire names
type autocompleteAnswer struct {
	TotalHits int      `json:"totalHits"`
	Data      []string `json:"data"`
}

// autocomplete answers the SearchAutocompleteService (decision 0038).
// With q, the ids, ordered, of the packages with a version the caller may
// read and the request shows whose id starts with q, ignoring case, which
// is how the client uses it (AutoCompleteResourceV3); with id, that
// package's versions, ascending. [I] 7.x's URL and matching are not shown
// by any source. skip and take page both, as for search.
func (h handler) autocomplete(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	f := filter{
		prerelease: strings.EqualFold(params.Get("prerelease"), "true"),
		semver2:    semVer2(params.Get("semVerLevel")),
	}
	at := pageOf(params)
	answer := autocompleteAnswer{Data: []string{}}
	add := func(s string) {
		answer.TotalHits++
		if answer.TotalHits > at.skip && len(answer.Data) < at.take {
			answer.Data = append(answer.Data, s)
		}
	}
	if params.Has("id") {
		p, _, err := h.load(r, params.Get("id"))
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		for _, v := range p.only(f.shows).versions {
			add(v.meta.Version)
		}
		writeJSON(w, answer)
		return
	}
	prefix := strings.ToLower(params.Get("q"))
	err := h.eachPackage(r, func(name string) bool { return strings.HasPrefix(name, prefix) }, f,
		func(p pkg) error {
			add(p.newest().meta.ID)
			return nil
		})
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeJSON(w, answer)
}
