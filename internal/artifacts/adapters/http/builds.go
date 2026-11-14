package artifactshttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/build"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/baseurl"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
)

// The Builds API (decision 0014, research 22 §3): internal/artifacts/core/build
// does the work, and these handlers bind its requests and render its
// answers.

const buildsPath = "/artifactory/api/build"

// buildAccess answers the build service's permission questions from the
// request's evaluator: the build resource on the build-info repository,
// and the artifact resource on an artifact's item.
type buildAccess struct{ itemkit.Access }

func (b buildAccess) Build(repoKey, path string, folder bool, want accessapi.Actions) bool {
	t := accessapi.Repo{Key: repoKey, Class: string(repo.Local)}
	if folder {
		return b.Decider.AllowsFolder(accessapi.ResourceBuild, t, path, want)
	}
	return b.Decider.Allows(accessapi.ResourceBuild, t, path, want)
}

func (b buildAccess) Item(c repo.Config, path string, want accessapi.Actions) bool {
	return b.File(c, path, want)
}

// buildCaller returns the build service, the caller and its permissions.
func (s ArtifactoryAPI) buildCaller(
	ctx context.Context,
) (*build.Service, kernel.Principal, buildAccess, error) {
	st, err := s.content()
	if err != nil {
		return nil, kernel.Principal{}, buildAccess{}, err
	}
	p, a, err := s.ItemCaller(ctx, st)
	return s.builds(st), p, buildAccess{a}, err
}

// buildError maps the build service's errors onto statuses; missing is the
// 404 message for a build or run that does not exist.
func (s ArtifactoryAPI) buildError(
	ctx context.Context,
	p kernel.Principal,
	project, missing string,
	err error,
) error {
	switch {
	case errors.Is(err, build.ErrForbidden):
		return httpkit.Denied(p)
	case errors.Is(err, build.ErrInvalid):
		return httpkit.BadRequestf(
			"%s",
			strings.TrimPrefix(err.Error(), build.ErrInvalid.Error()+": "),
		)
	case errors.Is(err, build.ErrNoRepository):
		return itemkit.NotFoundRepo(model.BuildInfoRepo(build.ProjectKey(project)))
	case errors.Is(err, build.ErrConflict):
		return apierror.New(http.StatusConflict, err.Error())
	case errors.Is(err, build.ErrNotFound):
		return apierror.New(http.StatusNotFound, missing)
	}
	return s.ItemError(ctx, "builds", err)
}

func noBuild(name string) string { return "No build was found for build name: " + name }

func noRun(name, number string) string {
	return fmt.Sprintf("No build was found for build name: %s, build number: %s", name, number)
}

// started reads an optional started parameter; nil or empty is zero, which
// selects a number's latest run.
func started(s *string) (time.Time, error) {
	if httpkit.Deref(s) == "" {
		return time.Time{}, nil
	}
	t, err := build.ParseStarted(*s)
	if err != nil {
		return t, httpkit.BadRequestf(
			"%s",
			strings.TrimPrefix(err.Error(), build.ErrInvalid.Error()+": "),
		)
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
func (s ArtifactoryAPI) GetAllBuilds(
	ctx context.Context,
	req artgen.GetAllBuildsRequestObject,
) (artgen.GetAllBuildsResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	project := httpkit.Deref(req.Params.Project)
	env, err := httpkit.EnvelopeOf(artgen.GetAllBuilds200JSONResponse{
		Uri: httpkit.PtrTo(buildURI(ctx)), Builds: &[]artgen.BuildSummary{},
	}, "builds")
	if err != nil {
		return nil, s.InternalError(ctx, "list builds", err)
	}
	pages, keep := svc.NamePages(project, a)
	l, err := httpkit.StreamList(ctx, pages,
		func(n model.BuildName) (artgen.BuildSummary, bool, error) {
			return artgen.BuildSummary{
				Uri:         httpkit.PtrTo("/" + n.Name),
				LastStarted: httpkit.PtrTo(build.FormatStarted(n.LastStarted)),
			}, keep(n), nil
		}, httpkit.ListOptions{Envelope: env})
	if err != nil {
		return nil, s.buildError(ctx, p, project, "", err)
	}
	return streamed{l}, nil
}

// UploadBuild publishes a build-info document and answers 204 with the
// stored document's SHA-256 in X-Checksum-Sha256. JFrog's uploadbuild page
// documents 200 and no header, but no 7.x release note or Jira issue
// records a move from the 204 that Artifactory 6.20's
// BuildResource.addBuild answers (RTFACT-20291 reports it too), so the 204
// stays until P19 compares it with 7.x. The JFrog CLI accepts 200, 201 and
// 204, and reads the header for --detailed-summary (jfrog-client-go
// PublishBuildInfo). The handler reads the raw body the router's request
// details kept rather than the decoded one, which would drop the fields the
// spec does not list.
func (s ArtifactoryAPI) UploadBuild(
	ctx context.Context,
	req artgen.UploadBuildRequestObject,
) (artgen.UploadBuildResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	project := httpkit.Deref(req.Params.Project)
	if err := checkBuildRepo(project, req.Params.BuildRepo); err != nil {
		return nil, err
	}
	pub, err := svc.Publish(ctx, project, httpkit.RawBody(ctx), p.Name, a)
	if err != nil {
		return nil, s.buildError(ctx, p, project, "", err)
	}
	if pub.Retention != nil {
		s.Log().WarnContext(ctx, "build retention", "request_id", httpkit.RequestIDFrom(ctx),
			"build", pub.Run.Name, "error", pub.Retention)
	}
	return artgen.UploadBuild204Response{
		Headers: artgen.UploadBuild204ResponseHeaders{XChecksumSha256: &pub.SHA256},
	}, nil
}

// checkBuildRepo refuses a buildRepo parameter that names another
// repository than the project's build-info repository, the only one that
// holds its runs.
func checkBuildRepo(project string, buildRepo *string) error {
	want := model.BuildInfoRepo(build.ProjectKey(project))
	if httpkit.Deref(buildRepo) != "" && *buildRepo != want {
		return httpkit.BadRequestf(
			"The build-info repository '%s' does not hold this project's builds; '%s' does",
			*buildRepo, want)
	}
	return nil
}

// GetBuildRuns lists a build's runs, a number once per run, newest first.
// The uri of each is the number as it is: jf rt bp --overwrite counts the
// runs of a number by comparing it with the uri less its slash.
func (s ArtifactoryAPI) GetBuildRuns(
	ctx context.Context,
	req artgen.GetBuildRunsRequestObject,
) (artgen.GetBuildRunsResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	project := httpkit.Deref(req.Params.Project)
	env, err := httpkit.EnvelopeOf(artgen.GetBuildRuns200JSONResponse{
		Uri: httpkit.PtrTo(buildURI(ctx, req.BuildName)), BuildsNumbers: &[]artgen.BuildNumber{},
	}, "buildsNumbers")
	if err != nil {
		return nil, s.InternalError(ctx, "list build runs", err)
	}
	pages, keep := svc.RunPages(project, req.BuildName, a)
	l, err := httpkit.StreamList(ctx, pages,
		func(r model.BuildRun) (artgen.BuildNumber, bool, error) {
			return artgen.BuildNumber{
				Uri:     httpkit.PtrTo("/" + r.Number),
				Started: httpkit.PtrTo(build.FormatStarted(r.Started)),
			}, keep(r), nil
		}, httpkit.ListOptions{Envelope: env})
	if err == nil && l.Empty() {
		err = build.NoRuns(req.BuildName, l.Seen())
	}
	if err != nil {
		return nil, s.buildError(ctx, p, project, noBuild(req.BuildName), err)
	}
	return streamed{l}, nil
}

// GetBuildInfo returns a run's document. A diff asks for the Builds Diff
// against an older run, which is not served (DV-088): the request is
// checked as Artifactory 6.20's BuildResource.getBuildInfo checks it, and
// one that passes answers 501 rather than the run, which a client would
// take for the comparison.
func (s ArtifactoryAPI) GetBuildInfo(
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
	project := httpkit.Deref(req.Params.Project)
	b, err := svc.Run(ctx, project, req.BuildName, req.BuildNumber, at, a)
	if err != nil {
		return nil, s.buildError(ctx, p, project, noRun(req.BuildName, req.BuildNumber), err)
	}
	if diff := req.Params.Diff; diff != nil {
		if *diff == "null" {
			return nil, httpkit.BadRequestf("Parameter 'diff' must contain a value, if specified.")
		}
		older, err := svc.Run(ctx, project, req.BuildName, *diff, time.Time{}, a)
		if err != nil {
			// 6.20's text, with its space before the comma.
			return nil, s.buildError(ctx, p, project, fmt.Sprintf(
				"No build was found for build name: %s , build number: %s",
				req.BuildName,
				*diff,
			), err)
		}
		if b.Started.Before(older.Started) {
			return nil, httpkit.BadRequestf(
				"Build number should be greater than the build number to compare against.")
		}
		return nil, apierror.New(http.StatusNotImplemented,
			"Comparing build runs (diff) is not supported")
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
func (s ArtifactoryAPI) AppendBuild(
	ctx context.Context,
	req artgen.AppendBuildRequestObject,
) (artgen.AppendBuildResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	ap, err := appendOf(ctx, req, p)
	if err != nil {
		return nil, err
	}
	if req.Params.IdempotencyKey != nil {
		// The shared replay runs this as the effect, inside its
		// transaction, and maps the error with the binding's Error, which
		// needs it unmapped to tell a spent budget (503) from the rest.
		id, err := svc.AppendKeyed(ctx, ap, s.currentBuildAccess(p))
		if err != nil {
			return nil, err //nolint:wrapcheck // the binding's Error maps it
		}
		idempotency.Report(ctx, id)
		return artgen.AppendBuild204Response{}, nil
	}
	if err := svc.Append(ctx, ap, a); err != nil {
		return nil, s.buildError(ctx, p, ap.ProjectKey, noRun(req.BuildName, req.BuildNumber), err)
	}
	return artgen.AppendBuild204Response{}, nil
}

// RenameBuild moves every run of a build to another name.
func (s ArtifactoryAPI) RenameBuild(
	ctx context.Context,
	req artgen.RenameBuildRequestObject,
) (artgen.RenameBuildResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	project, to := httpkit.Deref(req.Params.Project), req.Params.To
	if err := svc.Rename(ctx, project, req.BuildName, to, p.Name, a); err != nil {
		if errors.Is(err, build.ErrConflict) {
			return nil, apierror.New(http.StatusConflict,
				fmt.Sprintf("A build named '%s' already exists", to))
		}
		return nil, s.buildError(ctx, p, project, noBuild(req.BuildName), err)
	}
	// Artifactory's text (renamebuild), although the rename is done when
	// it answers.
	return artgen.RenameBuild200TextResponse(fmt.Sprintf(
		"Build renaming of '%s' to '%s' was successfully started.", req.BuildName, to,
	)), nil
}

// DeleteBuilds deletes runs of a build named in the path. buildNumbers is
// comma-separated, and a number listed twice deletes two runs.
func (s ArtifactoryAPI) DeleteBuilds(
	ctx context.Context,
	req artgen.DeleteBuildsRequestObject,
) (artgen.DeleteBuildsResponseObject, error) {
	msg, err := s.deleteBuilds(ctx, deleteOf(req), req.Params.IdempotencyKey != nil)
	if err != nil {
		return nil, err
	}
	return artgen.DeleteBuilds200TextResponse(msg), nil
}

// deleteOf reads the runs DeleteBuilds deletes from req.
func deleteOf(req artgen.DeleteBuildsRequestObject) build.Delete {
	var numbers []string
	for n := range strings.SplitSeq(httpkit.Deref(req.Params.BuildNumbers), ",") {
		if n = strings.TrimSpace(n); n != "" {
			numbers = append(numbers, n)
		}
	}
	return build.Delete{
		Name: req.BuildName, Numbers: numbers,
		All:       httpkit.Deref(req.Params.DeleteAll) == 1,
		Artifacts: httpkit.Deref(req.Params.Artifacts) == 1,
	}
}

// DeleteBuildsMultiple is the body form of DeleteBuilds, which jf rt bp
// --overwrite sends with a number once per existing run.
func (s ArtifactoryAPI) DeleteBuildsMultiple(
	ctx context.Context,
	req artgen.DeleteBuildsMultipleRequestObject,
) (artgen.DeleteBuildsMultipleResponseObject, error) {
	msg, err := s.deleteBuilds(ctx, deleteMultipleOf(req), req.Params.IdempotencyKey != nil)
	if err != nil {
		return nil, err
	}
	return artgen.DeleteBuildsMultiple200TextResponse(msg), nil
}

// deleteMultipleOf reads the runs DeleteBuildsMultiple deletes from req.
func deleteMultipleOf(req artgen.DeleteBuildsMultipleRequestObject) build.Delete {
	return build.Delete{
		ProjectKey: httpkit.Deref(req.Body.Project), Name: req.Body.BuildName,
		Numbers: httpkit.DerefSlice(req.Body.BuildNumbers), All: httpkit.Deref(req.Body.DeleteAll),
		Artifacts: httpkit.Deref(req.Body.DeleteArtifacts),
	}
}

// deleteBuilds deletes d's runs; keyed runs it as the shared replay's
// effect, inside its transaction, and returns the error unmapped for the
// binding's Error, which needs it so to tell a spent budget (503) from the
// rest.
func (s ArtifactoryAPI) deleteBuilds(
	ctx context.Context,
	d build.Delete,
	keyed bool,
) (string, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return "", err
	}
	d.By = p.Name
	if keyed {
		gone, id, err := svc.DeleteKeyed(ctx, d, s.currentBuildAccess(p), reportRetention)
		if err != nil {
			return "", err //nolint:wrapcheck // the binding's Error maps it
		}
		idempotency.Report(ctx, id)
		return deletedText(d.Name, gone), nil
	}
	gone, err := svc.Delete(ctx, d, a)
	if err != nil {
		return "", s.buildError(ctx, p, d.ProjectKey, noBuild(d.Name), err)
	}
	return deletedText(d.Name, gone), nil
}

// deletedText is Artifactory's answer to a delete (deletebuilds), one
// entry per deleted run.
func deletedText(name string, gone []model.BuildRun) string {
	runs := make([]string, 0, len(gone))
	for _, r := range gone {
		runs = append(runs, "'"+name+"#"+r.Number+"'")
	}
	return "The following builds has been deleted successfully: " + strings.Join(runs, ", ") + "."
}

// GetBuildArtifacts lists where the artifacts of a number's latest run are.
// The JFrog CLI calls it before its AQL fallback, and reads only repo, path
// and name (research 22 §3.1).
func (s ArtifactoryAPI) GetBuildArtifacts(
	ctx context.Context,
	req artgen.GetBuildArtifactsRequestObject,
) (artgen.GetBuildArtifactsResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	project, ok := buildInfoProject(req.BuildRepo)
	if !ok {
		return nil, itemkit.NotFoundRepo(req.BuildRepo)
	}
	items, err := svc.Artifacts(ctx, project, req.BuildName, req.BuildNumber, a)
	if err != nil {
		return nil, s.buildError(ctx, p, project, noRun(req.BuildName, req.BuildNumber), err)
	}
	out := make(artgen.GetBuildArtifacts200JSONResponse, 0, len(items))
	for _, it := range items {
		out = append(out, artgen.BuildArtifact{
			Repo: httpkit.PtrTo(
				it.ReportedRepoKey(),
			),
			Path: httpkit.PtrTo(it.Path),
			Name: httpkit.PtrTo(it.Name),
		})
	}
	return out, nil
}

// PromoteBuild promotes the latest run of a build number. A fail-fast
// promotion that met an error or a warning answers 400 with its report, as
// Artifactory does; otherwise the report answers 200, even when files
// failed.
func (s ArtifactoryAPI) PromoteBuild(
	ctx context.Context,
	req artgen.PromoteBuildRequestObject,
) (artgen.PromoteBuildResponseObject, error) {
	body := promotionBody(req)
	if body == nil {
		return refusePromotion("The promotion request must be JSON"), nil
	}
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	project := httpkit.Deref(req.Params.Project)
	promotion, refusal := promotionOf(req, body, p)
	if refusal != nil {
		return refusal, nil
	}
	var (
		res build.PromotionResult
		id  int64
	)
	keyed := req.Params.IdempotencyKey != nil
	if keyed {
		res, id, err = svc.PromoteKeyed(ctx, promotion, s.currentBuildAccess(p), reportRetention)
	} else {
		res, err = svc.Promote(ctx, promotion, a)
	}
	switch {
	case errors.Is(err, build.ErrNoTarget):
		return nil, apierror.New(http.StatusNotFound, fmt.Sprintf(
			"Cannot find target repository by the key '%s'.", httpkit.Deref(body.TargetRepo)))
	case errors.Is(err, build.ErrInvalid):
		return refusePromotion(strings.TrimPrefix(err.Error(), build.ErrInvalid.Error()+": ")), nil
	case err != nil && keyed:
		// The shared replay runs this as the effect and maps the error
		// with the binding's Error, which needs it unmapped to tell a
		// spent budget (503) from the rest.
		return nil, err //nolint:wrapcheck // the binding's Error maps it
	case err != nil:
		return nil, s.buildError(ctx, p, project, unpromoted(req), err)
	}
	out := promotionReport(res.Messages)
	if res.Aborted {
		return artgen.PromoteBuild400JSONResponse(out), nil
	}
	// A dry run changed nothing and has no report, so it gets no
	// receipt, and a retry runs it again.
	if id != 0 {
		idempotency.Report(ctx, id)
	}
	return artgen.PromoteBuild200JSONResponse(out), nil
}

// promotionBody is a promotion's body in either of its media types; nil
// when it has none.
func promotionBody(req artgen.PromoteBuildRequestObject) *artgen.BuildPromotionRequest {
	if req.JSONBody != nil {
		return req.JSONBody
	}
	return req.ApplicationVndOrgJfrogArtifactoryBuildPromotionRequestPlusJSONBody
}

// promotionOf reads the promotion req asks for, by p, or the answer that
// refuses it.
func promotionOf(
	req artgen.PromoteBuildRequestObject,
	body *artgen.BuildPromotionRequest,
	p kernel.Principal,
) (build.Promotion, artgen.PromoteBuildResponseObject) {
	at, err := started(body.Timestamp)
	if err != nil {
		return build.Promotion{}, refusePromotion("The timestamp " + httpkit.Deref(body.Timestamp) +
			" is not in the layout yyyy-MM-dd'T'HH:mm:ss.SSSZ")
	}
	props, bad := promotionProperties(httpkit.Deref(body.Properties))
	if bad != "" {
		return build.Promotion{}, refusePromotion(fmt.Sprintf(
			"The value of property '%s' must be a string or an array of strings", bad))
	}
	for k := range props {
		if err := model.CheckPropertyKeyLength(k); err != nil {
			return build.Promotion{}, refusePromotion(err.Error())
		}
	}
	artifacts, failFast := true, true
	if body.Artifacts != nil {
		artifacts = *body.Artifacts
	}
	if body.FailFast != nil {
		failFast = *body.FailFast
	}
	return build.Promotion{
		ProjectKey: httpkit.Deref(req.Params.Project),
		Name:       req.BuildName,
		Number:     req.BuildNumber,
		Status: httpkit.Deref(
			body.Status,
		),
		Comment:    httpkit.Deref(body.Comment),
		CIUser:     httpkit.Deref(body.CiUser),
		Timestamp:  at,
		SourceRepo: httpkit.Deref(body.SourceRepo),
		TargetRepo: httpkit.Deref(body.TargetRepo),
		Copy: httpkit.Deref(
			body.Copy,
		),
		Artifacts:    artifacts,
		Dependencies: httpkit.Deref(body.Dependencies),
		Scopes: httpkit.DerefSlice(
			body.Scopes,
		),
		Properties: props,
		DryRun:     httpkit.Deref(body.DryRun),
		FailFast:   failFast,
		By:         p.Name,
	}, nil
}

// unpromoted is the 404 message for a promotion of a run that does not
// exist.
func unpromoted(req artgen.PromoteBuildRequestObject) string {
	return fmt.Sprintf("Unable to find build '%s' #%s.", req.BuildName, req.BuildNumber)
}

func refusePromotion(text string) artgen.PromoteBuildResponseObject {
	return artgen.PromoteBuild400JSONResponse(promotionReport(
		[]item.Message{{Level: item.LevelError, Text: text}},
	))
}

func promotionReport(msgs []item.Message) artgen.BuildPromotionResponse {
	list := make([]artgen.PromotionMessage, 0, len(msgs))
	for _, m := range msgs {
		list = append(list, artgen.PromotionMessage{
			Level: httpkit.PtrTo(
				artgen.PromotionMessageLevel(m.Level),
			),
			Message: httpkit.PtrTo(m.Text),
		})
	}
	return artgen.BuildPromotionResponse{Messages: &list}
}

// promotionProperties reads a promotion's properties: each key maps to a
// value or an array of values, as the JFrog CLI sends them. A number or a
// boolean is kept as its JSON text, as a build-info property is. bad names
// a key whose value is none of these.
func promotionProperties(in map[string]any) (props model.Properties, bad string) {
	props = model.Properties{}
	for k, v := range in {
		list, ok := v.([]any)
		if !ok {
			list = []any{v}
		}
		for _, e := range list {
			switch e := e.(type) {
			case nil:
			case string:
				props.Add(k, e)
			case float64, bool:
				text, _ := json.Marshal(e)
				props.Add(k, string(text))
			default:
				return nil, k
			}
		}
	}
	return props, ""
}

// ControlBuildRetention discards the runs of a build its settings do not
// keep. With async it checks the request, answers, and discards in the
// background, logging what goes wrong there. The CLI accepts only 204
// (decision 0014).
func (s ArtifactoryAPI) ControlBuildRetention(
	ctx context.Context,
	req artgen.ControlBuildRetentionRequestObject,
) (artgen.ControlBuildRetentionResponseObject, error) {
	svc, p, a, err := s.buildCaller(ctx)
	if err != nil {
		return nil, err
	}
	project := httpkit.Deref(req.Params.Project)
	// The generated binder always decodes the body, whatever its type.
	r, err := build.RetentionSettings{
		Count: httpkit.Deref(
			req.Body.Count,
		),
		MinimumBuildDate:             httpkit.Deref(req.Body.MinimumBuildDate),
		DeleteBuildArtifacts:         httpkit.Deref(req.Body.DeleteBuildArtifacts),
		BuildNumbersNotToBeDiscarded: httpkit.DerefSlice(req.Body.BuildNumbersNotToBeDiscarded),
	}.Retention(true)
	if err != nil {
		return nil, s.buildError(ctx, p, project, "", err)
	}
	d := build.Discard{ProjectKey: project, Name: req.BuildName, Retention: r, By: p.Name}
	if !httpkit.Deref(req.Params.Async) {
		if _, err := svc.Discard(ctx, d, a); err != nil {
			return nil, s.buildError(ctx, p, project, noBuild(req.BuildName), err)
		}
		return artgen.ControlBuildRetention204Response{}, nil
	}
	if _, err := svc.Discards(ctx, d, a); err != nil {
		return nil, s.buildError(ctx, p, project, noBuild(req.BuildName), err)
	}
	// The background discard plans again: runs may have come or gone since.
	bg := context.WithoutCancel(ctx)
	go func() {
		if _, err := svc.Discard(bg, d, a); err != nil {
			s.Log().ErrorContext(bg, "build retention", "request_id", httpkit.RequestIDFrom(bg),
				"build", d.Name, "error", err)
		}
	}()
	return artgen.ControlBuildRetention204Response{}, nil
}

// buildInfoProject is the project whose build-info repository key is
// repoKey (model.BuildInfoRepo).
func buildInfoProject(repoKey string) (string, bool) {
	if repoKey == accessapi.BuildInfoRepository {
		return "", true
	}
	p, ok := strings.CutSuffix(repoKey, "-build-info")
	return p, ok && p != ""
}

// KeepsBuildBody reports a build-info publish or append, whose raw body the
// router's request details keep because the document is stored as
// published (decision 0014).
func KeepsBuildBody(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, buildsPath+"/append/") ||
		r.Method == http.MethodPut && r.URL.Path == buildsPath
}
