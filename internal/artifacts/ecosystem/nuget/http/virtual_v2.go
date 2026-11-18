package nugethttp

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	nugetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nuget"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type v2Owner struct {
	member  *http.Request
	meta    nugetfmt.Metadata
	local   version
	remote  remoteV2Version
	session *remoteSession
}
type v2Package = nugetfmt.Package[v2Owner]

func (h handler) virtualV2(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := pkgroute.RequestFrom(r)
		if q.Repo.Class != repo.Virtual {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.k.Error(
				w,
				r,
				apierror.New(http.StatusNotImplemented, fmt.Sprintf("'%s' is a virtual repository, which artifactweave does not serve through the nuget API yet", q.Repo.Key)),
			)
			return
		}
		parts, err := segments(r, "")
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		switch {
		case len(parts) == 0:
			writeV2Service(w, r)
		case len(parts) == 1 && parts[0] == "$metadata":
			writeV2Metadata(w)
		case len(parts) == 3 && parts[0] == "Download":
			h.virtualV2Download(w, r, parts[1], parts[2])
		case len(parts) == 1 && v2Key.MatchString(parts[0]):
			key := v2Key.FindStringSubmatch(parts[0])
			h.virtualV2Entry(w, r, strings.ReplaceAll(key[1], "''", "'"), strings.ReplaceAll(key[2], "''", "'"))
		default:
			count := len(parts) == 2 && parts[1] == "$count"
			if len(parts) != 1 && !count {
				h.k.Error(w, r, notFound(r))
				return
			}
			switch parts[0] {
			case "Packages", "Packages()", "Search()", "FindPackagesById()":
				h.virtualV2Feed(w, r, parts[0], count)
			default:
				h.k.Error(w, r, notFound(r))
			}
		}
	})
}

// A local cursor keeps one package's complete candidate metadata. Only the
// store's name/version window is paged; no whole-repository Go slice is built.
type localV2Cursor struct {
	h                     handler
	r                     *http.Request
	q                     v2Query
	whole, versions, done bool
	offset, total         int
	after                 string
}

func (h handler) localV2Cursor(r *http.Request, q v2Query) (nugetfmt.FeedCursor[v2Owner], error) {
	c := &localV2Cursor{h: h, r: r, q: q, whole: h.k.CanAll(r, "", api.ActionRead), versions: q.selects != v2SelectsLatest && q.selects != v2SelectsAbsoluteLatest}
	if c.whole && q.id == "" {
		var err error
		if c.versions {
			c.total, err = h.k.DB().Packages().CountRangeVersions(r.Context(), c.shown())
		} else {
			c.total, err = h.k.DB().Packages().CountPackages(r.Context(), c.shown())
		}
		if err != nil {
			return nil, fmt.Errorf("nuget virtual local count: %w", err)
		}
	}
	return c.next, nil
}

func (c *localV2Cursor) shown() port.PackageRange {
	// Priority and duplicate ownership precede request SemVer/prerelease filters.
	return port.PackageRange{RepoKey: pkgroute.RequestFrom(c.r).Repo.Key, Shown: &port.NuGetShown{Prerelease: true, SemVer2: true}}
}

func (c *localV2Cursor) next(ctx context.Context) (v2Package, bool, error) {
	if c.done {
		return v2Package{}, false, nil
	}
	r := c.r
	if err := ctx.Err(); err != nil {
		return v2Package{}, false, fmt.Errorf("nuget local cursor canceled: %w", err)
	}
	var p pkg
	var err error
	switch {
	case c.q.id != "":
		c.done = true
		p, _, err = c.h.load(r, c.q.id)
	case c.whole:
		if c.offset >= c.total {
			c.done = true
			return v2Package{}, false, nil
		}
		st := c.h.k.DB().Packages()
		var packages []model.Package
		if c.versions {
			var vs []port.ReleaseName
			vs, err = st.RangeVersionsAt(ctx, c.shown(), c.offset, 1)
			if len(vs) > 0 {
				packages = []model.Package{{ID: vs[0].PackageID, RepoKey: c.shown().RepoKey, Name: vs[0].Package}}
			}
		} else {
			packages, err = st.PackagesAt(ctx, c.shown(), c.offset, 1)
		}
		if err != nil {
			return v2Package{}, false, fmt.Errorf("nuget virtual local page: %w", err)
		}
		if len(packages) == 0 {
			c.done = true
			return v2Package{}, false, nil
		}
		var candidates []port.SearchCandidate
		candidates, err = st.Candidates(ctx, packages)
		if err == nil && len(candidates) > 0 {
			p, _, err = c.h.visible(r, candidates[0].Package, candidates[0].Versions)
		}
		advance := 1
		if c.versions {
			advance = max(advance, len(p.versions))
		}
		c.offset += advance
	default:
		var candidates []port.SearchCandidate
		candidates, c.after, err = c.h.k.DB().Packages().SearchNuGet(ctx, port.NuGetSearch{RepoKey: pkgroute.RequestFrom(r).Repo.Key}, kernel.Page{Limit: 1, After: c.after})
		c.done = c.after == ""
		if err == nil && len(candidates) > 0 {
			p, _, err = c.h.visible(r, candidates[0].Package, candidates[0].Versions)
		}
	}
	if err != nil {
		return v2Package{}, false, fmt.Errorf("nuget virtual local metadata: %w", err)
	}
	out := v2Package{ID: p.name}
	for _, v := range p.versions {
		if generic.CheckDownload(pkgroute.RequestFrom(r).Repo, v.meta.Path, false) != nil {
			continue
		}
		out.Versions = append(out.Versions, nugetfmt.Contribution[v2Owner]{Version: v.pv.Version, Path: v.meta.Path, Value: v2Owner{member: r, meta: v.meta, local: v}})
	}
	// An empty candidate can be a concurrent deletion; still advance the cursor.
	return out, true, nil
}

func (h handler) remoteV2Cursor(r *http.Request, id string) (nugetfmt.FeedCursor[v2Owner], *remoteSession, error) {
	s := &remoteSession{virtual: true, h: h, r: r, disclosed: map[string]source.Result{}}
	endpoint := "Packages"
	query := url.Values{"semVerLevel": {"2.0.0"}, "includePrerelease": {"true"}}
	if id != "" {
		if !h.k.CanFolder(r, id, api.ActionRead) {
			return nil, s, kernel.ErrNotFound
		}
		endpoint = "FindPackagesById()"
		query.Set("id", "'"+id+"'")
	}
	// Read the complete member feed before request predicates. Forwarding a
	// latest/search filter would lose a first owner's unlisted/mismatching copy.
	target, err := s.legacyTarget(endpoint, query)
	if err != nil {
		return nil, s, err
	}
	all, err := s.legacyCollect(target, id)
	if err != nil {
		return nil, s, err
	}
	at := 0
	next := func(ctx context.Context) (v2Package, bool, error) {
		if err := ctx.Err(); err != nil {
			return v2Package{}, false, fmt.Errorf("nuget virtual remote cursor: %w", err)
		}
		if at == len(all) {
			return v2Package{}, false, nil
		}
		name := nugetfmt.PackageName(all[at].entry.meta.ID)
		out := v2Package{ID: name}
		for at < len(all) && nugetfmt.PackageName(all[at].entry.meta.ID) == name {
			e := all[at]
			at++
			path := remotePackagePath(name, e.entry.meta.Version)
			if !h.k.Can(r, path, api.ActionRead) {
				continue
			}
			if e.result.CheckPermission != nil {
				if err := e.result.CheckPermission(ctx, path); err != nil {
					if nugetPassedOver(err) {
						continue
					}
					return out, false, fmt.Errorf("nuget virtual member authority: %w", err)
				}
			}
			if generic.CheckDownload(pkgroute.RequestFrom(r).Repo, path, false) != nil {
				continue
			}
			out.Versions = append(
				out.Versions,
				nugetfmt.Contribution[v2Owner]{Version: e.entry.meta.Version, Path: path, Value: v2Owner{member: r, meta: e.entry.meta, remote: e, session: s}},
			)
		}
		return out, true, nil
	}
	return next, s, nil
}

func (h handler) v2Parts(r *http.Request, q v2Query) ([]virtual.Part[nugetfmt.FeedCursor[v2Owner]], []*remoteSession, error) {
	members, err := h.k.MergeMembers(r, "")
	if err != nil {
		return nil, nil, fmt.Errorf("nuget virtual v2 members: %w", err)
	}
	parts := []virtual.Part[nugetfmt.FeedCursor[v2Owner]]{}
	sessions := []*remoteSession{}
	for _, mr := range members {
		var next nugetfmt.FeedCursor[v2Owner]
		var session *remoteSession
		if pkgroute.RequestFrom(mr).Repo.Class == repo.Remote {
			next, session, err = h.remoteV2Cursor(mr, q.id)
		} else {
			next, err = h.localV2Cursor(mr, q)
		}
		if nugetPassedOver(err) {
			h.k.WarnPassedOver(mr, err)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if session != nil {
			session.disclosed = map[string]source.Result{}
			sessions = append(sessions, session)
		}
		parts = append(parts, virtual.Part[nugetfmt.FeedCursor[v2Owner]]{Member: pkgroute.RequestFrom(mr).Repo, Doc: next})
	}
	return parts, sessions, nil
}

func (h handler) mergeV2(r *http.Request, q v2Query, count bool, find bool) (nugetfmt.FeedPage[v2Owner], []*remoteSession, error) {
	parts, sessions, err := h.v2Parts(r, q)
	if err != nil {
		return nugetfmt.FeedPage[v2Owner]{}, nil, err
	}
	allowed := h.virtualAllowed(r)
	merger := nugetfmt.FeedMerger[v2Owner]{
		Skip: q.skip, Take: min(q.top, v2PageSize), Count: count,
		Observe: func(e nugetfmt.FeedEntry[v2Owner]) {
			if count {
				retainV2(e)
			}
		},
		Allowed: func(member repo.Config, v nugetfmt.Contribution[v2Owner]) (bool, error) {
			return allowed(v.Path, member.Key)
		},
		Shows: func(v v2Owner) bool {
			return q.filter.shows(v.meta) && (find || v.session == nil || remoteV2Listed(v.remote.entry))
		},
		Keep: func(v nugetfmt.FeedEntry[v2Owner]) bool {
			return q.predicate(v2Version{meta: v.Value.meta, isLatest: v.Latest, isAbsoluteLatest: v.AbsoluteLatest}) && q.match.matches(v.Value.meta)
		},
	}
	page, err := merger.Merge(r.Context(), parts)
	for _, e := range page.Entries {
		retainV2(e)
	}
	if err != nil {
		return page, sessions, fmt.Errorf("nuget virtual v2 merge: %w", err)
	}
	return page, sessions, nil
}

// Latest flags also depend on their surviving newest owners. Keep those
// authority references with the page, rather than every losing upstream row.
func retainV2(e nugetfmt.FeedEntry[v2Owner]) {
	for _, owner := range []v2Owner{e.Value, e.LatestOwner, e.AbsoluteOwner} {
		if owner.session != nil {
			owner.session.disclosed[remotePackagePath(owner.meta.ID, owner.meta.Version)] = owner.remote.result
		}
	}
}

func (h handler) fenceV2(r *http.Request, sessions []*remoteSession) error {
	for _, s := range sessions {
		if len(s.disclosed) == 0 {
			continue
		}
		if err := s.fence(r.Context()); err != nil {
			return fmt.Errorf("nuget virtual v2 delivery: %w", pkgroute.SourceError(err, false))
		}
	}
	return nil
}

func (h handler) virtualV2Feed(w http.ResponseWriter, r *http.Request, endpoint string, count bool) {
	q, err := parseV2Query(r.URL.Query(), endpoint)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if endpoint == "FindPackagesById()" && !nugetfmt.ValidID(q.id) {
		h.k.Error(w, r, badV2Query())
		return
	}
	page, sessions, err := h.mergeV2(r, q, count, endpoint == "FindPackagesById()")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	w.Header().Set("DataServiceVersion", "2.0")
	if count {
		if err := h.fenceV2(r, sessions); err != nil {
			h.k.Error(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, page.Total)
		return
	}
	entries := make([]atomEntry, 0, len(page.Entries))
	for _, e := range page.Entries {
		entry, err := h.virtualV2Atom(r, e)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		entries = append(entries, entry)
	}
	feed := atomFeed{XMLNS: atomNS, ID: v2Base(r) + "/" + endpoint, Title: "Packages", Entries: entries}
	if n := len(entries); page.More && n > 0 && (!q.isLimited || q.top > n) {
		values := r.URL.Query()
		values.Set("$skip", strconv.Itoa(q.skip+n))
		if q.isLimited {
			values.Set("$top", strconv.Itoa(q.top-n))
		}
		feed.Links = []atomLink{{Rel: "next", Href: v2Base(r) + "/" + endpoint + "?" + values.Encode()}}
	}
	if err := h.fenceV2(r, sessions); err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeXML(w, "application/atom+xml;type=feed", feed)
}

func (h handler) virtualV2Atom(r *http.Request, e nugetfmt.FeedEntry[v2Owner]) (atomEntry, error) {
	v := e.Value
	if v.session != nil {
		entry := v.remote.entry
		entry.values = maps.Clone(entry.values)
		entry.values["IsLatestVersion"] = strconv.FormatBool(e.Latest)
		entry.values["IsAbsoluteLatestVersion"] = strconv.FormatBool(e.AbsoluteLatest)
		return rewriteAtomAt(r, entry), nil
	}
	n, err := h.downloads(v.member, v.meta)
	if err != nil {
		return atomEntry{}, err
	}
	// No cross-version total is invented across members; per-version statistics
	// remain the selected stored owner's, as remote totals are not disclosed.
	entry := v2Atom(r, v2Version{meta: v.meta, isLatest: e.Latest, isAbsoluteLatest: e.AbsoluteLatest}, 0, n)
	properties := entry.Properties.Items[:0]
	for _, p := range entry.Properties.Items {
		if p.XMLName.Local != "d:DownloadCount" {
			properties = append(properties, p)
		}
	}
	entry.Properties.Items = properties
	return entry, nil
}

func (h handler) virtualV2Entry(w http.ResponseWriter, r *http.Request, id, ver string) {
	key, ok := versionKey(ver)
	if !ok || !nugetfmt.ValidID(id) {
		h.k.Error(w, r, notFound(r))
		return
	}
	q := v2Query{
		id:        nugetfmt.PackageName(id),
		filter:    filter{allowPrerelease: true, allowSemVer2: true},
		top:       1,
		predicate: func(v v2Version) bool { return v.meta.Version == key },
	}
	page, sessions, err := h.mergeV2(r, q, false, true)
	if err == nil && len(page.Entries) == 0 {
		err = notFound(r)
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	entry, err := h.virtualV2Atom(r, page.Entries[0])
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if err := h.fenceV2(r, sessions); err != nil {
		h.k.Error(w, r, err)
		return
	}
	writeXML(w, "application/atom+xml;type=entry", entry)
}

func (h handler) virtualV2Download(w http.ResponseWriter, r *http.Request, id, ver string) {
	key, ok := versionKey(ver)
	if !ok || !nugetfmt.ValidID(id) {
		h.k.Error(w, r, notFound(r))
		return
	}
	id = nugetfmt.PackageName(id)
	members, err := h.k.MergeMembers(r, "")
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	allowed := h.virtualAllowed(r)
	priority := false
	for _, mr := range members {
		c := pkgroute.RequestFrom(mr).Repo
		if priority && !c.PriorityResolution.Bool() {
			break
		}
		var next nugetfmt.FeedCursor[v2Owner]
		if c.Class == repo.Remote {
			next, _, err = h.remoteV2Cursor(mr, id)
		} else {
			next, err = h.localV2Cursor(mr, v2Query{id: id})
		}
		if nugetPassedOver(err) {
			h.k.WarnPassedOver(mr, err)
			continue
		}
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		p, _, err := next(r.Context())
		if nugetPassedOver(err) {
			h.k.WarnPassedOver(mr, err)
			continue
		}
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if len(p.Versions) > 0 && c.PriorityResolution.Bool() {
			priority = true
		}
		for _, v := range p.Versions {
			if v.Version != key {
				continue
			}
			ok, err := allowed(v.Path, c.Key)
			if err != nil {
				h.k.Error(w, r, err)
				return
			}
			if !ok {
				continue
			}
			owner := v.Value
			if owner.session == nil {
				found, err := h.k.ResolveDownloadItem(mr, v.Path)
				if nugetPassedOver(err) {
					continue
				}
				if err != nil {
					h.k.Error(w, r, err)
					return
				}
				if found.Item.Folder {
					continue
				}
				h.k.ServeFound(w, mr, found)
				return
			}
			target, err := nugetContext(c, c.DownloadContextPath, owner.meta.ID, key)
			if err != nil {
				h.k.Error(w, r, err)
				return
			}
			result, err := owner.session.packageResult(r.Context(), id, key, target, owner.remote.entry.checks, false)
			if nugetPassedOver(err) {
				h.k.WarnPassedOver(mr, err)
				continue
			}
			if err != nil {
				h.k.Error(w, r, err)
				return
			}
			owner.session.deliverPackageResult(r.Context(), w, result, false)
			return
		}
	}
	h.k.Error(w, r, notFound(r))
}
