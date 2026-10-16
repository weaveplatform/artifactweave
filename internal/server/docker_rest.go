package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	layout "github.com/weaveplatform/artifactweave/internal/format/docker"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/docker"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/registry"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// DeleteDockerTag is the multi-architecture tag delete, the JFrog REST
// operation under a repository's Docker API path (decision 0031): the tag
// folder goes, with a list's platform manifests nothing else in the image
// references. It answers in the Artifactory envelope, as the other JFrog
// operations there do.
func (s artifactoryService) DeleteDockerTag(
	ctx context.Context,
	req artgen.DeleteDockerTagRequestObject,
) (artgen.DeleteDockerTagResponseObject, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		return nil, err
	}
	c, err := dockerRepo(ctx, st, req.RepoKey)
	if err != nil {
		return nil, s.itemError(ctx, "resolve repository", err)
	}
	var name, tag string
	if req.Body != nil {
		name, tag = req.Body.DockerRepository, req.Body.Tag
	}
	switch {
	case name == "" || tag == "":
		return nil, badRequest("dockerRepository and tag are required")
	case !docker.ValidName(name):
		return nil, badRequest("invalid image name '%s'", name)
	case !layout.ValidTag(tag):
		return nil, badRequest("invalid tag '%s'", tag)
	case !acc.file(c, name+"/", types.ActionDelete):
		return nil, denied(p)
	}
	missing := apierror.New(http.StatusNotFound,
		fmt.Sprintf("Tag '%s' of image '%s' does not exist in '%s'", tag, name, c.Key))
	if layout.ReservedTag(tag) {
		return nil, missing
	}
	_, err = (&registry.Service{Store: st, Blobs: s.cfg.Blobs}).DeleteMultiArch(ctx,
		registry.Owner{Repo: c, Name: name, By: p.Name}, tag)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, missing
	case err != nil:
		return nil, s.internalError(ctx, "delete docker tag", err)
	}
	return artgen.DeleteDockerTag200TextResponse(
		fmt.Sprintf("Deleted tag '%s' of image '%s' in '%s'.", tag, name, c.Key)), nil
}

// dockerRepo resolves the Docker, OCI or Helm OCI repository a JFrog
// Docker operation names, as the registry resolves it (decision 0003): a
// missing key or another package type is not found, and a remote or
// virtual repository is refused until the registry serves those.
func dockerRepo(ctx context.Context, st store.Store, key string) (repo.Config, error) {
	c, found, err := docker.Lookup(ctx, st, key)
	switch {
	case err != nil:
		return c, err //nolint:wrapcheck // the lookup names itself
	case !found:
		return c, notFoundRepo(key)
	case c.Class == repo.Remote || c.Class == repo.Virtual:
		return c, apierror.New(http.StatusMethodNotAllowed, fmt.Sprintf(
			"%s is a %s repository, which the registry does not serve yet", c.Key, c.Class))
	default:
		return c, nil
	}
}
