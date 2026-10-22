package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/repos"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The repositories API (research 02, decision 0003). Every write is for
// administrators; reads show a caller the repositories the permission
// evaluator lets them see (access.visible): those they may read anywhere in,
// or those a token's repo or system:info scope names (decision 0006).

const (
	// repoBatchLimit bounds the v2 batch reads and writes. JFrog documents
	// 100 for the batch read and only "batch limit exceeded" for the writes.
	repoBatchLimit = 100

	mediaLocal     = "application/vnd.org.jfrog.artifactory.repositories.LocalRepositoryConfiguration+json"
	mediaRemote    = "application/vnd.org.jfrog.artifactory.repositories.RemoteRepositoryConfiguration+json"
	mediaVirtual   = "application/vnd.org.jfrog.artifactory.repositories.VirtualRepositoryConfiguration+json"
	mediaFederated = "application/vnd.org.jfrog.artifactory.repositories.FederatedRepositoryConfiguration+json"
	mediaDetails   = "application/vnd.org.jfrog.artifactory.repositories.RepositoryDetailsList+json"
	mediaConfigs   = "application/vnd.org.jfrog.artifactory.repositories.RepositoryConfigurationsList+json"
	mediaShared    = "application/vnd.org.jfrog.artifactory.repositories.SharedRemoteRepositoryConfiguration+json"
)

// Messages quoted from JFrog's documented examples. The create confirmation,
// "Successfully created repository '<key>'", is quoted from JFrog's curl
// integration page
// (https://docs.jfrog.com/integrations/docs/curl-integration.md). No JFrog
// page shows the update confirmation, so its wording is our own, as are the
// error messages not listed here.
const (
	msgKeyExists  = repos.MsgKeyExists
	msgProtected  = "Cannot delete repository: repository key is blank, trash or support bundle"
	msgRemoved    = "Repository '%s' and all its content have been removed successfully."
	msgNoConfig   = "Cannot delete repository: '%s', repository config does not exist"
	msgNoRight    = "Cannot delete repository: '%s', Reason: User: ('%s') has insufficient permission to delete repositories: %s"
	msgAllRemoved = "All repositories were removed successfully"
	msgSomeFailed = "Some repositories failed to be removed"
	msgAllFailed  = "Repositories batch removal failed"
	msgNoKeys     = "No repository keys were provided for deletion"
	msgBuildInfo  = "Deletion of Build Info repository %s is not allowed."
)

// slashCollections are the collection paths the Terraform provider calls
// with a trailing slash, which chi would otherwise read as a member with an
// empty key: it lists repositories at /api/repositories/ (research 08) and
// creates key pairs with POST /api/security/keypair/ (its KeypairEndPoint).
// Only these exact paths are rewritten, because a trailing slash elsewhere
// under /artifactory names a folder.
var slashCollections = map[string]string{
	"/artifactory/api/repositories/":     "/artifactory/api/repositories",
	"/artifactory/api/security/keypair/": "/artifactory/api/security/keypair",
}

// collectionSlash routes each of slashCollections as its path without the
// trailing slash.
func collectionSlash(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bare, ok := slashCollections[r.URL.Path]; ok {
			r.URL.Path, r.URL.RawPath = bare, ""
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				rctx.RoutePath = r.URL.Path
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requestDetails keeps what the strict handlers need beyond their decoded
// request: the Accept header for media-type negotiation, the raw query of a
// property search, and the raw body of a repository or Access v2 group
// update. repo.Config cannot tell a field left out (keep the stored value)
// from one sent as null (reset it to its default), and an update needs both.
// A build-info publish or append keeps its raw body too, because the
// document is stored as published (decision 0014). It runs after bodyLimit,
// so the copy is bounded.
func requestDetails(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := withAccept(r.Context(), r.Header.Get("Accept"))
		if r.URL.Path == propertySearchPath {
			ctx = withSearchQuery(ctx, r.URL.RawQuery)
		}
		if r.Method == http.MethodPut && policyPaths[r.URL.Path] {
			if err := normalizePolicyBody(r); err != nil {
				bindingError(http.StatusBadRequest)(w, r, err)
				return
			}
		}
		// A permission create keeps its raw body too: the generated Access
		// type drops the destination and pipeline_source sections.
		if (r.Method == http.MethodPost &&
			(isRepositoryUpdate(r.URL.Path) || r.URL.Path == "/access/api/v2/permissions" ||
				strings.HasPrefix(r.URL.Path, buildsPath+"/append/"))) ||
			(r.Method == http.MethodPut && r.URL.Path == buildsPath) ||
			(r.Method == http.MethodPatch && isGroupUpdate(r.URL.Path)) {
			b, err := io.ReadAll(r.Body)
			if err != nil {
				bindingError(http.StatusBadRequest)(w, r, err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
			ctx = withRawBody(ctx, b)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isGroupUpdate matches PATCH /access/api/v2/groups/{name}, whose raw body
// tells a field left out from one sent with its stored value.
func isGroupUpdate(path string) bool {
	name, ok := strings.CutPrefix(path, accessGroups)
	return ok && name != "" && !strings.Contains(name, "/")
}

func isRepositoryUpdate(path string) bool {
	if path == "/artifactory/api/v2/repositories/batch" {
		return true
	}
	key, ok := strings.CutPrefix(path, "/artifactory/api/repositories/")
	return ok && key != "" && !strings.Contains(key, "/")
}

// content returns the store. A router built without one (tests of the
// routing alone) answers like an unimplemented operation.
func (s artifactoryService) content() (store.Store, error) {
	if s.cfg.Store == nil {
		return nil, apierror.ErrNotImplemented
	}
	return s.cfg.Store, nil
}

// internalError logs err and returns a 500 that names nothing internal.
func (s artifactoryService) internalError(ctx context.Context, op string, err error) error {
	s.log().ErrorContext(ctx, op, "request_id", requestIDFrom(ctx), "error", err)
	return apierror.New(http.StatusInternalServerError, "internal error")
}

// log is the configured logger, or slog's default for handlers built
// without one in tests.
func (s artifactoryService) log() *slog.Logger {
	if s.cfg.Logger == nil {
		return slog.Default()
	}
	return s.cfg.Logger
}

// adminOnly refuses a caller who is not an administrator (auth.RequireAdmin).
// The wrap keeps the typed error, so the response carries its status.
func adminOnly(ctx context.Context) error {
	if _, err := auth.RequireAdmin(ctx); err != nil {
		return fmt.Errorf("repositories: %w", err)
	}
	return nil
}

// principal returns the caller of a read. The middleware lets a request
// without one through only to public routes, so none here means a router
// built without authentication, which is refused as unauthenticated.
func principal(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return p, auth.ErrUnauthenticated
	}
	return p, nil
}

// denied refuses a read: anonymous is told to authenticate, a user that
// they lack the right.
func denied(p auth.Principal) error {
	if p.Anonymous {
		return auth.ErrUnauthenticated
	}
	return auth.ErrForbidden
}

func badRequest(format string, a ...any) error {
	return apierror.New(http.StatusBadRequest, fmt.Sprintf(format, a...))
}

func notFoundRepo(key string) error {
	return apierror.New(http.StatusNotFound, fmt.Sprintf("Repository '%s' does not exist", key))
}

// readerFor returns the principal of a read and what it may read.
func (s artifactoryService) readerFor(
	ctx context.Context,
	st store.Store,
) (auth.Principal, func(repo.Config) bool, error) {
	p, err := principal(ctx)
	if err != nil {
		return p, nil, err
	}
	acc, err := accessFor(ctx, st, p)
	if err != nil {
		return p, nil, s.internalError(ctx, "repository read rights", err)
	}
	return p, acc.visible, nil
}

// view is the configuration a reader receives: everything but secrets for
// an admin, the public subset for anyone else.
func view(p auth.Principal, c repo.Config) repo.Config {
	if p.Admin {
		return repo.Masked(c)
	}
	return repo.Public(c)
}

func (s artifactoryService) GetRepositoriesByTypeAndProject(
	ctx context.Context,
	req artgen.GetRepositoriesByTypeAndProjectRequestObject,
) (artgen.GetRepositoriesByTypeAndProjectResponseObject, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	_, can, err := s.readerFor(ctx, st)
	if err != nil {
		return nil, err
	}
	var f store.RepoFilter
	if req.Params.Type != nil {
		if *req.Params.Type == artgen.GetRepositoriesByTypeAndProjectParamsTypeDistribution {
			return artgen.GetRepositoriesByTypeAndProject200JSONResponse{}, nil
		}
		f.Class = repo.Class(strings.ToLower(string(*req.Params.Type)))
	}
	if req.Params.Project != nil {
		f.ProjectKey = *req.Params.Project
	}
	all, err := st.Repos().List(ctx, f)
	if err != nil {
		return nil, s.internalError(ctx, "list repositories", err)
	}
	base := baseurl.From(ctx)
	out := artgen.GetRepositoriesByTypeAndProject200JSONResponse{}
	for _, c := range all {
		// The documented package type enumeration is stale, so any value
		// filters, compared without case. The listed spelling matches too,
		// which is how the documented "yum" finds rpm repositories.
		if f := req.Params.PackageType; f != nil &&
			!strings.EqualFold(string(c.PackageType), string(*f)) &&
			!strings.EqualFold(repo.PackageTypeName(string(c.PackageType)), string(*f)) {
			continue
		}
		if !can(c) {
			continue
		}
		out = append(out, artgen.GetRepositoriesByTypeAndProject200JSONResponse{{}}[0])
		e := &out[len(out)-1]
		e.Key, e.Type = c.Key, strings.ToUpper(string(c.Class))
		e.PackageType = repo.PackageTypeName(string(c.PackageType))
		e.Url = base + "/artifactory/" + c.Key
		if c.Description != "" {
			d := c.Description
			e.Description = &d
		}
	}
	if accepts(ctx, mediaDetails) {
		return artgen.GetRepositoriesByTypeAndProject200ApplicationVndOrgJfrogArtifactoryRepositoriesRepositoryDetailsListPlusJSONResponse(
			out,
		), nil
	}
	return out, nil
}

func (s artifactoryService) GetAllRepositoryConfigurations(
	ctx context.Context,
	req artgen.GetAllRepositoryConfigurationsRequestObject,
) (artgen.GetAllRepositoryConfigurationsResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	all, err := st.Repos().List(ctx, store.RepoFilter{})
	if err != nil {
		return nil, s.internalError(ctx, "list repositories", err)
	}
	packageTypes := commaSet(req.Params.PackageType)
	classes := commaSet(req.Params.RepoType)
	// Every group is present even when empty, as JFrog returns it.
	out := artgen.GetAllRepositoryConfigurations200JSONResponse{
		LOCAL: []artgen.RepositoryConfiguration{}, REMOTE: []artgen.RepositoryConfiguration{},
		VIRTUAL: []artgen.RepositoryConfiguration{}, FEDERATED: []artgen.RepositoryConfiguration{},
		RELEASEBUNDLE: []artgen.RepositoryConfiguration{},
	}
	for _, c := range all {
		if !packageTypes.has(string(c.PackageType)) || !classes.has(string(c.Class)) {
			continue
		}
		// Each entry has the v2 read's shape: JFrog's sample of a generic
		// local repository here lacks the Maven fields, as that read's does.
		c = repo.Relevant(repo.Masked(c))
		switch c.Class {
		case repo.Local:
			out.LOCAL = append(out.LOCAL, c)
		case repo.Remote:
			out.REMOTE = append(out.REMOTE, c)
		case repo.Virtual:
			out.VIRTUAL = append(out.VIRTUAL, c)
		case repo.Federated:
			out.FEDERATED = append(out.FEDERATED, c)
		}
	}
	if accepts(ctx, mediaConfigs) {
		return artgen.GetAllRepositoryConfigurations200ApplicationVndOrgJfrogArtifactoryRepositoriesRepositoryConfigurationsListPlusJSONResponse(
			out,
		), nil
	}
	return out, nil
}

// set is a comma-separated filter; an empty one matches everything.
type set map[string]bool

func commaSet(v *string) set {
	s := set{}
	if v == nil {
		return s
	}
	for _, part := range strings.Split(*v, ",") {
		if part = strings.ToLower(strings.TrimSpace(part)); part != "" {
			s[part] = true
		}
	}
	return s
}

func (s set) has(v string) bool { return len(s) == 0 || s[strings.ToLower(v)] }

func (s artifactoryService) CheckIfRepositoryExists(
	ctx context.Context,
	req artgen.CheckIfRepositoryExistsRequestObject,
) (artgen.CheckIfRepositoryExistsResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	asked := []artgen.CheckIfRepositoryExistsParamsType{
		artgen.CheckIfRepositoryExistsParamsTypeLocal,
		artgen.CheckIfRepositoryExistsParamsTypeRemote,
		artgen.CheckIfRepositoryExistsParamsTypeVirtual,
		artgen.CheckIfRepositoryExistsParamsTypeFederated,
	}
	if req.Params.Type != nil && len(*req.Params.Type) > 0 {
		asked = *req.Params.Type
	}
	f := store.RepoFilter{}
	if req.Params.ProjectKey != nil {
		f.ProjectKey = *req.Params.ProjectKey
	}
	all, err := st.Repos().List(ctx, f)
	if err != nil {
		return nil, s.internalError(ctx, "list repositories", err)
	}
	// matchingRepoTypes lists the classes checked ("types used", JFrog's
	// description), not the ones found.
	out := artgen.CheckIfRepositoryExists200JSONResponse{
		MatchingRepoTypes: []string{},
		ProjectKey:        req.Params.ProjectKey,
	}
	for _, t := range asked {
		if t == artgen.CheckIfRepositoryExistsParamsTypeReleaseBundle {
			out.MatchingRepoTypes = append(out.MatchingRepoTypes, "RELEASE_BUNDLE")
			continue
		}
		out.MatchingRepoTypes = append(out.MatchingRepoTypes, strings.ToUpper(string(t)))
		if slices.ContainsFunc(
			all,
			func(c repo.Config) bool { return string(c.Class) == string(t) },
		) {
			out.Exists = true
		}
	}
	return out, nil
}

// readOne returns the configuration of key as p may see it.
func (s artifactoryService) readOne(
	ctx context.Context,
	key string,
) (auth.Principal, repo.Config, error) {
	st, err := s.content()
	if err != nil {
		return auth.Principal{}, repo.Config{}, err
	}
	p, can, err := s.readerFor(ctx, st)
	if err != nil {
		return p, repo.Config{}, err
	}
	c, err := st.Repos().Get(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return p, repo.Config{}, notFoundRepo(key)
	case err != nil:
		return p, repo.Config{}, s.internalError(ctx, "get repository", err)
	case !can(c):
		return p, repo.Config{}, denied(p)
	}
	return p, view(p, c), nil
}

func (s artifactoryService) GetRepositoryConfiguration(
	ctx context.Context,
	req artgen.GetRepositoryConfigurationRequestObject,
) (artgen.GetRepositoryConfigurationResponseObject, error) {
	_, c, err := s.readOne(ctx, string(req.RepoKey))
	if err != nil {
		return nil, err
	}
	return configResponse(ctx, c), nil
}

func (s artifactoryService) GetRepositoryConfigurationV2(
	ctx context.Context,
	req artgen.GetRepositoryConfigurationV2RequestObject,
) (artgen.GetRepositoryConfigurationV2ResponseObject, error) {
	_, c, err := s.readOne(ctx, string(req.RepoKey))
	if err != nil {
		return nil, err
	}
	c = repo.Relevant(c)
	switch {
	case c.Class == repo.Local && accepts(ctx, mediaLocal):
		return artgen.GetRepositoryConfigurationV2200ApplicationVndOrgJfrogArtifactoryRepositoriesLocalRepositoryConfigurationPlusJSONResponse(
			c,
		), nil
	case c.Class == repo.Remote && accepts(ctx, mediaRemote):
		return artgen.GetRepositoryConfigurationV2200ApplicationVndOrgJfrogArtifactoryRepositoriesRemoteRepositoryConfigurationPlusJSONResponse(
			c,
		), nil
	case c.Class == repo.Virtual && accepts(ctx, mediaVirtual):
		return artgen.GetRepositoryConfigurationV2200ApplicationVndOrgJfrogArtifactoryRepositoriesVirtualRepositoryConfigurationPlusJSONResponse(
			c,
		), nil
	case c.Class == repo.Federated && accepts(ctx, mediaFederated):
		return artgen.GetRepositoryConfigurationV2200ApplicationVndOrgJfrogArtifactoryRepositoriesFederatedRepositoryConfigurationPlusJSONResponse(
			c,
		), nil
	}
	return artgen.GetRepositoryConfigurationV2200JSONResponse(c), nil
}

// configResponse answers in the class's vendor media type when the client
// asks for it, as Artifactory does, and in plain JSON otherwise.
func configResponse(
	ctx context.Context,
	c repo.Config,
) artgen.GetRepositoryConfigurationResponseObject {
	switch {
	case c.Class == repo.Local && accepts(ctx, mediaLocal):
		return artgen.GetRepositoryConfiguration200ApplicationVndOrgJfrogArtifactoryRepositoriesLocalRepositoryConfigurationPlusJSONResponse(
			c,
		)
	case c.Class == repo.Remote && accepts(ctx, mediaRemote):
		return artgen.GetRepositoryConfiguration200ApplicationVndOrgJfrogArtifactoryRepositoriesRemoteRepositoryConfigurationPlusJSONResponse(
			c,
		)
	case c.Class == repo.Virtual && accepts(ctx, mediaVirtual):
		return artgen.GetRepositoryConfiguration200ApplicationVndOrgJfrogArtifactoryRepositoriesVirtualRepositoryConfigurationPlusJSONResponse(
			c,
		)
	case c.Class == repo.Federated && accepts(ctx, mediaFederated):
		return artgen.GetRepositoryConfiguration200ApplicationVndOrgJfrogArtifactoryRepositoriesFederatedRepositoryConfigurationPlusJSONResponse(
			c,
		)
	}
	return artgen.GetRepositoryConfiguration200JSONResponse(c)
}

// sharedFields are what a non-admin sees of a shared remote repository:
// enough to point a client at the same upstream, nothing that configures
// access.
var sharedFields = []string{
	"key", "rclass", "packageType", "description", "url", "repoLayoutRef",
	"remoteRepoLayoutRef", "includesPattern", "excludesPattern",
}

func (s artifactoryService) GetRemoteRepositoryConfiguration(
	ctx context.Context,
	req artgen.GetRemoteRepositoryConfigurationRequestObject,
) (artgen.GetRemoteRepositoryConfigurationResponseObject, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	if p.Anonymous {
		return nil, denied(p)
	}
	key := string(req.RemoteRepoName)
	c, err := st.Repos().Get(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound), err == nil && c.Class != repo.Remote:
		return nil, apierror.New(http.StatusNotFound,
			fmt.Sprintf("Remote repository '%s' does not exist", key))
	case err != nil:
		return nil, s.internalError(ctx, "get repository", err)
	case !p.Admin && (c.ShareConfiguration == nil || !*c.ShareConfiguration):
		return nil, denied(p)
	}
	doc, err := json.Marshal(repo.Masked(c))
	if err != nil {
		return nil, s.internalError(ctx, "encode repository", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(doc, &fields); err != nil {
		return nil, s.internalError(ctx, "encode repository", err)
	}
	if !p.Admin {
		maps.DeleteFunc(
			fields,
			func(name string, _ any) bool { return !slices.Contains(sharedFields, name) },
		)
	}
	media := "application/json"
	if accepts(ctx, mediaShared) {
		media = mediaShared
	}
	return sharedRemoteResponse{media: media, fields: fields}, nil
}

// sharedRemoteResponse writes the shared view itself: the generated
// response types redeclare SharedRemoteRepositoryConfiguration and so lose
// the method that encodes its additional properties, which here are nearly
// every field.
type sharedRemoteResponse struct {
	media  string
	fields map[string]any
}

func (r sharedRemoteResponse) VisitGetRemoteRepositoryConfigurationResponse(
	w http.ResponseWriter,
) error {
	w.Header().Set("Content-Type", r.media)
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(r.fields) //nolint:wrapcheck // the strict server logs it
}

func (s artifactoryService) GetBatchOfRepositoriesByName(
	ctx context.Context,
	req artgen.GetBatchOfRepositoriesByNameRequestObject,
) (artgen.GetBatchOfRepositoriesByNameResponseObject, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	p, can, err := s.readerFor(ctx, st)
	if err != nil {
		return nil, err
	}
	// Names arrive repeated, comma-separated, or both.
	var names []string
	for _, n := range req.Params.Names {
		for part := range strings.SplitSeq(n, ",") {
			if part = strings.TrimSpace(part); part != "" && !slices.Contains(names, part) {
				names = append(names, part)
			}
		}
	}
	switch {
	case len(names) == 0:
		return nil, badRequest("No repository names were given")
	case len(names) > repoBatchLimit:
		return nil, badRequest("At most %d repositories can be read at once", repoBatchLimit)
	}
	out := artgen.GetBatchOfRepositoriesByName200JSONResponse{}
	for _, key := range names {
		c, err := st.Repos().Get(ctx, key)
		switch {
		case errors.Is(err, store.ErrNotFound):
			continue
		case err != nil:
			return nil, s.internalError(ctx, "get repository", err)
		case can(c):
			out[key] = view(p, c)
		}
	}
	return out, nil
}

// requestConfig returns the configuration in a create or update body, which
// arrives in the plain JSON field or, when a client's vendor media type was
// not normalised, in that type's field.
func requestConfig(bodies ...*repo.Config) *repo.Config {
	for _, b := range bodies {
		if b != nil {
			return b
		}
	}
	return nil
}

func requestConfigs(bodies ...*[]repo.Config) []repo.Config {
	for _, b := range bodies {
		if b != nil {
			return *b
		}
	}
	return nil
}

func (s artifactoryService) CreateRepository(
	ctx context.Context,
	req artgen.CreateRepositoryRequestObject,
) (artgen.CreateRepositoryResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	body := requestConfig(
		req.JSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesLocalRepositoryConfigurationPlusJSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesRemoteRepositoryConfigurationPlusJSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesVirtualRepositoryConfigurationPlusJSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesFederatedRepositoryConfigurationPlusJSONBody,
	)
	if body == nil {
		return nil, badRequest("A repository configuration is required")
	}
	c := *body
	// The key in the path wins over the body (research 03).
	c.Key = string(req.RepoKey)
	existing, err := st.Repos().List(ctx, store.RepoFilter{})
	if err != nil {
		return nil, s.internalError(ctx, "list repositories", err)
	}
	if c, err = repos.PrepareCreate(c, existing); err != nil {
		return nil, fmt.Errorf("repositories: %w", err)
	}
	if err := repos.CheckMembers(c, existing); err != nil {
		return nil, fmt.Errorf("repositories: %w", err)
	}
	if err := repos.CheckKeypairs(ctx, st.Keys(), c); err != nil {
		return nil, fmt.Errorf("repositories: %w", err)
	}
	if err := repos.CheckEnvironments(ctx, st.Projects(), &c); err != nil {
		return nil, fmt.Errorf("repositories: %w", err)
	}
	switch err := st.Repos().Create(ctx, c); {
	case errors.Is(err, store.ErrConflict):
		return nil, badRequest(msgKeyExists)
	case err != nil:
		return nil, s.internalError(ctx, "create repository", err)
	}
	return artgen.CreateRepository200TextResponse(
		fmt.Sprintf("Successfully created repository '%s'", c.Key),
	), nil
}

func (s artifactoryService) CreateMultipleRepositories(
	ctx context.Context,
	req artgen.CreateMultipleRepositoriesRequestObject,
) (artgen.CreateMultipleRepositoriesResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	batch := requestConfigs(
		req.JSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesLocalRepositoryConfigurationPlusJSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesRemoteRepositoryConfigurationPlusJSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesVirtualRepositoryConfigurationPlusJSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesFederatedRepositoryConfigurationPlusJSONBody,
	)
	if err := batchSize(len(batch)); err != nil {
		return nil, err
	}
	known, err := st.Repos().List(ctx, store.RepoFilter{})
	if err != nil {
		return nil, s.internalError(ctx, "list repositories", err)
	}
	// Everything is validated before anything is created, so a bad entry
	// leaves the batch unapplied.
	prepared := make([]repo.Config, 0, len(batch))
	for i, c := range batch {
		if c.Key == "" {
			return nil, badRequest("Repository %d of the batch has no key", i+1)
		}
		if slices.ContainsFunc(
			known,
			func(k repo.Config) bool { return strings.EqualFold(k.Key, c.Key) },
		) {
			return nil, apierror.New(http.StatusConflict,
				fmt.Sprintf("%s: '%s'", msgKeyExists, c.Key))
		}
		c, err := repos.PrepareCreate(c, nil)
		if err != nil {
			return nil, fmt.Errorf("repositories: %w", err)
		}
		prepared = append(prepared, c)
		known = append(known, c)
	}
	for i := range prepared {
		if err := repos.CheckMembers(prepared[i], known); err != nil {
			return nil, fmt.Errorf("repositories: %w", err)
		}
		if err := repos.CheckKeypairs(ctx, st.Keys(), prepared[i]); err != nil {
			return nil, fmt.Errorf("repositories: %w", err)
		}
		if err := repos.CheckEnvironments(ctx, st.Projects(), &prepared[i]); err != nil {
			return nil, fmt.Errorf("repositories: %w", err)
		}
	}
	err = st.Tx(ctx, func(ctx context.Context) error {
		for _, c := range prepared {
			if err := st.Repos().Create(ctx, c); err != nil {
				return err //nolint:wrapcheck // classified below
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, store.ErrConflict):
		return nil, apierror.New(http.StatusConflict, msgKeyExists)
	case err != nil:
		return nil, s.internalError(ctx, "create repositories", err)
	}
	lines := make([]string, len(prepared))
	for i, c := range prepared {
		lines[i] = fmt.Sprintf("Successfully created repository '%s'", c.Key)
	}
	return artgen.CreateMultipleRepositories201TextResponse(strings.Join(lines, "\n")), nil
}

func batchSize(n int) error {
	switch {
	case n == 0:
		return badRequest("No repositories were given")
	case n > repoBatchLimit:
		return badRequest("At most %d repositories can be changed at once", repoBatchLimit)
	}
	return nil
}

// updateBody returns the raw update the middleware saved, or, for a handler
// reached without it, the decoded body re-encoded. The re-encoding cannot
// express null resets, and it always carries description and notes, so it
// overwrites both; only the router supplies a body that leaves them out.
func updateBody(ctx context.Context, decoded any) ([]byte, error) {
	if b := rawBody(ctx); b != nil {
		return b, nil
	}
	b, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("encode update: %w", err)
	}
	return b, nil
}

func (s artifactoryService) UpdateRepositoryConfiguration(
	ctx context.Context,
	req artgen.UpdateRepositoryConfigurationRequestObject,
) (artgen.UpdateRepositoryConfigurationResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	key := string(req.RepoKey)
	stored, err := st.Repos().Get(ctx, key)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFoundRepo(key)
	case err != nil:
		return nil, s.internalError(ctx, "get repository", err)
	}
	body, err := updateBody(ctx, requestConfig(
		req.JSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesLocalRepositoryConfigurationPlusJSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesRemoteRepositoryConfigurationPlusJSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesVirtualRepositoryConfigurationPlusJSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesFederatedRepositoryConfigurationPlusJSONBody,
	))
	if err != nil {
		return nil, s.internalError(ctx, "read update", err)
	}
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(body, &patch); err != nil || patch == nil {
		return nil, badRequest("A repository configuration object is required")
	}
	updated, err := repos.Merge(stored, patch)
	if err != nil {
		return nil, fmt.Errorf("repositories: %w", err)
	}
	if err := s.checkReferences(ctx, st, &updated); err != nil {
		return nil, err
	}
	if err := s.writeUpdates(ctx, st, []repo.Config{stored}, []repo.Config{updated}); err != nil {
		return nil, s.internalError(ctx, "update repository", err)
	}
	return artgen.UpdateRepositoryConfiguration200TextResponse(
		fmt.Sprintf("Successfully updated repository '%s'", key),
	), nil
}

// writeUpdates stores each of updated, the update of stored's entry at the
// same index, in one transaction, with the reindex an update owes its
// repository (repos.OwesReindex), which is then asked for exactly when the
// update commits.
func (s artifactoryService) writeUpdates(
	ctx context.Context,
	st store.Store,
	stored, updated []repo.Config,
) error {
	var by string
	if p, err := principal(ctx); err == nil {
		by = p.Name
	}
	err := st.Tx(ctx, func(ctx context.Context) error {
		for i, c := range updated {
			if err := st.Repos().Update(ctx, c); err != nil {
				return fmt.Errorf("update %s: %w", c.Key, err)
			}
			if repos.OwesReindex(stored[i], c) {
				if err := s.queue(ctx, st, format.KindReindex, c.Key, by); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("repositories: %w", err)
	}
	return nil
}

// checkReferences rejects an updated configuration whose signing key
// pairs, environments or (for a virtual repository) members do not exist,
// and rewrites its environments to their stored spelling.
func (s artifactoryService) checkReferences(
	ctx context.Context,
	st store.Store,
	c *repo.Config,
) error {
	if err := repos.CheckKeypairs(ctx, st.Keys(), *c); err != nil {
		return fmt.Errorf("repositories: %w", err)
	}
	if err := repos.CheckEnvironments(ctx, st.Projects(), c); err != nil {
		return fmt.Errorf("repositories: %w", err)
	}
	if c.Class != repo.Virtual {
		return nil
	}
	known, err := st.Repos().List(ctx, store.RepoFilter{})
	if err != nil {
		return s.internalError(ctx, "list repositories", err)
	}
	if err := repos.CheckMembers(*c, known); err != nil {
		return fmt.Errorf("repositories: %w", err)
	}
	return nil
}

func (s artifactoryService) UpdateMultipleRepositories(
	ctx context.Context,
	req artgen.UpdateMultipleRepositoriesRequestObject,
) (artgen.UpdateMultipleRepositoriesResponseObject, error) {
	if err := adminOnly(ctx); err != nil {
		return nil, err
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	body, err := updateBody(ctx, requestConfigs(
		req.JSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesLocalRepositoryConfigurationPlusJSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesRemoteRepositoryConfigurationPlusJSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesVirtualRepositoryConfigurationPlusJSONBody,
		req.ApplicationVndOrgJfrogArtifactoryRepositoriesFederatedRepositoryConfigurationPlusJSONBody,
	))
	if err != nil {
		return nil, s.internalError(ctx, "read update", err)
	}
	var patches []map[string]json.RawMessage
	if err := json.Unmarshal(body, &patches); err != nil {
		return nil, badRequest("A list of repository configurations is required")
	}
	if err := batchSize(len(patches)); err != nil {
		return nil, err
	}
	before := make([]repo.Config, 0, len(patches))
	updated := make([]repo.Config, 0, len(patches))
	for i, patch := range patches {
		var key string
		if raw, ok := patch["key"]; ok {
			_ = json.Unmarshal(raw, &key)
		}
		if key == "" {
			return nil, badRequest("Repository %d of the batch has no key", i+1)
		}
		stored, err := st.Repos().Get(ctx, key)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return nil, notFoundRepo(key)
		case err != nil:
			return nil, s.internalError(ctx, "get repository", err)
		}
		c, err := repos.Merge(stored, patch)
		if err != nil {
			return nil, fmt.Errorf("repositories: %w", err)
		}
		if err := s.checkReferences(ctx, st, &c); err != nil {
			return nil, err
		}
		before = append(before, stored)
		updated = append(updated, c)
	}
	if err := s.writeUpdates(ctx, st, before, updated); err != nil {
		return nil, s.internalError(ctx, "update repositories", err)
	}
	return artgen.UpdateMultipleRepositories200JSONResponse(
		fmt.Sprintf("Successfully updated %d repositories", len(updated)),
	), nil
}

func removed(key string, files int64) artgen.RemoveRepositorySuccessReport {
	return artgen.RemoveRepositorySuccessReport{
		RepoKey: key, StatusMsg: fmt.Sprintf(msgRemoved, key), DeletedArtifactsCount: files,
		Success: artgen.True,
	}
}

func noConfig(key string) artgen.RemoveRepositorySuccessReport {
	return artgen.RemoveRepositorySuccessReport{
		RepoKey: key, StatusMsg: fmt.Sprintf(msgNoConfig, key), Success: artgen.True,
	}
}

func failed(key, msg string) artgen.RemoveRepositoryFailureReport {
	return artgen.RemoveRepositoryFailureReport{
		RepoKey: key, StatusMsg: msg, Errors: []artgen.ArtifactDeleteFailure{},
		Success: artgen.False,
	}
}

// refuseDelete returns the message that refuses a delete of key before
// anything is deleted, and whether it is a 403 rather than a 400: the
// build-info repository is a 403, a blank key, the trash can and the support
// bundle repository a 400. ok is false for any other key.
func refuseDelete(key string) (msg string, forbidden, ok bool) {
	switch {
	case strings.EqualFold(key, types.BuildInfoRepository):
		// Keys are unique without case, so any spelling names this one
		// repository, and the message names it by its stored key.
		return fmt.Sprintf(msgBuildInfo, types.BuildInfoRepository), true, true
	case repos.Protected(key):
		return msgProtected, false, true
	}
	return "", false, false
}

// failFast403 is the batch delete's 403 that carries a statusMessage and no
// per-repository reports.
func failFast403(msg string) artgen.DeleteRepositories403JSONResponse {
	var body artgen.DeleteRepositories403JSONResponseBody
	_ = body.FromRepoBulkRemovalFailFastReport(
		artgen.RepoBulkRemovalFailFastReport{StatusMessage: msg},
	)
	return artgen.DeleteRepositories403JSONResponse(body)
}

func (s artifactoryService) DeleteRepository(
	ctx context.Context,
	req artgen.DeleteRepositoryRequestObject,
) (artgen.DeleteRepositoryResponseObject, error) {
	key := string(req.RepoKey)
	admin, err := auth.RequireAdmin(ctx)
	if err != nil {
		// A user without the right gets the documented failure report;
		// without credentials the ordinary 401.
		if p, ok := auth.FromContext(ctx); ok && !p.Anonymous {
			return artgen.DeleteRepository403JSONResponse(
				failed(key, fmt.Sprintf(msgNoRight, key, p.Name, key)),
			), nil
		}
		return nil, fmt.Errorf("repositories: %w", err)
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	if msg, forbidden, ok := refuseDelete(key); ok {
		if forbidden {
			return artgen.DeleteRepository403JSONResponse(failed(key, msg)), nil
		}
		return artgen.DeleteRepository400JSONResponse(failed(key, msg)), nil
	}
	files, found, err := repos.Remove(ctx, st, key, admin.Name)
	switch {
	case err != nil:
		_ = s.internalError(ctx, "delete repository", err)
		return artgen.DeleteRepository500JSONResponse(
			failed(key, fmt.Sprintf("Cannot delete repository: '%s'. See the server log.", key)),
		), nil
	case !found:
		return artgen.DeleteRepository404JSONResponse(noConfig(key)), nil
	}
	if s.cfg.RemoteHTTP != nil {
		s.cfg.RemoteHTTP.Remove(key)
	}
	return artgen.DeleteRepository200JSONResponse(removed(key, files)), nil
}

func (s artifactoryService) DeleteRepositories(
	ctx context.Context,
	req artgen.DeleteRepositoriesRequestObject,
) (artgen.DeleteRepositoriesResponseObject, error) {
	admin, err := auth.RequireAdmin(ctx)
	if err != nil {
		if p, ok := auth.FromContext(ctx); ok && !p.Anonymous {
			return failFast403(fmt.Sprintf(
				"User: ('%s') has insufficient permission to delete repositories", p.Name)), nil
		}
		return nil, fmt.Errorf("repositories: %w", err)
	}
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	var keys []string
	if req.Body != nil {
		keys = *req.Body
	}
	if len(keys) == 0 {
		return artgen.DeleteRepositories400JSONResponse{StatusMessage: msgNoKeys}, nil
	}
	// The whole batch is refused before the first delete, so a refused key
	// late in the list leaves the earlier ones in place. The first refused
	// key decides between 403 and 400.
	for _, key := range keys {
		if msg, forbidden, ok := refuseDelete(key); ok {
			if forbidden {
				return failFast403(msg), nil
			}
			return artgen.DeleteRepositories400JSONResponse{StatusMessage: msg}, nil
		}
	}
	report := artgen.RepoBulkRemovalReport{Reports: []artgen.RepoBulkRemovalReport_Reports_Item{}}
	failures := 0
	for _, key := range keys {
		var item artgen.RepoBulkRemovalReport_Reports_Item
		files, found, err := repos.Remove(ctx, st, key, admin.Name)
		switch {
		case err != nil:
			_ = s.internalError(ctx, "delete repository", err)
			failures++
			_ = item.FromRemoveRepositoryFailureReport(failed(key,
				fmt.Sprintf("Cannot delete repository: '%s'. See the server log.", key)))
		case !found:
			// A missing key counts as removed (JFrog's documented example).
			_ = item.FromRemoveRepositorySuccessReport(noConfig(key))
		default:
			_ = item.FromRemoveRepositorySuccessReport(removed(key, files))
		}
		if err == nil && s.cfg.RemoteHTTP != nil {
			s.cfg.RemoteHTTP.Remove(key)
		}
		report.Reports = append(report.Reports, item)
	}
	switch failures {
	case 0:
		report.StatusMessage = msgAllRemoved
		return artgen.DeleteRepositories200JSONResponse(report), nil
	case len(keys):
		report.StatusMessage = msgAllFailed
		return artgen.DeleteRepositories500JSONResponse(report), nil
	default:
		report.StatusMessage = msgSomeFailed
		return artgen.DeleteRepositories207JSONResponse(report), nil
	}
}
