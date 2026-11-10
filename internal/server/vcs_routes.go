package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	vcshttp "github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/vcs/http"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type vcsAccess struct {
	repo        repo.Config
	principal   kernel.Principal
	access      Access
	isCacheOnly bool
}

func (s artifactoryService) vcsRepository(
	ctx context.Context,
	key, owner, name string,
) (vcsAccess, error) {
	if !vcshttp.ValidProject(owner, name) {
		return vcsAccess{}, apierror.New(http.StatusBadRequest, "invalid VCS repository path")
	}
	st, err := s.Content()
	if err != nil {
		return vcsAccess{}, err
	}
	c, cacheOnly, err := ResolveRepo(ctx, st, key)
	if err != nil {
		return vcsAccess{}, s.ItemError(ctx, "VCS repository", err)
	}
	if c.PackageType != repo.VCS || c.Class != repo.Remote {
		return vcsAccess{}, NotFoundRepo(key)
	}
	switch c.VCSGitProvider {
	case "",
		repo.GitHub,
		repo.GitHubEnterprise,
		repo.GitLab,
		repo.Bitbucket,
		repo.CustomVCS,
		repo.Stash,
		repo.OldStash,
		repo.ArtifactoryVCS:
	default:
		return vcsAccess{}, apierror.New(
			http.StatusNotImplemented,
			"VCS provider adapter unavailable",
		)
	}
	p, acc, err := s.ItemCaller(ctx, st)
	if err != nil {
		return vcsAccess{}, err
	}
	if !acc.Folder(c, owner+"/"+name, types.ActionRead) {
		return vcsAccess{}, fmt.Errorf(
			"vcs repository read denied: %w",
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
		Subject:   accessapi.SubjectOf(a.principal),
		Access:    a.access.Decider,
		Resource:  resource,
		Project:   a.repo.ProjectKey,
		CacheOnly: a.isCacheOnly,
	})
	if err != nil {
		return source.Result{}, s.ItemError(
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
			return nil, s.InternalError(ctx, "VCS cached blob", err)
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
				"revalidate vcs references: %w",
				pkgroute.SourceError(err, false),
			)
		}
	}
	return data, nil
}

func (s artifactoryService) vcsRefs(
	ctx context.Context,
	key, owner, name string,
) ([]vcshttp.Ref, vcsAccess, error) {
	a, err := s.vcsRepository(ctx, key, owner, name)
	if err != nil {
		return nil, a, err
	}
	endpoints, err := vcshttp.RefEndpoints(a.repo, owner, name)
	if err != nil {
		return nil, a, apierror.New(http.StatusBadRequest, "invalid VCS upstream")
	}
	var refs []vcshttp.Ref
	for _, endpoint := range endpoints {
		found, err := s.vcsReadRefs(ctx, a, owner, name, endpoint)
		if err != nil {
			return nil, a, err
		}
		if len(found) > vcshttp.MaxRefs-len(refs) {
			return nil, a, apierror.New(http.StatusBadGateway, "too many VCS references")
		}
		refs = append(refs, found...)
	}
	filtered := make([]vcshttp.Ref, 0, len(refs))
	for _, ref := range refs {
		kind := "tags"
		if ref.Branch {
			kind = "heads"
		}
		if a.access.File(a.repo, owner+"/"+name+"/refs/"+kind+"/"+ref.Name, types.ActionRead) {
			filtered = append(filtered, ref)
		}
	}
	return filtered, a, nil
}

func (s artifactoryService) vcsReadRefs(
	ctx context.Context, a vcsAccess, owner, name string, endpoint vcshttp.RefEndpoint,
) ([]vcshttp.Ref, error) {
	resource := source.Resource{
		Kind:              source.ResourceKindVersions,
		Path:              owner + "/" + name + "/refs",
		ExternalURL:       endpoint.URL.String(),
		CheckURL:          vcsExactURL(endpoint.URL),
		NoCrossOriginAuth: true,
		Method:            http.MethodGet,
		PermissionFolder:  true,
		PermissionPath:    owner + "/" + name,
		Variant:           endpoint.Variant(),
		ValidateMetadata: func(_ context.Context, r io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			if size <= 0 || size > vcshttp.MaxRefsBytes {
				return source.ErrInvalidResult
			}
			data, readErr := io.ReadAll(io.LimitReader(r, vcshttp.MaxRefsBytes+1))
			if readErr != nil || int64(len(data)) != size {
				return source.ErrInvalidResult
			}
			if _, parseErr := endpoint.Parse(data); parseErr != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	result, err := s.vcsRead(ctx, a, resource)
	if err != nil {
		return nil, err
	}
	data, err := s.vcsResultBytes(ctx, result, vcshttp.MaxRefsBytes)
	if err != nil {
		return nil, s.ItemError(ctx, "VCS references", err)
	}
	refs, err := endpoint.Parse(data)
	if err != nil {
		return nil, apierror.New(http.StatusBadGateway, "invalid VCS references")
	}
	return refs, nil
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

func (r vcsListResponse) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(r); err != nil {
		return fmt.Errorf("write vcs references: %w", err)
	}
	return nil
}

func (r vcsListResponse) VisitGetVcsTagsResponse(w http.ResponseWriter) error {
	return r.write(w)
}

func (r vcsListResponse) VisitGetVcsBranchesResponse(w http.ResponseWriter) error {
	return r.write(w)
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
