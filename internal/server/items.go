package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/itemkit"
	"github.com/weaveplatform/artifactweave/migration/artifacts/adapters/http/pkgroute"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/build"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/format"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/debian"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	artgen "github.com/weaveplatform/artifactweave/migration/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// RegisterItemRoutes serves the item paths (decision 0002) and the AQL
// search. They are registered under exactly the patterns
// registerHandwrittenStubs derives from the spec, so they replace its 501
// stubs rather than sitting beside them. HEAD is not in the spec; it shares
// the GET handler. Item routes are outside the management body limit: an
// upload is streamed, however large.
func (s ArtifactoryAPI) RegisterItemRoutes(r chi.Router) {
	for _, p := range httpkit.ChiPatterns("/artifactory/{repoKey}/{filePath}") {
		r.Put(p, s.deployItem)
		r.Get(p, s.GetItem)
		r.Head(p, s.GetItem)
		r.Delete(p, s.deleteItem)
	}
	r.Post("/artifactory/api/search/aql", s.searchAQL)
}

// PrivateItem reports the paths that only a format's own protocol serves,
// which the router passes to the item kit (itemkit.Deps.Private): Hugging
// Face's Xet and Conda's shards, the machine learning formats' private
// files and Chef's metadata.
func PrivateItem(c repo.Config, p string) bool {
	return protocolPrivateItem(c, p) || machineLearningPrivate(c, p) || chefMetadataItem(c, p)
}

// builds returns the service that writes items on behalf of builds, with
// the same format hooks, and settings, as items.
func (s ArtifactoryAPI) builds(st store.Store) *build.Service {
	svc := &build.Service{
		Store: st, Blobs: s.cfg.Blobs, Formats: s.Format,
		AllowDuplicateEntries: s.cfg.AllowDuplicateBuildEntries,
	}
	if s.cfg.Work != nil {
		svc.Work = s.cfg.Work
	}
	return svc
}

// deployItem answers PUT on an item path: a file, a folder (trailing slash),
// a checksum deploy, or a checksum sidecar for an existing file.
func (s ArtifactoryAPI) deployItem(w http.ResponseWriter, r *http.Request) {
	s.deployEscapedItem(w, r, strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/"))
}

// deployEscapedItem also serves package APIs that expose ordinary item PUTs.
// Keeping the original request preserves its error envelope and base URL.
func (s ArtifactoryAPI) deployEscapedItem(
	w http.ResponseWriter,
	r *http.Request,
	escaped string,
) {
	ctx := r.Context()
	st, err := s.Content()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	req, err := itemkit.ParseEscapedItemPath(escaped)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	p, acc, err := s.ItemCaller(ctx, st)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	c, cached, err := itemkit.ResolveRepo(ctx, st, req.Key)
	if err == nil {
		c, err = itemkit.DeployTarget(ctx, st, c, cached)
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "resolve deploy target", err))
		return
	}
	if !acc.Item(c, req.Path, req.IsFolder, types.ActionWrite) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if req.Path == "" {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("a deploy needs a path inside the repository"))
		return
	}
	svc := s.Items(st)
	t := item.Target{Repo: c, Path: req.Path}
	explode := strings.EqualFold(r.Header.Get("X-Explode-Archive"), "true") ||
		strings.EqualFold(r.Header.Get("X-Explode-Archive-Atomic"), "true")
	byChecksum := strings.EqualFold(r.Header.Get("X-Checksum-Deploy"), "true")
	// Overwriting replaces content, which needs the right to delete it,
	// except with the same content (item.Deploy.OverwriteIdentical). Only
	// the item service can tell that, once it holds the content, so only a
	// deploy that may not replace a file in any case is refused here.
	existing, err := st.Items().Get(ctx, c.Key, false, req.Path)
	switch {
	case err == nil && !existing.Folder && !req.IsFolder && !explode &&
		!s.MayBeIdentical(byChecksum) && !acc.File(c, req.Path, types.ActionDelete):
		httpkit.WriteErr(w, r, itemkit.OverwriteRefusal(ctx, &item.OverwriteDeniedError{
			Repo: c.Key, Path: req.Path, User: p.Name,
		}))
		return
	case err != nil && !errors.Is(err, store.ErrNotFound):
		httpkit.WriteErr(w, r, s.InternalError(ctx, "get item", err))
		return
	}
	var it types.Item
	switch {
	case req.IsFolder:
		it, err = svc.MakeFolder(ctx, t, req.Props, p.Name)
	case explode:
		it, err = svc.Explode(ctx, item.Explode{
			Deploy: s.deployRequest(r, t, req, p),
			Atomic: strings.EqualFold(r.Header.Get("X-Explode-Archive-Atomic"), "true"),
			// Replacing files needs the right to delete them, as a deploy does;
			// it is checked at the archive's path, not per entry.
			CanOverwrite: acc.File(c, req.Path, types.ActionDelete),
		})
	case byChecksum:
		d := s.fileDeploy(r, t, req, p, acc)
		d.Reusable = s.reusable(st, p, acc, r.Header.Get("X-Checksum-Deploy-Token"))
		d.OverwriteIdentical = s.MayBeIdentical(true)
		it, err = svc.DeployByChecksum(ctx, d)
	default:
		if artifact, alg, ok := item.Sidecar(req.Path); ok &&
			(svc.OwnsSidecars(c) || s.isFile(ctx, st, c, artifact)) {
			s.recordChecksum(w, r, svc, item.Target{Repo: c, Path: artifact}, alg)
			return
		}
		d := s.fileDeploy(r, t, req, p, acc)
		d.OverwriteIdentical = s.MayBeIdentical(false)
		it, err = svc.Deploy(ctx, d)
	}
	if answered(w, err) {
		return
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "deploy", err))
		return
	}
	// Every deploy names what it stored, where a format may have put it, as
	// 6.20's SuccessfulDeploymentResponseHelper does; an archive names the
	// folder it was extracted into.
	w.Header().Set("Location", itemkit.DownloadURI(ctx, it.ReportedRepoKey(), it.FullPath()))
	if explode {
		// The deployartifactsfromarchive page documents a text/plain 201
		// and gives no text. jfrog-client-go decodes any body an upload
		// answers as JSON (ExtractSha256FromResponseBody), so jf rt u
		// --explode could only work against Artifactory with no body.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusCreated)
		return
	}
	httpkit.WriteJSON(w, http.StatusCreated, itemkit.ItemCreated(ctx, it))
}

// answered writes the status of a write the format took without making it
// (format.AcceptedError), with no body, as Artifactory answers one.
func answered(w http.ResponseWriter, err error) bool {
	var accepted *format.AcceptedError
	if !errors.As(err, &accepted) {
		return false
	}
	w.WriteHeader(accepted.Status)
	return true
}

func (ArtifactoryAPI) deployRequest(
	r *http.Request,
	t item.Target,
	req itemkit.ItemRequest,
	p kernel.Principal,
) item.Deploy {
	return item.Deploy{
		Target:     t,
		Body:       r.Body,
		Declared:   pkgroute.DeclaredChecksums(r.Header),
		Properties: req.Props,
		By:         p.Name,
	}
}

// fileDeploy is the deploy of one file. The format may store it under
// another name (Maven's unique snapshots), and replacing a file there needs
// the right to delete it, as at the path sent.
func (s ArtifactoryAPI) fileDeploy(
	r *http.Request,
	t item.Target,
	req itemkit.ItemRequest,
	p kernel.Principal,
	acc itemkit.Access,
) item.Deploy {
	d := s.deployRequest(r, t, req, p)
	d.CanOverwritePath = func(full string) bool { return acc.File(t.Repo, full, types.ActionDelete) }
	return d
}

// reusable decides whether a checksum deploy may store content the server
// already holds, as Artifactory does: only when the caller may read a file
// with that content. Otherwise anyone who learned a checksum could copy a
// file out of a repository it cannot read into one it can write. The
// exception is token, the checksum token of the caller's own finished
// multipart upload of the content, which no item holds until this deploy.
func (s ArtifactoryAPI) reusable(
	st store.Store,
	p kernel.Principal,
	acc itemkit.Access,
	token string,
) func(context.Context, types.Blob) (bool, error) {
	return func(ctx context.Context, b types.Blob) (bool, error) {
		if token != "" && s.cfg.Multipart != nil {
			ok, err := s.cfg.Multipart.Finished(ctx, token, p.Name, b.Checksums.SHA256)
			if err != nil || ok {
				return ok, err //nolint:wrapcheck // DeployByChecksum wraps it
			}
		}
		return readsContent(ctx, st, acc, b.Checksums.SHA256, s.Private)
	}
}

// readsContent reports whether the caller may read a file whose content
// has SHA-256 sum. Only files in repositories count, so content that only
// the trash can or a multipart upload holds is reused by no one; the client
// uploads it again. Neither does a file only its format's protocol serves
// (private, Deps.Private).
func readsContent(
	ctx context.Context,
	st store.Store,
	acc itemkit.Access,
	sum string,
	private func(repo.Config, string) bool,
) (bool, error) {
	// A nil entry is a repository that no longer exists.
	repos := map[string]*repo.Config{}
	page := store.Page{Limit: store.MaxLimit}
	for {
		files, next, err := st.Items().FindByChecksum(ctx, types.Checksums{SHA256: sum}, page)
		if err != nil {
			return false, fmt.Errorf("find files by checksum: %w", err)
		}
		for _, f := range files {
			c, seen := repos[f.RepoKey]
			if !seen {
				got, err := st.Repos().Get(ctx, f.RepoKey)
				switch {
				case err == nil:
					c = &got
				case !errors.Is(err, store.ErrNotFound):
					return false, fmt.Errorf("get repository %s: %w", f.RepoKey, err)
				}
				repos[f.RepoKey] = c
			}
			if c != nil && !private(*c, f.FullPath()) &&
				acc.File(*c, f.FullPath(), types.ActionRead) {
				return true, nil
			}
		}
		if next == "" {
			return false, nil
		}
		page.After = next
	}
}

func (ArtifactoryAPI) isFile(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	p string,
) bool {
	it, err := st.Items().Get(ctx, c.Key, false, p)
	return err == nil && !it.Folder
}

// recordChecksum stores a checksum sidecar upload (Maven sends one after
// each file) as the file's original checksum.
func (s ArtifactoryAPI) recordChecksum(
	w http.ResponseWriter,
	r *http.Request,
	svc *item.Service,
	t item.Target,
	alg item.Algorithm,
) {
	// A checksum is at most 64 hex characters; anything longer is not one.
	b, err := io.ReadAll(io.LimitReader(r.Body, 256))
	if err != nil {
		httpkit.WriteErr(w, r, httpkit.BadRequestf("reading the checksum: %v", err))
		return
	}
	// Sidecars may carry "<hex>  <file name>" as sha1sum writes them.
	value, _, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	it, err := svc.RecordSidecar(r.Context(), t, alg, value, r.ContentLength)
	if answered(w, err) {
		return
	}
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(r.Context(), "record checksum", err))
		return
	}
	// The file the checksum was recorded on, which a format may have named
	// differently from the sidecar (UploadServiceUtils
	// .sendUploadedChecksumResponse in 6.20).
	w.Header().
		Set("Location", itemkit.DownloadURI(r.Context(), it.ReportedRepoKey(), it.FullPath()))
	w.WriteHeader(http.StatusCreated)
}

// deleteItem answers DELETE on an item path: the item and everything below
// it, into the trash can for local repositories.
func (s ArtifactoryAPI) deleteItem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.Content()
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	req, err := itemkit.ParseItemPath(r)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	p, acc, err := s.ItemCaller(ctx, st)
	if err != nil {
		httpkit.WriteErr(w, r, err)
		return
	}
	c, cached, err := itemkit.ResolveRepo(ctx, st, req.Key)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "resolve repository", err))
		return
	}
	if c.Class == repo.Virtual {
		httpkit.WriteErr(w, r, apierror.New(http.StatusMethodNotAllowed,
			fmt.Sprintf("Cannot delete from the virtual repository '%s'", c.Key)))
		return
	}
	if !acc.File(c, req.Path, types.ActionDelete) {
		httpkit.WriteErr(w, r, httpkit.Denied(p))
		return
	}
	if c.PackageType == repo.Debian && strings.HasPrefix(req.Path, "snapshots/") {
		parts := strings.Split(req.Path, "/")
		if len(parts) != 2 || parts[1] == "" {
			httpkit.WriteErr(w, r, apierror.New(http.StatusConflict,
				"Delete the complete Debian snapshot folder"))
			return
		}
		ctx = debian.WithSnapshotDelete(ctx)
	}
	t := item.Target{Repo: c, Cached: cached || c.Class == repo.Remote, Path: req.Path}
	svc := s.Items(st)
	// A file's sidecars go with it, such as a Helm chart's .prov, so the
	// caller needs Delete on each as on the file (decision 0039).
	sidecars, err := svc.SidecarsBeside(ctx, t, t.Path)
	if err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "sidecars", err))
		return
	}
	for _, sc := range sidecars {
		if !acc.File(c, sc.From, types.ActionDelete) {
			httpkit.WriteErr(w, r, httpkit.Denied(p))
			return
		}
	}
	if _, err := svc.Delete(ctx, t, p.Name); err != nil {
		httpkit.WriteErr(w, r, s.ItemError(ctx, "delete item", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetUploadsConfig tells the JFrog CLI whether to upload large files in
// parts (migration/artifacts/core/multipart) or in one request (research 09).
func (s ArtifactoryAPI) GetUploadsConfig(
	context.Context,
	artgen.GetUploadsConfigRequestObject,
) (artgen.GetUploadsConfigResponseObject, error) {
	supported := s.cfg.Multipart != nil
	return artgen.GetUploadsConfig200JSONResponse{Supported: &supported}, nil
}
