package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/build"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The Builds API (decision 0014, research 22 §3): internal/service/build
// does the work, and these handlers bind its requests and render its
// answers. Promotion and retention are #166 and stay unimplemented.

const buildsPath = "/artifactory/api/build"

// buildAccess answers the build service's permission questions from the
// request's evaluator: the build resource on the build-info repository,
// and the artifact resource on an artifact's item.
type buildAccess struct{ access }

func (b buildAccess) Build(repoKey, path string, folder bool, want types.Actions) bool {
	t := authz.Repo{Key: repoKey, Class: string(repo.Local)}
	if folder {
		return b.d.AllowsFolder(types.ResourceBuild, t, path, want)
	}
	return b.d.Allows(types.ResourceBuild, t, path, want)
}

func (b buildAccess) Item(c repo.Config, path string, want types.Actions) bool {
	return b.file(c, path, want)
}

// buildCaller returns the build service, the caller and its permissions.
func (s artifactoryService) buildCaller(
	ctx context.Context,
) (*build.Service, auth.Principal, buildAccess, error) {
	st, err := s.content()
	if err != nil {
		return nil, auth.Principal{}, buildAccess{}, err
	}
	p, a, err := s.itemCaller(ctx, st)
	return &build.Service{Store: st, Blobs: s.cfg.Blobs}, p, buildAccess{a}, err
}

// buildError maps the build service's errors onto statuses; missing is the
// 404 message for a build or run that does not exist.
func (s artifactoryService) buildError(
	ctx context.Context,
	p auth.Principal,
	project, missing string,
	err error,
) error {
	switch {
	case errors.Is(err, build.ErrForbidden):
		return denied(p)
	case errors.Is(err, build.ErrInvalid):
		return badRequest("%s", strings.TrimPrefix(err.Error(), build.ErrInvalid.Error()+": "))
	case errors.Is(err, build.ErrNoRepository):
		return notFoundRepo(types.BuildInfoRepo(build.ProjectKey(project)))
	case errors.Is(err, build.ErrConflict):
		return apierror.New(http.StatusConflict, err.Error())
	case errors.Is(err, build.ErrNotFound):
		return apierror.New(http.StatusNotFound, missing)
	}
	return s.itemError(ctx, "builds", err)
}

func noBuild(name string) string { return "No build was found for build name: " + name }

func noRun(name, number string) string {
	return fmt.Sprintf("No build was found for build name: %s, build number: %s", name, number)
}

// started reads an optional started parameter; nil or empty is zero, which
// selects a number's latest run.
func started(s *string) (time.Time, error) {
	if deref(s) == "" {
		return time.Time{}, nil
	}
	t, err := build.ParseStarted(*s)
	if err != nil {
		return t, badRequest("%s", strings.TrimPrefix(err.Error(), build.ErrInvalid.Error()+": "))
	}
	return t, nil
}

func buildURI(ctx context.Context, segments ...string) string {
	u := baseurl.From(ctx) + buildsPath
	for _, s := range segments {
		u += "/" + url.PathEscape(s)
	}
	return u
}

// GetAllBuilds lists the build names the caller may read. It answers 200
// with an empty list when there are none (decision 0014). The uri of each
// entry is the name as it is, not escaped, as the JFrog CLI reads it.
func (s artifactoryService) GetAllBuilds(
	ctx context.Context,
	req artgen.GetAllBuildsRequestObject,
) (artgen.GetAllBuildsResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	project := deref(req.Params.Project)
	names, err := svc.Names(ctx, project, a)
	if err != nil {
		return nil, s.buildError(ctx, p, project, "", err)
	}
	list := make([]artgen.BuildSummary, 0, len(names))
	for _, n := range names {
		list = append(list, artgen.BuildSummary{
			Uri: ptrTo("/" + n.Name), LastStarted: ptrTo(build.FormatStarted(n.LastStarted)),
		})
	}
	return artgen.GetAllBuilds200JSONResponse{Uri: ptrTo(buildURI(ctx)), Builds: &list}, nil
}

// UploadBuild publishes a build-info document. The handler reads the raw
// body requestDetails kept rather than the decoded one, which would drop
// the fields the spec does not list.
func (s artifactoryService) UploadBuild(
	ctx context.Context,
	req artgen.UploadBuildRequestObject,
) (artgen.UploadBuildResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	project := deref(req.Params.Project)
	pub, err := svc.Publish(ctx, project, rawBody(ctx), p.Name, a)
	if err != nil {
		return nil, s.buildError(ctx, p, project, "", err)
	}
	return artgen.UploadBuild200Response{
		Headers: artgen.UploadBuild200ResponseHeaders{XChecksumSha256: &pub.SHA256},
	}, nil
}

// GetBuildRuns lists a build's runs, a number once per run, newest first.
// The uri of each is the number as it is: jf rt bp --overwrite counts the
// runs of a number by comparing it with the uri less its slash.
func (s artifactoryService) GetBuildRuns(
	ctx context.Context,
	req artgen.GetBuildRunsRequestObject,
) (artgen.GetBuildRunsResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	project := deref(req.Params.Project)
	runs, err := svc.Runs(ctx, project, req.BuildName, a)
	if err != nil {
		return nil, s.buildError(ctx, p, project, noBuild(req.BuildName), err)
	}
	list := make([]artgen.BuildNumber, 0, len(runs))
	for _, r := range runs {
		list = append(list, artgen.BuildNumber{
			Uri: ptrTo("/" + r.Number), Started: ptrTo(build.FormatStarted(r.Started)),
		})
	}
	return artgen.GetBuildRuns200JSONResponse{
		Uri: ptrTo(buildURI(ctx, req.BuildName)), BuildsNumbers: &list,
	}, nil
}

// GetBuildInfo returns a run's document. diff is accepted and ignored.
func (s artifactoryService) GetBuildInfo(
	ctx context.Context,
	req artgen.GetBuildInfoRequestObject,
) (artgen.GetBuildInfoResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	at, err := started(req.Params.Started)
	if err != nil {
		return nil, err
	}
	project := deref(req.Params.Project)
	b, err := svc.Run(ctx, project, req.BuildName, req.BuildNumber, at, a)
	if err != nil {
		return nil, s.buildError(ctx, p, project, noRun(req.BuildName, req.BuildNumber), err)
	}
	return buildInfoResponse{
		uri: buildURI(ctx, req.BuildName, req.BuildNumber), doc: b.Document,
	}, nil
}

// buildInfoResponse writes a run's document as it was published, which
// artgen.BuildInfo would trim to the fields the spec lists.
type buildInfoResponse struct {
	uri string
	doc []byte
}

func (r buildInfoResponse) VisitGetBuildInfoResponse(w http.ResponseWriter) error {
	uri, err := json.Marshal(r.uri)
	if err != nil {
		return fmt.Errorf("build info uri: %w", err)
	}
	doc := r.doc
	if len(doc) == 0 {
		doc = []byte("{}")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err = fmt.Fprintf(w, `{"uri":%s,"buildInfo":%s}`, uri, doc)
	return err //nolint:wrapcheck // the response writer's own error
}

// AppendBuild adds modules to a run. Like the published document, the
// modules are read from the raw body, so their unlisted fields survive.
func (s artifactoryService) AppendBuild(
	ctx context.Context,
	req artgen.AppendBuildRequestObject,
) (artgen.AppendBuildResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	project := deref(req.Params.Project)
	if want := types.BuildInfoRepo(build.ProjectKey(project)); deref(req.Params.BuildRepo) != "" &&
		*req.Params.BuildRepo != want {
		return nil, badRequest(
			"The build-info repository '%s' does not hold this project's builds; '%s' does",
			*req.Params.BuildRepo, want)
	}
	at, err := started(req.Params.Started)
	if err != nil {
		return nil, err
	}
	err = svc.Append(ctx, build.Append{
		ProjectKey: project, Name: req.BuildName, Number: req.BuildNumber, Started: at,
		Modules: rawBody(ctx), By: p.Name,
	}, a)
	if err != nil {
		return nil, s.buildError(ctx, p, project, noRun(req.BuildName, req.BuildNumber), err)
	}
	return artgen.AppendBuild204Response{}, nil
}

// RenameBuild moves every run of a build to another name.
func (s artifactoryService) RenameBuild(
	ctx context.Context,
	req artgen.RenameBuildRequestObject,
) (artgen.RenameBuildResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	project, to := deref(req.Params.Project), req.Params.To
	if err := svc.Rename(ctx, project, req.BuildName, to, p.Name, a); err != nil {
		if errors.Is(err, build.ErrConflict) {
			return nil, apierror.New(http.StatusConflict,
				fmt.Sprintf("A build named '%s' already exists", to))
		}
		return nil, s.buildError(ctx, p, project, noBuild(req.BuildName), err)
	}
	return artgen.RenameBuild200TextResponse(
		fmt.Sprintf("Build '%s' was renamed to '%s'.", req.BuildName, to),
	), nil
}

// DeleteBuilds deletes runs of a build named in the path. buildNumbers is
// comma-separated, and a number listed twice deletes two runs.
func (s artifactoryService) DeleteBuilds(
	ctx context.Context,
	req artgen.DeleteBuildsRequestObject,
) (artgen.DeleteBuildsResponseObject, error) {
	var numbers []string
	for n := range strings.SplitSeq(deref(req.Params.BuildNumbers), ",") {
		if n = strings.TrimSpace(n); n != "" {
			numbers = append(numbers, n)
		}
	}
	msg, err := s.deleteBuilds(ctx, build.Delete{
		Name: req.BuildName, Numbers: numbers,
		All:       deref(req.Params.DeleteAll) == 1,
		Artifacts: deref(req.Params.Artifacts) == 1,
	})
	if err != nil {
		return nil, err
	}
	return artgen.DeleteBuilds200TextResponse(msg), nil
}

// DeleteBuildsMultiple is the body form of DeleteBuilds, which jf rt bp
// --overwrite sends with a number once per existing run.
func (s artifactoryService) DeleteBuildsMultiple(
	ctx context.Context,
	req artgen.DeleteBuildsMultipleRequestObject,
) (artgen.DeleteBuildsMultipleResponseObject, error) {
	msg, err := s.deleteBuilds(ctx, build.Delete{
		ProjectKey: deref(req.Body.Project), Name: req.Body.BuildName,
		Numbers: derefSlice(req.Body.BuildNumbers), All: deref(req.Body.DeleteAll),
		Artifacts: deref(req.Body.DeleteArtifacts),
	})
	if err != nil {
		return nil, err
	}
	return artgen.DeleteBuildsMultiple200TextResponse(msg), nil
}

func (s artifactoryService) deleteBuilds(ctx context.Context, d build.Delete) (string, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return "", err
	}
	d.By = p.Name
	gone, err := svc.Delete(ctx, d, a)
	if err != nil {
		return "", s.buildError(ctx, p, d.ProjectKey, noBuild(d.Name), err)
	}
	numbers := make([]string, 0, len(gone))
	for _, r := range gone {
		numbers = append(numbers, r.Number)
	}
	return fmt.Sprintf("Deleted %d runs of build '%s', numbers: %s.",
		len(gone), d.Name, strings.Join(numbers, ", ")), nil
}

// GetBuildArtifacts lists where the artifacts of a number's latest run are.
// The JFrog CLI calls it before its AQL fallback, and reads only repo, path
// and name (research 22 §3.1).
func (s artifactoryService) GetBuildArtifacts(
	ctx context.Context,
	req artgen.GetBuildArtifactsRequestObject,
) (artgen.GetBuildArtifactsResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	project, ok := buildInfoProject(req.BuildRepo)
	if !ok {
		return nil, notFoundRepo(req.BuildRepo)
	}
	items, err := svc.Artifacts(ctx, project, req.BuildName, req.BuildNumber, a)
	if err != nil {
		return nil, s.buildError(ctx, p, project, noRun(req.BuildName, req.BuildNumber), err)
	}
	out := make(artgen.GetBuildArtifacts200JSONResponse, 0, len(items))
	for _, it := range items {
		out = append(out, artgen.BuildArtifact{
			Repo: ptrTo(it.ReportedRepoKey()), Path: ptrTo(it.Path), Name: ptrTo(it.Name),
		})
	}
	return out, nil
}

// buildInfoProject is the project whose build-info repository key is
// repoKey (types.BuildInfoRepo).
func buildInfoProject(repoKey string) (string, bool) {
	if repoKey == types.BuildInfoRepository {
		return "", true
	}
	p, ok := strings.CutSuffix(repoKey, "-build-info")
	return p, ok && p != ""
}
