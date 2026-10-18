package server

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

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The GET searches (decision 0012, research 22 §2) build an items query and
// run it through the store's AQL path, so they share its compilation to each
// dialect and its parity fixtures; this file only scopes, filters, caps and
// renders.

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
	// requestDetails keeps its raw query, which the generated binding drops.
	propertySearchPath = "/artifactory/api/search/prop"
	// badChecksumCap is the documented ceiling of the bad checksum search,
	// which the user query limit does not apply to.
	badChecksumCap = 10000
	// searchPage is how many rows one store query reads. Permission
	// filtering runs on each page, so a search keeps reading until it holds
	// its limit of readable results or the rows run out.
	searchPage = 1000
)

// searchResponse is a search's 200, in the vendor media type when the
// client's Accept names it: Artifactory's resources produce both.
type searchResponse struct {
	media string
	body  any
}

func answer(ctx context.Context, vendor string, body any) searchResponse {
	media := "application/json"
	if accepts(ctx, vendor) {
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

// searchRun is one GET search: the caller, what it may read, and the
// repositories as they stand, as configured and as repo criteria resolve.
type searchRun struct {
	s       artifactoryService
	st      store.Store
	p       auth.Principal
	acc     access
	configs map[string]repo.Config
	repos   aql.Repositories
	now     time.Time
}

// newSearch starts a search. named refuses anonymous callers with 401, for
// the searches JFrog documents as needing a non-anonymous user.
func (s artifactoryService) newSearch(ctx context.Context, named bool) (*searchRun, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	p, err := principal(ctx)
	if named {
		p, err = auth.RequireAuthenticated(ctx)
	}
	if err != nil {
		return nil, err //nolint:wrapcheck // apierror values, rendered by status
	}
	acc, err := accessFor(ctx, st, p)
	if err != nil {
		return nil, s.internalError(ctx, "search permissions", err)
	}
	configs, err := repoConfigs(ctx, st)
	if err != nil {
		return nil, s.internalError(ctx, "search repositories", err)
	}
	return &searchRun{
		s: s, st: st, p: p, acc: acc, configs: configs, repos: aqlRepositories(configs),
		now: time.Now().UTC(),
	}, nil
}

// userLimit is the most results the caller may get from a search the user
// query limit applies to, under the AQL hard cap; -1 is no limit.
func (r *searchRun) userLimit() int {
	l := r.s.cfg.Search
	if r.p.Anonymous || (!r.p.Admin && l.LimitInternalUsers) {
		return r.capped(l.UserQueryLimit)
	}
	return r.capped(0)
}

// capped is limit, zero for none, under the AQL hard cap; -1 is no limit.
func (r *searchRun) capped(limit int) int {
	out := -1
	for _, n := range []int{limit, r.s.cfg.AQL.MaxResults} {
		if n > 0 && (out < 0 || n < out) {
			out = n
		}
	}
	return out
}

// collect runs criteria a page at a time, in item order, and returns the
// hits the caller may read that keep accepts (nil accepts all), at most want
// of them (-1 for all). Administrators read everything, as in AQL; anyone
// else gets the rows readable reports, so a page may yield fewer.
func (r *searchRun) collect(
	ctx context.Context,
	criteria aql.Node,
	include []string,
	want int,
	keep func(aql.Hit) bool,
) ([]aql.Hit, error) {
	out := []aql.Hit{}
	for offset := 0; ; offset += searchPage {
		q := &aql.Query{
			Criteria: criteria, Include: include,
			Offset: offset, HasOffset: true, Limit: searchPage, HasLimit: true,
		}
		q.Resolve(r.repos)
		hits, err := r.st.Items().Search(ctx, q, aql.Options{Now: r.now})
		if err != nil {
			return nil, r.s.internalError(ctx, "search", err)
		}
		read := len(hits)
		if !r.p.Admin {
			if hits, err = readable(ctx, r.st, r.p, r.configs, hits); err != nil {
				return nil, r.s.internalError(ctx, "search permissions", err)
			}
		}
		for _, h := range hits {
			if keep != nil && !keep(h) {
				continue
			}
			out = append(out, h)
			if len(out) == want {
				return out, nil
			}
		}
		if read < searchPage {
			return out, nil
		}
	}
}

// repoScope turns a repos parameter into a criterion on the items'
// repositories, nil for every repository, which collect resolves as AQL
// resolves repo criteria: a remote's key, like its cache key, selects its
// cache, and a virtual key its members, through nested virtual
// repositories. A virtual key counts only when virtual is set. Keys that
// name no repository select nothing; missing lists them, with the keys of
// repositories the caller may not see, for the search that refuses them. A
// parameter whose every key selects nothing matches no item.
func (r *searchRun) repoScope(repos *string, virtual bool) (scope aql.Node, missing []string) {
	if repos == nil || strings.TrimSpace(*repos) == "" {
		return nil, nil
	}
	var keys []string
	for _, key := range strings.Split(*repos, ",") {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		// A configured key wins over reading "-cache" as a remote's cache,
		// as AQL resolves it.
		c, ok := r.configs[key]
		if !ok {
			stored, cached := types.ResolveRepoKey(key)
			c, ok = r.configs[stored]
			ok = ok && cached && c.Class == repo.Remote
		}
		switch {
		case !ok:
			missing = append(missing, key)
			continue
		case c.Class == repo.Virtual && !virtual:
			continue
		// Permissions name no virtual repository, so a virtual key is
		// not checked here: readable drops what the caller may not read.
		case c.Class != repo.Virtual && !r.acc.visible(c):
			missing = append(missing, key)
			continue
		}
		if !slices.Contains(keys, c.Key) {
			keys = append(keys, c.Key)
		}
	}
	either := make(aql.Or, 0, len(keys))
	for _, key := range keys {
		either = append(either, &aql.FieldCriterion{Field: aql.FieldRepo, Op: aql.OpEq, Text: key})
	}
	return either, missing
}

// files is the criterion that leaves folders out: a hand-built query has no
// implicit type, unlike a parsed one.
func files() aql.Node {
	return &aql.FieldCriterion{Field: aql.FieldType, Op: aql.OpEq, Text: "file"}
}

// all is the conjunction of the criteria given, skipping nil ones.
func all(nodes ...aql.Node) aql.And {
	out := make(aql.And, 0, len(nodes))
	for _, n := range nodes {
		if n != nil {
			out = append(out, n)
		}
	}
	return out
}

// within is the criterion that a time field lies in [from, to].
func within(f aql.Field, from, to time.Time) aql.Node {
	return aql.And{
		&aql.FieldCriterion{Field: f, Op: aql.OpGte, Time: from},
		&aql.FieldCriterion{Field: f, Op: aql.OpLte, Time: to},
	}
}

// uri is the storage API URL of an item under the key clients see for it;
// a repository root, which the property search finds by the properties set
// on it, is the repository's URL with no trailing slash.
func (r *searchRun) uri(ctx context.Context, it types.Item) string {
	u := baseurl.From(ctx) + storagePrefix + it.ReportedRepoKey()
	if p := it.FullPath(); p != "" {
		u += "/" + escapePath(p)
	}
	return u
}

// resultDetail is what X-Result-Detail asks for beyond each result's uri.
type resultDetail struct{ info, properties bool }

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
			d.info = true
		case "properties":
			d.properties = true
		}
	}
	return d
}

// include is the query include that loads what the detail renders.
func (d resultDetail) include() []string {
	if d.properties {
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
		if d.info {
			c := r.configs[h.Item.RepoKey]
			v := storageView{
				s: r.s, st: r.st, key: h.Item.ReportedRepoKey(), repo: c,
				cached: h.Item.Cached, acc: r.acc,
			}
			_, body, err := v.info(ctx, found{repo: c, item: h.Item})
			if err != nil {
				return nil, r.s.internalError(ctx, "search result info", err)
			}
			e, _ = body.(map[string]any)
		}
		if d.properties && len(h.Properties) > 0 {
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
	hits, err := r.collect(ctx, criteria, d.include(), r.userLimit(), nil)
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
func (s artifactoryService) SearchArtifact(
	ctx context.Context,
	req artgen.SearchArtifactRequestObject,
) (artgen.SearchArtifactResponseObject, error) {
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	name := req.Params.Name
	if name == "" {
		return nil, badRequest("name must not be empty")
	}
	scope, _ := r.repoScope(req.Params.Repos, false)
	return r.uriSearch(ctx, all(files(), scope, &aql.FieldCriterion{
		Field: aql.FieldName, Op: aql.OpMatch, Text: "*" + name + "*",
	}), detailOf(req.Params.XResultDetail), mediaArtifactSearch)
}

// SearchProperty answers the property search: items, folders included,
// carrying every property the query names, with any of its comma-separated
// values or with any value at all when it has none. A value with * or ?
// matches as a wildcard pattern.
func (s artifactoryService) SearchProperty(
	ctx context.Context,
	req artgen.SearchPropertyRequestObject,
) (artgen.SearchPropertyResponseObject, error) {
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	criteria, err := propertyCriteria(rawSearchQuery(ctx))
	if err != nil {
		return nil, err
	}
	scope, _ := r.repoScope(req.Params.Repos, true)
	return r.uriSearch(ctx, all(scope, criteria), detailOf(req.Params.XResultDetail),
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
			return nil, badRequest("a property key must not be empty")
		}
		keys = append(keys, k)
	}
	if need && len(keys) == 0 {
		return nil, badRequest("name at least one property to search for")
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
func (s artifactoryService) SearchChecksum(
	ctx context.Context,
	req artgen.SearchChecksumRequestObject,
) (artgen.SearchChecksumResponseObject, error) {
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	crit := aql.And{files()}
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
			return nil, badRequest("%s is not a hex %s checksum", *c.value, c.name)
		}
		crit = append(crit, &aql.FieldCriterion{Field: c.field, Op: aql.OpEq, Text: v})
	}
	if len(crit) == 1 {
		return nil, badRequest("name one of md5, sha1 or sha256")
	}
	scope, _ := r.repoScope(req.Params.Repos, true)
	return r.uriSearch(ctx, all(append(crit, scope)...), detailOf(req.Params.XResultDetail),
		mediaChecksumSearch)
}

// SearchBadChecksum answers the bad checksum search: files whose checksum
// of the type asked for was not declared by the uploading client, or was
// declared and differs from the computed one. AQL cannot compare two
// fields, so the files in scope are read and compared here.
func (s artifactoryService) SearchBadChecksum(
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
		return nil, badRequest("type must be md5 or sha1")
	}
	scope, _ := r.repoScope(req.Params.Repos, true)
	hits, err := r.collect(ctx, all(files(), scope), nil, r.capped(badChecksumCap),
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
	end := r.now
	if to != nil {
		end = *to
	}
	if from.After(end) {
		return from, end, badRequest("from %s is after to %s", formatTime(from), formatTime(end))
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
func (s artifactoryService) SearchCreation(
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
	scope, missing := r.repoScope(req.Params.Repos, false)
	if len(missing) > 0 {
		return nil, notFoundRepo(missing[0])
	}
	hits, err := r.collect(ctx, all(files(), scope, within(aql.FieldCreated, from, to)), nil,
		r.userLimit(), nil)
	if err != nil {
		return nil, err
	}
	entries := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		entries = append(entries, map[string]any{
			"uri": r.uri(ctx, h.Item), "created": formatTime(h.Item.Created),
		})
	}
	return answer(ctx, mediaCreationSearch, results(entries)), nil
}

// dateFields are the fields the dates search tests and returns.
var dateFields = []string{"created", "lastModified", "lastDownloaded"}

// SearchDates answers the dates search: files for which any of the named
// date fields lies in [from, to], in local repositories and remote caches,
// each with the named fields it has.
func (s artifactoryService) SearchDates(
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
			return nil, badRequest("dateFields: %q is not one of %s", f,
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
			either = append(either, within(aql.FieldCreated, from, to))
		case "lastModified":
			either = append(either, within(aql.FieldModified, from, to))
		default:
			include = []string{"stat"}
			either = append(either, aql.And{
				&aql.StatCriterion{Field: aql.StatDownloaded, Op: aql.OpGte, Time: from},
				&aql.StatCriterion{Field: aql.StatDownloaded, Op: aql.OpLte, Time: to},
			})
		}
	}
	scope, _ := r.repoScope(req.Params.Repos, false)
	hits, err := r.collect(ctx, all(files(), scope, either), include, r.userLimit(), nil)
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
	return time.Time{}, badRequest("%s must be epoch milliseconds or an ISO 8601 time, not %q",
		name, v)
}

// SearchUsage answers the usage search: files created before createdBefore
// (notUsedSince by default) and not downloaded since notUsedSince, never
// downloaded included, in local repositories and remote caches.
func (s artifactoryService) SearchUsage(
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
	scope, _ := r.repoScope(req.Params.Repos, false)
	hits, err := r.collect(ctx, all(
		files(), scope,
		&aql.FieldCriterion{Field: aql.FieldCreated, Op: aql.OpLt, Time: before},
		aql.Or{
			&aql.StatCriterion{Field: aql.StatDownloaded, Op: aql.OpEq, Null: true},
			&aql.StatCriterion{Field: aql.StatDownloaded, Op: aql.OpLt, Time: since},
		},
	), []string{"stat"}, r.userLimit(), nil)
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
func (s artifactoryService) SearchPattern(
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
		return nil, badRequest("pattern must be repo-key:path/pattern, not %q", req.Params.Pattern)
	case strings.Contains(pattern, "**"):
		return nil, badRequest("pattern search does not support **")
	case pattern == "" || strings.HasSuffix(pattern, "/"):
		return nil, badRequest("pattern must end in a file name pattern, not %q", pattern)
	}
	segs := strings.Split(pattern, "/")
	if slices.Contains(segs, "") {
		return nil, badRequest("pattern %q has an empty path segment", pattern)
	}
	paths := []string{}
	scope, _ := r.repoScope(&key, false)
	if scope, ok := scope.(aql.Or); ok && len(scope) == 1 {
		dir := types.RootPath
		if len(segs) > 1 {
			dir = strings.Join(segs[:len(segs)-1], "/")
		}
		// $match's * crosses "/", so the query narrows by depth and globs,
		// and the Ant pattern decides.
		ant := authz.CompilePattern(pattern)
		hits, err := r.collect(ctx, all(
			files(), scope,
			&aql.FieldCriterion{Field: aql.FieldDepth, Op: aql.OpEq, Number: int64(len(segs))},
			&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: dir},
			&aql.FieldCriterion{Field: aql.FieldName, Op: aql.OpMatch, Text: segs[len(segs)-1]},
		), nil, r.capped(0), func(h aql.Hit) bool { return ant.Match(h.Item.FullPath()) })
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
