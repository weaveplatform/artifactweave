package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	artgen "github.com/weaveplatform/artifactweave/internal/server/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// registerItemRoutes serves the item paths (decision 0002). They are
// registered under exactly the patterns registerHandwrittenStubs derives from
// the spec, so they replace its 501 stubs rather than sitting beside them.
// HEAD is not in the spec; it shares the GET handler. Item routes are outside
// the management body limit: an upload is streamed, however large.
func (s artifactoryService) registerItemRoutes(r chi.Router) {
	for _, p := range chiPatterns("/artifactory/{repoKey}/{filePath}") {
		r.Put(p, s.deployItem)
		r.Get(p, s.getItem)
		r.Head(p, s.getItem)
		r.Delete(p, s.deleteItem)
	}
	r.Post("/artifactory/api/search/aql", s.searchAQL)
	s.registerTransferRoutes(r)
}

// itemRequest is a parsed item URL.
type itemRequest struct {
	key    string
	path   string
	folder bool
	// props are the matrix parameters; mandatory holds the keys sent as
	// "key+=value", which a download requires the item to carry.
	props     types.Properties
	mandatory map[string]bool
}

// parseItemPath splits /artifactory/{repoKey}/{path} into the key, the path
// and the matrix parameters (";key=value" after any segment). It works on
// the escaped path, so an encoded ";" or "/" in a name stays part of it.
func parseItemPath(r *http.Request) (itemRequest, error) {
	return parseEscapedItemPath(strings.TrimPrefix(r.URL.EscapedPath(), "/artifactory/"))
}

// parseEscapedItemPath parses an escaped "{repoKey}/{path}", as found after
// a route's prefix.
func parseEscapedItemPath(escaped string) (itemRequest, error) {
	req := itemRequest{
		folder: strings.HasSuffix(escaped, "/"), props: types.Properties{},
		mandatory: map[string]bool{},
	}
	var names []string
	segments := strings.Split(strings.Trim(escaped, "/"), "/")
	for i, seg := range segments {
		name, params, _ := strings.Cut(seg, ";")
		n, err := url.PathUnescape(name)
		if err != nil {
			return req, badRequest("malformed path: %v", err)
		}
		switch {
		case i == 0:
			req.key = n
		case n == "" && i == len(segments)-1 && params != "":
			// "a/b/;k=v": matrix parameters after a folder's slash.
			req.folder = true
		default:
			names = append(names, n)
		}
		if params == "" {
			continue
		}
		if err := req.addMatrix(params); err != nil {
			return req, err
		}
	}
	req.path = strings.Join(names, "/")
	if req.path != "" {
		if _, _, _, err := types.SplitItemPath(req.path); err != nil {
			return req, badRequest("malformed path %q", req.path)
		}
	}
	return req, nil
}

// addMatrix parses "k=v1,v2;k2+=v3". Commas separate values unless escaped
// with a backslash, as in the properties API. A value is form-decoded, so "+"
// is a space: the JFrog CLI escapes values with url.QueryEscape, and
// Artifactory decodes them with URLDecoder (research 17). Keys keep "+",
// whose trailing use marks a mandatory property.
func (req *itemRequest) addMatrix(params string) error {
	for _, p := range strings.Split(params, ";") {
		if p == "" {
			continue
		}
		k, v, _ := strings.Cut(p, "=")
		k, err := url.PathUnescape(k)
		if err != nil {
			return badRequest("malformed matrix parameter %q", p)
		}
		mandatory := false
		if base, ok := strings.CutSuffix(k, "+"); ok {
			k, mandatory = base, true
		}
		if err := types.ValidatePropertyKey(k); err != nil {
			return badRequest("invalid property key %q", k)
		}
		v, err = url.QueryUnescape(v)
		if err != nil {
			return badRequest("malformed matrix parameter %q", p)
		}
		req.props[k] = append(req.props[k], splitValues(v)...)
		if mandatory {
			req.mandatory[k] = true
		}
	}
	return nil
}

func splitValues(v string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(v); i++ {
		switch {
		case v[i] == '\\' && i+1 < len(v):
			i++
			cur.WriteByte(v[i])
		case v[i] == ',':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(v[i])
		}
	}
	return append(out, cur.String())
}

// itemCaller returns the caller and its permissions for an item request.
func (s artifactoryService) itemCaller(
	ctx context.Context,
	st store.Store,
) (auth.Principal, access, error) {
	p, err := principal(ctx)
	if err != nil {
		return p, access{}, err
	}
	a, err := accessFor(ctx, st, p)
	if err != nil {
		return p, access{}, s.internalError(ctx, "item permissions", err)
	}
	return p, a, nil
}

// resolveRepo returns the repository a key names and whether the key is a
// remote repository's cache alias.
func resolveRepo(ctx context.Context, st store.Store, key string) (repo.Config, bool, error) {
	stored, cached := types.ResolveRepoKey(key)
	c, err := st.Repos().Get(ctx, stored)
	if err == nil && (!cached || c.Class == repo.Remote) {
		return c, cached, nil
	}
	if err == nil || errors.Is(err, store.ErrNotFound) {
		return c, cached, notFoundRepo(key)
	}
	return c, cached, fmt.Errorf("get repository %s: %w", key, err)
}

// itemError maps the item service's errors onto statuses.
func (s artifactoryService) itemError(ctx context.Context, op string, err error) error {
	var api *apierror.Error
	switch {
	case errors.As(err, &api):
		return api
	case errors.Is(err, item.ErrChecksumMismatch):
		return apierror.New(http.StatusConflict,
			"Checksum policy rejected the upload: the content does not match the declared checksum")
	case errors.Is(err, item.ErrBadChecksum):
		return apierror.New(http.StatusBadRequest, err.Error())
	case errors.Is(err, item.ErrNoBlob):
		return apierror.New(http.StatusNotFound,
			"Checksum deploy failed: no stored content matches the checksum")
	case errors.Is(err, item.ErrConflict):
		return apierror.New(http.StatusConflict, err.Error())
	case errors.Is(err, item.ErrCollected):
		return apierror.New(http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, item.ErrBadArchive):
		return apierror.New(http.StatusBadRequest, err.Error())
	case errors.Is(err, item.ErrOverwriteDenied):
		return auth.ErrForbidden
	case errors.Is(err, types.ErrInvalidItemPath):
		return apierror.New(http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		return itemNotFound()
	}
	return s.internalError(ctx, op, err)
}

// itemNotFound is the 404 for a missing item. No public source documents
// Artifactory's wording.
func itemNotFound() error { return apierror.New(http.StatusNotFound, "Could not find resource") }

func (s artifactoryService) items(st store.Store) *item.Service {
	return &item.Service{Store: st, Blobs: s.cfg.Blobs}
}

// writeErr renders err in r's surface. An error that is not an
// *apierror.Error reaches the client as a bare 500, so its cause goes to the
// access log.
func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var typed *apierror.Error
	if !errors.As(err, &typed) {
		noteCause(r.Context(), err)
	}
	apierror.Write(w, surfaceFor(r.URL.Path), err)
}

// deployTarget resolves where a deploy to c lands: a local or federated
// repository itself, or a virtual repository's default deployment
// repository. Remote repositories and their caches take no deploys.
func deployTarget(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	cached bool,
) (repo.Config, error) {
	switch {
	case cached || c.Class == repo.Remote:
		return c, apierror.New(http.StatusMethodNotAllowed,
			fmt.Sprintf("Cannot deploy to the remote repository '%s'", c.Key))
	case c.Class == repo.Virtual && c.DefaultDeploymentRepo == "":
		return c, apierror.New(http.StatusMethodNotAllowed, fmt.Sprintf(
			"Virtual repository '%s' has no default deployment repository", c.Key))
	case c.Class == repo.Virtual:
		target, err := st.Repos().Get(ctx, c.DefaultDeploymentRepo)
		if err != nil {
			return c, fmt.Errorf("default deployment repository of %s: %w", c.Key, err)
		}
		return target, nil
	}
	return c, nil
}

// deployItem answers PUT on an item path: a file, a folder (trailing slash),
// a checksum deploy, or a checksum sidecar for an existing file.
func (s artifactoryService) deployItem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	req, err := parseItemPath(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	c, cached, err := resolveRepo(ctx, st, req.key)
	if err == nil {
		c, err = deployTarget(ctx, st, c, cached)
	}
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "resolve deploy target", err))
		return
	}
	if !acc.item(c, req.path, req.folder, types.ActionWrite) {
		writeErr(w, r, denied(p))
		return
	}
	if req.path == "" {
		writeErr(w, r, badRequest("a deploy needs a path inside the repository"))
		return
	}
	svc := s.items(st)
	t := item.Target{Repo: c, Path: req.path}
	explode := strings.EqualFold(r.Header.Get("X-Explode-Archive"), "true") ||
		strings.EqualFold(r.Header.Get("X-Explode-Archive-Atomic"), "true")
	existing, err := st.Items().Get(ctx, c.Key, false, req.path)
	switch {
	case err == nil && !existing.Folder && !req.folder && !explode &&
		!acc.file(c, req.path, types.ActionDelete):
		// Overwriting replaces content, which needs the right to delete it.
		writeErr(w, r, denied(p))
		return
	case err != nil && !errors.Is(err, store.ErrNotFound):
		writeErr(w, r, s.internalError(ctx, "get item", err))
		return
	}
	var it types.Item
	switch {
	case req.folder:
		it, err = svc.MakeFolder(ctx, t, req.props, p.Name)
	case explode:
		it, err = svc.Explode(ctx, item.Explode{
			Deploy: s.deployRequest(r, t, req, p),
			Atomic: strings.EqualFold(r.Header.Get("X-Explode-Archive-Atomic"), "true"),
			// Replacing files needs the right to delete them, as a deploy does;
			// it is checked at the archive's path, not per entry.
			CanOverwrite: acc.file(c, req.path, types.ActionDelete),
		})
	case strings.EqualFold(r.Header.Get("X-Checksum-Deploy"), "true"):
		it, err = svc.DeployByChecksum(ctx, s.deployRequest(r, t, req, p))
	default:
		if artifact, alg, ok := item.Sidecar(req.path); ok && s.isFile(ctx, st, c, artifact) {
			s.recordChecksum(w, r, svc, item.Target{Repo: c, Path: artifact}, alg)
			return
		}
		it, err = svc.Deploy(ctx, s.deployRequest(r, t, req, p))
	}
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "deploy", err))
		return
	}
	writeJSON(w, http.StatusCreated, itemCreated(ctx, it))
}

func (artifactoryService) deployRequest(
	r *http.Request,
	t item.Target,
	req itemRequest,
	p auth.Principal,
) item.Deploy {
	declared := types.Checksums{
		SHA1: r.Header.Get("X-Checksum-Sha1"), SHA256: r.Header.Get("X-Checksum-Sha256"),
		MD5: r.Header.Get("X-Checksum-Md5"),
	}
	// X-Checksum names its algorithm by length; the JFrog CLI sends sha256
	// in it.
	if v := strings.TrimSpace(r.Header.Get("X-Checksum")); v != "" {
		switch len(v) {
		case 64:
			declared.SHA256 = v
		case 40:
			declared.SHA1 = v
		default:
			declared.MD5 = v
		}
	}
	return item.Deploy{
		Target:     t,
		Body:       r.Body,
		Declared:   declared,
		Properties: req.props,
		By:         p.Name,
	}
}

func (artifactoryService) isFile(
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
func (s artifactoryService) recordChecksum(
	w http.ResponseWriter,
	r *http.Request,
	svc *item.Service,
	t item.Target,
	alg item.Algorithm,
) {
	// A checksum is at most 64 hex characters; anything longer is not one.
	b, err := io.ReadAll(io.LimitReader(r.Body, 256))
	if err != nil {
		writeErr(w, r, badRequest("reading the checksum: %v", err))
		return
	}
	// Sidecars may carry "<hex>  <file name>" as sha1sum writes them.
	value, _, _ := strings.Cut(strings.TrimSpace(string(b)), " ")
	if _, err := svc.RecordChecksum(r.Context(), t, alg, value); err != nil {
		writeErr(w, r, s.itemError(r.Context(), "record checksum", err))
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// itemCreated is the ItemCreated body of a deploy.
func itemCreated(ctx context.Context, it types.Item) map[string]any {
	base := baseurl.From(ctx)
	rel := it.ReportedRepoKey() + "/" + escapePath(it.FullPath())
	body := map[string]any{
		"repo":      it.ReportedRepoKey(),
		"path":      it.StoragePath(),
		"created":   formatTime(it.Created),
		"createdBy": it.CreatedBy,
		"uri":       base + "/artifactory/api/storage/" + rel,
	}
	if it.Folder {
		return body
	}
	body["downloadUri"] = base + "/artifactory/" + rel
	body["mimeType"] = it.MimeType
	body["size"] = fmt.Sprint(it.Size)
	body["checksums"] = checksumsJSON(it.Actual)
	body["originalChecksums"] = checksumsJSON(it.Original)
	return body
}

// checksumsJSON is the file info's checksum object, which in Artifactory names
// sha1, md5 and sha256 only; SHA-512 reaches clients as a ".sha512" file.
func checksumsJSON(c types.Checksums) map[string]string {
	out := map[string]string{}
	for k, v := range map[string]string{"sha1": c.SHA1, "md5": c.MD5, "sha256": c.SHA256} {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// formatTime renders Artifactory's ISO 8601 form with milliseconds.
func formatTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// found is an item a download resolved, with the repository it lives in.
type found struct {
	repo repo.Config
	item types.Item
}

// lookup finds the item at p for a download from c: a local repository's
// own tree, a remote repository's cache (nothing is fetched upstream until
// P9), or the first member of a virtual repository, in order, that the
// caller may read and that holds it. root reports the repository root, which
// is not an item.
func lookup(
	ctx context.Context,
	st store.Store,
	c repo.Config,
	cached bool,
	p string,
	acc access,
	seen map[string]bool,
) (found, error) {
	if seen[c.Key] {
		return found{}, store.ErrNotFound
	}
	seen[c.Key] = true
	if c.Class != repo.Virtual {
		it, err := st.Items().Get(ctx, c.Key, cached || c.Class == repo.Remote, p)
		if err != nil {
			return found{}, err //nolint:wrapcheck // ErrNotFound is the answer
		}
		return found{repo: c, item: it}, nil
	}
	for _, key := range c.Repositories {
		m, err := st.Repos().Get(ctx, key)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return found{}, fmt.Errorf("member %s of %s: %w", key, c.Key, err)
		}
		if !acc.folder(m, p, types.ActionRead) {
			continue
		}
		f, err := lookup(ctx, st, m, false, p, acc, seen)
		if err == nil || !errors.Is(err, store.ErrNotFound) {
			return f, err
		}
	}
	return found{}, store.ErrNotFound
}

// getItem answers GET and HEAD on an item path: file content, a checksum
// sidecar, or a folder listing.
func (s artifactoryService) getItem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	req, err := parseItemPath(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	c, cached, err := resolveRepo(ctx, st, req.key)
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "resolve repository", err))
		return
	}
	// A folder check first: the path may be a file or a folder, and in a
	// virtual repository the item lives in a member. The exact right is
	// checked once the item is found.
	if !acc.folder(c, req.path, types.ActionRead) {
		writeErr(w, r, denied(p))
		return
	}
	if req.path == "" {
		s.listFolder(
			w,
			r,
			st,
			found{repo: c, item: types.Item{RepoKey: c.Key, Cached: cached, Folder: true}},
			acc,
		)
		return
	}
	f, err := lookup(ctx, st, c, cached, req.path, acc, map[string]bool{})
	if errors.Is(err, store.ErrNotFound) {
		// "<archive>!/<entry>" names an entry inside an archive file; a path
		// that exists as an item is served as one first.
		if archive, entry, ok := strings.Cut(req.path, "!/"); ok {
			a, err := lookup(ctx, st, c, cached, archive, acc, map[string]bool{})
			if err != nil {
				writeErr(w, r, itemNotFound())
				return
			}
			if !acc.file(a.repo, a.item.FullPath(), types.ActionRead) {
				writeErr(w, r, denied(p))
				return
			}
			s.getArchiveEntry(w, r, st, a, entry)
			return
		}
		s.getSidecar(w, r, st, c, cached, req.path, acc)
		return
	}
	if err != nil {
		writeErr(w, r, s.internalError(ctx, "lookup item", err))
		return
	}
	if !acc.item(f.repo, f.item.FullPath(), f.item.Folder, types.ActionRead) {
		writeErr(w, r, denied(p))
		return
	}
	if ok, err := matchesMatrix(ctx, st, f.item, req); err != nil {
		writeErr(w, r, s.internalError(ctx, "item properties", err))
		return
	} else if !ok {
		writeErr(w, r, itemNotFound())
		return
	}
	if f.item.Folder {
		s.listFolder(w, r, st, f, acc)
		return
	}
	s.serveFile(w, r, st, f, p)
}

// matchesMatrix applies download matrix parameters: a key the item carries
// must have one of the requested values; a mandatory key ("key+=v") must be
// carried. Keys the item lacks do not stop a non-mandatory match.
func matchesMatrix(
	ctx context.Context,
	st store.Store,
	it types.Item,
	req itemRequest,
) (bool, error) {
	if len(req.props) == 0 {
		return true, nil
	}
	props, err := st.Items().Properties(ctx, it.ID)
	if err != nil {
		return false, fmt.Errorf("properties of %s: %w", it.FullPath(), err)
	}
	for k, want := range req.props {
		have, ok := props[k]
		if !ok {
			if req.mandatory[k] {
				return false, nil
			}
			continue
		}
		if !anyIn(want, have) {
			return false, nil
		}
	}
	return true, nil
}

func anyIn(want, have []string) bool {
	for _, w := range want {
		for _, h := range have {
			if w == h {
				return true
			}
		}
	}
	return false
}

// getSidecar serves "<file>.sha256", ".sha512", ".sha1" and ".md5" from the file's
// recorded checksums (decision 0004); they are never stored as items. The
// computed digest is served: under client-checksums the declared one must
// equal it, and under server-generated-checksums it is the one that counts.
func (s artifactoryService) getSidecar(
	w http.ResponseWriter,
	r *http.Request,
	st store.Store,
	c repo.Config,
	cached bool,
	p string,
	acc access,
) {
	artifact, alg, ok := item.Sidecar(p)
	if !ok {
		writeErr(w, r, itemNotFound())
		return
	}
	f, err := lookup(r.Context(), st, c, cached, artifact, acc, map[string]bool{})
	// A file the caller may not read has no visible checksum either.
	if err != nil || f.item.Folder || alg.Of(f.item.Actual) == "" ||
		!acc.file(f.repo, f.item.FullPath(), types.ActionRead) {
		writeErr(w, r, itemNotFound())
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Last-Modified", f.item.Modified.UTC().Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, alg.Of(f.item.Actual))
	}
}

// serveFile streams a file's content with the headers the JFrog CLI reads
// (Content-Length, Accept-Ranges and the X-Checksum headers); ranges and
// conditional requests are http.ServeContent's.
func (s artifactoryService) serveFile(
	w http.ResponseWriter,
	r *http.Request,
	st store.Store,
	f found,
	p auth.Principal,
) {
	ctx := r.Context()
	it := f.item
	h := w.Header()
	h.Set("X-Checksum-Sha1", it.Actual.SHA1)
	h.Set("X-Checksum-Sha256", it.Actual.SHA256)
	h.Set("X-Checksum-Md5", it.Actual.MD5)
	h.Set("X-Artifactory-Filename", it.Name)
	h.Set("Content-Type", it.MimeType)
	h.Set("Content-Disposition", blob.Disposition(it.Name))
	if it.Actual.SHA1 != "" {
		h.Set("ETag", `"`+it.Actual.SHA1+`"`)
	}
	if s.redirect(w, r, st, f, p) {
		return
	}
	content, _, err := s.cfg.Blobs.Open(ctx, it.Actual.SHA256)
	if err != nil {
		// The index names content the blob store does not hold: data loss,
		// not a client error.
		writeErr(w, r, s.internalError(ctx, "open blob", err))
		return
	}
	defer func() { _ = content.Close() }()
	sw := &statusWriter{ResponseWriter: w}
	http.ServeContent(sw, r, it.Name, it.Modified, content)
	if countsDownload(r, sw.status) {
		recordDownload(ctx, st, f, p)
	}
}

// statusWriter records the status ServeContent chose, so a 304 or a later
// range of a split download is not counted as a download.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b) //nolint:wrapcheck // a pass-through writer
}

// ReadFrom keeps the underlying writer's zero-copy path (sendfile for fs
// blobs) that io.Copy would otherwise lose behind this wrapper.
func (s *statusWriter) ReadFrom(r io.Reader) (int64, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return io.Copy(s.ResponseWriter, r) //nolint:wrapcheck // a pass-through writer
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// startsAtZero reports whether a Range header starts at the first byte, so a
// split download counts once rather than once per part.
func startsAtZero(rng string) bool { return strings.HasPrefix(strings.TrimSpace(rng), "bytes=0-") }

// listFolder answers a folder GET with a plain HTML index, as Artifactory
// serves browsers.
func (s artifactoryService) listFolder(
	w http.ResponseWriter,
	r *http.Request,
	st store.Store,
	f found,
	acc access,
) {
	ctx := r.Context()
	folder := f.item.FullPath()
	var children []types.Item
	page := store.Page{Limit: store.MaxLimit}
	for {
		batch, next, err := st.Items().Children(ctx, f.repo.Key, f.item.Cached, folder, page)
		if err != nil {
			writeErr(w, r, s.internalError(ctx, "list folder", err))
			return
		}
		children = append(children, batch...)
		if next == "" {
			break
		}
		page.After = next
	}
	title := html.EscapeString(path.Join(f.repo.Key, folder))
	var b strings.Builder
	fmt.Fprintf(&b, "<!DOCTYPE html>\n<html><head><title>Index of %s</title></head><body>\n", title)
	fmt.Fprintf(&b, "<h1>Index of %s</h1>\n<pre>", title)
	for _, c := range children {
		// Children the caller may not see are left out; a folder shows when
		// something below it is readable.
		if !acc.item(f.repo, c.FullPath(), c.Folder, types.ActionRead) {
			continue
		}
		href, text := url.PathEscape(c.Name), c.Name
		if c.Folder {
			href, text = href+"/", text+"/"
		}
		fmt.Fprintf(&b, "<a href=\"%s\">%s</a>\n", html.EscapeString(href), html.EscapeString(text))
	}
	b.WriteString("</pre></body></html>\n")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, b.String())
	}
}

// deleteItem answers DELETE on an item path: the item and everything below
// it, into the trash can for local repositories.
func (s artifactoryService) deleteItem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st, err := s.content()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	req, err := parseItemPath(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p, acc, err := s.itemCaller(ctx, st)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	c, cached, err := resolveRepo(ctx, st, req.key)
	if err != nil {
		writeErr(w, r, s.itemError(ctx, "resolve repository", err))
		return
	}
	if c.Class == repo.Virtual {
		writeErr(w, r, apierror.New(http.StatusMethodNotAllowed,
			fmt.Sprintf("Cannot delete from the virtual repository '%s'", c.Key)))
		return
	}
	if !acc.file(c, req.path, types.ActionDelete) {
		writeErr(w, r, denied(p))
		return
	}
	t := item.Target{Repo: c, Cached: cached || c.Class == repo.Remote, Path: req.path}
	if _, err := s.items(st).Delete(ctx, t, p.Name); err != nil {
		writeErr(w, r, s.itemError(ctx, "delete item", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetUploadsConfig tells the JFrog CLI whether to upload large files in
// parts (internal/service/multipart) or in one request (research 09).
func (s artifactoryService) GetUploadsConfig(
	context.Context,
	artgen.GetUploadsConfigRequestObject,
) (artgen.GetUploadsConfigResponseObject, error) {
	supported := s.cfg.Multipart != nil
	return artgen.GetUploadsConfig200JSONResponse{Supported: &supported}, nil
}
