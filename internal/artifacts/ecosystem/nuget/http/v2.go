package nugethttp

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// The key grammar is deliberately narrower than OData: these are the two
// literals NuGet.Protocol's BuildGetPackageUri writes. Escaped quotes are
// decoded before lookup; package validation still owns which IDs exist.
var v2Key = regexp.MustCompile(`^Packages\(Id='((?:[^']|'')*)',Version='((?:[^']|'')*)'\)$`)

const v2PageSize = 100

// v2 reads the legacy feed. The wildcard shares the kit's path validation
// and avoids treating an OData key's punctuation as chi route syntax.
func (h handler) v2(w http.ResponseWriter, r *http.Request) {
	segs, err := segments(r, "")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if len(segs) == 0 {
		writeV2Service(w, r)
		return
	}
	if len(segs) == 3 && segs[0] == "Download" {
		h.download(w, r, segs[1], segs[2])
		return
	}
	if len(segs) == 1 && segs[0] == "$metadata" {
		writeV2Metadata(w)
		return
	}
	if len(segs) == 1 {
		if key := v2Key.FindStringSubmatch(segs[0]); key != nil {
			h.v2Entry(
				w,
				r,
				strings.ReplaceAll(key[1], "''", "'"),
				strings.ReplaceAll(key[2], "''", "'"),
			)
			return
		}
	}
	count := len(segs) == 2 && segs[1] == "$count"
	if len(segs) != 1 && !count {
		h.k.Error(w, r, notFound(r))
		return
	}
	switch segs[0] {
	case "Packages", "Packages()", "FindPackagesById()", "Search()":
		h.v2Feed(w, r, segs[0], count)
	default:
		h.k.Error(w, r, notFound(r))
	}
}

// An exact lookup includes a SemVer 2 version even without semVerLevel:
// NuGet's exact restore request carries no query parameters at all.
func (h handler) v2Entry(w http.ResponseWriter, r *http.Request, id, ver string) {
	if h.blackedOut(w, r) {
		return
	}
	key, valid := versionKey(ver)
	if !valid {
		h.k.Error(w, r, notFound(r))
		return
	}
	p, ok, err := h.load(r, id)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	v, found := p.find(key)
	if !ok || !found {
		h.k.Error(w, r, notFound(r))
		return
	}
	entries, err := h.v2Entries(
		r,
		p,
		func(vv v2Version) bool { return vv.meta.Version == v.meta.Version },
	)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeXML(w, "application/atom+xml;type=entry", entries[0])
}

type v2Query struct {
	filter    filter
	match     match
	id        string
	skip, top int
	isLimited bool
	predicate v2Predicate
	// selects is which versions predicate keeps when the store can tell
	// them apart itself (v2Stored).
	selects v2Selects
}

// v2Selects is a $filter the store applies exactly: none, or one latest
// flag alone, the forms NuGet's V2 client sends to list a feed. Any other
// filter is v2SelectsOther, and only the handler applies it.
type v2Selects int

const (
	v2SelectsOther v2Selects = iota
	// v2SelectsAll keeps every version shown.
	v2SelectsAll
	// v2SelectsLatest keeps a package's last shown release
	// (IsLatestVersion), which a package has when one of its shown
	// versions is not a pre-release.
	v2SelectsLatest
	// v2SelectsAbsoluteLatest keeps a package's last shown version
	// (IsAbsoluteLatestVersion), which every package with one has.
	v2SelectsAbsoluteLatest
)

func badV2Query() error {
	return apierror.New(http.StatusBadRequest, "unsupported or invalid NuGet v2 query")
}

func parseV2Query(q url.Values, endpoint string) (v2Query, error) {
	out := v2Query{
		filter: filter{
			allowPrerelease: endpoint != "Search()",
			allowSemVer2:    semVer2(q.Get("semVerLevel")),
		},
		top: v2PageSize,
	}
	for name, values := range q {
		if len(values) != 1 {
			return out, badV2Query()
		}
		if strings.HasPrefix(name, "$") && name != "$filter" && name != "$orderby" &&
			name != "$skip" &&
			name != "$top" {
			return out, badV2Query()
		}
	}
	if order := q.Get("$orderby"); order != "" && order != "Id" {
		return out, badV2Query()
	}
	for _, item := range []struct {
		name  string
		value *int
	}{{"$skip", &out.skip}, {"$top", &out.top}} {
		if q.Has(item.name) {
			n, err := strconv.Atoi(q.Get(item.name))
			if err != nil || n < 0 {
				return out, badV2Query()
			}
			*item.value = n
		}
	}
	out.isLimited = q.Has("$top")
	if q.Has("includePrerelease") {
		out.filter.allowPrerelease = strings.EqualFold(q.Get("includePrerelease"), "true")
	}
	if endpoint == "FindPackagesById()" {
		id, ok := odataLiteral(q.Get("id"))
		if !ok {
			return out, badV2Query()
		}
		out.id = strings.ToLower(id)
	}
	if endpoint == "Search()" {
		term := q.Get("searchTerm")
		if strings.HasPrefix(term, "'") {
			var ok bool
			term, ok = odataLiteral(term)
			if !ok {
				return out, badV2Query()
			}
		}
		out.match.terms = strings.Fields(strings.ToLower(term))
	}
	pred, ok := parseV2Filter(q.Get("$filter"), endpoint == "Packages" || endpoint == "Packages()")
	if !ok {
		return out, badV2Query()
	}
	out.predicate = pred
	// Only the whole expression is matched: a latest flag in parentheses
	// or beside another term is left to the predicate.
	switch strings.TrimSpace(q.Get("$filter")) {
	case "":
		out.selects = v2SelectsAll
	case "IsLatestVersion":
		out.selects = v2SelectsLatest
	case "IsAbsoluteLatestVersion":
		out.selects = v2SelectsAbsoluteLatest
	}
	return out, nil
}

func odataLiteral(s string) (string, bool) {
	if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
		return "", false
	}
	body := s[1 : len(s)-1]
	for i := 0; i < len(body); i++ {
		if body[i] == '\'' {
			i++
			if i == len(body) || body[i] != '\'' {
				return "", false
			}
		}
	}
	return strings.ReplaceAll(body, "''", "'"), true
}

func (h handler) v2Feed(w http.ResponseWriter, r *http.Request, endpoint string, count bool) {
	// FindPackagesById() is what restore reads; Packages() and Search()
	// list and search, which keep their answers.
	if endpoint == "FindPackagesById()" && h.blackedOut(w, r) {
		return
	}
	q, err := parseV2Query(r.URL.Query(), endpoint)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	var (
		entries []atomEntry
		total   int
	)
	if endpoint != "FindPackagesById()" && len(q.match.terms) == 0 &&
		q.selects != v2SelectsOther && h.k.CanAll(r, "", api.ActionRead) {
		entries, total, err = h.v2Stored(r, q, count)
	} else {
		entries, total, err = h.v2Scan(r, endpoint, q, count)
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if count {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, total) //nolint:gosec // G705: a number, as text/plain
		return
	}
	feed := atomFeed{
		XMLNS:   atomNS,
		ID:      v2Base(r) + "/" + endpoint,
		Title:   "Packages",
		Entries: entries,
	}
	// $top is a total result limit. Preserve its remaining allowance across
	// server pages rather than restarting it at every next link.
	if n := len(entries); n > 0 && total-q.skip > n && (!q.isLimited || q.top > n) {
		values := r.URL.Query()
		values.Set("$skip", strconv.Itoa(q.skip+n))
		if q.isLimited {
			values.Set("$top", strconv.Itoa(q.top-n))
		}
		feed.Links = []atomLink{
			{Rel: "next", Href: v2Base(r) + "/" + endpoint + "?" + values.Encode()},
		}
	}
	writeXML(w, "application/atom+xml;type=feed", feed)
}

// v2Scan is v2Feed's count of the versions q keeps and its page of them,
// over every package the caller may read. It retains one page, not the
// whole repository. Count and paging are applied after permission, SemVer
// and latest filters, so they reveal no hidden files.
func (h handler) v2Scan(
	r *http.Request,
	endpoint string,
	q v2Query,
	count bool,
) ([]atomEntry, int, error) {
	entries := make([]atomEntry, 0, v2PageSize)
	total, picked := 0, 0
	take := min(q.top, v2PageSize)
	packages := scan{terms: q.match.terms}
	if endpoint == "FindPackagesById()" {
		packages = scan{prefix: q.id, exact: true}
	}
	err := h.eachPackage(
		r,
		packages,
		q.filter,
		func(p pkg) error {
			if !q.match.matches(p.newest().meta) {
				return nil
			}
			selected := func(v v2Version) bool {
				if !q.predicate(v) {
					return false
				}
				total++
				if count || total <= q.skip || picked >= take {
					return false
				}
				picked++
				return true
			}
			found, err := h.v2Entries(r, p, selected)
			if err != nil {
				return err
			}
			entries = append(entries, found...)
			return nil
		},
	)
	return entries, total, err
}

// v2Stored is v2Scan for a caller who may read every path of the
// repository (#1425), over Packages() or Search() without a search term,
// whose $filter the store applies (v2Selects). That caller sees each
// version whose .nupkg is stored, so the store counts the versions q
// shows, or for a latest flag the packages with one, and pages them
// (port.NuGetShown); the page's packages are then read alone, each with
// all its versions, which its latest flags and download count are taken
// over. The answer is v2Scan's, unless a version's stored metadata is JSON
// that does not decode as a version's, as for pageAll.
func (h handler) v2Stored(r *http.Request, q v2Query, count bool) ([]atomEntry, int, error) {
	f := q.filter
	shown := port.PackageRange{
		RepoKey: pkgroute.RequestFrom(r).Repo.Key,
		Shown:   &port.NuGetShown{Prerelease: f.allowPrerelease, SemVer2: f.allowSemVer2},
	}
	take := min(q.top, v2PageSize)
	if count {
		take = 0
	}
	var (
		total int
		found []port.SearchCandidate
		// keep is the index keys of the versions the page holds of each of
		// its packages; nil keeps those q.predicate keeps.
		keep map[int64][]string
		err  error
	)
	if q.selects == v2SelectsAll {
		total, found, keep, err = h.v2Versions(r, shown, q.skip, take)
	} else {
		// A package's last release is shown when any of its releases is,
		// whatever pre-releases are.
		if q.selects == v2SelectsLatest {
			shown.Shown.Prerelease = false
		}
		total, found, err = h.v2Packages(r, shown, q.skip, take)
	}
	if err != nil || take == 0 {
		return nil, total, err
	}
	entries := make([]atomEntry, 0, take)
	for _, c := range found {
		p, ok, err := h.visible(r, c.Package, c.Versions)
		if err != nil {
			return nil, 0, err
		}
		// Left with none only once its versions changed since the page
		// was read: the page is one short.
		if p = p.only(f.shows); !ok || len(p.versions) == 0 {
			continue
		}
		selected := q.predicate
		if keep != nil {
			on := map[string]bool{}
			for _, key := range keep[c.Package.ID] {
				if v, ok := p.find(key); ok {
					on[v.meta.Version] = true
				}
			}
			selected = func(v v2Version) bool { return on[v.meta.Version] }
		}
		page, err := h.v2Entries(r, p, selected)
		if err != nil {
			return nil, 0, err
		}
		entries = append(entries, page...)
	}
	return entries, total, nil
}

// v2Versions counts the versions of shown and reads those at skip of take,
// with each of their packages and its versions, and which of them the page
// holds, by index key; take 0 reads only the count.
func (h handler) v2Versions(
	r *http.Request,
	shown port.PackageRange,
	skip, take int,
) (int, []port.SearchCandidate, map[int64][]string, error) {
	ctx := r.Context()
	st := h.k.DB().Packages()
	total, err := st.CountRangeVersions(ctx, shown)
	if err != nil || take == 0 {
		return total, nil, nil, v2StoreError(shown, err)
	}
	page, err := st.RangeVersionsAt(ctx, shown, skip, take)
	if err != nil {
		return 0, nil, nil, v2StoreError(shown, err)
	}
	keep := map[int64][]string{}
	var packages []model.Package
	for _, v := range page {
		if _, ok := keep[v.PackageID]; !ok {
			// Candidates reads a package by its ID, and the feed takes
			// its name alone.
			packages = append(packages, model.Package{
				ID: v.PackageID, RepoKey: shown.RepoKey, Name: v.Package,
			})
		}
		keep[v.PackageID] = append(keep[v.PackageID], v.Version)
	}
	found, err := st.Candidates(ctx, packages)
	return total, found, keep, v2StoreError(shown, err)
}

// v2Packages counts the packages of shown and reads those at skip of take,
// each with its versions; take 0 reads only the count. take is at most
// v2PageSize, so one read of the store's holds the page.
func (h handler) v2Packages(
	r *http.Request,
	shown port.PackageRange,
	skip, take int,
) (int, []port.SearchCandidate, error) {
	ctx := r.Context()
	st := h.k.DB().Packages()
	total, err := st.CountPackages(ctx, shown)
	if err != nil || take == 0 {
		return total, nil, v2StoreError(shown, err)
	}
	found, _, err := port.CandidatesAt(ctx, st, shown, skip, take)
	return total, found, v2StoreError(shown, err)
}

// v2StoreError is err, from a read of shown's packages, naming them.
func v2StoreError(shown port.PackageRange, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("nuget: packages of %s: %w", shown.RepoKey, err)
}
