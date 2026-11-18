package nugethttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	nugetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nuget"
)

// Keep the graph bound meaningful even for max-int skip. A remote session has
// 128 metadata reads, one of which discovers its resources; each search page
// has at most 100 hits. Local fan-out uses the same ceiling and never allocates
// from a caller's skip. totalHits describes only this bounded observation [I].
const virtualSearchLimit = (remoteGraphLimit - 1) * 100

func searchBudget(at page) int {
	if at.skip >= virtualSearchLimit-at.take {
		return virtualSearchLimit
	}
	return at.skip + at.take
}

var errSearchWindow = errors.New("NuGet member search window complete")

func searchFilter(r *http.Request) filter {
	q := r.URL.Query()
	return filter{allowPrerelease: strings.EqualFold(q.Get("prerelease"), "true"), allowSemVer2: semVer2(q.Get("semVerLevel"))}
}

func (h handler) localSearch(r, render *http.Request, autocomplete bool, budget int, allowed func(string, string) (bool, error)) ([]virtualPackage, error) {
	out := []virtualPackage{}
	if budget == 0 {
		return out, nil
	}
	q := r.URL.Query()
	f := searchFilter(r)
	match := match{terms: strings.Fields(strings.ToLower(q.Get("q"))), packageType: strings.TrimSpace(q.Get("packageType"))}
	s := scan{terms: match.terms}
	if autocomplete {
		s = scan{prefix: strings.ToLower(q.Get("q"))}
		if q.Has("id") {
			s = scan{prefix: nugetfmt.PackageName(q.Get("id")), exact: true}
		}
	}
	hv := hives[0]
	if f.allowSemVer2 {
		hv = hives[1]
	}
	visited := 0
	add := func(p pkg) error {
		visited++
		if visited > virtualSearchLimit {
			return errSearchWindow
		}
		contribution, err := h.localSearchPackage(r, render, p, autocomplete, match, hv, allowed)
		if err != nil {
			return err
		}
		if len(contribution.Versions) == 0 {
			return nil
		}
		if autocomplete && q.Has("id") && len(contribution.Versions) > budget {
			contribution.Versions = contribution.Versions[:budget]
		}
		out = append(out, contribution)
		if len(out) >= budget || autocomplete && q.Has("id") {
			return errSearchWindow
		}
		return nil
	}
	// pageAll avoids scanning a whole unfiltered catalog. Policy filtering needs
	// additional candidate windows, but no more than budget hits are admitted.
	var err error
	if !autocomplete && len(match.terms) == 0 && match.packageType == "" && h.k.CanAll(r, "", api.ActionRead) || autocomplete && !q.Has("id") && h.k.CanAll(r, "", api.ActionRead) {
		prefix := ""
		if autocomplete {
			prefix = s.prefix
		}
		for offset := 0; offset < virtualSearchLimit && len(out) < budget; {
			n := min(budget-len(out), listPage, virtualSearchLimit-offset)
			total, readErr := h.pageAll(r, prefix, f, page{skip: offset, take: n}, add)
			if errors.Is(readErr, errSearchWindow) {
				break
			}
			if readErr != nil {
				err = readErr
				break
			}
			offset += n
			if offset >= total {
				break
			}
		}
	} else {
		err = h.eachPackage(r, s, f, add)
	}
	if errors.Is(err, errSearchWindow) {
		err = nil
	}
	return out, err
}

func (h handler) localSearchPackage(r, render *http.Request, p pkg, autocomplete bool, match match, hv hive, allowed func(string, string) (bool, error)) (virtualPackage, error) {
	key := pkgroute.RequestFrom(r).Repo.Key
	visible := p.versions[:0]
	for _, v := range p.versions {
		if generic.CheckDownload(pkgroute.RequestFrom(r).Repo, v.meta.Path, false) != nil {
			continue
		}
		ok, err := allowed(v.meta.Path, key)
		if err != nil {
			return virtualPackage{}, err
		}
		if ok {
			visible = append(visible, v)
		}
	}
	p.versions = visible
	if len(p.versions) == 0 || !autocomplete && !match.matches(p.newest().meta) {
		return virtualPackage{}, nil
	}
	result, err := h.result(r, urlsOf(render, hv), p)
	if err != nil {
		return virtualPackage{}, err
	}
	doc := objectFrom(encode(result))
	vs := arrayField(doc, "versions")
	contribution := virtualPackage{ID: p.name}
	for i, v := range p.versions {
		contribution.Versions = append(
			contribution.Versions,
			nugetfmt.Contribution[virtualVersion]{Version: v.pv.Version, Path: v.meta.Path, Value: virtualVersion{member: r, local: v, search: doc, searchVersion: vs[i]}},
		)
	}
	return contribution, nil
}

func (h handler) remoteSearch(r *http.Request, autocomplete bool, budget int, allowed func(string, string) (bool, error)) ([]virtualPackage, error) {
	out := []virtualPackage{}
	if budget == 0 {
		return out, nil
	}
	s := &remoteSession{virtual: true, h: h, r: r, disclosed: map[string]source.Result{}}
	if err := s.discover(); err != nil {
		return nil, err
	}
	route, kind := "query", "search"
	id := ""
	if autocomplete {
		route, kind = "autocomplete", "autocomplete-id"
		if r.URL.Query().Has("id") {
			id = nugetfmt.PackageName(r.URL.Query().Get("id"))
			if !nugetfmt.ValidID(id) {
				return out, notFound(r)
			}
			kind = "autocomplete-version"
		}
	}
	key := pkgroute.RequestFrom(r).Repo.Key
	admit := func(id, version string, result source.Result) (bool, error) {
		if !s.allowed(id, version, result) {
			return false, nil
		}
		path := remotePackagePath(id, version)
		if generic.CheckDownload(pkgroute.RequestFrom(r).Repo, path, false) != nil {
			return false, nil
		}
		return allowed(path, key)
	}
	for offset := 0; offset < budget; {
		n := min(budget-offset, 100)
		if autocomplete && id == "" {
			n = min(n, remoteGraphLimit-s.calls-1)
		}
		if n <= 0 || s.calls >= remoteGraphLimit {
			break
		}
		request := r.Clone(r.Context())
		u := *r.URL
		request.URL = &u
		q := u.Query()
		q.Set("skip", strconv.Itoa(offset))
		q.Set("take", strconv.Itoa(n))
		u.RawQuery = q.Encode()
		s.r = request
		target, err := s.queryTarget(route)
		if err != nil {
			return out, err
		}
		d, result, err := s.document(target, ".nuget/"+route+"/"+shortHash(target)+".json", "", kind, id)
		s.r = r
		if err != nil {
			return out, err
		}
		received := 0
		var hits []virtualPackage
		if autocomplete {
			hits, received, err = s.virtualAutocompletePage(d, result, id, n, admit)
		} else {
			hits, received, err = s.virtualQueryPage(d, result, n, admit)
		}
		if err != nil {
			return out, err
		}
		out = append(out, hits...)

		offset += n
		if received < n {
			break
		}
	}
	return out, nil
}

func (h handler) virtualSearch(w http.ResponseWriter, r *http.Request, autocomplete bool) {
	at := pageOf(r.URL.Query())
	budget := searchBudget(at)
	members, err := h.k.MergeMembers(r, "")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	allowed := h.virtualAllowed(r)
	parts := []virtual.Part[[]virtualPackage]{}
	for _, mr := range members {
		var hits []virtualPackage
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			hits, err = h.remoteSearch(mr, autocomplete, budget, allowed)
		} else {
			hits, err = h.localSearch(mr, r, autocomplete, budget, allowed)
		}
		if nugetPassedOver(err) {
			h.k.WarnPassedOver(mr, err)
			continue
		}
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		parts = append(parts, virtual.Part[[]virtualPackage]{Member: pkgroute.RequestFrom(mr).Repo, Doc: hits})
	}
	merged, err := (nugetfmt.SearchMerger[virtualVersion]{}).Merge(r.Context(), parts)
	if err != nil {
		h.k.Error(w, r, fmt.Errorf("nuget virtual search: %w", err))
		return
	}
	if autocomplete {
		values := []string{}
		if r.URL.Query().Has("id") {
			if len(merged) > 0 {
				for _, v := range merged[0].Versions {
					values = append(values, v.Version)
				}
			}
		} else {
			for _, p := range merged {
				values = append(values, p.ID)
			}
		}
		total := len(values)
		values = values[min(at.skip, total):min(min(at.skip, total)+at.take, total)]
		for _, p := range merged {
			if err := h.fenceVirtual(r, p); err != nil {
				h.k.Error(w, r, err)
				return
			}
		}
		writeJSON(w, autocompleteAnswer{TotalHits: total, Data: values})
		return
	}
	total := len(merged)
	window := merged[min(at.skip, total):min(min(at.skip, total)+at.take, total)]
	data := []remoteDoc{}
	hv := hives[0]
	if searchFilter(r).allowSemVer2 {
		hv = hives[1]
	}
	u := urlsOf(r, hv)
	for _, p := range window {
		if err := h.fenceVirtual(r, p); err != nil {
			h.k.Error(w, r, err)
			return
		}
		entry := maps.Clone(p.Versions[len(p.Versions)-1].Value.search)
		versions := []remoteDoc{}
		for _, v := range p.Versions {
			d := maps.Clone(v.Value.searchVersion)
			setField(d, "@id", u.leaf(p.ID, v.Version))
			versions = append(versions, d)
		}
		setField(entry, "version", p.Versions[len(p.Versions)-1].Version)
		setField(entry, "versions", versions)
		setField(entry, "registration", u.index(p.ID))
		setField(entry, "@id", u.index(p.ID))
		delete(entry, "totalDownloads")
		data = append(data, entry)
	}
	writeJSON(w, map[string]any{"totalHits": total, "data": data})
}

func (s *remoteSession) virtualAutocompletePage(
	d remoteDoc,
	result source.Result,
	id string,
	n int,
	admit func(string, string, source.Result) (bool, error),
) ([]virtualPackage, int, error) {
	out := []virtualPackage{}
	r := s.r

	var values []string
	_ = json.Unmarshal(d["data"], &values)
	received := len(values)
	for _, value := range values[:min(n, len(values))] {
		if id != "" {
			ok, err := admit(id, value, result)
			if err != nil {
				return out, received, err
			}
			if !ok {
				continue
			}
			vkey, _ := versionKey(value)
			out = append(
				out,
				virtualPackage{
					ID: id,
					Versions: []nugetfmt.Contribution[virtualVersion]{
						{Version: vkey, Path: remotePackagePath(id, vkey), Value: virtualVersion{member: r, session: s, remote: remoteVersion{key: vkey, result: result}}},
					},
				},
			)
			continue
		}
		name := nugetfmt.PackageName(value)
		if !s.h.k.CanFolder(r, name, api.ActionRead) {
			continue
		}
		versions, owner, err := s.versions(name)
		if nugetPassedOver(err) {
			continue
		}
		if err != nil {
			return out, received, err
		}
		p := virtualPackage{ID: name}
		for _, v := range versions {
			ok, err := admit(name, v, owner)
			if err != nil {
				return out, received, err
			}
			if !ok {
				continue
			}
			vkey, _ := versionKey(v)
			p.Versions = append(
				p.Versions,
				nugetfmt.Contribution[virtualVersion]{
					Version: vkey,
					Path:    remotePackagePath(name, vkey),
					Value:   virtualVersion{member: r, session: s, remote: remoteVersion{key: vkey, result: owner}},
				},
			)
		}
		if len(p.Versions) > 0 {
			out = append(out, p)
		}
	}
	return out, received, nil
}

func (s *remoteSession) virtualQueryPage(d remoteDoc, result source.Result, n int, admit func(string, string, source.Result) (bool, error)) ([]virtualPackage, int, error) {
	out := []virtualPackage{}
	r := s.r

	data := arrayField(d, "data")
	received := len(data)
	for _, entry := range data[:min(n, len(data))] {
		name := nugetfmt.PackageName(stringField(entry, "id"))
		version := stringField(entry, "version")
		if !listedEntry(entry) || !s.searchShows(entry) {
			continue
		}
		ok, err := admit(name, version, result)
		if err != nil {
			return out, received, err
		}
		if !ok {
			continue
		}
		p := virtualPackage{ID: name}
		for _, v := range arrayField(entry, "versions") {
			version := stringField(v, "version")
			if !s.searchShows(v) {
				continue
			}
			ok, err := admit(name, version, result)
			if err != nil {
				return out, received, err
			}
			if !ok {
				continue
			}
			vkey, _ := versionKey(version)
			p.Versions = append(
				p.Versions,
				nugetfmt.Contribution[virtualVersion]{
					Version: vkey,
					Path:    remotePackagePath(name, vkey),
					Value:   virtualVersion{member: r, session: s, search: entry, searchVersion: v, remote: remoteVersion{key: vkey, result: result}},
				},
			)
		}
		if len(p.Versions) > 0 {
			out = append(out, p)
		}
	}
	return out, received, nil
}
