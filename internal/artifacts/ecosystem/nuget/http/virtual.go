package nugethttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	nugetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/nuget"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// No virtual item is stored: each response is assembled from real holders.
type virtualSource struct{}

func (virtualSource) Item(context.Context, pkgroute.Store, repo.Config, string) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

type virtualVersion struct {
	member        *http.Request
	local         version
	remote        remoteVersion
	session       *remoteSession
	search        remoteDoc
	searchVersion remoteDoc
}
type virtualPackage = nugetfmt.Package[virtualVersion]

func (h handler) virtualV3(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := pkgroute.RequestFrom(r)
		if q.Repo.Class != repo.Virtual {
			next.ServeHTTP(w, r)
			return
		}
		// Retain the old class gate before parsing/admission for operations still
		// deferred; publishing and the three-segment symbol route are now served.
		root, _, _ := strings.Cut(q.Rest, "/")
		if h.virtualSymbolRoute(w, r, root) {
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead ||
			root != "" && root != "index.json" && root != "flatcontainer" && root != "registration" && root != "registration-semver2" && root != "query" && root != "autocomplete" {
			h.k.Error(
				w,
				r,
				apierror.New(http.StatusNotImplemented, fmt.Sprintf("'%s' is a virtual repository, which artifactweave does not serve through the nuget/v3 API yet", q.Repo.Key)),
			)
			return
		}
		p, err := pkgroute.ParsePath(q.Rest)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		parts := p.Segments
		switch {
		case len(parts) == 0 || len(parts) == 1 && parts[0] == "index.json":
			h.serviceIndex(w, r)
		case len(parts) > 1 && parts[0] == "flatcontainer":
			h.virtualFlat(w, r, parts[1:])
		case len(parts) > 1 && (parts[0] == "registration" || parts[0] == "registration-semver2"):
			h.virtualRegistration(w, r, hive{path: parts[0], allowSemVer2: parts[0] == "registration-semver2"}, parts[1:])
		case len(parts) == 1 && parts[0] == "query":
			h.virtualSearch(w, r, false)
		case len(parts) == 1 && parts[0] == "autocomplete":
			h.virtualSearch(w, r, true)
		default:
			h.k.Error(w, r, notFound(r))
		}
	})
}

func nugetPassedOver(err error) bool {
	if pkgroute.PassedOver(err) {
		return true
	}
	var upstream *source.HTTPError
	if errors.As(err, &upstream) {
		return upstream.Status == 401 || upstream.Status == 403 || upstream.Status == 404
	}
	var answer *apierror.Error
	return errors.As(err, &answer) && answer.NoChallenge && (answer.Status == 401 || answer.Status == 403)
}

func (h handler) memberVersions(r *http.Request, id string, hv *hive) (virtualPackage, error) {
	out := virtualPackage{ID: id}
	if pkgroute.RequestFrom(r).Repo.Class != repo.Remote {
		p, _, err := h.load(r, id)
		if err != nil {
			return out, err
		}
		for _, v := range p.versions {
			if hv != nil && !hv.shows(v.meta) {
				continue
			}
			if err := generic.CheckDownload(pkgroute.RequestFrom(r).Repo, v.meta.Path, false); err != nil {
				if nugetPassedOver(err) {
					continue
				}
				return out, fmt.Errorf("nuget member policy: %w", err)
			}
			out.Versions = append(out.Versions, nugetfmt.Contribution[virtualVersion]{Version: v.pv.Version, Path: v.meta.Path, Value: virtualVersion{member: r, local: v}})
		}
		return out, nil
	}
	// A package-specific Read refusal must not disclose a remote's policy or
	// cause upstream traffic, even if the caller reads an unrelated folder.
	if !h.k.CanFolder(r, id, api.ActionRead) {
		return out, kernel.ErrNotFound
	}
	s := &remoteSession{virtual: true, h: h, r: r, disclosed: map[string]source.Result{}}
	if err := s.discover(); err != nil {
		return out, err
	}
	if hv != nil {
		vs, err := s.registrations(*hv, id)
		if err != nil {
			return out, err
		}
		for _, v := range vs {
			if !remoteHiveShows(*hv, v) || !s.allowed(id, v.key, v.result) {
				continue
			}
			path := remotePackagePath(id, v.key)
			if err := generic.CheckDownload(pkgroute.RequestFrom(r).Repo, path, false); err != nil {
				if nugetPassedOver(err) {
					continue
				}
				return out, fmt.Errorf("nuget member policy: %w", err)
			}
			out.Versions = append(out.Versions, nugetfmt.Contribution[virtualVersion]{Version: v.key, Path: path, Value: virtualVersion{member: r, remote: v, session: s}})
		}
	} else {
		vs, result, err := s.versions(id)
		if err != nil {
			return out, err
		}
		for _, v := range vs {
			if !s.allowed(id, v, result) {
				continue
			}
			key, _ := versionKey(v)
			path := remotePackagePath(id, key)
			if err := generic.CheckDownload(pkgroute.RequestFrom(r).Repo, path, false); err != nil {
				if nugetPassedOver(err) {
					continue
				}
				return out, fmt.Errorf("nuget member policy: %w", err)
			}
			out.Versions = append(
				out.Versions,
				nugetfmt.Contribution[virtualVersion]{Version: key, Path: path, Value: virtualVersion{member: r, remote: remoteVersion{key: key, result: result}, session: s}},
			)
		}
	}
	return out, nil
}

// Metadata must filter actual package paths before paging. Walking at each
// distinct path also retains nested virtual patterns and the engine's visited
// set; the initial empty-path walk supplies cache-only member requests.
func (h handler) virtualAllowed(r *http.Request) func(string, string) (bool, error) {
	byPath := map[string]map[string]bool{}
	return func(path, key string) (bool, error) {
		if allowed, ok := byPath[path]; ok {
			return allowed[key], nil
		}
		members, err := h.k.MergeMembers(r, path)
		if err != nil {
			if nugetPassedOver(err) {
				byPath[path] = map[string]bool{}
				return false, nil
			}
			return false, fmt.Errorf("nuget virtual path: %w", err)
		}
		allowed := map[string]bool{}
		for _, mr := range members {
			allowed[pkgroute.RequestFrom(mr).Repo.Key] = true
		}
		byPath[path] = allowed
		return allowed[key], nil
	}
}

func (h handler) mergedVersions(r *http.Request, id string, hv *hive) (virtualPackage, error) {
	members, err := h.k.MergeMembers(r, "")
	if err != nil {
		return virtualPackage{}, fmt.Errorf("nuget virtual members: %w", err)
	}
	parts := []virtual.Part[virtualPackage]{}
	allowed := h.virtualAllowed(r)
	priority := false
	for _, mr := range members {
		c := pkgroute.RequestFrom(mr).Repo
		if priority && !c.PriorityResolution.Bool() {
			break
		}
		p, err := h.memberVersions(mr, id, hv)
		if nugetPassedOver(err) {
			h.k.WarnPassedOver(mr, err)
			continue
		}
		if err != nil {
			return virtualPackage{}, err
		}
		if len(p.Versions) > 0 && c.PriorityResolution.Bool() {
			priority = true
		}
		visible := p.Versions[:0]
		for _, v := range p.Versions {
			ok, err := allowed(v.Path, c.Key)
			if err != nil {
				return virtualPackage{}, err
			}
			if ok {
				visible = append(visible, v)
			}
		}
		p.Versions = visible
		parts = append(parts, virtual.Part[virtualPackage]{Member: c, Doc: p})
	}
	merged, err := (nugetfmt.Merger[virtualVersion]{}).Merge(r.Context(), parts)
	if err != nil {
		return merged, fmt.Errorf("nuget virtual merge: %w", err)
	}
	return merged, nil
}

func (h handler) fenceVirtual(r *http.Request, p virtualPackage) error {
	used := map[*remoteSession]map[string]source.Result{}
	for _, v := range p.Versions {
		owner := v.Value.session
		if owner == nil {
			continue
		}
		if used[owner] == nil {
			used[owner] = map[string]source.Result{}
		}
		used[owner][remotePackagePath(p.ID, v.Version)] = v.Value.remote.result
	}
	for owner, retained := range used {
		// Only selected, visible whole records are disclosed. A duplicate or a
		// policy-filtered version must not add a delivery-time permission dependency.
		owner.disclosed = retained
		if err := owner.fence(r.Context()); err != nil {
			return fmt.Errorf("nuget virtual authority: %w", pkgroute.SourceError(err, false))
		}
	}
	return nil
}

func (h handler) virtualFlat(w http.ResponseWriter, r *http.Request, parts []string) {
	if len(parts) == 2 && parts[1] == "index.json" && nugetfmt.ValidID(parts[0]) {
		p, err := h.mergedVersions(r, nugetfmt.PackageName(parts[0]), nil)
		if err == nil && len(p.Versions) == 0 {
			err = notFound(r)
		}
		if err == nil {
			err = h.fenceVirtual(r, p)
		}
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		vs := make([]string, 0, len(p.Versions))
		for _, v := range p.Versions {
			vs = append(vs, v.Version)
		}
		writeJSON(w, map[string]any{"versions": vs})
		return
	}
	if len(parts) != 3 || !nugetfmt.ValidID(parts[0]) {
		h.k.Error(w, r, notFound(r))
		return
	}
	id := nugetfmt.PackageName(parts[0])
	key, ok := versionKey(parts[1])
	filename := strings.ToLower(parts[2])
	nuspec := filename == id+".nuspec"
	if !ok || !nuspec && filename != id+"."+strings.ToLower(parts[1])+".nupkg" && filename != id+"."+key+".nupkg" {
		h.k.Error(w, r, notFound(r))
		return
	}
	h.virtualDownload(w, r, id, key, nuspec)
}

func (h handler) virtualDownload(w http.ResponseWriter, r *http.Request, id, key string, nuspec bool) {
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
		p, err := h.memberVersions(mr, id, nil)
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
			if v.Value.session != nil {
				s := v.Value.session
				target, checks, err := s.packageTarget(id, key)
				if nugetPassedOver(err) {
					h.k.WarnPassedOver(mr, err)
					continue
				}
				if err != nil {
					h.k.Error(w, r, err)
					return
				}
				result, err := s.packageResult(r.Context(), id, key, target, checks, nuspec)
				if nugetPassedOver(err) {
					h.k.WarnPassedOver(mr, err)
					continue
				}
				if err != nil {
					h.k.Error(w, r, err)
					return
				}
				s.deliverPackageResult(r.Context(), w, result, nuspec)
				return
			}
			found, err := h.k.ResolveDownloadItem(mr, v.Path)
			if nugetPassedOver(err) {
				h.k.WarnPassedOver(mr, err)
				continue
			}
			if err != nil {
				h.k.Error(w, r, err)
				return
			}
			if found.Item.Folder {
				continue
			}
			if nuspec {
				w.Header().Set("Content-Type", "application/xml")
				_, _ = w.Write([]byte(v.Value.local.meta.Nuspec))
			} else {
				h.k.ServeFound(w, mr, found)
			}
			return
		}
	}
	h.k.Error(w, r, notFound(r))
}

func (h handler) virtualEntry(r *http.Request, hv hive, id string, v nugetfmt.Contribution[virtualVersion]) remoteDoc {
	if v.Value.session != nil {
		return rewriteEntryAt(r, hv, id, v.Value.remote)
	}
	u := urlsOf(r, hv)
	m := v.Value.local
	raw := encode(leaf{CatalogEntry: u.entry(id, m), PackageContent: u.content(m.meta), Registration: u.index(id), ID: u.leaf(id, v.Version)})
	return objectFrom(raw)
}
func objectFrom(raw []byte) remoteDoc { var d remoteDoc; _ = json.Unmarshal(raw, &d); return d }

func (h handler) virtualRegistration(w http.ResponseWriter, r *http.Request, hv hive, parts []string) {
	if len(parts) == 3 && parts[0] == "Download" {
		key, ok := versionKey(parts[2])
		if ok && nugetfmt.ValidID(parts[1]) {
			h.virtualDownload(w, r, nugetfmt.PackageName(parts[1]), key, false)
			return
		}
	}
	valid := len(parts) == 2 && (parts[1] == "index.json" || strings.HasSuffix(parts[1], ".json")) || len(parts) == 4 && parts[1] == "page" && strings.HasSuffix(parts[3], ".json")
	if !valid || !nugetfmt.ValidID(parts[0]) {
		h.k.Error(w, r, notFound(r))
		return
	}
	id := nugetfmt.PackageName(parts[0])
	p, err := h.mergedVersions(r, id, &hv)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	u := urlsOf(r, hv)
	kind := "index"
	lo, hi := "", ""
	if len(parts) == 4 {
		kind = "page"
		var ok bool
		lo, ok = versionKey(parts[2])
		if !ok {
			h.k.Error(w, r, notFound(r))
			return
		}
		hi, ok = versionKey(strings.TrimSuffix(parts[3], ".json"))
		if !ok {
			h.k.Error(w, r, notFound(r))
			return
		}
	} else if parts[1] != "index.json" {
		kind = "leaf"
		var ok bool
		lo, ok = versionKey(strings.TrimSuffix(parts[1], ".json"))
		if !ok {
			h.k.Error(w, r, notFound(r))
			return
		}
		hi = lo
	}
	if kind != "index" {
		vs := p.Versions[:0]
		for _, v := range p.Versions {
			if model.CompareNuGet(v.Version, lo) >= 0 && model.CompareNuGet(v.Version, hi) <= 0 {
				vs = append(vs, v)
			}
		}
		p.Versions = vs
	}
	if len(p.Versions) == 0 {
		h.k.Error(w, r, notFound(r))
		return
	}
	if err := h.fenceVirtual(r, p); err != nil {
		h.k.Error(w, r, err)
		return
	}
	if kind == "leaf" {
		d := h.virtualEntry(r, hv, id, p.Versions[0])
		catalog := objectField(d, "catalogEntry")
		delete(d, "catalogEntry")
		for _, field := range []string{"listed", "published"} {
			if value, ok := catalog[field]; ok {
				d[field] = value
			}
		}
		writeCompressed(w, r, d)
		return
	}
	entries := func(vs []nugetfmt.Contribution[virtualVersion]) []remoteDoc {
		out := make([]remoteDoc, 0, len(vs))
		for _, v := range vs {
			out = append(out, h.virtualEntry(r, hv, id, v))
		}
		return out
	}
	if kind == "page" {
		writeCompressed(w, r, map[string]any{"@id": u.page(id, lo, hi), "parent": u.index(id), "count": len(p.Versions), "lower": lo, "upper": hi, "items": entries(p.Versions)})
		return
	}
	pages := []pageLink{}
	for start := 0; start < len(p.Versions); start += pageSize {
		vs := p.Versions[start:min(start+pageSize, len(p.Versions))]
		lower, upper := vs[0].Version, vs[len(vs)-1].Version
		pages = append(pages, pageLink{ID: u.page(id, lower, upper), Count: len(vs), Lower: lower, Upper: upper})
	}
	writeCompressed(w, r, registrationIndex{ID: u.index(id), Count: len(pages), Items: pages})
}
