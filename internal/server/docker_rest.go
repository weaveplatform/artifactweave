package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/weaveplatform/artifactweave/internal/auth"
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
		return nil, badRequestf("dockerRepository and tag are required")
	case !docker.ValidName(name):
		return nil, badRequestf("invalid image name '%s'", name)
	case !layout.ValidTag(tag):
		return nil, badRequestf("invalid tag '%s'", tag)
	case !acc.file(c, name+"/", types.ActionDelete):
		return nil, denied(p)
	}
	missing := apierror.New(http.StatusNotFound,
		fmt.Sprintf("Tag '%s' of image '%s' does not exist in '%s'", tag, name, c.Key))
	if layout.ReservedTag(tag) {
		return nil, missing
	}
	_, err = s.registry(st).DeleteMultiArch(ctx,
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

// PromoteDockerImage copies or moves one tag, or every tag, of an image to
// another local or federated repository of the same package type, under the
// same or another name and tag (decision 0031). It answers in the
// Artifactory envelope, with plain text on success as Artifactory does;
// jf rt docker-promote checks only the status.
func (s artifactoryService) PromoteDockerImage(
	ctx context.Context,
	req artgen.PromoteDockerImageRequestObject,
) (artgen.PromoteDockerImageResponseObject, error) {
	st, err := s.content()
	if err != nil {
		return nil, err
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		return nil, err
	}
	args, err := promotionArgs(req.Body)
	if err != nil {
		return nil, err
	}
	from, err := promotionRepo(ctx, st, req.RepoKey)
	if err != nil {
		return nil, s.itemError(ctx, "resolve source repository", err)
	}
	to, err := promotionRepo(ctx, st, args.targetRepo)
	if err != nil {
		return nil, s.itemError(ctx, "resolve target repository", err)
	}
	if to.PackageType != from.PackageType {
		return nil, badRequestf(
			"'%s' is a %s repository and '%s' a %s one: promotion keeps the package type",
			from.Key,
			from.PackageType,
			to.Key,
			to.PackageType,
		)
	}
	read := types.ActionRead
	if !args.copy {
		read |= types.ActionDelete
	}
	if !acc.file(from, args.name+"/", read) || !acc.file(to, args.toName+"/", types.ActionWrite) {
		return nil, denied(p)
	}
	svc := s.registry(st)
	promotion := registry.Promotion{
		From: registry.Owner{Repo: from, Name: args.name, By: p.Name},
		To:   registry.Owner{Repo: to, Name: args.toName, By: p.Name},
		Copy: args.copy,
	}
	if promotion.Tags, err = s.promotionTags(ctx, svc, promotion, args, acc, p); err != nil {
		return nil, err
	}
	err = svc.Promote(ctx, promotion)
	switch {
	case errors.Is(err, registry.ErrSamePlace):
		return nil, badRequestf("the image would be promoted onto itself")
	case err != nil:
		return nil, s.internalError(ctx, "docker promotion", err)
	}
	return artgen.PromoteDockerImage200TextResponse(fmt.Sprintf(
		"Promoted %d tag(s) of '%s' from '%s' to '%s/%s'.",
		len(promotion.Tags), args.name, from.Key, to.Key, args.toName)), nil
}

// promotion is a promote request's fields, with the CLI's empty strings
// read as absent and the target's names defaulted to the source's.
type promotion struct {
	targetRepo    string
	name, tag     string
	toName, toTag string
	copy          bool
}

// promotionArgs validates a promote request's body.
func promotionArgs(b *artgen.PromoteDockerImageJSONRequestBody) (promotion, error) {
	if b == nil || b.TargetRepo == "" || b.DockerRepository == "" {
		return promotion{}, badRequestf("targetRepo and dockerRepository are required")
	}
	a := promotion{
		targetRepo: b.TargetRepo,
		name:       b.DockerRepository, tag: deref(b.Tag),
		copy: b.Copy != nil && *b.Copy,
	}
	a.toName = orDefault(deref(b.TargetDockerRepository), a.name)
	a.toTag = orDefault(deref(b.TargetTag), a.tag)
	switch {
	case !docker.ValidName(a.name):
		return a, badRequestf("invalid image name '%s'", a.name)
	case !docker.ValidName(a.toName):
		return a, badRequestf("invalid image name '%s'", a.toName)
	case a.tag == "" && a.toTag != "":
		return a, badRequestf("targetTag needs tag: promoting every tag keeps their names")
	case a.tag != "" && !layout.ValidTag(a.tag):
		return a, badRequestf("invalid tag '%s'", a.tag)
	case a.tag != "" && !layout.ValidTag(a.toTag):
		return a, badRequestf("invalid tag '%s'", a.toTag)
	}
	return a, nil
}

// promotionTags lists the tags a promotion takes, each with the tag it
// lands as, and refuses one that would replace a different manifest at the
// target without Delete there, which a push over a tag needs too (DV-035).
func (s artifactoryService) promotionTags(
	ctx context.Context,
	svc *registry.Service,
	pr registry.Promotion,
	args promotion,
	acc access,
	p auth.Principal,
) ([]registry.Rename, error) {
	tags, err := svc.PromotedTags(ctx, pr.From, args.tag)
	switch {
	case errors.Is(err, store.ErrNotFound) && args.tag == "":
		return nil, apierror.New(http.StatusNotFound,
			fmt.Sprintf("Image '%s' does not exist in '%s'", args.name, pr.From.Repo.Key))
	case errors.Is(err, store.ErrNotFound):
		return nil, apierror.New(http.StatusNotFound, fmt.Sprintf(
			"Tag '%s' of image '%s' does not exist in '%s'", args.tag, args.name, pr.From.Repo.Key))
	case err != nil:
		return nil, s.internalError(ctx, "docker promotion tags", err)
	}
	renames := make([]registry.Rename, 0, len(tags))
	for _, t := range tags {
		r := registry.Rename{From: t, To: orDefault(args.toTag, t)}
		manifest, err := svc.TagManifest(ctx, pr.From, t)
		if err != nil {
			return nil, s.internalError(ctx, "docker promotion source", err)
		}
		replaces, err := svc.Replaces(ctx, pr.To, r.To, manifest.Actual.SHA256)
		if err != nil {
			return nil, s.internalError(ctx, "docker promotion target", err)
		}
		if replaces && !acc.file(pr.To.Repo, pr.To.Name+"/", types.ActionDelete) {
			return nil, denied(p)
		}
		renames = append(renames, r)
	}
	return renames, nil
}

// FindParentManifestLists answers which manifest lists and image indexes of
// an image name the manifest a tag holds (decision 0031).
func (s artifactoryService) FindParentManifestLists(
	ctx context.Context,
	req artgen.FindParentManifestListsRequestObject,
) (artgen.FindParentManifestListsResponseObject, error) {
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
		return nil, badRequestf("dockerRepository and tag are required")
	case !docker.ValidName(name):
		return nil, badRequestf("invalid image name '%s'", name)
	case !layout.ValidTag(tag):
		return nil, badRequestf("invalid tag '%s'", tag)
	case !acc.file(c, name+"/", types.ActionRead):
		return nil, denied(p)
	}
	parents, err := s.registry(st).ParentManifests(ctx,
		registry.Owner{Repo: c, Name: name, By: p.Name}, tag)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, apierror.New(http.StatusNotFound,
			fmt.Sprintf("Tag '%s' of image '%s' does not exist in '%s'", tag, name, c.Key))
	case err != nil:
		return nil, s.internalError(ctx, "docker parent manifests", err)
	}
	//nolint:tagliatelle // the generated response's field names, JFrog's own
	type entry = struct {
		Digest           *string   `json:"digest,omitempty"`
		DockerRepository *string   `json:"dockerRepository,omitempty"`
		Tags             *[]string `json:"tags,omitempty"`
	}
	lists := make([]entry, 0, len(parents))
	for _, pm := range parents {
		lists = append(lists, entry{Digest: &pm.Digest, DockerRepository: &name, Tags: &pm.Tags})
	}
	return artgen.FindParentManifestLists200JSONResponse{ParentManifestLists: &lists}, nil
}

// promotionRepo resolves a repository a promotion names: a missing key is
// not found, and anything but a local or federated Docker, OCI or Helm OCI
// repository cannot take part (decision 0031).
func promotionRepo(ctx context.Context, st store.Store, key string) (repo.Config, error) {
	c, served, err := docker.Lookup(ctx, st, key)
	switch {
	case err != nil:
		return c, err //nolint:wrapcheck // the lookup names itself
	case c.Key == "":
		return c, notFoundRepo(key)
	case !served:
		return c, badRequestf("'%s' is not a Docker, OCI or Helm OCI repository", c.Key)
	case c.Class != repo.Local && c.Class != repo.Federated:
		return c, badRequestf("'%s' is a %s repository; promotion needs local or federated ones",
			c.Key, c.Class)
	}
	return c, nil
}

// orDefault is v, or def when v is empty: the JFrog CLI sends an empty
// string for every name or tag it was not given.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
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
