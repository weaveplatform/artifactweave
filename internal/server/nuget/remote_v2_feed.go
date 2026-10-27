package nuget

import (
	"cmp"
	"encoding/xml"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	nugetfmt "github.com/weaveplatform/artifactweave/internal/format/nuget"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type remoteV2Version struct {
	entry  remoteAtomEntry
	result source.Result
}

// Follow only validated upstream continuations. Client pagination is applied
// after permissions and filters, so upstream counts/offsets cannot reveal hidden versions.
func (s *remoteSession) legacyCollect(target, id string) ([]remoteV2Version, error) {
	c := pkgroute.RequestFrom(s.r).Repo
	seenURLs, seenVersions := map[string]bool{}, map[string]bool{}
	var out []remoteV2Version
	for target != "" {
		if seenURLs[target] || len(seenURLs) >= 100 {
			return nil, remoteFailure()
		}
		seenURLs[target] = true
		doc, result, base, err := s.xmlDocument(target, id, "feed")
		if err != nil {
			return nil, err
		}
		page, err := parseRemoteAtom(c, doc, base, id)
		if err != nil {
			return nil, err
		}
		for _, entry := range page.entries {
			key := nugetfmt.PackageName(entry.meta.ID) + "/" + entry.meta.Version
			if seenVersions[key] || len(out) >= 10000 {
				return nil, remoteFailure()
			}
			seenVersions[key] = true
			out = append(out, remoteV2Version{entry: entry, result: result})
		}
		target = page.next
	}
	slices.SortFunc(out, func(a, b remoteV2Version) int {
		return cmp.Or(
			strings.Compare(
				nugetfmt.PackageName(a.entry.meta.ID),
				nugetfmt.PackageName(b.entry.meta.ID),
			),
			types.CompareNuGet(a.entry.meta.Version, b.entry.meta.Version),
		)
	})
	return out, nil
}

func (s *remoteSession) legacyEntries(id string) ([]remoteV2Version, error) {
	target, err := s.legacyTarget(
		"FindPackagesById()",
		url.Values{"id": {"'" + id + "'"}, "semVerLevel": {"2.0.0"}},
	)
	if err != nil {
		return nil, err
	}
	return s.legacyCollect(target, id)
}

func (s *remoteSession) legacyEntry(w http.ResponseWriter, id, ver string) {
	key, valid := versionKey(ver)
	if !valid || !nugetfmt.ValidID(id) {
		s.h.k.Error(w, s.r, notFound(s.r))
		return
	}
	id = nugetfmt.PackageName(id)
	entries, err := s.legacyEntries(id)
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	for _, e := range entries {
		if e.entry.meta.Version == key && s.allowed(id, key, e.result) {
			s.xml(w, s.rewriteAtom(e.entry), "application/atom+xml;type=entry")
			return
		}
	}
	s.h.k.Error(w, s.r, notFound(s.r))
}

func (s *remoteSession) legacyFeed(w http.ResponseWriter, endpoint string, count bool) {
	q, err := parseV2Query(s.r.URL.Query(), endpoint)
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	if endpoint == "FindPackagesById()" && !nugetfmt.ValidID(q.id) {
		s.h.k.Error(w, s.r, badV2Query())
		return
	}
	query := s.r.URL.Query()
	// The retained grammar is evaluated over the permission-filtered collection.
	// Upstream paging and latest-only filtering cannot decide a caller's visible latest version.
	query.Del("$skip")
	query.Del("$top")
	query.Del("$filter")
	if q.id != "" {
		query.Set("id", "'"+q.id+"'")
	}
	target, err := s.legacyTarget(endpoint, query)
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	all, err := s.legacyCollect(target, q.id)
	if err != nil {
		s.h.k.Error(w, s.r, err)
		return
	}
	visible := make([]remoteV2Version, 0, len(all))
	latest, absolute := map[string]string{}, map[string]string{}
	for _, e := range all {
		m := e.entry.meta
		id := nugetfmt.PackageName(m.ID)
		if !q.filter.shows(m) || (endpoint != "FindPackagesById()" && !remoteV2Listed(e.entry)) ||
			!s.allowed(id, m.Version, e.result) {
			continue
		}
		visible = append(visible, e)
		absolute[id] = m.Version
		if !m.IsPrerelease() {
			latest[id] = m.Version
		}
	}
	entries := make([]atomEntry, 0, v2PageSize)
	total := 0
	for _, e := range visible {
		m := e.entry.meta
		id := nugetfmt.PackageName(m.ID)
		vv := v2Version{
			meta:             m,
			isLatest:         latest[id] == m.Version,
			isAbsoluteLatest: absolute[id] == m.Version,
		}
		if !q.predicate(vv) || !q.match.matches(m) {
			continue
		}
		total++
		if count || total <= q.skip || len(entries) >= min(q.top, v2PageSize) {
			continue
		}
		e.entry.values["IsLatestVersion"] = strconv.FormatBool(vv.isLatest)
		e.entry.values["IsAbsoluteLatestVersion"] = strconv.FormatBool(vv.isAbsoluteLatest)
		entries = append(entries, s.rewriteAtom(e.entry))
	}
	w.Header().Set("DataServiceVersion", "2.0")
	if count {
		s.bytesResponse(w, []byte(strconv.Itoa(total)), "text/plain")
		return
	}
	feed := atomFeed{
		XMLNS:   atomNS,
		ID:      v2Base(s.r) + "/" + endpoint,
		Title:   "Packages",
		Entries: entries,
	}
	if n := len(entries); n > 0 && total-q.skip > n && (!q.isLimited || q.top > n) {
		values := s.r.URL.Query()
		values.Set("$skip", strconv.Itoa(q.skip+n))
		if q.isLimited {
			values.Set("$top", strconv.Itoa(q.top-n))
		}
		feed.Links = []atomLink{
			{Rel: "next", Href: v2Base(s.r) + "/" + endpoint + "?" + values.Encode()},
		}
	}
	s.xml(w, feed, "application/atom+xml;type=feed")
}

func remoteV2Listed(e remoteAtomEntry) bool {
	if strings.EqualFold(e.values["Listed"], "false") {
		return false
	}
	published := e.values["Published"]
	return len(published) < 4 || published[:4] > "1901"
}

func (s *remoteSession) rewriteAtom(e remoteAtomEntry) atomEntry {
	out := e.atom
	id, key := e.meta.ID, e.meta.Version
	out.ID = v2Base(
		s.r,
	) + "/Packages(Id='" + url.PathEscape(
		id,
	) + "',Version='" + url.PathEscape(
		key,
	) + "')"
	out.Content = atomContent{
		Type: "application/zip",
		Src:  v2Base(s.r) + "/Download/" + url.PathEscape(id) + "/" + url.PathEscape(key),
	}
	e.values["Id"], e.values["NormalizedVersion"] = id, key
	delete(e.values, "DownloadCount")
	names := make([]string, 0, len(e.values))
	for name := range e.values {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		out.Properties.Items = append(
			out.Properties.Items,
			atomProperty{
				XMLName: xml.Name{Local: "d:" + name},
				Type:    v2PropertyTypes[name],
				Value:   e.values[name],
			},
		)
	}
	return out
}
