package vcshttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func (s VCSAPI) vcsOpenResult(
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
		if s.Blobs == nil {
			return nil, 0, apierror.ErrNotImplemented
		}
		var err error
		body, _, err = s.Blobs.Open(ctx, result.Item.Actual.SHA256)
		if err != nil {
			return nil, 0, s.InternalError(ctx, "VCS cached archive", err)
		}
		size = result.Item.Size
	default:
		return nil, 0, source.ErrInvalidResult
	}
	return body, size, nil
}

func vcsArchiveURLPolicy(
	c repo.Config,
	owner, name string,
	target *url.URL,
) func(context.Context, *url.URL) error {
	if c.VCSGitProvider == repo.Stash || c.VCSGitProvider == repo.OldStash ||
		c.VCSGitProvider == repo.ArtifactoryVCS {
		return vcsExactURL(target)
	}
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

func (s VCSAPI) vcsArchive(
	ctx context.Context,
	key, owner, name, kind, ref, ext, propertyFilter string,
) (vcsServed, error) {
	if !ValidExtension(ext) || kind != "tags" && kind != "heads" && kind != "commits" {
		return vcsServed{}, apierror.New(http.StatusBadRequest, "invalid VCS archive request")
	}
	a, err := s.vcsRepository(ctx, key, owner, name)
	if err != nil {
		return vcsServed{}, err
	}
	if !ValidRef(ref) || kind == "commits" && !ValidCommitID(ref) {
		return vcsServed{}, apierror.New(http.StatusBadRequest, "invalid VCS reference")
	}
	commit := ref
	if kind != "commits" {
		refs, _, err := s.vcsRefs(ctx, key, owner, name)
		if err != nil {
			return vcsServed{}, err
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
			return vcsServed{}, apierror.New(http.StatusNotFound, "VCS reference not found")
		}
	}
	permissionPath := owner + "/" + name + "/refs/" + kind + "/" + ref
	if !a.access.File(a.repo, permissionPath, api.ActionRead) {
		return vcsServed{}, fmt.Errorf(
			"vcs archive read denied: %w",
			pkgroute.SourceError(source.ErrDenied, a.principal.Anonymous),
		)
	}
	target, err := ArchiveURL(a.repo, owner, name, commit, ext)
	if err != nil {
		return vcsServed{}, apierror.New(http.StatusBadRequest, "invalid VCS archive source")
	}
	resource := source.Resource{
		Kind:              source.ResourceKindArtifact,
		Path:              permissionPath + "/" + name + "-" + commit + "." + ext,
		ExternalURL:       target.String(),
		CheckURL:          vcsArchiveURLPolicy(a.repo, owner, name, target),
		NoCrossOriginAuth: true,
		Method:            http.MethodGet,
		Variant:           "vcs-archive-v2-" + ext,
		Validate: func(ctx context.Context, body io.ReadSeeker, size int64) error {
			if err := ValidateCommitArchive(ctx, body, size, ext, commit); err != nil {
				return source.ErrInvalidResult
			}
			return nil
		},
	}
	filter, err := vcsPropertyFilter(propertyFilter)
	if err != nil {
		return vcsServed{}, err
	}
	if len(filter) > 0 {
		// A property constraint can only select an already cached artifact.
		// It must never trigger an upstream download to create a match.
		a.isCacheOnly = true
	}
	result, err := s.vcsRead(ctx, a, resource)
	if err != nil {
		return vcsServed{}, err
	}
	if len(filter) > 0 {
		if err := s.vcsMatchCachedProperties(ctx, result, filter); err != nil {
			return vcsServed{}, err
		}
	}
	file, size, err := s.vcsOpenResult(ctx, result)
	if err != nil {
		return vcsServed{}, err
	}
	return vcsServed{
		body: file, size: size, sums: resultChecksums(result),
		modified: resultModified(result), used: result.Used,
		name: archiveName(name, kind, ref, commit, ext), media: archiveMedia(ext),
	}, nil
}

func (s VCSAPI) vcsMatchCachedProperties(
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
	st, err := s.Content()
	if err != nil {
		return err
	}
	props, err := st.Items().Properties(ctx, result.Item.ID)
	if err != nil {
		return s.InternalError(ctx, "VCS cached properties", err)
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

// DownloadVcsTag serves the archive of a tag's commit in the ext format
// (tar.gz by default). The tag resolves to its commit through the project's
// reference listing, and the archive is fetched through the remote source and
// checked against that commit before the cache admits it. A tag the listing
// does not name answers 404.
func (s VCSAPI) DownloadVcsTag(
	ctx context.Context,
	req artgen.DownloadVcsTagRequestObject,
) (artgen.DownloadVcsTagResponseObject, error) {
	a, err := s.vcsArchive(
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
	return vcsResponse{file: a, r: requestFrom(ctx)}, nil
}

// DownloadVcsBranch serves the archive of a branch's commit, as DownloadVcsTag
// does for a tag. A properties filter selects only an already cached archive
// whose properties match, and never fetches one to create a match; none
// matching answers 404.
func (s VCSAPI) DownloadVcsBranch(
	ctx context.Context,
	req artgen.DownloadVcsBranchRequestObject,
) (artgen.DownloadVcsBranchResponseObject, error) {
	a, err := s.vcsArchive(
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
	return vcsResponse{file: a, r: requestFrom(ctx)}, nil
}

// DownloadVcsCommit serves the archive of a commit named by its ID, which
// needs no reference listing; an invalid commit ID answers 400.
func (s VCSAPI) DownloadVcsCommit(
	ctx context.Context,
	req artgen.DownloadVcsCommitRequestObject,
) (artgen.DownloadVcsCommitResponseObject, error) {
	a, err := s.vcsArchive(
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
	return vcsResponse{file: a, r: requestFrom(ctx)}, nil
}

// DownloadVcsRelease serves the archive of a release, which resolves as the
// tag of the same name (DownloadVcsTag).
func (s VCSAPI) DownloadVcsRelease(
	ctx context.Context,
	req artgen.DownloadVcsReleaseRequestObject,
) (artgen.DownloadVcsReleaseResponseObject, error) {
	a, err := s.vcsArchive(
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
	return vcsResponse{file: a, r: requestFrom(ctx)}, nil
}

// DownloadVcsTagAlternative serves a tag's archive for JFrog's short form,
// whose one userOrg segment names both the owner and the project.
func (s VCSAPI) DownloadVcsTagAlternative(
	ctx context.Context,
	req artgen.DownloadVcsTagAlternativeRequestObject,
) (artgen.DownloadVcsTagAlternativeResponseObject, error) {
	a, err := s.vcsArchive(
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
	return vcsResponse{file: a, r: requestFrom(ctx)}, nil
}

// DownloadVcsBranchAlternative serves a branch's archive for JFrog's short
// form, whose one userOrg segment names both the owner and the project.
func (s VCSAPI) DownloadVcsBranchAlternative(
	ctx context.Context,
	req artgen.DownloadVcsBranchAlternativeRequestObject,
) (artgen.DownloadVcsBranchAlternativeResponseObject, error) {
	a, err := s.vcsArchive(
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
	return vcsResponse{file: a, r: requestFrom(ctx)}, nil
}

// DownloadVcsTagFromSubgroupRepository serves a tag's archive for the
// /archive/refs/ form, with the properties filter DownloadVcsBranch takes.
// VCSNested serves the same form when the owner spans several segments.
func (s VCSAPI) DownloadVcsTagFromSubgroupRepository(
	ctx context.Context,
	req artgen.DownloadVcsTagFromSubgroupRepositoryRequestObject,
) (artgen.DownloadVcsTagFromSubgroupRepositoryResponseObject, error) {
	a, err := s.vcsArchive(
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
	return vcsResponse{file: a, r: requestFrom(ctx)}, nil
}

// DownloadVcsBranchFromSubgroupRepository serves a branch's archive for the
// /archive/refs/ form, with the properties filter DownloadVcsBranch takes.
// VCSNested serves the same form when the owner spans several segments.
func (s VCSAPI) DownloadVcsBranchFromSubgroupRepository(
	ctx context.Context,
	req artgen.DownloadVcsBranchFromSubgroupRepositoryRequestObject,
) (artgen.DownloadVcsBranchFromSubgroupRepositoryResponseObject, error) {
	a, err := s.vcsArchive(
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
	return vcsResponse{file: a, r: requestFrom(ctx)}, nil
}

// vcsFile is the file selector names, <ref>!<path>, within the tag or
// branch ref (kind) of the project, as 7.x answers a file within a tag or
// branch: the file itself, as a remote VCS repository answered
// downloadBranchFile/.../main!bazel_registry.json with its JSON
// (RTFACT-29495), taken from the archive where the provider's contents API
// would not give it (RTFACT-26549). It is named by its base name, as 7.x
// names a file it serves from inside an archive by the entry's name
// (RequestResponseHelper and ZipEntryResource.getZipEntryName, 7.161.26)
// [I], typed by that name through the MIME table, and carries the
// checksums of its bytes and the time of the archive it came from [I].
//
// ext=zip asks for it "as a zip" (JFrog's VCS repositories page): the ZIP
// holding the file at its repository-relative path, typed application/zip
// and named <file>.zip [I]. A peer Artifactory VCS repository is asked for
// the file itself; any other provider's tar.gz archive is fetched and the
// file selected from it. A path the archive does not hold answers 404.
func (s VCSAPI) vcsFile(
	ctx context.Context,
	key, owner, name, kind, selector, ext string,
) (vcsServed, error) {
	ref, filename, ok := strings.Cut(selector, "!")
	if !ok || !ValidRef(ref) || !ValidFilePath(filename) {
		return vcsServed{}, apierror.New(http.StatusBadRequest, "invalid VCS file path")
	}
	a, err := s.vcsRepository(ctx, key, owner, name)
	if err != nil {
		return vcsServed{}, err
	}
	if !a.access.File(a.repo, owner+"/"+name+"/refs/"+kind+"/"+ref+"/"+filename, api.ActionRead) {
		return vcsServed{}, fmt.Errorf(
			"vcs file read denied: %w",
			pkgroute.SourceError(source.ErrDenied, a.principal.Anonymous),
		)
	}
	var data []byte
	var modified time.Time
	var used func(context.Context) error
	if a.repo.VCSGitProvider == repo.ArtifactoryVCS {
		data, modified, used, err = s.vcsPeerFile(ctx, a, owner, name, kind, ref, filename)
	} else {
		data, modified, err = s.vcsArchiveFile(ctx, a, key, owner, name, kind, ref, filename)
	}
	if err != nil {
		return vcsServed{}, err
	}
	file := selectedFile(filename, data, modified)
	if ext == "zip" {
		zipped, err := FileZIP(filename, data)
		if err != nil {
			return vcsServed{}, apierror.New(http.StatusBadGateway, "invalid VCS archive file")
		}
		file = selectedFile(filename, zipped, modified)
		file.name += ".zip"
		file.media = "application/zip"
	}
	file.used = used
	return file, nil
}

// vcsArchiveFile selects filename from the tar.gz archive of ref, whose
// whole read records the archive's use, and gives the archive's time.
func (s VCSAPI) vcsArchiveFile(
	ctx context.Context,
	a vcsAccess,
	key, owner, name, kind, ref, filename string,
) ([]byte, time.Time, error) {
	tarball, err := s.vcsArchive(ctx, key, owner, name, kind, ref, "tar.gz", "")
	if err != nil {
		return nil, time.Time{}, err
	}
	archive, err := io.ReadAll(io.LimitReader(tarball.body, MaxArchiveBytes+1))
	closeErr := tarball.body.Close()
	if err != nil || closeErr != nil || len(archive) > MaxArchiveBytes {
		return nil, time.Time{}, apierror.New(http.StatusBadGateway, "invalid VCS archive")
	}
	if tarball.used != nil {
		_ = tarball.used(ctx)
	}
	selectFile := SelectWrappedFile
	switch a.repo.VCSGitProvider {
	case repo.Stash, repo.OldStash:
		selectFile = SelectFile
	case repo.CustomVCS:
		selectFile = SelectCustomFile
	}
	data, err := selectFile(ctx, archive, "tar.gz", filename)
	if errors.Is(err, ErrInvalidPath) {
		return nil, time.Time{}, apierror.New(http.StatusNotFound, "VCS file not found")
	}
	if err != nil {
		return nil, time.Time{}, apierror.New(http.StatusBadGateway, "invalid VCS archive file")
	}
	return data, tarball.modified, nil
}

// selectedFile is data, the file filename within a tag or branch, as
// vcsFile answers it.
func selectedFile(filename string, data []byte, modified time.Time) vcsServed {
	return vcsServed{
		body: inMemory(data), size: int64(len(data)),
		name: path.Base(filename), media: item.MimeType(filename), sums: checksums(data),
		modified: modified,
	}
}

// DownloadFileWithinVcsTag serves one file of a tag, selected as
// <tag>!<path>: the file itself, or with ext=zip a ZIP that holds it
// (vcsFile). A path the tag does not hold answers 404.
func (s VCSAPI) DownloadFileWithinVcsTag(
	ctx context.Context,
	req artgen.DownloadFileWithinVcsTagRequestObject,
) (artgen.DownloadFileWithinVcsTagResponseObject, error) {
	file, err := s.vcsFile(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.Repo,
		"tags",
		req.TagName,
		vcsExt(req.Params.Ext),
	)
	if err != nil {
		return nil, err
	}
	return vcsResponse{file: file, r: requestFrom(ctx)}, nil
}

// DownloadFileWithinVcsBranch serves one file of a branch, selected as
// <branch>!<path>, as DownloadFileWithinVcsTag does for a tag.
func (s VCSAPI) DownloadFileWithinVcsBranch(
	ctx context.Context,
	req artgen.DownloadFileWithinVcsBranchRequestObject,
) (artgen.DownloadFileWithinVcsBranchResponseObject, error) {
	file, err := s.vcsFile(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.Repo,
		"heads",
		req.BranchName,
		vcsExt(req.Params.Ext),
	)
	if err != nil {
		return nil, err
	}
	return vcsResponse{file: file, r: requestFrom(ctx)}, nil
}

// DownloadFileWithinVcsTagAlternative serves one file of a tag
// (DownloadFileWithinVcsTag) for JFrog's short form, whose one userOrg segment
// names both the owner and the project.
func (s VCSAPI) DownloadFileWithinVcsTagAlternative(
	ctx context.Context,
	req artgen.DownloadFileWithinVcsTagAlternativeRequestObject,
) (artgen.DownloadFileWithinVcsTagAlternativeResponseObject, error) {
	file, err := s.vcsFile(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.UserOrg,
		"tags",
		req.TagName,
		vcsExt(req.Params.Ext),
	)
	if err != nil {
		return nil, err
	}
	return vcsResponse{file: file, r: requestFrom(ctx)}, nil
}

// DownloadFileWithinVcsBranchAlternative serves one file of a branch
// (DownloadFileWithinVcsBranch) for JFrog's short form, whose one userOrg
// segment names both the owner and the project.
func (s VCSAPI) DownloadFileWithinVcsBranchAlternative(
	ctx context.Context,
	req artgen.DownloadFileWithinVcsBranchAlternativeRequestObject,
) (artgen.DownloadFileWithinVcsBranchAlternativeResponseObject, error) {
	file, err := s.vcsFile(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.UserOrg,
		"heads",
		req.BranchName,
		vcsExt(req.Params.Ext),
	)
	if err != nil {
		return nil, err
	}
	return vcsResponse{file: file, r: requestFrom(ctx)}, nil
}

// DownloadFileInVcsTagInSubgroupRepository serves one file of a tag
// (DownloadFileWithinVcsTag) for the /archive/refs/ form. VCSNested serves the
// same form when the owner spans several segments.
func (s VCSAPI) DownloadFileInVcsTagInSubgroupRepository(
	ctx context.Context,
	req artgen.DownloadFileInVcsTagInSubgroupRepositoryRequestObject,
) (artgen.DownloadFileInVcsTagInSubgroupRepositoryResponseObject, error) {
	file, err := s.vcsFile(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.Repo,
		"tags",
		req.TagName,
		vcsExt(req.Params.Ext),
	)
	if err != nil {
		return nil, err
	}
	return vcsResponse{file: file, r: requestFrom(ctx)}, nil
}

// DownloadFileInVcsBranchInSubgroupRepository serves one file of a branch
// (DownloadFileWithinVcsBranch) for the /archive/refs/ form. VCSNested serves
// the same form when the owner spans several segments.
func (s VCSAPI) DownloadFileInVcsBranchInSubgroupRepository(
	ctx context.Context,
	req artgen.DownloadFileInVcsBranchInSubgroupRepositoryRequestObject,
) (artgen.DownloadFileInVcsBranchInSubgroupRepositoryResponseObject, error) {
	file, err := s.vcsFile(
		ctx,
		req.RepoKey,
		req.UserOrg,
		req.Repo,
		"heads",
		req.BranchName,
		vcsExt(req.Params.Ext),
	)
	if err != nil {
		return nil, err
	}
	return vcsResponse{file: file, r: requestFrom(ctx)}, nil
}
