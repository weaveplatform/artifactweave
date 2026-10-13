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
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The repositories API (research 02, decision 0003). Every write is for
// administrators; reads show a caller the repositories the seeded
// permissions let them read (auth.RepoReader) until P4 evaluates permissions
// fully.

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

// Messages quoted from JFrog's documented examples. The plain-text
// confirmations of create and update are documented only as plain text, so
// theirs are our own wording, as are the error messages not listed here.
const (
	msgKeyExists  = "Case insensitive repository key already exists"
	msgProtected  = "Cannot delete repository: repository key is blank, trash or support bundle"
	msgRemoved    = "Repository '%s' and all its content have been removed successfully."
	msgNoConfig   = "Cannot delete repository: '%s', repository config does not exist"
	msgNoRight    = "Cannot delete repository: '%s', Reason: User: ('%s') has insufficient permission to delete repositories: %s"
	msgAllRemoved = "All repositories were removed successfully"
	msgSomeFailed = "Some repositories failed to be removed"
)

// protectedRepos cannot be deleted: Artifactory's trash can and support
// bundle repositories, and the built-in build-info repository, which the
// repositories API cannot create again (its package type is in no allowed
// list).
var protectedRepos = []string{"auto-trashcan", "jfrog-support-bundles", types.BuildInfoRepository}

func protectedRepo(key string) bool {
	return strings.TrimSpace(key) == "" ||
		slices.ContainsFunc(
			protectedRepos,
			func(p string) bool { return strings.EqualFold(p, key) },
		)
}

// repositoriesSlash routes /artifactory/api/repositories/ as
// /artifactory/api/repositories: the Terraform provider lists repositories
// with the trailing slash (research 08), which chi would otherwise read as a
// repository with an empty key.
func repositoriesSlash(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/artifactory/api/repositories/" {
			r.URL.Path, r.URL.RawPath = "/artifactory/api/repositories", ""
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				rctx.RoutePath = r.URL.Path
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requestDetails keeps what the strict handlers need beyond their decoded
// request: the Accept header for media-type negotiation, and the raw body of
// a repository update. repo.Config cannot tell a field left out (keep the
// stored value) from one sent as null (reset it to its default), and an
// update needs both. It runs after bodyLimit, so the copy is bounded.
func requestDetails(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := withAccept(r.Context(), r.Header.Get("Accept"))
		if r.Method == http.MethodPost && isRepositoryUpdate(r.URL.Path) {
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
	log := s.cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	log.ErrorContext(ctx, op, "request_id", requestIDFrom(ctx), "error", err)
	return apierror.New(http.StatusInternalServerError, "internal error")
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
	can, err := auth.RepoReader(ctx, st.Permissions(), p)
	if err != nil {
		return p, nil, s.internalError(ctx, "repository read rights", err)
	}
	return p, can, nil
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
		// filters, compared without case.
		if req.Params.PackageType != nil &&
			!strings.EqualFold(string(c.PackageType), string(*req.Params.PackageType)) {
			continue
		}
		if !can(c) {
			continue
		}
		out = append(out, artgen.GetRepositoriesByTypeAndProject200JSONResponse{{}}[0])
		e := &out[len(out)-1]
		e.Key, e.Type, e.PackageType = c.Key, strings.ToUpper(
			string(c.Class),
		), string(
			c.PackageType,
		)
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
		c = repo.Masked(c)
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

// prepareCreate applies defaults and validates c for creation against the
// keys already taken (stored and earlier in the same batch).
func prepareCreate(c repo.Config, existing []repo.Config) (repo.Config, error) {
	repo.Defaults(&c)
	if err := repo.Validate(&c); err != nil {
		return c, badRequest("%s", err.Error())
	}
	if slices.ContainsFunc(
		existing,
		func(e repo.Config) bool { return strings.EqualFold(e.Key, c.Key) },
	) {
		return c, badRequest(msgKeyExists)
	}
	return c, nil
}

// checkMembers rejects a virtual repository whose members or default
// deployment repository do not exist among known.
func checkMembers(c repo.Config, known []repo.Config) error {
	if c.Class != repo.Virtual {
		return nil
	}
	for _, m := range c.Repositories {
		if !slices.ContainsFunc(known, func(k repo.Config) bool { return k.Key == m }) {
			return badRequest("Virtual repository member '%s' does not exist", m)
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
	if c, err = prepareCreate(c, existing); err != nil {
		return nil, err
	}
	if err := checkMembers(c, existing); err != nil {
		return nil, err
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
		c, err := prepareCreate(c, nil)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, c)
		known = append(known, c)
	}
	for _, c := range prepared {
		if err := checkMembers(c, known); err != nil {
			return nil, err
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

// applyUpdate merges an update body into the stored configuration with
// Artifactory's rules: a field present replaces the stored value, a field
// left out keeps it, and null resets it to its default. The key, class and
// package type cannot change.
func applyUpdate(stored repo.Config, patch map[string]json.RawMessage) (repo.Config, error) {
	for field, current := range map[string]string{
		"key": stored.Key, "rclass": string(stored.Class), "packageType": string(stored.PackageType),
	} {
		raw, ok := patch[field]
		if !ok || isNull(raw) {
			delete(patch, field)
			continue
		}
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return stored, badRequest("The repository %s cannot be changed", field)
		}
		// Empty is no key, class or package type, so it changes nothing; a
		// re-encoded config carries rclass this way when it was left out.
		if v == "" {
			delete(patch, field)
			continue
		}
		if !strings.EqualFold(v, current) {
			return stored, badRequest("The repository %s cannot be changed", field)
		}
	}
	base, err := json.Marshal(stored)
	if err != nil {
		return stored, fmt.Errorf("encode repository: %w", err)
	}
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(base, &doc); err != nil {
		return stored, fmt.Errorf("decode repository: %w", err)
	}
	for field, raw := range patch {
		if isNull(raw) {
			delete(doc, field)
		} else {
			doc[field] = raw
		}
	}
	merged, err := json.Marshal(doc)
	if err != nil {
		return stored, fmt.Errorf("encode update: %w", err)
	}
	var out repo.Config
	if err := json.Unmarshal(merged, &out); err != nil {
		return stored, badRequest("Invalid repository configuration: %s", err.Error())
	}
	repo.RestoreSecrets(stored, &out)
	repo.Defaults(&out)
	if err := repo.Validate(&out); err != nil {
		return stored, badRequest("%s", err.Error())
	}
	return out, nil
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// updateBody returns the raw update the middleware saved, or, for a handler
// reached without it, the decoded body re-encoded (which cannot express
// null resets).
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
	updated, err := applyUpdate(stored, patch)
	if err != nil {
		return nil, err
	}
	if err := s.checkMembersStored(ctx, st, updated); err != nil {
		return nil, err
	}
	if err := st.Repos().Update(ctx, updated); err != nil {
		return nil, s.internalError(ctx, "update repository", err)
	}
	return artgen.UpdateRepositoryConfiguration200TextResponse(
		fmt.Sprintf("Successfully updated repository '%s'", key),
	), nil
}

func (s artifactoryService) checkMembersStored(
	ctx context.Context,
	st store.Store,
	c repo.Config,
) error {
	if c.Class != repo.Virtual {
		return nil
	}
	known, err := st.Repos().List(ctx, store.RepoFilter{})
	if err != nil {
		return s.internalError(ctx, "list repositories", err)
	}
	return checkMembers(c, known)
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
		c, err := applyUpdate(stored, patch)
		if err != nil {
			return nil, err
		}
		if err := s.checkMembersStored(ctx, st, c); err != nil {
			return nil, err
		}
		updated = append(updated, c)
	}
	err = st.Tx(ctx, func(ctx context.Context) error {
		for _, c := range updated {
			if err := st.Repos().Update(ctx, c); err != nil {
				return err //nolint:wrapcheck // logged by internalError
			}
		}
		return nil
	})
	if err != nil {
		return nil, s.internalError(ctx, "update repositories", err)
	}
	return artgen.UpdateMultipleRepositories200JSONResponse(
		fmt.Sprintf("Successfully updated %d repositories", len(updated)),
	), nil
}

// removeRepository deletes key with its content, in one transaction: the
// items of the repository and, for a remote, its cache (dropping their blob
// references for garbage collection to reclaim), its package index, its
// place in the virtual repositories that list it, and its configuration.
// found is false when no configuration exists.
//
// Deleted content does not pass through the trash can yet; that arrives with
// the trash can itself (#48).
func removeRepository(ctx context.Context, st store.Store, key string) (int64, bool, error) {
	c, err := st.Repos().Get(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, true, fmt.Errorf("get repository: %w", err)
	}
	var files int64
	err = st.Tx(ctx, func(ctx context.Context) error {
		caches := []bool{false}
		if c.Class == repo.Remote {
			caches = append(caches, true)
		}
		for _, cached := range caches {
			res, err := st.Items().Delete(ctx, key, cached, "")
			if err != nil {
				return fmt.Errorf("delete content: %w", err)
			}
			files += res.Files
			for sha, n := range res.BlobRefs {
				if _, err := st.Blobs().Unref(ctx, sha, n); err != nil {
					return fmt.Errorf("release blob %s: %w", sha, err)
				}
			}
		}
		for {
			pkgs, _, err := st.Packages().List(ctx, key, store.Page{Limit: 100})
			if err != nil {
				return fmt.Errorf("list packages: %w", err)
			}
			if len(pkgs) == 0 {
				break
			}
			for _, p := range pkgs {
				if err := st.Packages().Delete(ctx, p.ID); err != nil {
					return fmt.Errorf("delete package %s: %w", p.Name, err)
				}
			}
		}
		including, err := st.Repos().Including(ctx, key)
		if err != nil {
			return fmt.Errorf("virtual repositories including %s: %w", key, err)
		}
		for _, vk := range including {
			v, err := st.Repos().Get(ctx, vk)
			if err != nil {
				return fmt.Errorf("get %s: %w", vk, err)
			}
			v.Repositories = slices.DeleteFunc(
				v.Repositories,
				func(m string) bool { return m == key },
			)
			if v.DefaultDeploymentRepo == key {
				v.DefaultDeploymentRepo = ""
			}
			if err := st.Repos().Update(ctx, v); err != nil {
				return fmt.Errorf("update %s: %w", vk, err)
			}
		}
		if err := st.Repos().Delete(ctx, key); err != nil {
			return fmt.Errorf("delete configuration: %w", err)
		}
		return nil
	})
	if err != nil {
		return files, true, fmt.Errorf("delete repository %s: %w", key, err)
	}
	return files, true, nil
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

func (s artifactoryService) DeleteRepository(
	ctx context.Context,
	req artgen.DeleteRepositoryRequestObject,
) (artgen.DeleteRepositoryResponseObject, error) {
	key := string(req.RepoKey)
	if _, err := auth.RequireAdmin(ctx); err != nil {
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
	if protectedRepo(key) {
		return artgen.DeleteRepository400JSONResponse(failed(key, msgProtected)), nil
	}
	files, found, err := removeRepository(ctx, st, key)
	switch {
	case err != nil:
		_ = s.internalError(ctx, "delete repository", err)
		return artgen.DeleteRepository500JSONResponse(
			failed(key, fmt.Sprintf("Cannot delete repository: '%s'. See the server log.", key)),
		), nil
	case !found:
		return artgen.DeleteRepository404JSONResponse(noConfig(key)), nil
	}
	return artgen.DeleteRepository200JSONResponse(removed(key, files)), nil
}

func (s artifactoryService) DeleteRepositories(
	ctx context.Context,
	req artgen.DeleteRepositoriesRequestObject,
) (artgen.DeleteRepositoriesResponseObject, error) {
	if _, err := auth.RequireAdmin(ctx); err != nil {
		if p, ok := auth.FromContext(ctx); ok && !p.Anonymous {
			var body artgen.DeleteRepositories403JSONResponseBody
			_ = body.FromRepoBulkRemovalFailFastReport(artgen.RepoBulkRemovalFailFastReport{
				StatusMessage: fmt.Sprintf(
					"User: ('%s') has insufficient permission to delete repositories", p.Name),
			})
			return artgen.DeleteRepositories403JSONResponse(body), nil
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
		return artgen.DeleteRepositories400JSONResponse{
			StatusMessage: "No repositories were given",
		}, nil
	}
	if slices.ContainsFunc(keys, protectedRepo) {
		return artgen.DeleteRepositories400JSONResponse{StatusMessage: msgProtected}, nil
	}
	report := artgen.RepoBulkRemovalReport{Reports: []artgen.RepoBulkRemovalReport_Reports_Item{}}
	failures := 0
	for _, key := range keys {
		var item artgen.RepoBulkRemovalReport_Reports_Item
		files, found, err := removeRepository(ctx, st, key)
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
		report.Reports = append(report.Reports, item)
	}
	switch failures {
	case 0:
		report.StatusMessage = msgAllRemoved
		return artgen.DeleteRepositories200JSONResponse(report), nil
	case len(keys):
		report.StatusMessage = "No repository could be removed"
		return artgen.DeleteRepositories500JSONResponse(report), nil
	default:
		report.StatusMessage = msgSomeFailed
		return artgen.DeleteRepositories207JSONResponse(report), nil
	}
}
