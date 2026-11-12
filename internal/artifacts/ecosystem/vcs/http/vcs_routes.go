package vcshttp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// VCSAPI is the VCS ecosystem's partial strict server: the branch, tag and
// ref listings and the archive and file downloads of a VCS remote
// repository, and the route for GitLab subgroup owners (VCSNested), over
// the item kit. VCS repositories are remote only, so every operation reads
// through the kit's remote sources, after the caller's read permission on
// the project and reference paths.
type VCSAPI struct{ *itemkit.Kit }

type vcsAccess struct {
	repo        repo.Config
	principal   kernel.Principal
	access      itemkit.Access
	isCacheOnly bool
}

func (s VCSAPI) vcsRepository(
	ctx context.Context,
	key, owner, name string,
) (vcsAccess, error) {
	if !ValidProject(owner, name) {
		return vcsAccess{}, apierror.New(http.StatusBadRequest, "invalid VCS repository path")
	}
	st, err := s.Content()
	if err != nil {
		return vcsAccess{}, err
	}
	c, cacheOnly, err := itemkit.ResolveRepo(ctx, st, key)
	if err != nil {
		return vcsAccess{}, s.ItemError(ctx, "VCS repository", err)
	}
	if c.PackageType != repo.VCS || c.Class != repo.Remote {
		return vcsAccess{}, itemkit.NotFoundRepo(key)
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
	if !acc.Folder(c, owner+"/"+name, accessapi.ActionRead) {
		return vcsAccess{}, fmt.Errorf(
			"vcs repository read denied: %w",
			pkgroute.SourceError(source.ErrDenied, p.Anonymous),
		)
	}
	return vcsAccess{repo: c, principal: p, access: acc, isCacheOnly: cacheOnly}, nil
}

func (s VCSAPI) vcsRead(
	ctx context.Context,
	a vcsAccess,
	resource source.Resource,
) (source.Result, error) {
	if s.RemoteSources == nil {
		return source.Result{}, apierror.ErrNotImplemented
	}
	result, err := s.RemoteSources.Read(ctx, source.Request{
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

func (s VCSAPI) vcsResultBytes(
	ctx context.Context,
	result source.Result,
	limit int64,
) ([]byte, error) {
	var body io.ReadCloser
	switch {
	case result.Stream != nil:
		body = result.Stream.Body
	case result.Item != nil:
		if s.Blobs == nil {
			return nil, apierror.ErrNotImplemented
		}
		var err error
		body, _, err = s.Blobs.Open(ctx, result.Item.Actual.SHA256)
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

func (s VCSAPI) vcsRefs(
	ctx context.Context,
	key, owner, name string,
) ([]Ref, vcsAccess, error) {
	a, err := s.vcsRepository(ctx, key, owner, name)
	if err != nil {
		return nil, a, err
	}
	endpoints, err := RefEndpoints(a.repo, owner, name)
	if err != nil {
		return nil, a, apierror.New(http.StatusBadRequest, "invalid VCS upstream")
	}
	var refs []Ref
	for _, endpoint := range endpoints {
		found, err := s.vcsReadRefs(ctx, a, owner, name, endpoint)
		if err != nil {
			return nil, a, err
		}
		if len(found) > MaxRefs-len(refs) {
			return nil, a, apierror.New(http.StatusBadGateway, "too many VCS references")
		}
		refs = append(refs, found...)
	}
	filtered := make([]Ref, 0, len(refs))
	for _, ref := range refs {
		kind := "tags"
		if ref.Branch {
			kind = "heads"
		}
		if a.access.File(a.repo, owner+"/"+name+"/refs/"+kind+"/"+ref.Name, accessapi.ActionRead) {
			filtered = append(filtered, ref)
		}
	}
	return filtered, a, nil
}

func (s VCSAPI) vcsReadRefs(
	ctx context.Context, a vcsAccess, owner, name string, endpoint RefEndpoint,
) ([]Ref, error) {
	// Read permission is checked at the project folder, and the download
	// policy at 7.x's cached refs file, <owner>/<name>/gitrefs
	// (RTFACT-30171), which a listing and every tag, branch or release
	// download read first (RTFACT-30340). An exclude such as
	// <owner>/<name>/* then refuses them, as RTFACT-30743 shows 7.x
	// refusing jqlang/jq/*, where the folder admitted it, and the refusal
	// states the pattern (RTDEV-47144).
	resource := source.Resource{
		Kind:              source.ResourceKindVersions,
		Path:              owner + "/" + name + "/refs",
		ExternalURL:       endpoint.URL.String(),
		CheckURL:          vcsExactURL(endpoint.URL),
		NoCrossOriginAuth: true,
		Method:            http.MethodGet,
		PermissionFolder:  true,
		PermissionPath:    owner + "/" + name,
		PolicyPath:        owner + "/" + name + "/gitrefs",
		Variant:           endpoint.Variant(),
		ValidateMetadata: func(_ context.Context, r io.ReadSeeker, size int64, _ source.MetadataResponse) error {
			if size <= 0 || size > MaxRefsBytes {
				return source.ErrInvalidResult
			}
			data, readErr := io.ReadAll(io.LimitReader(r, MaxRefsBytes+1))
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
	data, err := s.vcsResultBytes(ctx, result, MaxRefsBytes)
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

// GetVcsTags lists the project's tags that the caller may read, as JFrog's
// JSON array of name, commitId and isBranch false. The references come from
// the provider's listing, read through the remote source within
// MaxRefsBytes and MaxRefs, and a reference the caller may not read is
// left out.
func (s VCSAPI) GetVcsTags(
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

// GetVcsBranches lists the project's branches that the caller may read, as
// JFrog's JSON array of name, commitId and isBranch true, from the same
// reference listing as GetVcsTags.
func (s VCSAPI) GetVcsBranches(
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

// GetVcsRefs returns the references the caller may read in git's advertisement
// form, one "<commit> refs/<heads|tags>/<name>" line each, as a JSON string,
// from the same reference listing as GetVcsTags.
func (s VCSAPI) GetVcsRefs(
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
