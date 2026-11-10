package artifactshttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/search"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/kernel/antpath"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The GET searches (decision 0012, research 22 §2) build an items query and
// run it through the store's AQL path, so they share its compilation to each
// dialect and its parity fixtures. artifacts/core/search scopes, pages,
// filters and caps them; this file binds their parameters, builds their
// criteria and renders their results.

// SearchLimits bounds the GET searches, as Artifactory's
// artifactory.search.userQueryLimit and limitAnonymousUsersOnly do.
type SearchLimits struct {
	// UserQueryLimit caps the results anonymous callers get, and every
	// non-admin's when LimitInternalUsers is set; zero means no cap.
	UserQueryLimit int
	// LimitInternalUsers applies UserQueryLimit to signed-in non-admins too.
	LimitInternalUsers bool
}

// Search result media types, as Artifactory's search resources name them.
const (
	mediaArtifactSearch    = "application/vnd.org.jfrog.artifactory.search.ArtifactSearchResult+json"
	mediaBadChecksumSearch = "application/vnd.org.jfrog.artifactory.search.BadChecksumSearchResult+json"
	mediaChecksumSearch    = "application/vnd.org.jfrog.artifactory.search.ChecksumSearchResult+json"
	mediaCreationSearch    = "application/vnd.org.jfrog.artifactory.search.ArtifactCreationResult+json"
	mediaDatesSearch       = "application/vnd.org.jfrog.artifactory.search.ArtifactResult+json"
	mediaPatternSearch     = "application/vnd.org.jfrog.artifactory.search.PatternResultFileSet+json"
	mediaPropertySearch    = "application/vnd.org.jfrog.artifactory.search.MetadataSearchResult+json"
	mediaUsageSearch       = "application/vnd.org.jfrog.artifactory.search.ArtifactUsageResult+json"
)

const (
	// propertySearchPath is the one search whose parameters are not fixed:
	// the router's request details keep its raw query, which the generated
	// binding drops.
	propertySearchPath = "/artifactory/api/search/prop"
	// badChecksumCap is the documented ceiling of the bad checksum search,
	// which the user query limit does not apply to.
	badChecksumCap = 10000
)

// searchResponse is a search's 200, in the vendor media type when the
// client's Accept names it: Artifactory's resources produce both.
type searchResponse struct {
	media string
	body  any
}

func answer(ctx context.Context, vendor string, body any) searchResponse {
	media := "application/json"
	if httpkit.Accepts(ctx, vendor) {
		media = vendor
	}
	return searchResponse{media: media, body: body}
}

func (r searchResponse) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", r.media)
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(r.body) //nolint:wrapcheck // the response is already committed
}

func (r searchResponse) VisitSearchArtifactResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r searchResponse) VisitSearchBadChecksumResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r searchResponse) VisitSearchChecksumResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r searchResponse) VisitSearchCreationResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r searchResponse) VisitSearchDatesResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r searchResponse) VisitSearchPatternResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r searchResponse) VisitSearchPropertyResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r searchResponse) VisitSearchUsageResponse(w http.ResponseWriter) error {
	return r.write(w)
}

// results is the {"results": [...]} body every search but pattern answers.
func results(entries []map[string]any) map[string]any {
	return map[string]any{"results": entries}
}

// searchRun is one GET search: the search core's run, with the handler's
// store, whose builds the build searches read, and the caller's access,
// which the result info and the upstream search read.
type searchRun struct {
	*search.SearchRun
	s   ArtifactoryAPI
	st  Store
	acc itemkit.Access
}

// searchAccess is the search core's view of what the caller may read: the
// request's decider for the repositories they may see, and readable, which
// the AQL search shares, for the rows of each page.
type searchAccess struct {
	acc itemkit.Access
	az  accessapi.Authorizer
	p   kernel.Principal
}

// Visible reports whether the caller may see c.
func (a searchAccess) Visible(c repo.Config) bool { return a.acc.Visible(c) }

// Readable keeps the hits the caller may read that their repository's
// patterns accept (readable).
func (a searchAccess) Readable(
	ctx context.Context,
	configs map[string]repo.Config,
	hits []aql.Hit,
) ([]aql.Hit, error) {
	return readable(ctx, a.az, a.p, configs, hits)
}

// newSearch starts a search. named refuses anonymous callers with 401, for
// the searches JFrog documents as needing a non-anonymous user.
func (s ArtifactoryAPI) newSearch(ctx context.Context, named bool) (*searchRun, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	p, err := httpkit.Principal(ctx)
	if named {
		p, err = kernel.RequireAuthenticated(ctx)
	}
	if err != nil {
		return nil, err //nolint:wrapcheck // apierror values, rendered by status
	}
	d, _ := accessapi.DeciderFrom(ctx)
	acc, err := itemkit.AccessFor(ctx, s.cfg.Access.Authorizer, p, d)
	if err != nil {
		return nil, s.InternalError(ctx, "search permissions", err)
	}
	run, err := search.NewSearch(
		ctx,
		st,
		search.Limits{
			UserQueryLimit:     s.cfg.Search.UserQueryLimit,
			LimitInternalUsers: s.cfg.Search.LimitInternalUsers,
			MaxResults:         s.cfg.AQL.MaxResults,
		},
		p,
		searchAccess{acc: acc, az: s.cfg.Access.Authorizer, p: p},
		(&item.Service{Store: st}).Layout,
	)
	if err != nil {
		return nil, s.InternalError(ctx, "search repositories", err)
	}
	run.Internal = s.InternalError
	return &searchRun{SearchRun: run, s: s, st: st, acc: acc}, nil
}

// uri is the storage API URL of an item under the key clients see for it;
// a repository root, which the property search finds by the properties set
// on it, is the repository's URL with no trailing slash.
func (r *searchRun) uri(ctx context.Context, it types.Item) string {
	u := baseurl.From(ctx) + storagePrefix + it.ReportedRepoKey()
	if p := it.FullPath(); p != "" {
		u += "/" + httpkit.EscapePath(p)
	}
	return u
}

// resultDetail is what X-Result-Detail asks for beyond each result's uri.
type resultDetail struct{ shouldIncludeInfo, shouldIncludeProperties bool }

// detailOf reads the header. Artifactory looks for the two words, so a
// value spelled differently from the documented ones still selects them.
func detailOf[T ~string](h *T) resultDetail {
	var d resultDetail
	if h == nil {
		return d
	}
	for _, part := range strings.Split(string(*h), ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "info":
			d.shouldIncludeInfo = true
		case "properties":
			d.shouldIncludeProperties = true
		}
	}
	return d
}

// include is the query include that loads what the detail renders.
func (d resultDetail) include() []string {
	if d.shouldIncludeProperties {
		return []string{"property"}
	}
	return nil
}

// entries renders hits as uri results, widened to the file or folder info
// document and the item's properties as d asks. An item without properties
// has no properties field.
func (r *searchRun) entries(
	ctx context.Context,
	hits []aql.Hit,
	d resultDetail,
) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		e := map[string]any{"uri": r.uri(ctx, h.Item)}
		if d.shouldIncludeInfo {
			c := r.Configs[h.Item.RepoKey]
			v := storageView{
				s: r.s, st: r.st, key: h.Item.ReportedRepoKey(), repo: c,
				isCached: h.Item.Cached, acc: r.acc,
			}
			_, body, err := v.info(ctx, itemkit.Found{Repo: c, Item: h.Item})
			if err != nil {
				return nil, r.s.InternalError(ctx, "search result info", err)
			}
			e, _ = body.(map[string]any)
		}
		if d.shouldIncludeProperties && len(h.Properties) > 0 {
			e["properties"] = h.Properties
		}
		out = append(out, e)
	}
	return out, nil
}

// uriSearch runs a search answered with uri results.
func (r *searchRun) uriSearch(
	ctx context.Context,
	criteria aql.Node,
	d resultDetail,
	vendor string,
) (searchResponse, error) {
	hits, err := r.Collect(ctx, criteria, d.include(), r.UserLimit(), nil)
	if err != nil {
		return searchResponse{}, err
	}
	entries, err := r.entries(ctx, hits, d)
	if err != nil {
		return searchResponse{}, err
	}
	return answer(ctx, vendor, results(entries)), nil
}

// SearchArtifact answers the quick search: files whose name contains name,
// in local repositories and remote caches. Like AQL's $match, which it
// compiles to, the match is case-sensitive.
func (s ArtifactoryAPI) SearchArtifact(
	ctx context.Context,
	req artgen.SearchArtifactRequestObject,
) (artgen.SearchArtifactResponseObject, error) {
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	name := req.Params.Name
	if name == "" {
		return nil, httpkit.BadRequestf("name must not be empty")
	}
	scope, _ := r.RepoScope(req.Params.Repos, false)
	return r.uriSearch(ctx, search.All(search.Files(), scope, &aql.FieldCriterion{
		Field: aql.FieldName, Op: aql.OpMatch, Text: "*" + name + "*",
	}), detailOf(req.Params.XResultDetail), mediaArtifactSearch)
}

// SearchProperty answers the property search: items, folders included,
// carrying every property the query names, with any of its comma-separated
// values or with any value at all when it has none. A value with * or ?
// matches as a wildcard pattern.
func (s ArtifactoryAPI) SearchProperty(
	ctx context.Context,
	req artgen.SearchPropertyRequestObject,
) (artgen.SearchPropertyResponseObject, error) {
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	criteria, err := propertyCriteria(httpkit.RawSearchQuery(ctx))
	if err != nil {
		return nil, err
	}
	scope, _ := r.RepoScope(req.Params.Repos, true)
	return r.uriSearch(ctx, search.All(scope, criteria), detailOf(req.Params.XResultDetail),
		mediaPropertySearch)
}

// propertyCriteria reads every query parameter but repos as a property, as
// propertyFilters does, and needs at least one.
func propertyCriteria(raw string) (aql.Node, error) {
	q, err := rawQuery(raw)
	if err != nil {
		return nil, err
	}
	delete(q, "repos")
	return propertyFilters(q, true)
}

// propertyFilters reads q as properties: one criterion per key, holding
// when the item has the key with one of its values. A key given twice pools
// its values. need refuses a q without keys.
func propertyFilters(q url.Values, need bool) (aql.And, error) {
	keys := make([]string, 0, len(q))
	for k := range q {
		if k == "" {
			return nil, httpkit.BadRequestf("a property key must not be empty")
		}
		keys = append(keys, k)
	}
	if need && len(keys) == 0 {
		return nil, httpkit.BadRequestf("name at least one property to search for")
	}
	slices.Sort(keys)
	out := make(aql.And, 0, len(keys))
	for _, k := range keys {
		var values []string
		for _, v := range q[k] {
			for _, one := range pkgroute.SplitValues(v) {
				if one != "" {
					values = append(values, one)
				}
			}
		}
		if len(values) == 0 {
			values = []string{"*"}
		}
		either := make(aql.Or, 0, len(values))
		for _, v := range values {
			op := aql.OpEq
			if strings.ContainsAny(v, "*?") {
				op = aql.OpMatch
			}
			either = append(either,
				&aql.PropertyCriterion{Part: aql.PartPair, Key: k, Op: op, Value: v})
		}
		out = append(out, either)
	}
	return out, nil
}

// checksumHex is a hex digest of the length its algorithm gives.
var checksumHex = map[string]*regexp.Regexp{
	"md5":    regexp.MustCompile(`^[0-9a-f]{32}$`),
	"sha1":   regexp.MustCompile(`^[0-9a-f]{40}$`),
	"sha256": regexp.MustCompile(`^[0-9a-f]{64}$`),
}

// SearchChecksum answers the checksum search: files whose content has every
// checksum given, in local repositories, remote caches and virtual
// repositories' members.
func (s ArtifactoryAPI) SearchChecksum(
	ctx context.Context,
	req artgen.SearchChecksumRequestObject,
) (artgen.SearchChecksumResponseObject, error) {
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	crit := aql.And{search.Files()}
	for _, c := range []struct {
		name  string
		field aql.Field
		value *string
	}{
		{"md5", aql.FieldActualMD5, req.Params.Md5},
		{"sha1", aql.FieldActualSHA1, req.Params.Sha1},
		{"sha256", aql.FieldSHA256, req.Params.Sha256},
	} {
		if c.value == nil || *c.value == "" {
			continue
		}
		v := strings.ToLower(*c.value)
		if !checksumHex[c.name].MatchString(v) {
			return nil, httpkit.BadRequestf("%s is not a hex %s checksum", *c.value, c.name)
		}
		crit = append(crit, &aql.FieldCriterion{Field: c.field, Op: aql.OpEq, Text: v})
	}
	if len(crit) == 1 {
		return nil, httpkit.BadRequestf("name one of md5, sha1 or sha256")
	}
	scope, _ := r.RepoScope(req.Params.Repos, true)
	return r.uriSearch(ctx, search.All(append(crit, scope)...), detailOf(req.Params.XResultDetail),
		mediaChecksumSearch)
}

// SearchBadChecksum answers the bad checksum search: files whose checksum
// of the type asked for was not declared by the uploading client, or was
// declared and differs from the computed one. AQL cannot compare two
// fields, so the files in scope are read and compared here.
func (s ArtifactoryAPI) SearchBadChecksum(
	ctx context.Context,
	req artgen.SearchBadChecksumRequestObject,
) (artgen.SearchBadChecksumResponseObject, error) {
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	of := func(c types.Checksums) string { return c.MD5 }
	switch req.Params.Type {
	case artgen.Md5:
	case artgen.Sha1:
		of = func(c types.Checksums) string { return c.SHA1 }
	default:
		return nil, httpkit.BadRequestf("type must be md5 or sha1")
	}
	scope, _ := r.RepoScope(req.Params.Repos, true)
	hits, err := r.Collect(ctx, search.All(search.Files(), scope), nil, r.Capped(badChecksumCap),
		func(h aql.Hit) bool { return of(h.Item.Original) != of(h.Item.Actual) })
	if err != nil {
		return nil, err
	}
	entries := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		entries = append(entries, map[string]any{
			"uri":       r.uri(ctx, h.Item),
			"serverMd5": of(h.Item.Actual),
			"clientMd5": of(h.Item.Original),
		})
	}
	return answer(ctx, mediaBadChecksumSearch, results(entries)), nil
}

// window reads a search's [from, to] bounds; a missing to is now.
func (r *searchRun) window(from time.Time, to *time.Time) (time.Time, time.Time, error) {
	end := r.Now
	if to != nil {
		end = *to
	}
	if from.After(end) {
		return from, end, httpkit.BadRequestf(
			"from %s is after to %s",
			httpkit.FormatTime(from),
			httpkit.FormatTime(end),
		)
	}
	return from, end, nil
}

// millis converts an optional epoch-milliseconds parameter.
func millis(ms *int64) *time.Time {
	if ms == nil {
		return nil
	}
	t := time.UnixMilli(*ms).UTC()
	return &t
}

// SearchCreation answers the creation search: files created in [from, to],
// in local repositories and remote caches. A repos key naming no
// repository the caller may see is the documented 404.
func (s ArtifactoryAPI) SearchCreation(
	ctx context.Context,
	req artgen.SearchCreationRequestObject,
) (artgen.SearchCreationResponseObject, error) {
	r, err := s.newSearch(ctx, true)
	if err != nil {
		return nil, err
	}
	from, to, err := r.window(time.UnixMilli(req.Params.From).UTC(), millis(req.Params.To))
	if err != nil {
		return nil, err
	}
	scope, missing := r.RepoScope(req.Params.Repos, false)
	if len(missing) > 0 {
		return nil, itemkit.NotFoundRepo(missing[0])
	}
	hits, err := r.Collect(
		ctx,
		search.All(search.Files(), scope, search.Within(aql.FieldCreated, from, to)),
		nil,
		r.UserLimit(),
		nil,
	)
	if err != nil {
		return nil, err
	}
	entries := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		entries = append(entries, map[string]any{
			"uri": r.uri(ctx, h.Item), "created": httpkit.FormatTime(h.Item.Created),
		})
	}
	return answer(ctx, mediaCreationSearch, results(entries)), nil
}

// dateFields are the fields the dates search tests and returns.
var dateFields = []string{"created", "lastModified", "lastDownloaded"}

// SearchDates answers the dates search: files for which any of the named
// date fields lies in [from, to], in local repositories and remote caches,
// each with the named fields it has.
func (s ArtifactoryAPI) SearchDates(
	ctx context.Context,
	req artgen.SearchDatesRequestObject,
) (artgen.SearchDatesResponseObject, error) {
	r, err := s.newSearch(ctx, true)
	if err != nil {
		return nil, err
	}
	var fields []string
	for _, f := range strings.Split(req.Params.DateFields, ",") {
		f = strings.TrimSpace(f)
		if !slices.Contains(dateFields, f) {
			return nil, httpkit.BadRequestf("dateFields: %q is not one of %s", f,
				strings.Join(dateFields, ", "))
		}
		if !slices.Contains(fields, f) {
			fields = append(fields, f)
		}
	}
	start, err := instantParam("from", req.Params.From)
	if err != nil {
		return nil, err
	}
	var end *time.Time
	if req.Params.To != nil {
		t, err := instantParam("to", *req.Params.To)
		if err != nil {
			return nil, err
		}
		end = &t
	}
	from, to, err := r.window(start, end)
	if err != nil {
		return nil, err
	}
	var include []string
	either := aql.Or{}
	for _, f := range fields {
		switch f {
		case "created":
			either = append(either, search.Within(aql.FieldCreated, from, to))
		case "lastModified":
			either = append(either, search.Within(aql.FieldModified, from, to))
		default:
			include = []string{"stat"}
			either = append(either, aql.And{
				&aql.StatCriterion{Field: aql.StatDownloaded, Op: aql.OpGte, Time: from},
				&aql.StatCriterion{Field: aql.StatDownloaded, Op: aql.OpLte, Time: to},
			})
		}
	}
	scope, _ := r.RepoScope(req.Params.Repos, false)
	hits, err := r.Collect(
		ctx,
		search.All(search.Files(), scope, either),
		include,
		r.UserLimit(),
		nil,
	)
	if err != nil {
		return nil, err
	}
	entries := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		e := map[string]any{"uri": r.uri(ctx, h.Item)}
		for _, f := range fields {
			switch f {
			case "created":
				putTime(e, f, h.Item.Created)
			case "lastModified":
				putTime(e, f, h.Item.Modified)
			default:
				if h.Stats != nil {
					putTime(e, f, h.Stats.LastDownloaded)
				}
			}
		}
		entries = append(entries, e)
	}
	return answer(ctx, mediaDatesSearch, results(entries)), nil
}

// instantLayouts are the ISO 8601 forms the dates search reads; a time
// without an offset is UTC. Fractional seconds are accepted by each.
var instantLayouts = []string{
	time.RFC3339, "2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05",
	"2006-01-02T15:04Z07:00", "2006-01-02T15:04", "2006-01-02",
}

// instantParam reads a dates bound: epoch milliseconds or ISO 8601.
func instantParam(name, v string) (time.Time, error) {
	if ms, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.UnixMilli(ms).UTC(), nil
	}
	// An offset's "+" reaches here as a space when the client left it
	// unescaped in the query, and a space is in no ISO 8601 form.
	v = strings.ReplaceAll(v, " ", "+")
	for _, layout := range instantLayouts {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, httpkit.BadRequestf(
		"%s must be epoch milliseconds or an ISO 8601 time, not %q",
		name,
		v,
	)
}

// SearchUsage answers the usage search: files created before createdBefore
// (notUsedSince by default) and not downloaded since notUsedSince, never
// downloaded included, in local repositories and remote caches.
func (s ArtifactoryAPI) SearchUsage(
	ctx context.Context,
	req artgen.SearchUsageRequestObject,
) (artgen.SearchUsageResponseObject, error) {
	r, err := s.newSearch(ctx, true)
	if err != nil {
		return nil, err
	}
	since := time.UnixMilli(req.Params.NotUsedSince).UTC()
	before := since
	if req.Params.CreatedBefore != nil {
		before = time.UnixMilli(*req.Params.CreatedBefore).UTC()
	}
	scope, _ := r.RepoScope(req.Params.Repos, false)
	hits, err := r.Collect(ctx, search.All(
		search.Files(), scope,
		&aql.FieldCriterion{Field: aql.FieldCreated, Op: aql.OpLt, Time: before},
		aql.Or{
			&aql.StatCriterion{Field: aql.StatDownloaded, Op: aql.OpEq, Null: true},
			&aql.StatCriterion{Field: aql.StatDownloaded, Op: aql.OpLt, Time: since},
		},
	), []string{"stat"}, r.UserLimit(), nil)
	if err != nil {
		return nil, err
	}
	entries := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		e := map[string]any{"uri": r.uri(ctx, h.Item)}
		if h.Stats != nil {
			putTime(e, "lastDownloaded", h.Stats.LastDownloaded)
		}
		entries = append(entries, e)
	}
	return answer(ctx, mediaUsageSearch, results(entries)), nil
}

// SearchPattern answers the pattern search: the files of one local
// repository or remote cache whose path matches an Ant pattern, given as
// repo-key:pattern, whose * and ? stay within a segment. ** is refused to
// keep the search bounded, and so is a trailing /, which Ant reads as **.
// A key that names no such repository the caller may see finds nothing,
// as an unknown repos key does in the other searches.
func (s ArtifactoryAPI) SearchPattern(
	ctx context.Context,
	req artgen.SearchPatternRequestObject,
) (artgen.SearchPatternResponseObject, error) {
	r, err := s.newSearch(ctx, true)
	if err != nil {
		return nil, err
	}
	key, pattern, ok := strings.Cut(req.Params.Pattern, ":")
	pattern = strings.TrimPrefix(pattern, "/")
	switch {
	case !ok || key == "":
		return nil, httpkit.BadRequestf(
			"pattern must be repo-key:path/pattern, not %q",
			req.Params.Pattern,
		)
	case strings.Contains(pattern, "**"):
		return nil, httpkit.BadRequestf("pattern search does not support **")
	case pattern == "" || strings.HasSuffix(pattern, "/"):
		return nil, httpkit.BadRequestf("pattern must end in a file name pattern, not %q", pattern)
	}
	segs := strings.Split(pattern, "/")
	if slices.Contains(segs, "") {
		return nil, httpkit.BadRequestf("pattern %q has an empty path segment", pattern)
	}
	paths := []string{}
	scope, _ := r.RepoScope(&key, false)
	if scope, ok := scope.(aql.Or); ok && len(scope) == 1 {
		dir := types.RootPath
		if len(segs) > 1 {
			dir = strings.Join(segs[:len(segs)-1], "/")
		}
		// $match's * crosses "/", so the query narrows by depth and globs,
		// and the Ant pattern decides.
		ant := antpath.CompilePattern(pattern)
		hits, err := r.Collect(ctx, search.All(
			search.Files(), scope,
			&aql.FieldCriterion{Field: aql.FieldDepth, Op: aql.OpEq, Number: int64(len(segs))},
			&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: dir},
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpMatch, Text: segs[len(segs)-1]},
		), nil, r.Capped(0), func(h aql.Hit) bool { return ant.Match(h.Item.FullPath()) })
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			paths = append(paths, h.Item.FullPath())
		}
	}
	return answer(ctx, mediaPatternSearch, map[string]any{
		"repositoryUri": baseurl.From(ctx) + "/artifactory/" + key,
		"sourcePattern": req.Params.Pattern,
		"files":         paths,
	}), nil
}

// KeepsSearchQuery reports the searches whose raw query the router's
// request details keep: the property search, whose parameters are property
// keys, and the GAVC search, whose older callers send remote=0/1
// (gavcRemote). The generated binding drops both.
func KeepsSearchQuery(r *http.Request) bool {
	return r.URL.Path == propertySearchPath || r.URL.Path == "/artifactory/api/search/gavc"
}
