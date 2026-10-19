package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/format/maven"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	mavenCalculatePrefix = "/artifactory/api/maven/calculateMetadata/"
	mavenGeneratePrefix  = "/artifactory/api/maven/generatePom/"
)

// calculateMetadataActions are what calculateMetadata needs on the folder:
// "Manage, Delete/Overwrite, Deploy/Cache, Annotate, and Read", as JFrog's
// REST reference listed them from 4.8 (research 27 section 9). Manage
// alone is not enough, since a grant may hold it without the others.
const calculateMetadataActions = types.ActionManage | types.ActionDelete | types.ActionWrite |
	types.ActionAnnotate | types.ActionRead

// registerMavenRoutes serves the Maven REST operations, hand-written in
// the spec, under the patterns chiPatterns derives so they replace the 501
// stubs (decision 0036).
func (s artifactoryService) registerMavenRoutes(r chi.Router) {
	for _, p := range chiPatterns(mavenCalculatePrefix + "{repoKey}/{folderPath}") {
		r.Post(p, s.calculateMavenMetadata)
	}
	for _, p := range chiPatterns(mavenGeneratePrefix + "{repoKey}/{jarFilePath}") {
		r.Post(p, s.generateMavenPom)
	}
}

// mavenTarget is the repository and path a Maven operation names, once the
// caller may act on it.
type mavenTarget struct {
	p    auth.Principal
	acc  access
	st   store.Store
	c    repo.Config
	path string
	it   types.Item
}

// mavenTargetOf resolves the repository and path after prefix in r's URL
// for a caller who holds want at that path. The answers are Artifactory's
// (research 27 section 9): 404 for a missing path or a repository that is
// not local, and 400 for a type outside the Maven group.
func (s artifactoryService) mavenTargetOf(
	r *http.Request,
	prefix string,
	want types.Actions,
) (mavenTarget, error) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		return mavenTarget{}, err
	}
	key, p, _, err := splitRepoPath(strings.TrimPrefix(r.URL.EscapedPath(), prefix))
	if err != nil {
		return mavenTarget{}, err
	}
	who, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		return mavenTarget{}, err
	}
	missing := apierror.New(http.StatusNotFound, fmt.Sprintf("Could not find path '%s/%s'", key, p))
	c, cached, err := resolveRepo(ctx, st, key)
	var api *apierror.Error
	switch {
	case errors.As(err, &api) && api.Status == http.StatusNotFound:
		return mavenTarget{}, missing
	case err != nil:
		return mavenTarget{}, s.itemError(ctx, "resolve repository", err)
	case !acc.file(c, p, want):
		return mavenTarget{}, denied(who)
	case cached || c.Class != repo.Local:
		return mavenTarget{}, apierror.New(http.StatusNotFound,
			fmt.Sprintf("Unable to find local repository '%s'.", key))
	case !maven.Serves(c.PackageType):
		return mavenTarget{}, badRequest("Repository '%s' is not a maven repository.", key)
	}
	t := mavenTarget{p: who, acc: acc, st: st, c: c, path: p}
	if p == "" {
		return t, nil
	}
	t.it, err = st.Items().Get(ctx, c.Key, false, p)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return mavenTarget{}, missing
	case err != nil:
		return mavenTarget{}, s.itemError(ctx, "get item", err)
	}
	return t, nil
}

// calculateMavenMetadata answers POST /api/maven/calculateMetadata: it
// queues the metadata of the folder and, unless noneRecursive is true,
// everything under it, and answers 200 with an empty body once it has.
func (s artifactoryService) calculateMavenMetadata(w http.ResponseWriter, r *http.Request) {
	recursive := true
	if v := r.URL.Query().Get("noneRecursive"); v != "" {
		none, err := strconv.ParseBool(v)
		if err != nil {
			writeErr(w, r, badRequest("noneRecursive must be true or false"))
			return
		}
		recursive = !none
	}
	t, err := s.mavenTargetOf(r, mavenCalculatePrefix, calculateMetadataActions)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	if err := maven.CalculateMetadata(
		ctx,
		s.items(t.st).Env(),
		t.c,
		t.path,
		recursive,
	); err != nil {
		writeErr(w, r, s.itemError(ctx, "calculate Maven metadata", err))
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
}

// generateMavenPom answers POST /api/maven/generatePom: it writes the POM
// of a deployed jar through the item service, so the POM checks, the index
// and the metadata queue apply, and answers 200 with no body, as it does
// when the POM is already there. The caller needs Read on the jar, whose
// content the POM may come from, and Deploy where the POM goes, which is
// what writing it by hand would take (decision 0036).
func (s artifactoryService) generateMavenPom(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, err := s.mavenTargetOf(r, mavenGeneratePrefix, types.ActionRead)
	if err == nil && (t.path == "" || t.it.Folder) {
		err = badRequest("'%s/%s' is not a file", t.c.Key, t.path)
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	svc := s.items(t.st)
	at, body, err := maven.GeneratePOM(ctx, svc.Env(), t.c, t.it)
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "generate POM", err))
		return
	}
	if err := s.writePom(ctx, svc, t, at, body); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// writePom deploys body at p unless something is there, for a caller who
// may deploy at p.
func (s artifactoryService) writePom(
	ctx context.Context,
	svc *item.Service,
	t mavenTarget,
	p string,
	body []byte,
) error {
	if !t.acc.file(t.c, p, types.ActionWrite) {
		return denied(t.p)
	}
	_, err := svc.Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: t.c, Path: p}, Body: bytes.NewReader(body), By: t.p.Name,
		IfAbsent: true,
	})
	if err == nil || errors.Is(err, item.ErrExists) {
		return nil
	}
	return s.itemError(ctx, "deploy POM", err)
}
