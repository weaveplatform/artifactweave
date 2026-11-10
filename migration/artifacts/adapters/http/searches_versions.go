package artifactshttp

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/search"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
)

// versionsPrefix is the hand-written latest version by properties search:
// its path spans segments, which a generated route cannot take.
const versionsPrefix = "/artifactory/api/versions/"

// anyTarget is the repo or path segment that stands for every one.
const anyTarget = "_any"

// versionProperty is the property the search compares. Only this lowercase
// spelling counts, as JFrog documents.
const versionProperty = "version"

// RegisterSearchRoutes serves the latest version by properties search,
// GET /api/versions/{repo}/{path}, whose path spans segments, which a
// generated route cannot take. Registered before the router's stubs of the
// hand-written operations, it replaces the 501 stub.
func (s ArtifactoryAPI) RegisterSearchRoutes(r chi.Router) {
	for _, p := range httpkit.ChiPatterns(versionsPrefix + "{repo}/{path}") {
		r.Get(p, s.latestVersionByProperties)
	}
}

// latestVersionByProperties answers GET /api/versions/{repo}/{path}: the
// highest value of the version property, in the Mercury order the versions
// searches use, among the files under path in repo that carry every other
// query parameter as a property, and with listFiles=1 the files that carry
// it, by download URL. It needs a named user, and only files the caller may
// read count.
func (s ArtifactoryAPI) latestVersionByProperties(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	r, err := s.newSearch(ctx, true)
	if err != nil {
		httpkit.WriteErr(w, req, err)
		return
	}
	// A repository key has no slash, so the first one ends it.
	key, folder, _ := strings.Cut(strings.TrimPrefix(req.URL.Path, versionsPrefix), "/")
	folder = strings.Trim(folder, "/")
	q, err := rawQuery(req.URL.RawQuery)
	if err != nil {
		httpkit.WriteErr(w, req, err)
		return
	}
	listFiles := q.Get("listFiles")
	if listFiles != "" && listFiles != "0" && listFiles != "1" {
		httpkit.WriteErr(w, req, httpkit.BadRequestf("listFiles must be 0 or 1"))
		return
	}
	delete(q, "listFiles")
	filters, err := propertyFilters(q, false)
	if err != nil {
		httpkit.WriteErr(w, req, err)
		return
	}
	var scope aql.Node
	if key != anyTarget {
		scope, _ = r.RepoScope(&key, true)
	}
	var within aql.Node
	if folder != anyTarget && folder != "" {
		within = aql.Or{
			&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpEq, Text: folder},
			&aql.FieldCriterion{Field: aql.FieldPath, Op: aql.OpMatch, Text: folder + "/*"},
		}
	}
	criteria := search.All(search.Files(), scope, within, &aql.PropertyCriterion{
		Part: aql.PartPair, Key: versionProperty, Op: aql.OpMatch, Value: "*",
	})
	hits, err := r.Collect(ctx, append(criteria, filters...), []string{"property"}, r.Capped(0),
		func(h aql.Hit) bool {
			// $match's * reads a * or ? in the folder as a wildcard too.
			return within == nil || h.Item.Path == folder ||
				strings.HasPrefix(h.Item.Path, folder+"/")
		})
	if err != nil {
		httpkit.WriteErr(w, req, err)
		return
	}
	best := ""
	for _, h := range hits {
		for _, v := range h.Properties[versionProperty] {
			if best == "" || model.CompareMercury(v, best) > 0 {
				best = v
			}
		}
	}
	if best == "" {
		httpkit.WriteErr(w, req, apierror.New(http.StatusNotFound,
			"Unable to find artifacts with a version property"))
		return
	}
	artifacts := []map[string]any{}
	if listFiles == "1" {
		for _, h := range hits {
			if h.Properties.Has(versionProperty, best) {
				artifacts = append(artifacts, map[string]any{
					"uri": itemkit.DownloadURI(ctx, h.Item.ReportedRepoKey(), h.Item.FullPath()),
				})
			}
		}
	}
	httpkit.WriteJSON(w, http.StatusOK, map[string]any{"version": best, "artifacts": artifacts})
}
