package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/server/vcs"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type vcsBody struct {
	io.ReadCloser
	ctx        context.Context
	used       func(context.Context) error
	isComplete bool
}

func (b *vcsBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) && !b.isComplete {
		b.isComplete = true
		if b.used != nil {
			_ = b.used(b.ctx)
		}
	}
	// io.Reader callers require an unwrapped io.EOF to terminate cleanly.
	return n, err //nolint:wrapcheck
}

func (s artifactoryService) vcsOpenResult(
	ctx context.Context,
	result source.Result,
) (io.ReadCloser, int64, error) {
	if result.Revalidate != nil {
		if err := result.Revalidate(ctx); err != nil {
			if result.Stream != nil {
				_ = result.Stream.Body.Close()
			}
			return nil, 0, fmt.Errorf("revalidate vcs source: %w", pkgroute.SourceError(err, false))
		}
	}
	var body io.ReadCloser
	var size int64
	switch {
	case result.Stream != nil:
		body, size = result.Stream.Body, result.Stream.Size
	case result.Item != nil:
		if s.cfg.Blobs == nil {
			return nil, 0, apierror.ErrNotImplemented
		}
		var err error
		body, _, err = s.cfg.Blobs.Open(ctx, result.Item.Actual.SHA256)
		if err != nil {
			return nil, 0, s.internalError(ctx, "VCS cached archive", err)
		}
		size = result.Item.Size
	default:
		return nil, 0, source.ErrInvalidResult
	}
	if size < 0 {
		size = 0
	}
	return &vcsBody{ReadCloser: body, ctx: ctx, used: result.Used}, size, nil
}

func vcsArchiveURLPolicy(
	c repo.Config,
	owner, name string,
	target *url.URL,
) func(context.Context, *url.URL) error {
	root := "/" + owner + "/" + name + "/"
	base, err := url.Parse(c.URL)
	if err != nil {
		return vcsExactURL(target)
	}
	baseRoot := strings.TrimSuffix(base.Path, "/") + root
	return func(_ context.Context, next *url.URL) error {
		if target.String() == next.String() {
			return nil
		}
		if next.Scheme == base.Scheme && strings.EqualFold(next.Host, base.Host) &&
			strings.HasPrefix(next.Path, baseRoot) && next.User == nil && next.Fragment == "" {
			return nil
		}
		if (c.VCSGitProvider == repo.GitHub || c.VCSGitProvider == "") &&
			base.Scheme == "https" && next.Scheme == "https" &&
			strings.EqualFold(base.Hostname(), "github.com") &&
			strings.EqualFold(next.Hostname(), "codeload.github.com") &&
			strings.HasPrefix(next.Path, root) && next.User == nil && next.Fragment == "" {
			return nil
		}
		return source.ErrDenied
	}
}

func (s artifactoryService) vcsArchive(
	ctx context.Context,
	key, owner, name, kind, ref, ext, propertyFilter string,
) (io.ReadCloser, int64, error) {
	if !vcs.ValidExtension(ext) || kind != "tags" && kind != "heads" && kind != "commits" {
		return nil, 0, apierror.New(http.StatusBadRequest, "invalid VCS archive request")
	}
	a, err := s.vcsRepository(ctx, key, owner, name)
	if err != nil {
		return nil, 0, err
	}
	if !vcs.ValidRef(ref) || kind == "commits" && !vcs.ValidCommitID(ref) {
		return nil, 0, apierror.New(http.StatusBadRequest, "invalid VCS reference")
	}
	commit := ref
	if kind != "commits" {
		refs, _, err := s.vcsRefs(ctx, key, owner, name)
		if err != nil {
			return nil, 0, err
		}
		found := false
		for _, candidate := range refs {
			if candidate.Name == ref && candidate.Branch == (kind == "heads") {
				commit = candidate.CommitID
				found = true
				break
			}
		}
		if !found {
			return nil, 0, apierror.New(http.StatusNotFound, "VCS reference not found")
		}
	}
	permissionPath := owner + "/" + name + "/refs/" + kind + "/" + ref
	if !a.access.file(a.repo, permissionPath, types.ActionRead) {
		return nil, 0, fmt.Errorf(
			"vcs archive read denied: %w",
			pkgroute.SourceError(source.ErrDenied, a.principal.Anonymous),
		)
	}
	target, err := vcs.ArchiveURL(a.repo, owner, name, commit, ext)
	if err != nil {
		return nil, 0, apierror.New(http.StatusBadRequest, "invalid VCS archive source")
	}
	resource := source.Resource{
		Kind:              source.ResourceKindArtifact,
		Path:              permissionPath + "/" + name + "-" + commit + "." + ext,
		ExternalURL:       target.String(),
		CheckURL:          vcsArchiveURLPolicy(a.repo, owner, name, target),
		NoCrossOriginAuth: true,
		Method:            http.MethodGet,
		Variant:           "vcs-archive-v1-" + ext,
		Validate: func(ctx context.Context, body io.ReadSeeker, size int64) error {
			if err := vcs.ValidateArchive(ctx, body, size, ext); err != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	filter, err := vcsPropertyFilter(propertyFilter)
	if err != nil {
		return nil, 0, err
	}
	if len(filter) > 0 {
		// A property constraint can only select an already cached artifact.
		// It must never trigger an upstream download to create a match.
		a.isCacheOnly = true
	}
	result, err := s.vcsRead(ctx, a, resource)
	if err != nil {
		return nil, 0, err
	}
	if len(filter) > 0 {
		if err := s.vcsMatchCachedProperties(ctx, result, filter); err != nil {
			return nil, 0, err
		}
	}
	return s.vcsOpenResult(ctx, result)
}

func (s artifactoryService) vcsMatchCachedProperties(
	ctx context.Context,
	result source.Result,
	filter map[string]string,
) error {
	notFound := apierror.New(http.StatusNotFound, "VCS archive with matching properties not found")
	if result.Item == nil || result.Item.ID == 0 {
		if result.Stream != nil {
			_ = result.Stream.Body.Close()
		}
		return notFound
	}
	st, err := s.content()
	if err != nil {
		return err
	}
	props, err := st.Items().Properties(ctx, result.Item.ID)
	if err != nil {
		return s.internalError(ctx, "VCS cached properties", err)
	}
	for key, value := range filter {
		found := false
		for _, got := range props[key] {
			if got == value {
				found = true
			}
		}
		if !found {
			return notFound
		}
	}
	return nil
}

func vcsPropertyFilter(raw string) (map[string]string, error) {
	if raw == "" {
		return map[string]string{}, nil
	}
	if len(raw) > 1024 || strings.Contains(raw, ";") {
		return nil, apierror.New(http.StatusBadRequest, "invalid VCS property filter")
	}
	parsed, err := url.ParseQuery(raw)
	if err != nil || len(parsed) == 0 || len(parsed) > 8 {
		return nil, apierror.New(http.StatusBadRequest, "invalid VCS property filter")
	}
	filter := make(map[string]string, len(parsed))
	for key, values := range parsed {
		if key == "" || len(key) > 128 || len(values) != 1 || values[0] == "" ||
			len(values[0]) > 256 {
			return nil, apierror.New(http.StatusBadRequest, "invalid VCS property filter")
		}
		filter[key] = values[0]
	}
	return filter, nil
}

func vcsExt[T ~string](p *T) string {
	if p == nil || string(*p) == "" {
		return "tar.gz"
	}
	return string(*p)
}

func vcsProperties(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (s artifactoryService) DownloadVcsTag(
	ctx context.Context,
	req artgen.DownloadVcsTagRequestObject,
) (artgen.DownloadVcsTagResponseObject, error) {
	body, size, err := s.vcsArchive(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.Repo,
		"tags",
		req.TagName,
		vcsExt(req.Params.Ext),
		"",
	)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadVcsTag200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}

func (s artifactoryService) DownloadVcsBranch(
	ctx context.Context,
	req artgen.DownloadVcsBranchRequestObject,
) (artgen.DownloadVcsBranchResponseObject, error) {
	body, size, err := s.vcsArchive(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.Repo,
		"heads",
		req.BranchName,
		vcsExt(req.Params.Ext),
		vcsProperties(req.Params.Properties),
	)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadVcsBranch200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}

func (s artifactoryService) DownloadVcsCommit(
	ctx context.Context,
	req artgen.DownloadVcsCommitRequestObject,
) (artgen.DownloadVcsCommitResponseObject, error) {
	body, size, err := s.vcsArchive(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.Repo,
		"commits",
		req.CommitId,
		vcsExt(req.Params.Ext),
		"",
	)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadVcsCommit200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}

func (s artifactoryService) DownloadVcsRelease(
	ctx context.Context,
	req artgen.DownloadVcsReleaseRequestObject,
) (artgen.DownloadVcsReleaseResponseObject, error) {
	body, size, err := s.vcsArchive(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.Repo,
		"tags",
		req.ReleaseName,
		vcsExt(req.Params.Ext),
		"",
	)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadVcsRelease200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}

func (s artifactoryService) DownloadVcsTagAlternative(
	ctx context.Context,
	req artgen.DownloadVcsTagAlternativeRequestObject,
) (artgen.DownloadVcsTagAlternativeResponseObject, error) {
	body, size, err := s.vcsArchive(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.UserOrg,
		"tags",
		req.TagName,
		vcsExt(req.Params.Ext),
		"",
	)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadVcsTagAlternative200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}

func (s artifactoryService) DownloadVcsBranchAlternative(
	ctx context.Context,
	req artgen.DownloadVcsBranchAlternativeRequestObject,
) (artgen.DownloadVcsBranchAlternativeResponseObject, error) {
	body, size, err := s.vcsArchive(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.UserOrg,
		"heads",
		req.BranchName,
		vcsExt(req.Params.Ext),
		"",
	)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadVcsBranchAlternative200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}

func (s artifactoryService) DownloadVcsTagFromSubgroupRepository(
	ctx context.Context,
	req artgen.DownloadVcsTagFromSubgroupRepositoryRequestObject,
) (artgen.DownloadVcsTagFromSubgroupRepositoryResponseObject, error) {
	body, size, err := s.vcsArchive(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.Repo,
		"tags",
		req.TagName,
		vcsExt(req.Params.Ext),
		vcsProperties(req.Params.Properties),
	)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadVcsTagFromSubgroupRepository200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}

func (s artifactoryService) DownloadVcsBranchFromSubgroupRepository(
	ctx context.Context,
	req artgen.DownloadVcsBranchFromSubgroupRepositoryRequestObject,
) (artgen.DownloadVcsBranchFromSubgroupRepositoryResponseObject, error) {
	body, size, err := s.vcsArchive(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.Repo,
		"heads",
		req.BranchName,
		vcsExt(req.Params.Ext),
		vcsProperties(req.Params.Properties),
	)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadVcsBranchFromSubgroupRepository200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}

func (s artifactoryService) vcsFile(
	ctx context.Context,
	key, owner, name, kind, selector string,
) (io.Reader, int64, error) {
	ref, filename, ok := strings.Cut(selector, "!")
	if !ok || !vcs.ValidRef(ref) || !vcs.ValidFilePath(filename) {
		return nil, 0, apierror.New(http.StatusBadRequest, "invalid VCS file path")
	}
	a, err := s.vcsRepository(ctx, key, owner, name)
	if err != nil {
		return nil, 0, err
	}
	if !a.access.file(a.repo, owner+"/"+name+"/refs/"+kind+"/"+ref+"/"+filename, types.ActionRead) {
		return nil, 0, fmt.Errorf(
			"vcs file read denied: %w",
			pkgroute.SourceError(source.ErrDenied, a.principal.Anonymous),
		)
	}
	body, _, err := s.vcsArchive(ctx, key, owner, name, kind, ref, "tar.gz", "")
	if err != nil {
		return nil, 0, err
	}
	archive, err := io.ReadAll(io.LimitReader(body, vcs.MaxArchiveBytes+1))
	closeErr := body.Close()
	if err != nil || closeErr != nil || len(archive) > vcs.MaxArchiveBytes {
		return nil, 0, apierror.New(http.StatusBadGateway, "invalid VCS archive")
	}
	data, err := vcs.FileZIP(ctx, archive, "tar.gz", filename)
	if errors.Is(err, vcs.ErrInvalidPath) {
		return nil, 0, apierror.New(http.StatusNotFound, "VCS file not found")
	}
	if err != nil {
		return nil, 0, apierror.New(http.StatusBadGateway, "invalid VCS archive file")
	}
	return bytes.NewReader(data), int64(len(data)), nil
}

func (s artifactoryService) DownloadFileWithinVcsTag(
	ctx context.Context,
	req artgen.DownloadFileWithinVcsTagRequestObject,
) (artgen.DownloadFileWithinVcsTagResponseObject, error) {
	body, size, err := s.vcsFile(ctx, req.RepoKey, req.UserOrg, req.Repo, "tags", req.TagName)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadFileWithinVcsTag200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}

func (s artifactoryService) DownloadFileWithinVcsBranch(
	ctx context.Context,
	req artgen.DownloadFileWithinVcsBranchRequestObject,
) (artgen.DownloadFileWithinVcsBranchResponseObject, error) {
	body, size, err := s.vcsFile(ctx, req.RepoKey, req.UserOrg, req.Repo, "heads", req.BranchName)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadFileWithinVcsBranch200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}

func (s artifactoryService) DownloadFileWithinVcsTagAlternative(
	ctx context.Context,
	req artgen.DownloadFileWithinVcsTagAlternativeRequestObject,
) (artgen.DownloadFileWithinVcsTagAlternativeResponseObject, error) {
	body, size, err := s.vcsFile(ctx, req.RepoKey, req.UserOrg, req.UserOrg, "tags", req.TagName)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadFileWithinVcsTagAlternative200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}

func (s artifactoryService) DownloadFileWithinVcsBranchAlternative(
	ctx context.Context,
	req artgen.DownloadFileWithinVcsBranchAlternativeRequestObject,
) (artgen.DownloadFileWithinVcsBranchAlternativeResponseObject, error) {
	body, size, err := s.vcsFile(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.UserOrg,
		"heads",
		req.BranchName,
	)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadFileWithinVcsBranchAlternative200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}

func (s artifactoryService) DownloadFileInVcsTagInSubgroupRepository(
	ctx context.Context,
	req artgen.DownloadFileInVcsTagInSubgroupRepositoryRequestObject,
) (artgen.DownloadFileInVcsTagInSubgroupRepositoryResponseObject, error) {
	body, size, err := s.vcsFile(ctx, req.RepoKey, req.UserOrg, req.Repo, "tags", req.TagName)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadFileInVcsTagInSubgroupRepository200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}

func (s artifactoryService) DownloadFileInVcsBranchInSubgroupRepository(
	ctx context.Context,
	req artgen.DownloadFileInVcsBranchInSubgroupRepositoryRequestObject,
) (artgen.DownloadFileInVcsBranchInSubgroupRepositoryResponseObject, error) {
	body, size, err := s.vcsFile(ctx, req.RepoKey, req.UserOrg, req.Repo, "heads", req.BranchName)
	if err != nil {
		return nil, err
	}
	return artgen.DownloadFileInVcsBranchInSubgroupRepository200ApplicationoctetStreamResponse{
		Body:          body,
		ContentLength: size,
	}, nil
}
