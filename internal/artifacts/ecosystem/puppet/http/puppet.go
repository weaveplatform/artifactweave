package puppethttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/go-chi/chi/v5"
	"golang.org/x/mod/semver"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/generic"
	puppetfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/puppet"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

const mediaType = "application/json"

// Options configures the Puppet protocol. Blobs opens a remote's cached
// release metadata when the remote source answers from its cache; without
// it such an answer fails as invalid upstream metadata (502).
// RemoteSources serves remote repositories and fetches through them; nil
// leaves the remote class without a source, so a remote repository answers
// 501.
type Options struct {
	Blobs         blob.Store
	RemoteSources *source.Service
}

type handler struct {
	k     *pkgroute.Kit
	blobs blob.Store
}

type virtualSource struct{}

func (virtualSource) Item(
	context.Context,
	pkgroute.Store,
	repo.Config,
	string,
) (pkgroute.Found, error) {
	return pkgroute.Found{}, source.ErrUnsupported
}

// Protocol returns the Forge v3 protocol for the router kit, under
// /artifactory/api/puppet/{repoKey}: GET and HEAD of the release list of
// one module (v3/releases?module=), a release (v3/releases/{slug}), a
// module (v3/modules/{name}), the exact-name module search
// (v3/modules?query=), a release's archive (v3/files/{file}), and the
// lists of every release and module a local or federated repository
// hosts (v3/releases and v3/modules with no query but a page). Local and
// federated repositories are served from their package index, virtual
// ones from their members in order, and remote ones through
// o.RemoteSources when it is set; a class without a source answers 501.
// The virtual class's source supports no single-item lookup, because the
// protocol reads a virtual repository's members itself.
func Protocol(o Options) pkgroute.Protocol {
	sources := map[repo.Class]pkgroute.Source{
		repo.Local:     pkgroute.Local{},
		repo.Federated: pkgroute.Local{},
		repo.Virtual:   virtualSource{},
	}
	if o.RemoteSources != nil {
		sources[repo.Remote] = pkgroute.Remote{Service: o.RemoteSources}
	}
	return pkgroute.Protocol{
		Name:         "puppet",
		Types:        []repo.PackageType{repo.Puppet},
		ErrorProfile: apierror.ProfileArtifactory,
		Sources:      sources,
		Routes: func(r chi.Router, k *pkgroute.Kit) {
			h := handler{k: k, blobs: o.Blobs}
			r.Get("/v3/releases", h.releases)
			r.Head("/v3/releases", h.releases)
			r.Get("/v3/releases/{slug}", h.release)
			r.Head("/v3/releases/{slug}", h.release)
			r.Get("/v3/modules/{name}", h.module)
			r.Head("/v3/modules/{name}", h.module)
			r.Get("/v3/modules", h.modules)
			r.Head("/v3/modules", h.modules)
			r.Get("/v3/files/{file}", h.archive)
			r.Head("/v3/files/{file}", h.archive)
		},
	}
}

type release struct {
	meta puppetfmt.Metadata
	// metadata is the release's metadata.json as its record answers it
	// (releaseMetadata).
	metadata json.RawMessage
	item     model.Item
	// details are the record's fields that the archive's metadata does not
	// give: a hosted release's from its archive item, a remote's as its
	// upstream gave them.
	details details
	member  *http.Request
	// refused marks a virtual repository's release whose archive every
	// member holding it refuses: it stays listed, and the archive route
	// does not serve it.
	refused    bool
	remoteURL  string
	revalidate func(context.Context) error
	// checkPermission rechecks only Read on the archive, for an answer
	// that lists the release; checkRead rechecks the remote's download
	// policy too, before the archive is fetched.
	checkPermission func(context.Context, string) error
	checkRead       func(context.Context, string) error
}

func splitSlug(slug string) (string, string, bool) {
	owner, rest, ok := strings.Cut(slug, "-")
	if !ok {
		return "", "", false
	}
	module, version, ok := strings.Cut(rest, "-")
	name := owner + "-" + module
	return name, version, ok && puppetfmt.ValidName(name) && puppetfmt.ValidVersion(version)
}

func (h handler) releases(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(r.URL.RawQuery) > 4096 {
		h.k.Error(w, r, apierror.New(400, "invalid Puppet query"))
		return
	}
	for key, values := range query {
		// module_groups is taken only beside module, since only one module's
		// list is asked for it (moduleGroups); the whole-repository lists
		// keep refusing it, as they did.
		if len(values) != 1 ||
			!slices.Contains(
				[]string{"module", "sort_by", "exclude_fields", "limit", "offset"},
				key,
			) && (key != moduleGroups || !query.Has("module")) {
			h.k.Error(w, r, apierror.New(400, "unsupported Puppet query"))
			return
		}
	}
	if pagingOnly(query) {
		h.listReleases(w, r, query)
		return
	}
	name := query.Get("module")
	if !puppetfmt.ValidName(name) ||
		query.Get("sort_by") != "" && query.Get("sort_by") != "version" {
		h.k.Error(w, r, apierror.New(400, "invalid Puppet module"))
		return
	}
	// The list pages as the whole-repository lists do: 20 a page unless
	// limit asks for up to 100 (parsePage), as RTFACT-14557 quotes 7.x
	// answering a release list and as the Forge pages. An offset beyond the
	// most releases a module may list keeps its 400.
	limit, offset, err := parsePage(query)
	if err == nil && offset > maxVersions {
		err = apierror.New(http.StatusBadRequest, "invalid Puppet offset")
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if !h.k.RequireFolder(w, r, name, api.ActionRead) {
		return
	}
	page, total, err := h.releasePage(r, name, offset, limit)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	omit := excludedFields(query)
	out := make([]trimmedRecord, 0, len(page))
	for _, rel := range page {
		out = append(out, trimmedRecord{record(rel), omit})
	}
	writeJSON(w, r, http.StatusOK, struct {
		Pagination listPage        `json:"pagination"`
		Results    []trimmedRecord `json:"results"`
	}{newListPage("/v3/releases", releaseFilter(query), limit, offset, total), out})
}

// releasePage returns the releases of module name on the page at offset of
// limit of its release list, and how many the list holds. A local or
// federated repository reads only its page's releases (hostedPage). A
// remote or virtual repository still collects every release, as it must
// to merge its upstream's pages or its members' lists, within maxVersions,
// and checks the authority of each before it answers any.
func (h handler) releasePage(
	r *http.Request,
	name string,
	offset, limit int,
) ([]release, int, error) {
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Local, repo.Federated:
		if err := h.listingBlackout(r); err != nil {
			return nil, 0, err
		}
		return h.hostedPage(r, name, offset, limit)
	}
	releases, err := h.lookup(r, name, listing)
	if err != nil {
		return nil, 0, err
	}
	if err = h.authority(r, releases); err != nil {
		return nil, 0, err
	}
	start, end := window(offset, limit, len(releases))
	return releases[start:end], len(releases), nil
}

// releaseFilter returns the part of a one-module release list's links
// before limit and offset: the module, then sort_by and exclude_fields when
// the request names them, each as the request gives it, so that the client
// reads every page as it asked for the first (#1327). The Forge keeps
// exclude_fields in its links (it answered
// "/v3/releases?module=puppetlabs-stdlib&limit=1&exclude_fields=readme+changelog+license&offset=1"
// on 2026-11-12); where 7.x writes them is [I], so they follow the module
// in the order the Puppet client sends them, and limit and offset close the
// link as they do in RTFACT-14557's.
func releaseFilter(query url.Values) string {
	filter := "module=" + query.Get("module")
	for _, key := range []string{"sort_by", "exclude_fields"} {
		if query.Has(key) {
			filter += "&" + key + "=" + url.QueryEscape(query.Get(key))
		}
	}
	return filter
}

// excludedFields returns the record fields exclude_fields names. The
// Forge's v3 API description gives it as a list separated by spaces, and
// the Puppet client has sent it separated by commas since PUP-9364
// ("readme,changelog,license,uri,module,tags,supported,file_size,
// downloads,created_at,updated_at,deleted_at"), which the Forge honours, as
// that change cut stdlib's release pages from 2.5 MB to 77 KB; so both
// separate names. A name no record has leaves nothing out.
func excludedFields(query url.Values) map[string]bool {
	names := strings.FieldsFunc(query.Get("exclude_fields"), func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]bool, len(names))
	for _, name := range names {
		out[name] = true
	}
	return out
}

// trimmedRecord is a release record without the top-level fields omit
// names, as the Forge answers a release list with exclude_fields ("List of
// top level keys to exclude from response object", its v3 API
// description). Whether 7.x leaves them out is [I]; the Puppet client asks
// to, and reads none of the fields it names.
type trimmedRecord struct {
	record releaseRecord
	omit   map[string]bool
}

// MarshalJSON writes the record's fields in its own order, leaving out
// those omit names.
func (t trimmedRecord) MarshalJSON() ([]byte, error) {
	var full bytes.Buffer
	enc := json.NewEncoder(&full)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(t.record); err != nil {
		return nil, fmt.Errorf("puppet: release record: %w", err)
	}
	if len(t.omit) == 0 {
		return full.Bytes(), nil
	}
	dec := json.NewDecoder(&full)
	var out bytes.Buffer
	out.WriteByte('{')
	// The record is a struct this package encoded, so its tokens read back.
	_, _ = dec.Token()
	for dec.More() {
		token, _ := dec.Token()
		key, _ := token.(string)
		var value json.RawMessage
		_ = dec.Decode(&value)
		if t.omit[key] {
			continue
		}
		if out.Len() > 1 {
			out.WriteByte(',')
		}
		out.Write(jsonString(key))
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func (h handler) release(w http.ResponseWriter, r *http.Request) {
	name, version, ok := splitSlug(chi.URLParam(r, "slug"))
	if !ok || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(400, "invalid Puppet release"))
		return
	}
	if !h.k.Require(w, r, puppetfmt.Path(name, version), api.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name, listing)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	for _, rel := range releases {
		if rel.meta.Version == version {
			if err = h.authority(r, []release{rel}); err != nil {
				h.k.Error(w, r, err)
				return
			}
			writeJSON(w, r, 200, record(rel))
			return
		}
	}
	rel, found, err := h.upstreamRecord(r, name, version, listing)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if found {
		writeJSON(w, r, 200, record(rel))
		return
	}
	h.k.Error(w, r, apierror.New(404, "Puppet release not found"))
}

// upstreamRecord returns release version of module name from an upstream's
// own record of it, v3/releases/<slug>, for a release the release pages
// leave out, and false when no upstream holds it: a remote's upstream
// (remoteRelease), or a virtual repository's remote members' upstreams, in
// member order (virtualRecord). A hosted repository has no upstream.
func (h handler) upstreamRecord(
	r *http.Request,
	name, version string,
	u use,
) (release, bool, error) {
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Remote:
		return h.remoteRelease(r, name, version, u)
	case repo.Virtual:
		return h.virtualRecord(r, name, version, u)
	default:
		return release{}, false, nil
	}
}

// virtualRecord asks a virtual repository's remote members, in member
// order, for the upstream's record of a release every member's release
// pages leave out, and returns the first that holds it, served through that
// member (#1369). JFrog's Puppet repositories page says a virtual repository
// aggregates its members for the Puppet client and r10k, and r10k reads
// v3/releases/<slug> and then fetches the file_uri it names, so the virtual
// repository answers both from the member that holds the record. That 7.x's
// virtual repository asks its remote members so is [I]. A member is passed
// over as the merge of the release pages passes it over (virtual): one the
// caller may not read the module folder of, and one whose read answers 404,
// as a blacked-out member's or one whose patterns reject the read does. To
// serve the archive, a member whose own patterns reject the archive is
// passed over too, as it is for a listed release (R5).
func (h handler) virtualRecord(
	r *http.Request,
	name, version string,
	u use,
) (release, bool, error) {
	path := puppetfmt.Path(name, version)
	for _, key := range pkgroute.RequestFrom(r).Repo.Repositories {
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return release{}, false, fmt.Errorf("puppet: virtual member: %w", err)
		}
		if member.Class != repo.Remote ||
			u == download && generic.CheckDownload(member, path, false) != nil {
			continue
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return release{}, false, err //nolint:wrapcheck // protocol class refusal
		}
		if !h.k.CanFolder(mr, name, api.ActionRead) {
			continue
		}
		rel, found, err := h.remoteRelease(mr, name, version, u)
		if err != nil {
			var response *apierror.Error
			if errors.Is(err, kernel.ErrNotFound) ||
				errors.As(err, &response) && response.Status == http.StatusNotFound {
				continue
			}
			return release{}, false, err
		}
		if found {
			rel.member = mr
			return rel, true, nil
		}
	}
	return release{}, false, nil
}

func (h handler) module(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !puppetfmt.ValidName(name) || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(400, "invalid Puppet module"))
		return
	}
	if !h.k.RequireFolder(w, r, name, api.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name, listing)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if len(releases) == 0 {
		h.unlistedModule(w, r, name)
		return
	}
	if err = h.authority(r, releases); err != nil {
		h.k.Error(w, r, err)
		return
	}
	d, found, err := h.moduleDetails(r, name, releases, listing)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if !found {
		h.k.Error(w, r, apierror.New(404, "Puppet module not found"))
		return
	}
	writeJSON(w, r, 200, newModuleRecord(releases, d))
}

// moduleDetails returns the details of the module record of name, whose
// releases the caller may read are releases, for u, and false when the
// module has none to answer. A remote takes them from its upstream's
// v3/modules/<m> record (remoteModule); any other repository from the
// releases (derivedModule).
func (h handler) moduleDetails(
	r *http.Request,
	name string,
	releases []release,
	u use,
) (moduleDetails, bool, error) {
	if pkgroute.RequestFrom(r).Repo.Class == repo.Remote {
		read, found, err := h.remoteModule(r, name, u)
		return read.details, found, err
	}
	return derivedModule(releases), true, nil
}

// unlistedModule answers v3/modules/<m> for a module whose release pages,
// or whose virtual repository's members' pages, list no release the caller
// may read: on a remote from its upstream's own record of the module
// (unlistedRecord), on a virtual repository from its remote members'
// upstreams (virtualModule), and on a hosted repository, which has no
// upstream, with 404.
func (h handler) unlistedModule(w http.ResponseWriter, r *http.Request, name string) {
	var (
		doc   moduleRecord
		found bool
		err   error
	)
	switch pkgroute.RequestFrom(r).Repo.Class {
	case repo.Remote:
		doc, found, err = h.unlistedRecord(r, name, nil)
	case repo.Virtual:
		doc, found, err = h.virtualModule(r, name)
	}
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if !found {
		h.k.Error(w, r, apierror.New(404, "Puppet module not found"))
		return
	}
	writeJSON(w, r, 200, doc)
}

// unlistedRecord returns the module record of module name that a remote
// whose upstream's release pages list no release of it the caller may read
// answers from the upstream's own record of the module (#1370) [I], and
// false when it has none to answer. RTFACT-19130 reports a 6.8.6 remote
// answering 404 for puppetlabs-sqlserver, a Puppet Enterprise module the
// Forge's module record served, and names that 404 as the fault; the
// Forge's v3 API description says its module list holds only the base
// group unless module_groups names others, "unless requesting modules by
// their slugs". r10k reads this record for a module's current release
// before it installs, so the record answers the module. Its releases are
// the versions the record lists that the caller may read (recordReleases),
// checked as a release page's are, and its current release is the newest
// one's own record, v3/releases/<slug> (remoteRelease); both are read under
// the release pages' authority, Read on the module folder and the download
// policy at .puppet/<m>/releases.json (DV-057). via, when it is set, is the
// request of the virtual repository r is a member request of, whose Read
// must hold on each release too, as it must on a release a member's pages
// list (virtual). A record the upstream does not hold, or that lists
// no release the caller may read, is none to answer; a listed release whose
// own record the upstream does not hold is invalid upstream metadata. The
// release list and the search still answer from the release pages, as the
// Forge's own search leaves out a module of another group (it found
// puppetlabs-sqlserver only with module_groups on 2026-11-12).
func (h handler) unlistedRecord(
	r *http.Request,
	name string,
	via *http.Request,
) (moduleRecord, bool, error) {
	read, found, err := h.remoteModule(r, name, listing)
	if err != nil || !found {
		return moduleRecord{}, false, err
	}
	versions, err := recordReleases(read.releases)
	if err != nil {
		return moduleRecord{}, false, err
	}
	if read.checkPermission == nil {
		return moduleRecord{}, false, fmt.Errorf(
			"puppet: missing authority: %w",
			pkgroute.SourceError(source.ErrInvalidResult, false),
		)
	}
	var releases []release
	for _, v := range versions {
		path := puppetfmt.Path(name, v)
		if via != nil && !h.k.Can(via, path, api.ActionRead) ||
			!h.k.Can(r, path, api.ActionRead) {
			continue
		}
		if err = read.checkPermission(r.Context(), path); err != nil {
			if errors.Is(err, source.ErrDenied) {
				continue
			}
			return moduleRecord{}, false, fmt.Errorf(
				"puppet: authority: %w",
				pkgroute.SourceError(err, false),
			)
		}
		releases = append(releases, release{meta: puppetfmt.Metadata{Name: name, Version: v}})
	}
	if len(releases) == 0 {
		return moduleRecord{}, false, nil
	}
	current, found, err := h.remoteRelease(r, name, releases[0].meta.Version, listing)
	if err == nil && !found {
		err = invalidUpstream()
	}
	if err != nil {
		return moduleRecord{}, false, err
	}
	releases[0] = current
	return newModuleRecord(releases, read.details), true, nil
}

// modules answers v3/modules: the whole-repository list when the query
// names nothing but a page, else the module search. The search is
// deliberately exact-name and bounded, and does not proxy arbitrary
// queries: it answers the one module named, paged as the lists are. 7.x
// pages it too, since its own virtual repository asks a member's upstream
// for "v3/modules?query=...&limit=100" (RTFACT-13687), and the Forge pages
// its search the same way, keeping query in each link.
func (h handler) modules(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err == nil && pagingOnly(q) {
		h.listModules(w, r, q)
		return
	}
	if err != nil || !searchQuery(q) {
		h.k.Error(w, r, apierror.New(400, "search requires an exact Puppet module name"))
		return
	}
	limit, offset, err := parsePage(q)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	name := q.Get("query")
	if !h.k.RequireFolder(w, r, name, api.ActionRead) {
		return
	}
	releases, err := h.lookup(r, name, search)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	if err = h.authority(r, releases); err != nil {
		h.k.Error(w, r, err)
		return
	}
	found := []moduleRecord{}
	if len(releases) > 0 {
		d, ok, err := h.moduleDetails(r, name, releases, search)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if ok {
			found = append(found, newModuleRecord(releases, d))
		}
	}
	start, end := window(offset, limit, len(found))
	writeJSON(w, r, http.StatusOK, struct {
		Pagination listPage       `json:"pagination"`
		Results    []moduleRecord `json:"results"`
	}{newListPage("/v3/modules", "query="+name, limit, offset, len(found)), found[start:end]})
}

// searchQuery reports whether q is a module search: one valid module name
// as query, and at most one limit, one offset and one module_groups
// (moduleGroups).
func searchQuery(q url.Values) bool {
	for key, values := range q {
		if len(values) != 1 ||
			key != "query" && key != "limit" && key != "offset" && key != moduleGroups {
			return false
		}
	}
	return puppetfmt.ValidName(q.Get("query"))
}

// moduleGroups is the Forge's module_groups parameter, which one module's
// release list and the module search accept and leave without effect
// (#1343). The Puppet client adds it to both whenever its internal
// module_groups setting is set (lib/puppet/forge.rb, fetch and search), and
// Puppet Enterprise sets that to "base+pe_only", sent as
// module_groups=base%20pe_only (puppetlabs/puppet 23a8b7c241, PUP-1890);
// PUP-8650 shows a Puppet Enterprise server sending it to an Artifactory
// Puppet repository. Refusing it would refuse every module such a node
// installs, where JFrog's Puppet repositories page says the Puppet client
// is served from every repository type. The Forge narrows its lists to the
// groups named (its v3 API description), but no source shows 7.x doing so
// or what its links keep, so every value is taken and none narrows a list,
// the links leave it out, and a remote does not pass it on [I]. A remote
// that passed it on would list Puppet Enterprise modules its upstream's
// own lists leave out: without the parameter the Forge's module list holds
// only the base group, its description says.
const moduleGroups = "module_groups"

func (h handler) archive(w http.ResponseWriter, r *http.Request) {
	file := chi.URLParam(r, "file")
	name, version, ok := splitSlug(strings.TrimSuffix(file, ".tar.gz"))
	if !ok || !strings.HasSuffix(file, ".tar.gz") || r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(400, "invalid Puppet archive"))
		return
	}
	path := puppetfmt.Path(name, version)
	if !h.k.Require(w, r, path, api.ActionRead) {
		return
	}
	// The archive is a download, which Artifactory refuses at its own path
	// before it looks the release up (decision 0032; RTFACT-13710 shows the
	// Puppet service reading archives through that check): the blackout
	// 404 (R9), the patterns 404 or a virtual repository's own
	// "No repository found" 404, with the policy's message rather than
	// "Puppet release not found" (R6, R10). CheckDocument rather than
	// CheckDownload, because it checks a remote repository's own policy
	// too, which the source boundary would otherwise check only once the
	// release pages had been fetched from upstream.
	if err := h.k.CheckDocument(r, path); err != nil {
		h.k.Error(w, r, err)
		return
	}
	releases, err := h.lookup(r, name, download)
	if err != nil {
		h.k.Error(w, r, err)
		return
	}
	listed := false
	for _, rel := range releases {
		if rel.meta.Version != version {
			continue
		}
		listed = true
		// A version every member of a virtual repository refuses has no
		// member left to serve it, and answers as one none holds (R5).
		if !rel.refused {
			h.serveArchive(w, r, rel, path)
			return
		}
	}
	// A release the pages leave out is served from the archive its
	// upstream's record names, as v3/releases/<slug> answers that record
	// (#1344): r10k reads the record and then fetches its file_uri, so a
	// record whose archive answers 404 is one no client can install
	// (#1369). The archive is checked against the record as a listed
	// release's is against its page (remoteArchive). What 7.x answers here
	// is [I]: RTFACT-19130 shows a remote answering the record of a release
	// the Forge's lists leave out, not the archive request that follows.
	if !listed {
		rel, found, err := h.upstreamRecord(r, name, version, download)
		if err != nil {
			h.k.Error(w, r, err)
			return
		}
		if found {
			h.serveArchive(w, r, rel, path)
			return
		}
	}
	h.k.Error(w, r, apierror.New(404, "Puppet release not found"))
}

// serveArchive answers the archive at path of rel, once its authority
// holds: from the upstream when rel is a remote's, and otherwise from the
// stored item of the repository, or virtual member, that holds it.
func (h handler) serveArchive(w http.ResponseWriter, r *http.Request, rel release, path string) {
	if err := h.authority(r, []release{rel}); err != nil {
		h.k.Error(w, r, err)
		return
	}
	if rel.remoteURL != "" {
		h.remoteArchive(w, r, rel)
		return
	}
	member := r
	if rel.member != nil {
		member = rel.member
	}
	h.k.ServeItem(w, member, path)
}

func (h handler) authority(r *http.Request, releases []release) error {
	for _, rel := range releases {
		if rel.revalidate != nil {
			if err := rel.revalidate(r.Context()); err != nil {
				return fmt.Errorf("puppet: authority: %w", pkgroute.SourceError(err, false))
			}
		}
		// Read permission only: a listed release whose archive the
		// patterns reject stays listed (R3), and remoteArchive
		// checks the policy when the archive itself is read.
		if rel.checkPermission != nil {
			if err := rel.checkPermission(r.Context(), rel.meta.Path); err != nil {
				return fmt.Errorf("puppet: authority: %w", pkgroute.SourceError(err, false))
			}
		}
	}
	return nil
}

// use is what a module's releases are looked up to answer, which decides
// the download policy the lookup applies.
type use int

const (
	// listing answers a release list, a release or a module record, which
	// 7.x reads from stored documents (RTFACT-26143).
	listing use = iota
	// search answers the exact-name module search, a native search route,
	// which keeps its answers under a blackout (R2b): a blacked-out local
	// repository finds nothing, and a blacked-out remote answers the
	// blackout 404 for the module folder. No pattern refuses it, since a
	// release whose archive the patterns reject stays listed (R3).
	search
	// download finds the release whose archive the archive route serves,
	// once that route has checked the archive's own path.
	download
)

// lookup returns the releases of module name the caller may read, for u.
func (h handler) lookup(r *http.Request, name string, u use) ([]release, error) {
	q := pkgroute.RequestFrom(r)
	switch q.Repo.Class {
	case repo.Local, repo.Federated:
		// The search, a native search route, keeps its answer under a
		// blackout (R2b): it finds none of a blacked-out repository's
		// releases, an empty result rather than the blackout 404, and a
		// virtual repository's search leaves such a member out.
		if u == search {
			if generic.CheckBlackedOut(q.Repo, q.Rest) != nil {
				return nil, nil
			}
			return h.hosted(r, name)
		}
		if err := h.listingBlackout(r); err != nil {
			return nil, err
		}
		return h.hosted(r, name)
	case repo.Remote:
		return h.remoteVersions(r, name, u)
	case repo.Virtual:
		return h.virtual(r, name, u)
	default:
		return nil, source.ErrUnsupported
	}
}

func (h handler) hosted(r *http.Request, name string) ([]release, error) {
	q := pkgroute.RequestFrom(r)
	p, err := h.k.DB().Packages().Find(r.Context(), q.Repo.Key, name)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("puppet: find package: %w", err)
	}
	versions, err := h.k.DB().Packages().Versions(r.Context(), p.ID)
	if err != nil {
		return nil, fmt.Errorf("puppet: package versions: %w", err)
	}
	if len(versions) > maxVersions {
		return nil, apierror.New(503, "Puppet releases exceed limit")
	}
	out := make([]release, 0, len(versions))
	for _, v := range versions {
		// A release whose archive the include and exclude patterns reject
		// stays listed, and only the archive is refused (R3): 7.x
		// writes Puppet's listings as stored documents (RTFACT-26143),
		// which list such a release as the other formats' do.
		if !h.k.Can(r, puppetfmt.Path(name, v.Version), api.ActionRead) {
			continue
		}
		rel, err := h.stored(r, name, v)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	sortReleases(out)
	return out, nil
}

// listingBlackout refuses a local or federated repository's release list,
// release or module record when the repository is blacked out. 7.x keeps a
// local repository's listings as documents (RTFACT-26143) at a path no
// source shows, so only the blackout refuses them, before anything is
// computed, naming the request's own path (R2a, R9); patterns never do. A
// member of a virtual repository is passed over instead (R5).
func (h handler) listingBlackout(r *http.Request) error {
	if err := h.k.CheckBlackedOut(r, pkgroute.RequestFrom(r).Rest); err != nil {
		return fmt.Errorf("puppet: releases: %w", err)
	}
	return nil
}

// hostedPage returns the page at offset of limit of the release list hosted
// gives module name, and how many releases that list holds, reading only
// the page's releases (#1404). Read permission and the list's order need
// only each version's name, so every version is counted and ordered by its
// name alone; the page's releases are then read and checked against their
// archives (stored), in the index's order, as hosted checks them, so that
// a page holding two that fail answers the failure hosted would. A release
// deleted between the count and the page leaves the page short, as the
// whole-repository lists' pages are left.
func (h handler) hostedPage(
	r *http.Request,
	name string,
	offset, limit int,
) ([]release, int, error) {
	q := pkgroute.RequestFrom(r)
	p, err := h.k.DB().Packages().Find(r.Context(), q.Repo.Key, name)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("puppet: find package: %w", err)
	}
	indexed, err := h.k.DB().Packages().VersionNames(r.Context(), p.ID)
	if err != nil {
		return nil, 0, fmt.Errorf("puppet: package versions: %w", err)
	}
	if len(indexed) > maxVersions {
		return nil, 0, apierror.New(503, "Puppet releases exceed limit")
	}
	// Read permission alone filters, as in hosted (R3).
	indexed = slices.DeleteFunc(indexed, func(v string) bool {
		return !h.k.Can(r, puppetfmt.Path(name, v), api.ActionRead)
	})
	listed := slices.SortedFunc(slices.Values(indexed), newestFirst)
	start, end := window(offset, limit, len(listed))
	onPage := make(map[string]bool, end-start)
	for _, v := range listed[start:end] {
		onPage[v] = true
	}
	found := make(map[string]release, end-start)
	for _, version := range indexed {
		if !onPage[version] {
			continue
		}
		v, err := h.k.DB().Packages().Version(r.Context(), p.ID, version)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, 0, fmt.Errorf("puppet: package version: %w", err)
		}
		if found[version], err = h.stored(r, name, v); err != nil {
			return nil, 0, err
		}
	}
	out := make([]release, 0, len(found))
	for _, v := range listed[start:end] {
		if rel, ok := found[v]; ok {
			out = append(out, rel)
		}
	}
	return out, len(listed), nil
}

// stored returns the release of module name that the package index's
// version v records, once the index entry and the stored archive agree; a
// release whose entry or archive does not match fails with 502 rather than
// being advertised.
func (h handler) stored(r *http.Request, name string, v model.PackageVersion) (release, error) {
	path := puppetfmt.Path(name, v.Version)
	var meta puppetfmt.Metadata
	if json.Unmarshal(v.Metadata, &meta) != nil || meta.Name != name ||
		meta.Version != v.Version || meta.Path != path {
		return release{}, apierror.New(http.StatusBadGateway, "invalid stored Puppet package index")
	}
	// An entry without metadata.json's document has no metadata to answer,
	// and fails as an entry that does not match its archive does.
	metadata, tags, err := releaseMetadata(meta.Document)
	if err != nil {
		return release{}, apierror.New(http.StatusBadGateway, "invalid stored Puppet package index")
	}
	key := pkgroute.RequestFrom(r).Repo.Key
	item, err := h.k.DB().Items().Get(r.Context(), key, false, path)
	if err != nil || item.Folder || meta.SHA256 == "" || meta.SHA256 != item.Actual.SHA256 {
		return release{}, apierror.New(http.StatusBadGateway, "invalid stored Puppet archive")
	}
	stats, err := h.k.DB().Items().Stats(r.Context(), item.ID)
	if err != nil {
		return release{}, fmt.Errorf("puppet: statistics of %s/%s: %w", key, path, err)
	}
	d := hostedDetails(item, stats)
	d.tags = tags
	return release{meta: meta, metadata: metadata, item: item, details: d}, nil
}

func (h handler) virtual(r *http.Request, name string, u use) ([]release, error) {
	q := pkgroute.RequestFrom(r)
	byVersion := map[string]release{}
	for _, key := range q.Repo.Repositories {
		member, err := h.k.DB().Repos().Get(r.Context(), key)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("puppet: virtual member: %w", err)
		}
		mr, err := h.k.MemberRequest(r, member)
		if err != nil {
			return nil, err //nolint:wrapcheck // protocol class refusal
		}
		if !h.k.CanFolder(mr, name, api.ActionRead) {
			continue
		}
		// A blacked-out member, local or remote, and, outside the search, a
		// remote member whose patterns reject its release pages answer 404
		// here, or for the search find nothing, and are left out of the
		// merge (R5, R2b).
		found, err := h.lookup(mr, name, u)
		if err != nil {
			var response *apierror.Error
			if errors.Is(err, kernel.ErrNotFound) ||
				errors.As(err, &response) && response.Status == http.StatusNotFound {
				continue
			}
			return nil, err
		}
		for _, v := range found {
			if !h.k.Can(r, v.meta.Path, api.ActionRead) {
				continue
			}
			// A member whose patterns reject only a release's archive
			// stays in the merge with the release listed, and the archive
			// comes from the first member that holds the version and
			// serves it (R5, RTFACT-25571), so the listing gives
			// that member's checksum. The virtual repository's own
			// patterns leave the release listed too (R3); the archive
			// route refuses its archive.
			v.member = mr
			v.refused = generic.CheckDownload(member, v.meta.Path, false) != nil
			if prior, exists := byVersion[v.meta.Version]; !exists || prior.refused && !v.refused {
				byVersion[v.meta.Version] = v
			}
		}
	}
	if len(byVersion) > maxVersions {
		return nil, apierror.New(503, "Puppet releases exceed limit")
	}
	out := make([]release, 0, len(byVersion))
	for _, v := range byVersion {
		out = append(out, v)
	}
	sortReleases(out)
	return out, nil
}

func sortReleases(out []release) {
	slices.SortFunc(
		out,
		func(a, b release) int { return newestFirst(a.meta.Version, b.meta.Version) },
	)
}

// newestFirst orders Puppet versions from the highest semantic version
// down, the order every release list answers in.
func newestFirst(a, b string) int {
	if cmp := semver.Compare("v"+a, "v"+b); cmp != 0 {
		return -cmp
	}
	return strings.Compare(a, b)
}

// writeJSON answers doc as JSON, writing "&", "<" and ">" as they are
// rather than as &, < and >, as 7.x writes them: in its
// links (RTFACT-14557) and in a release's dependency requirements, such as
// ">= 4.13.1 < 6.0.0" (RTFACT-19130). The JSON is the same either way; the
// bytes are 7.x's.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, doc any) {
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(doc)
	}
}
