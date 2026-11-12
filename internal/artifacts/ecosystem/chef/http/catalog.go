package chefhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	cheffmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/chef"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const (
	// pageLimit is the most cookbooks a catalog or search page lists. A
	// larger items is clamped to it rather than refused: Knife 19.0.134's
	// supermarket list and search, which knife-art's artifactory list and
	// search subclass unchanged, ask items=9999999 "to avoid pagination"
	// (chef/knife supermarket_list.rb and supermarket_search.rb), and
	// Supermarket clamps items to its API_ITEM_LIMIT the same way
	// (chef/supermarket v1_controller.rb, init_params). The JFrog Chef guide
	// documents both commands, so 7.x takes that items; the page it then
	// serves is not documented [I] (#1305).
	pageLimit = 1000
	// maxPosition bounds start where a catalog holds an upstream's, and an
	// upstream's total. Such a catalog has no bound of its own: the public
	// Supermarket held 4,078 cookbooks on 2026-11-12, 18 short of
	// maxVersions, which bounds only what the registry holds (#1306).
	maxPosition = 1 << 30
)

// catalogKeys are the query keys the catalog and search take. Knife adds
// order for --sort-by and user for --owned-by (chef/knife
// supermarket_list.rb, v19.0.134). They are taken and change nothing:
// Artifactory has no Supermarket owners or download ranking to apply them
// to, and its REST resources ignore a parameter they do not read [I]
// (#1305). Any other key is still refused.
var catalogKeys = map[string]bool{
	"start": true, "items": true, "q": true, "order": true, "user": true,
}

// entry is a cookbook a catalog or search page lists.
type entry struct{ name, description, maintainer string }

func (e entry) document(r *http.Request) map[string]any {
	return map[string]any{
		"cookbook_name":        e.name,
		"cookbook_description": e.description,
		"cookbook_maintainer":  e.maintainer,
		"cookbook":             base(r) + "/api/v1/cookbooks/" + e.name,
	}
}

// upstreamPart is what one remote's upstream adds to a catalog or search
// page: the cookbooks at the offsets the page covers that the caller may
// read, the upstream's total, and the authority of each page it was read
// from, which is checked again just before the answer is written.
type upstreamPart struct {
	entries []entry
	total   int
	fences  []func(context.Context) error
}

// catalog answers the catalog (api/v1/cookbooks) and search (api/v1/search,
// or q on either route). A page lists, from start, the cookbooks the
// repository's index holds, by name, and then each remote upstream's
// answer, in the upstream's order, leaving out a cookbook already listed:
// 7.x searches a remote's cache, then forwards the query to the upstream
// and merges the results, and searches a virtual repository's local
// members, then its remote members, and merges those (JFrog, "Search for
// Chef Cookbooks"). So a remote's search lists its cached cookbooks, then
// its upstream's search; a remote's catalog, which 7.x does not document,
// lists its upstream's catalog alone, as it did before [I]; and a virtual
// repository lists its hosted members' cookbooks, and on a search its
// remote members' cached ones, then each remote member's upstream in
// member order. start counts the upstream's own offsets after the indexed
// cookbooks, so a cookbook left out leaves its place empty rather than
// moving the next page, and total adds the upstream's total. No upstream
// cookbook record is read: a remote over the public Supermarket answers
// from one search page, or the few catalog pages the page covers, rather
// than from every cookbook and version record (#1306).
func (h handler) catalog(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		h.k.Error(w, r, apierror.New(400, "invalid search query"))
		return
	}
	for key, values := range q {
		if len(values) != 1 || !catalogKeys[key] {
			h.k.Error(w, r, apierror.New(400, "unsupported search query"))
			return
		}
	}
	start, limit := 0, pageLimit
	if q.Has("start") {
		start, err = strconv.Atoi(q.Get("start"))
	}
	if err == nil && q.Has("items") {
		limit, err = strconv.Atoi(q.Get("items"))
	}
	query := q.Get("q")
	bounded := err == nil && start >= 0 && limit >= 1 && len(query) <= 128
	if bounded && start > maxVersions {
		last, e := h.lastStart(r)
		if e != nil {
			h.k.Error(w, r, e)
			return
		}
		bounded = start <= last
	}
	if !bounded {
		h.k.Error(w, r, apierror.New(400, "invalid search bounds"))
		return
	}
	limit = min(limit, pageLimit)
	if !h.k.RequireFolder(w, r, "", api.ActionRead) {
		return
	}
	listed, checks, err := h.listed(r, query)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	upstreams, err := h.upstreams(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	items := []map[string]any{}
	held := map[string]bool{}
	for _, e := range listed {
		held[e.name] = true
	}
	for _, e := range listed[min(start, len(listed)):min(start+limit, len(listed))] {
		items = append(items, e.document(r))
	}
	total := len(listed)
	var fences []func(context.Context) error
	for _, mr := range upstreams {
		part, e := h.upstreamPart(r, mr, query, max(start-total, 0),
			max(start+limit-max(start, total), 0))
		if e != nil {
			h.k.Error(w, r, e)
			return
		}
		for _, entry := range part.entries {
			if !held[entry.name] {
				held[entry.name] = true
				items = append(items, entry.document(r))
			}
		}
		total += part.total
		fences = append(fences, part.fences...)
	}
	if err = h.authority(r, checks); err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, fence := range fences {
		if err = fence(r.Context()); err != nil {
			h.k.Error(
				w,
				r,
				fmt.Errorf("chef: catalog authority: %w", pkgroute.SourceError(err, false)),
			)
			return
		}
	}
	writeJSON(w, r, 200, map[string]any{"total": total, "start": start, "items": items})
}

// lastStart is the highest start a catalog takes: one past what a hosted
// catalog can hold, as before, unless the catalog holds an upstream's,
// whose pages a client must reach past maxVersions: a remote's, or a
// virtual repository's with a remote member. A virtual repository of
// hosted members alone keeps the hosted bound, since #1306 changes only
// catalogs over an upstream. Membership is read from the configuration,
// not filtered by blackout or the caller, so the bound does not change with
// either.
func (h handler) lastStart(r *http.Request) (int, error) {
	c := pkgroute.RequestFrom(r).Repo
	switch c.Class {
	case repo.Remote:
		return maxPosition, nil
	case repo.Virtual:
		for _, key := range c.Repositories {
			member, err := h.k.DB().Repos().Get(r.Context(), key)
			if errors.Is(err, kernel.ErrNotFound) {
				continue
			}
			if err != nil {
				return 0, fmt.Errorf("chef: member: %w", err)
			}
			if member.Class == repo.Remote {
				return maxPosition, nil
			}
		}
	}
	return maxVersions, nil
}

// listed returns the cookbooks the repository's index holds that answer
// query and that the caller may read, by name, and the releases they were
// built from, whose authority is checked before the answer is written.
func (h handler) listed(r *http.Request, query string) ([]entry, []release, error) {
	search := query != ""
	names, err := h.names(r, search)
	if err != nil {
		return nil, nil, err
	}
	out := []entry{}
	checks := []release{}
	for _, name := range names {
		if !h.k.CanFolder(r, name, api.ActionRead) {
			continue
		}
		found, err := h.catalogLookup(r, name, search)
		if err != nil {
			return nil, nil, err
		}
		if len(found) == 0 {
			continue
		}
		m := found[0].meta.Manifest
		if !matches(query, name, m) {
			continue
		}
		checks = append(checks, found...)
		out = append(out, entry{name: name, description: m.Description, maintainer: m.Maintainer})
	}
	return out, checks, nil
}

// matches reports whether an indexed cookbook answers query. 7.x looks
// for a search's terms "in the name, description and maintainer fields"
// of a local repository's cookbooks (JFrog, "Search for Chef Cookbooks"),
// and the description and maintainer listed are those of the highest
// version the caller may read, so those are the ones searched. Knife sends
// its one query argument as q, which is matched whole, without case [I].
func matches(query, name string, m cheffmt.Manifest) bool {
	q := strings.ToLower(query)
	return strings.Contains(strings.ToLower(name), q) ||
		strings.Contains(strings.ToLower(m.Description), q) ||
		strings.Contains(strings.ToLower(m.Maintainer), q)
}

// names returns the cookbooks the repository's index holds: a hosted
// repository's own, a remote's cache when it searches, and the union of a
// virtual repository's members', by name.
func (h handler) names(r *http.Request, search bool) ([]string, error) {
	c := pkgroute.RequestFrom(r).Repo
	if c.Class == repo.Remote && !search {
		return nil, nil
	}
	if c.Class == repo.Virtual {
		members, err := h.members(r)
		if err != nil {
			return nil, err
		}
		unique := map[string]bool{}
		for _, mr := range members {
			names, err := h.names(mr, search)
			if err != nil {
				return nil, err
			}
			for _, n := range names {
				unique[n] = true
			}
			if len(unique) > maxVersions {
				return nil, apierror.New(503, "cookbook catalog exceeds limit")
			}
		}
		out := make([]string, 0, len(unique))
		for name := range unique {
			out = append(out, name)
		}
		slices.Sort(out)
		return out, nil
	}
	out := []string{}
	page := kernel.Page{Limit: kernel.MaxLimit}
	for {
		packages, next, err := h.k.DB().Packages().List(r.Context(), c.Key, page)
		if err != nil {
			return nil, fmt.Errorf("chef: catalog: %w", err)
		}
		for _, p := range packages {
			out = append(out, p.Name)
		}
		if len(out) > maxVersions {
			return nil, apierror.New(503, "cookbook catalog exceeds limit")
		}
		if next == "" {
			break
		}
		page.After = next
	}
	return out, nil
}

// catalogLookup is lookup for a catalog: a remote's versions are those its
// cache holds, and only when it searches, so a catalog never reads an
// upstream cookbook or version record.
func (h handler) catalogLookup(r *http.Request, name string, search bool) ([]release, error) {
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Remote:
		if !search {
			return nil, nil
		}
		return h.hosted(r, name)
	case repo.Virtual:
		return h.virtual(r, name, func(mr *http.Request) ([]release, error) {
			return h.catalogLookup(mr, name, search)
		})
	default:
		return h.hosted(r, name)
	}
}

// members returns a virtual repository's member requests, in order,
// leaving out a member that no longer exists, one that is blacked out,
// which 7.x ignores when it resolves (R5), so that a blacked-out remote
// member's refusal does not refuse the virtual repository's catalog, and
// one whose root the caller may not read.
func (h handler) members(r *http.Request) ([]*http.Request, error) {
	out := []*http.Request{}
	for _, key := range pkgroute.RequestFrom(r).Repo.Repositories {
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("chef: member: %w", err)
		}
		if generic.CheckBlackedOut(member, "") != nil {
			continue
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, fmt.Errorf("chef: member request: %w", err)
		}
		if h.k.CanFolder(mr, "", api.ActionRead) {
			out = append(out, mr)
		}
	}
	return out, nil
}

// upstreams returns the requests whose upstreams a catalog page reads: a
// remote's own, or a virtual repository's remote members', in order.
func (h handler) upstreams(r *http.Request) ([]*http.Request, error) {
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Remote:
		return []*http.Request{r}, nil
	case repo.Virtual:
		members, err := h.members(r)
		if err != nil {
			return nil, err
		}
		out := []*http.Request{}
		for _, mr := range members {
			if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
				out = append(out, mr)
			}
		}
		return out, nil
	default:
		return nil, nil
	}
}

// upstreamPart reads what remote mr's upstream answers at the offsets
// [from, from+room), keeping the cookbooks the caller may read through mr
// and through r. A search forwards the query once, at from: Supermarket's
// search total counts every cookbook, not the matches (chef/supermarket
// cookbooks_controller.rb, search), so no page says whether more matches
// follow it. A catalog reads its upstream upstreamPage cookbooks a page,
// from the page that holds from, until the offsets are covered or the
// catalog ends: at most 11 pages for a page of 1,000, and the page starts
// stay those of a whole read (#1298). Either way it reads at least one
// page, whose total the answer's total needs.
func (h handler) upstreamPart(
	r, mr *http.Request,
	query string,
	from, room int,
) (upstreamPart, error) {
	var part upstreamPart
	search := query != ""
	path := "/api/v1/cookbooks"
	if search {
		path = "/api/v1/search"
	}
	pos := from
	if !search {
		pos -= from % upstreamPage
	}
	seen := map[string]bool{}
	for {
		var doc catalogDocument
		var more bool
		values := url.Values{"start": {strconv.Itoa(pos)}, "items": {strconv.Itoa(upstreamPage)}}
		if search {
			values.Set("q", query)
		}
		offset := pos
		_, result, err := h.metadata(mr, path, "", values, func(data []byte) (e error) {
			doc, more, e = parseCatalog(data, offset, search)
			return e
		})
		if err != nil {
			return part, err
		}
		part.total = doc.Total
		part.fences = append(part.fences, result.Revalidate)
		for i, item := range doc.Items {
			// A cookbook named twice across the pages one answer reads is
			// refused, as a whole read refused it (#1298).
			if seen[item.Name] {
				return part, invalidUpstream()
			}
			seen[item.Name] = true
			if at := pos + i; at < from || at >= from+room ||
				!h.k.CanFolder(mr, item.Name, api.ActionRead) ||
				!h.k.CanFolder(r, item.Name, api.ActionRead) {
				continue
			}
			part.entries = append(part.entries, entry{
				name: item.Name, description: item.Description, maintainer: item.Maintainer,
			})
		}
		pos += len(doc.Items)
		if search || !more || pos >= from+room {
			return part, nil
		}
	}
}
