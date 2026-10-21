package nuget

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
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
	limited   bool
	predicate v2Predicate
}

func badV2Query() error {
	return apierror.New(http.StatusBadRequest, "unsupported or invalid NuGet v2 query")
}

func parseV2Query(q url.Values, endpoint string) (v2Query, error) {
	out := v2Query{
		filter: filter{prerelease: endpoint != "Search()", semver2: semVer2(q.Get("semVerLevel"))},
		top:    v2PageSize,
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
	out.limited = q.Has("$top")
	if q.Has("includePrerelease") {
		out.filter.prerelease = strings.EqualFold(q.Get("includePrerelease"), "true")
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
	q, err := parseV2Query(r.URL.Query(), endpoint)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	// Retain one page, not the whole repository. Count/paging are applied
	// after permission, SemVer and latest filters, so they reveal no hidden files.
	entries := make([]atomEntry, 0, v2PageSize)
	total, picked := 0, 0
	take := min(q.top, v2PageSize)
	err = h.eachPackage(
		r,
		func(name string) bool { return endpoint != "FindPackagesById()" || name == q.id },
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
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if count {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, total)
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
	if n := len(entries); n > 0 && total-q.skip > n && (!q.limited || q.top > n) {
		values := r.URL.Query()
		values.Set("$skip", strconv.Itoa(q.skip+n))
		if q.limited {
			values.Set("$top", strconv.Itoa(q.top-n))
		}
		feed.Links = []atomLink{
			{Rel: "next", Href: v2Base(r) + "/" + endpoint + "?" + values.Encode()},
		}
	}
	writeXML(w, "application/atom+xml;type=feed", feed)
}
