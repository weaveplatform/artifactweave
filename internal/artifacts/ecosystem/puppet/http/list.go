package puppethttp

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	puppetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// The whole-repository lists: v3/releases without module and v3/modules
// without query, 7.x's Get Puppet Releases and Get Puppet Modules ("a list
// of all Puppet releases [modules] hosted by the specified repository.
// Results are paginated."), which r10k and librarian-puppet read through
// the puppet_forge gem.
const (
	// defaultListLimit is the page a list answers when the request names
	// none: JFrog's sample answer for Get Puppet Modules and its release
	// list in RTFACT-14557 both page 20, as the Forge does.
	defaultListLimit = 20
	// maxListLimit is the largest page: 7.x lists at most 100 modules
	// whatever limit asks for (RTFACT-30529, closed with no change), and
	// the release list is taken to page the same way [I].
	maxListLimit = 100
	// maxListed bounds the releases one list reads from the package index,
	// as maxVersions bounds one module's and the modules a list reads.
	maxListed = 16 * maxVersions
)

// pagingOnly reports whether q asks for a whole-repository list: nothing
// but limit and offset, each at most once, which is all a list's own links
// carry. Any other query keeps the answer the one-module routes give it.
func pagingOnly(q url.Values) bool {
	for key, values := range q {
		if len(values) != 1 || key != "limit" && key != "offset" {
			return false
		}
	}
	return true
}

// listPage is a list's pagination object. The API reference's schema draws
// these fields at the top level of the answer, but JFrog's own sample
// answer for Get Puppet Modules and RTFACT-14557's release list nest them
// under "pagination", in the Forge's shape, and puppet_forge reads them
// only there: it pages through a whole list from first and follows next.
type listPage struct {
	Limit    int     `json:"limit"`
	Offset   int     `json:"offset"`
	First    string  `json:"first"`
	Previous *string `json:"previous"`
	Current  string  `json:"current"`
	Next     *string `json:"next"`
	Total    int     `json:"total"`
}

// newListPage returns the pagination of a page of route's list, which
// filter, when set, narrows to one module ("module=<m>" or "query=<m>").
// The links are Forge-relative, as 7.x's are
// ("/v3/releases?module=sql&limit=20&offset=0" in RTFACT-14557); the client
// resolves them against the repository's Forge URL. Each keeps the filter
// first, as 7.x's and the Forge's do.
func newListPage(route, filter string, limit, offset, total int) listPage {
	link := func(offset int) string {
		page := "limit=" + strconv.Itoa(limit) + "&offset=" + strconv.Itoa(offset)
		if filter != "" {
			page = filter + "&" + page
		}
		return route + "?" + page
	}
	p := listPage{
		Limit: limit, Offset: offset, Total: total,
		First: link(0), Current: link(offset),
	}
	if offset > 0 {
		previous := link(max(0, offset-limit))
		p.Previous = &previous
	}
	// total-offset rather than offset+limit, which an offset near the
	// largest int would overflow.
	if total-offset > limit {
		next := link(offset + limit)
		p.Next = &next
	}
	return p
}

// window returns the bounds of the page at offset of limit entries in a
// list of total.
func window(offset, limit, total int) (int, int) {
	start := min(offset, total)
	return start, start + min(limit, total-start)
}

// parsePage reads a list's limit and offset. A limit above maxListLimit is
// served as maxListLimit, as 7.x serves it (RTFACT-30529), rather than
// refused; what 7.x answers a limit below 1 or an offset below 0 is [I],
// and they take the one-module list's 400s.
func parsePage(q url.Values) (int, int, error) {
	limit, offset := defaultListLimit, 0
	var err error
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
	}
	if err != nil || limit < 1 {
		return 0, 0, apierror.New(http.StatusBadRequest, "invalid Puppet limit")
	}
	if q.Has("offset") {
		offset, err = strconv.Atoi(q.Get("offset"))
	}
	if err != nil || offset < 0 {
		return 0, 0, apierror.New(http.StatusBadRequest, "invalid Puppet offset")
	}
	return min(limit, maxListLimit), offset, nil
}

// listable answers a list request to a repository that does not serve the
// lists, and reports whether the request's repository serves them: local
// and federated repositories do. remoteMessage is the 400 the route gave
// before it served the lists.
func (h handler) listable(w http.ResponseWriter, r *http.Request, remoteMessage string) bool {
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Local, repo.Federated:
		return true
	case repo.Virtual:
		// A virtual repository answers the lists 404: the fix of
		// RTFACT-14028 (5.4.5) left "the root '/v3/modules' index"
		// unsupported on virtual repositories, RTFACT-15072 shows both
		// lists answering 404 there, and JFrog support called that the
		// intended behaviour ("this API call can only be served by local
		// or remote repositories"); the request to change it is still
		// open. Its body there, {"errors": ["404 Not found"] }, is 5.4.6's;
		// this route keeps the protocol's envelope with that text [I].
		// Read permission comes first, as on every route (DV-057).
		if h.k.RequireFolder(w, r, "", api.ActionRead) {
			h.k.Error(w, r, apierror.New(http.StatusNotFound, "404 Not found"))
		}
		return false
	default:
		// JFrog support says a remote repository serves the lists too
		// (RTFACT-15072), but no source shows what it lists, its
		// upstream's whole catalog or only what it has cached, nor how it
		// pages and filters them, so a remote keeps the answer it gave
		// before until P19 settles that.
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, remoteMessage))
		return false
	}
}

// listStart checks a list request in the order every Puppet route checks
// one: the request's own 400, then read permission, which needs only one
// readable path in the repository, as a list answers what the caller may
// read (403, or 401 to an anonymous caller), then the blackout (R2a). 7.x
// writes Puppet's listings as stored documents (RTFACT-26143 names an
// all.json beside each module's releases.json) at paths no source shows,
// so the blackout refuses a list naming the request's own path and no
// pattern refuses it, as for one module's list. It returns the page, and
// false once it has answered.
func (h handler) listStart(
	w http.ResponseWriter,
	r *http.Request,
	q url.Values,
	remoteMessage string,
) (int, int, bool) {
	if !h.listable(w, r, remoteMessage) {
		return 0, 0, false
	}
	limit, offset, err := parsePage(q)
	if err != nil {
		h.k.Error(w, r, err)
		return 0, 0, false
	}
	if !h.k.RequireFolder(w, r, "", api.ActionRead) {
		return 0, 0, false
	}
	if err = h.k.CheckBlackedOut(r, pkgroute.RequestFrom(r).Rest); err != nil {
		h.k.Error(w, r, fmt.Errorf("puppet: list: %w", err))
		return 0, 0, false
	}
	return limit, offset, true
}

// listReleases answers v3/releases without module: every release of the
// repository the caller may read, by module name and then newest first
// [I for the order]. Each release is the record the one-module list gives.
func (h handler) listReleases(w http.ResponseWriter, r *http.Request, q url.Values) {
	limit, offset, ok := h.listStart(w, r, q, "invalid Puppet module")
	if !ok {
		return
	}
	modules, total, err := h.catalog(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	type entry struct {
		id            int64
		name, version string
	}
	all := make([]entry, 0, total)
	for _, m := range modules {
		for _, v := range m.versions {
			all = append(all, entry{m.id, m.name, v})
		}
	}
	start, end := window(offset, limit, len(all))
	out := []releaseRecord{}
	for _, e := range all[start:end] {
		v, err := h.k.DB().Packages().Version(r.Context(), e.id, e.version)
		if errors.Is(err, kernel.ErrNotFound) {
			// Deleted since the list was counted: the page is one short
			// rather than failing.
			continue
		}
		if err != nil {
			h.k.Error(w, r, fmt.Errorf("puppet: package version: %w", err))
			return
		}
		rel, err := h.stored(r, e.name, v)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		out = append(out, record(rel))
	}
	writeJSON(w, r, http.StatusOK, struct {
		Pagination listPage        `json:"pagination"`
		Results    []releaseRecord `json:"results"`
	}{newListPage("/v3/releases", "", limit, offset, len(all)), out})
}

// listModules answers v3/modules without query: every module of the
// repository the caller may read a release of, by name [I for the order].
// Each module is the record the module search gives it, listing only the
// releases the caller may read.
func (h handler) listModules(w http.ResponseWriter, r *http.Request, q url.Values) {
	limit, offset, ok := h.listStart(w, r, q, "search requires an exact Puppet module name")
	if !ok {
		return
	}
	modules, _, err := h.catalog(r)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	start, end := window(offset, limit, len(modules))
	out := []moduleRecord{}
	for _, m := range modules[start:end] {
		releases, err := h.hosted(r, m.name)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		// Deleted since the list was counted, as listReleases passes over.
		if len(releases) == 0 {
			continue
		}
		out = append(out, newModuleRecord(releases, derivedModule(releases)))
	}
	writeJSON(w, r, http.StatusOK, struct {
		Pagination listPage       `json:"pagination"`
		Results    []moduleRecord `json:"results"`
	}{newListPage("/v3/modules", "", limit, offset, len(modules)), out})
}

// listed is a module of a whole-repository list with the versions of it
// the caller may read, newest first.
type listed struct {
	id       int64
	name     string
	versions []string
}

// catalog returns the modules of the request's repository that the caller
// may read a release of, in name order, and how many releases they list.
// It reads only the names of the package index's modules and versions
// (#1404); the releases a page answers are read, and checked against their
// archives, when they are answered (stored). Read permission alone
// filters, so a release whose archive the patterns reject stays listed
// (R3).
func (h handler) catalog(r *http.Request) ([]listed, int, error) {
	ctx := r.Context()
	key := pkgroute.RequestFrom(r).Repo.Key
	var out []listed
	scanned, total, read := 0, 0, 0
	for page := (kernel.Page{Limit: kernel.MaxLimit}); ; {
		packages, next, err := h.k.DB().Packages().List(ctx, key, page)
		if err != nil {
			return nil, 0, fmt.Errorf("puppet: list modules: %w", err)
		}
		if scanned += len(packages); scanned > maxVersions {
			return nil, 0, apierror.New(
				http.StatusServiceUnavailable,
				"Puppet modules exceed limit",
			)
		}
		for _, p := range packages {
			versions, err := h.k.DB().Packages().VersionNames(ctx, p.ID)
			if err != nil {
				return nil, 0, fmt.Errorf("puppet: package versions: %w", err)
			}
			if read += len(versions); len(versions) > maxVersions || read > maxListed {
				return nil, 0, apierror.New(
					http.StatusServiceUnavailable,
					"Puppet releases exceed limit",
				)
			}
			m := listed{id: p.ID, name: p.Name}
			for _, v := range versions {
				if h.k.Can(r, puppetfmt.Path(p.Name, v), api.ActionRead) {
					m.versions = append(m.versions, v)
				}
			}
			if len(m.versions) == 0 {
				continue
			}
			slices.SortFunc(m.versions, newestFirst)
			out = append(out, m)
			total += len(m.versions)
		}
		if next == "" {
			return out, total, nil
		}
		page.After = next
	}
}
