package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/server/vcs"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type vcsAccess struct {
	repo        repo.Config
	principal   auth.Principal
	access      access
	isCacheOnly bool
}

func (s artifactoryService) vcsRepository(
	ctx context.Context,
	key, owner, name string,
) (vcsAccess, error) {
	if !vcs.ValidProject(owner, name) {
		return vcsAccess{}, apierror.New(http.StatusBadRequest, "invalid VCS repository path")
	}
	st, err := s.content()
	if err != nil {
		return vcsAccess{}, err
	}
	c, cacheOnly, err := resolveRepo(ctx, st, key)
	if err != nil {
		return vcsAccess{}, s.itemError(ctx, "VCS repository", err)
	}
	if c.PackageType != repo.VCS || c.Class != repo.Remote {
		return vcsAccess{}, notFoundRepo(key)
	}
	switch c.VCSGitProvider {
	case "", repo.GitHub, repo.GitHubEnterprise, repo.GitLab, repo.Bitbucket, repo.CustomVCS:
	default:
		return vcsAccess{}, apierror.New(
			http.StatusNotImplemented,
			"VCS provider adapter unavailable",
		)
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		return vcsAccess{}, err
	}
	if !acc.folder(c, owner+"/"+name, types.ActionRead) {
		return vcsAccess{}, fmt.Errorf(
			"VCS repository read denied: %w",
			pkgroute.SourceError(source.ErrDenied, p.Anonymous),
		)
	}
	return vcsAccess{repo: c, principal: p, access: acc, isCacheOnly: cacheOnly}, nil
}

func (s artifactoryService) vcsRead(
	ctx context.Context,
	a vcsAccess,
	resource source.Resource,
) (source.Result, error) {
	if s.cfg.RemoteSources == nil {
		return source.Result{}, apierror.ErrNotImplemented
	}
	result, err := s.cfg.RemoteSources.Read(ctx, source.Request{
		Repo:      a.repo,
		Subject:   a.principal.Subject(),
		Access:    a.access.d,
		Resource:  resource,
		Project:   a.repo.ProjectKey,
		CacheOnly: a.isCacheOnly,
	})
	if err != nil {
		return source.Result{}, s.itemError(
			ctx,
			"VCS source",
			pkgroute.SourceError(err, a.principal.Anonymous),
		)
	}
	return result, nil
}

func (s artifactoryService) vcsResultBytes(
	ctx context.Context,
	result source.Result,
	limit int64,
) ([]byte, error) {
	var body io.ReadCloser
	switch {
	case result.Stream != nil:
		body = result.Stream.Body
	case result.Item != nil:
		if s.cfg.Blobs == nil {
			return nil, apierror.ErrNotImplemented
		}
		var err error
		body, _, err = s.cfg.Blobs.Open(ctx, result.Item.Actual.SHA256)
		if err != nil {
			return nil, s.internalError(ctx, "VCS cached blob", err)
		}
	default:
		return nil, source.ErrInvalidResult
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, source.ErrInvalidResult
	}
	if result.Revalidate != nil {
		if err := result.Revalidate(ctx); err != nil {
			return nil, fmt.Errorf(
				"revalidate VCS references: %w",
				pkgroute.SourceError(err, false),
			)
		}
	}
	return data, nil
}

func (s artifactoryService) vcsRefs(
	ctx context.Context,
	key, owner, name string,
) ([]vcs.Ref, vcsAccess, error) {
	a, err := s.vcsRepository(ctx, key, owner, name)
	if err != nil {
		return nil, a, err
	}
	target, err := vcs.GitRefsURL(a.repo, owner, name)
	if err != nil {
		return nil, a, apierror.New(http.StatusBadRequest, "invalid VCS upstream")
	}
	resource := source.Resource{
		Kind:             source.Versions,
		Path:             owner + "/" + name + "/refs",
		ExternalURL:      target.String(),
		CheckURL:         vcsExactURL(target),
		Method:           http.MethodGet,
		PermissionFolder: true,
		PermissionPath:   owner + "/" + name,
		Variant:          "vcs-git-refs-v1",
		ValidateMetadata: func(_ context.Context, r io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			if size <= 0 || size > vcs.MaxRefsBytes {
				return source.ErrInvalidResult
			}
			data, readErr := io.ReadAll(io.LimitReader(r, vcs.MaxRefsBytes+1))
			if readErr != nil || int64(len(data)) != size {
				return source.ErrInvalidResult
			}
			if _, parseErr := vcs.ParseRefs(data); parseErr != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := s.vcsRead(ctx, a, resource)
	if err != nil {
		return nil, a, err
	}
	data, err := s.vcsResultBytes(ctx, result, vcs.MaxRefsBytes)
	if err != nil {
		return nil, a, s.itemError(ctx, "VCS references", err)
	}
	refs, err := vcs.ParseRefs(data)
	if err != nil {
		return nil, a, apierror.New(http.StatusBadGateway, "invalid VCS references")
	}
	filtered := make([]vcs.Ref, 0, len(refs))
	for _, ref := range refs {
		kind := "tags"
		if ref.Branch {
			kind = "heads"
		}
		if a.access.file(a.repo, owner+"/"+name+"/refs/"+kind+"/"+ref.Name, types.ActionRead) {
			filtered = append(filtered, ref)
		}
	}
	return filtered, a, nil
}

func vcsExactURL(target *url.URL) func(context.Context, *url.URL) error {
	return func(_ context.Context, next *url.URL) error {
		if target.String() != next.String() {
			return source.ErrDenied
		}
		return nil
	}
}

type vcsListRef struct {
	Name     string `json:"name"`
	CommitID string `json:"commitId"` //nolint:tagliatelle // JFrog VCS wire field.
	IsBranch bool   `json:"isBranch"` //nolint:tagliatelle // JFrog VCS wire field.
}

type vcsListResponse []vcsListRef

func (refs vcsListResponse) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(refs); err != nil {
		return fmt.Errorf("write VCS references: %w", err)
	}
	return nil
}

func (refs vcsListResponse) VisitGetVcsTagsResponse(w http.ResponseWriter) error {
	return refs.write(w)
}

func (refs vcsListResponse) VisitGetVcsBranchesResponse(w http.ResponseWriter) error {
	return refs.write(w)
}

func (s artifactoryService) GetVcsTags(
	ctx context.Context,
	req artgen.GetVcsTagsRequestObject,
) (artgen.GetVcsTagsResponseObject, error) {
	refs, _, err := s.vcsRefs(ctx, req.RepoKey, req.UserOrg, req.Repo)
	if err != nil {
		return nil, err
	}
	out := make(vcsListResponse, 0)
	for _, ref := range refs {
		if !ref.Branch {
			out = append(out, vcsListRef{ref.Name, ref.CommitID, false})
		}
	}
	return out, nil
}

func (s artifactoryService) GetVcsBranches(
	ctx context.Context,
	req artgen.GetVcsBranchesRequestObject,
) (artgen.GetVcsBranchesResponseObject, error) {
	refs, _, err := s.vcsRefs(ctx, req.RepoKey, req.UserOrg, req.Repo)
	if err != nil {
		return nil, err
	}
	out := make(vcsListResponse, 0)
	for _, ref := range refs {
		if ref.Branch {
			out = append(out, vcsListRef{ref.Name, ref.CommitID, true})
		}
	}
	return out, nil
}

func (s artifactoryService) GetVcsRefs(
	ctx context.Context,
	req artgen.GetVcsRefsRequestObject,
) (artgen.GetVcsRefsResponseObject, error) {
	refs, _, err := s.vcsRefs(ctx, req.RepoKey, req.UserOrg, req.Repo)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for _, ref := range refs {
		kind := "tags"
		if ref.Branch {
			kind = "heads"
		}
		_, _ = fmt.Fprintf(&b, "%s refs/%s/%s\n", ref.CommitID, kind, ref.Name)
	}
	return artgen.GetVcsRefs200JSONResponse(b.String()), nil
}
