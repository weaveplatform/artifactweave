package server

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/build"
	"github.com/weaveplatform/artifactweave/pkg/aql"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The build searches (decision 0012, research 22 §2) read build-info
// through the build store and service (decision 0014): which runs depend on
// a file, and which files a run produced.

const (
	mediaDependencySearch     = "application/vnd.org.jfrog.artifactory.search.DependencyBuilds+json"
	mediaBuildArtifactsSearch = "application/vnd.org.jfrog.artifactory.search.BuildArtifactsSearchResult+json"
)

func (r searchResponse) VisitSearchDependencyResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r searchResponse) VisitSearchBuildArtifactsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

// SearchDependency answers the dependency search: the build numbers with a
// run the caller may read that lists a dependency with the SHA-1, each
// once, in the order their runs were published.
func (s artifactoryService) SearchDependency(
	ctx context.Context,
	req artgen.SearchDependencyRequestObject,
) (artgen.SearchDependencyResponseObject, error) {
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	sha1 := strings.ToLower(req.Params.Sha1)
	if !checksumHex["sha1"].MatchString(sha1) {
		return nil, badRequest("%s is not a hex sha1 checksum", req.Params.Sha1)
	}
	refs, err := r.st.Builds().FindDependencies(ctx, types.Checksums{SHA1: sha1})
	if err != nil {
		return nil, s.internalError(ctx, "dependency search", err)
	}
	a, limit := buildAccess{r.acc}, r.capped(0)
	seen := map[[3]string]bool{}
	entries := []map[string]any{}
	for _, ref := range refs {
		run := ref.Run
		id := [3]string{run.ProjectKey, run.Name, run.Number}
		if seen[id] {
			continue
		}
		repoKey, doc := types.Build{
			ProjectKey: run.ProjectKey, Name: run.Name, Number: run.Number, Started: run.Started,
		}.DocumentItem()
		if !a.Build(repoKey, doc, false, types.ActionRead) {
			continue
		}
		seen[id] = true
		u := buildURI(ctx, run.Name, run.Number)
		if run.ProjectKey != "" {
			u += "?project=" + url.QueryEscape(run.ProjectKey)
		}
		entries = append(entries, map[string]any{"uri": u})
		if len(entries) == limit {
			break
		}
	}
	return answer(ctx, mediaDependencySearch, results(entries)), nil
}

// SearchBuildArtifacts answers the build artifacts search: the download
// URLs of the files a run of the default project produced, the run chosen
// as build.Service.Find chooses and the files as a promotion finds them
// (build.Service.SearchArtifacts), in the repositories in repos, narrowed to
// the paths any mapping matches in full.
func (s artifactoryService) SearchBuildArtifacts(
	ctx context.Context,
	req artgen.SearchBuildArtifactsRequestObject,
) (artgen.SearchBuildArtifactsResponseObject, error) {
	r, err := s.newSearch(ctx, false)
	if err != nil {
		return nil, err
	}
	body := req.JSONBody
	if body == nil {
		body = req.ApplicationVndOrgJfrogArtifactorySearchBuildArtifactsRequestPlusJSONBody
	}
	if body == nil || body.BuildName == "" || body.BuildNumber == "" {
		return nil, badRequest("buildName and buildNumber are required")
	}
	var mappings []*regexp.Regexp
	for _, m := range deref(body.Mappings) {
		if deref(m.Input) == "" {
			continue
		}
		re, err := regexp.Compile(`^(?:` + *m.Input + `)$`)
		if err != nil {
			return nil, badRequest("mapping %q is not a regular expression: %v", *m.Input, err)
		}
		mappings = append(mappings, re)
	}
	var in func(types.Item) bool
	if refs := r.refsOf(deref(body.Repos)); refs != nil {
		in = func(it types.Item) bool { return refs[aql.RepoRef{Key: it.RepoKey, Cached: it.Cached}] }
	}
	svc, a := &build.Service{Store: r.st, Blobs: s.cfg.Blobs}, buildAccess{r.acc}
	b, err := svc.Find(ctx, "", body.BuildName, body.BuildNumber, deref(body.BuildStatus), a)
	if err != nil {
		return nil, s.buildError(ctx, r.p, "", noRun(body.BuildName, body.BuildNumber), err)
	}
	items, err := svc.SearchArtifacts(ctx, b, in, a)
	if err != nil {
		return nil, s.internalError(ctx, "build artifacts search", err)
	}
	limit := r.capped(0)
	entries := []map[string]any{}
	for _, it := range items {
		full := it.FullPath()
		if len(mappings) > 0 &&
			!slices.ContainsFunc(
				mappings,
				func(re *regexp.Regexp) bool { return re.MatchString(full) },
			) {
			continue
		}
		entries = append(entries, map[string]any{
			"downloadUri": downloadURI(ctx, it.ReportedRepoKey(), full),
		})
		if len(entries) == limit {
			break
		}
	}
	return answer(ctx, mediaBuildArtifactsSearch, results(entries)), nil
}

// refsOf is the stored locations the repository keys stand for, as a repo
// criterion's $eq resolves them; nil for no keys.
func (r *searchRun) refsOf(keys []string) map[aql.RepoRef]bool {
	var out map[aql.RepoRef]bool
	for _, key := range keys {
		if key = strings.TrimSpace(key); key == "" {
			continue
		}
		if out == nil {
			out = map[aql.RepoRef]bool{}
		}
		for _, ref := range r.repos.Expand(key) {
			out[ref] = true
		}
	}
	return out
}
