package bower

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	bowerfmt "github.com/weaveplatform/artifactweave/internal/format/bower"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/server/pkgroute"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const dependencyTargetPrefix = "$$$art-shorthand-resolver$$$-"

func dependencyRewrite(c repo.Config) bool {
	return c.Class == repo.Virtual && c.ExternalDependenciesEnabled != nil &&
		*c.ExternalDependenciesEnabled
}

// dependencyURL rejects credentials and ambiguous paths before matching the
// configured Ant allowlist. The source transport still enforces DNS and IP policy.
func dependencyURL(c repo.Config, raw string) (*url.URL, error) {
	if !dependencyRewrite(c) || len(raw) > 4096 {
		return nil, source.ErrDenied
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil ||
		u.Fragment != "" ||
		u.Opaque != "" ||
		strings.ContainsAny(u.EscapedPath(), "%\\\x00") {
		return nil, source.ErrDenied
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return nil, source.ErrDenied
		}
	}
	for _, pattern := range c.ExternalDependenciesPatterns {
		if pattern != "" && authz.CompilePattern(pattern).Match(u.String()) {
			return u, nil
		}
	}
	return nil, source.ErrDenied
}

func dependencyTarget(raw string) string {
	return dependencyTargetPrefix + base64.RawURLEncoding.EncodeToString([]byte(raw)) + ".tar.gz"
}

func decodeDependencyTarget(tag string) (string, error) {
	if !strings.HasPrefix(tag, dependencyTargetPrefix) || !strings.HasSuffix(tag, ".tar.gz") ||
		len(tag) > 6000 {
		return "", source.ErrInvalidRequest
	}
	raw, err := base64.RawURLEncoding.Strict().
		DecodeString(strings.TrimSuffix(strings.TrimPrefix(tag, dependencyTargetPrefix), ".tar.gz"))
	if err != nil || dependencyTarget(string(raw)) != tag {
		return "", source.ErrInvalidRequest
	}
	return string(raw), nil
}

func (h handler) dependencyCache(r *http.Request) (*http.Request, error) {
	virtual := pkgroute.RequestFrom(r).Repo
	if !dependencyRewrite(virtual) || virtual.ExternalDependenciesRemoteRepo == "" ||
		!slices.Contains(virtual.Repositories, virtual.ExternalDependenciesRemoteRepo) {
		return nil, source.ErrDenied
	}
	c, err := h.k.DB().Repos().Get(r.Context(), virtual.ExternalDependenciesRemoteRepo)
	if err != nil {
		return nil, fmt.Errorf("bower dependency cache repository: %w", err)
	}
	if c.Class != repo.Remote || c.PackageType != repo.Bower ||
		c.AllowAnyHostAuth != nil && *c.AllowAnyHostAuth {
		return nil, source.ErrDenied
	}
	member, err := h.k.MemberRequest(r, c)
	if err != nil {
		return nil, fmt.Errorf("bower dependency member: %w", err)
	}
	return member, nil
}

type dependencyAuthorityKey struct{}

func (h handler) dependencyRequest(r *http.Request) (*http.Request, error) {
	id, err := h.k.DB().RemoteCache().Source(r.Context(), pkgroute.RequestFrom(r).Repo.Key)
	if err != nil {
		return r, fmt.Errorf("bower virtual identity: %w", err)
	}
	return r.WithContext(context.WithValue(r.Context(), dependencyAuthorityKey{}, id)), nil
}

func (h handler) checkDependencyAuthority(ctx context.Context, r *http.Request) error {
	before := pkgroute.RequestFrom(r).Repo
	current, err := h.k.DB().Repos().Get(ctx, before.Key)
	if err != nil {
		return source.ErrSuperseded
	}
	id, err := h.k.DB().RemoteCache().Source(ctx, before.Key)
	if err != nil {
		return source.ErrSuperseded
	}
	initial, ok := r.Context().Value(dependencyAuthorityKey{}).(types.SourceIdentity)
	if !ok || id != initial {
		return source.ErrSuperseded
	}
	if !reflect.DeepEqual(before, current) {
		return source.ErrSuperseded
	}
	return nil
}

func (h handler) currentDependencyRead(
	ctx context.Context,
	r *http.Request,
	path string,
	folder bool,
) error {
	if err := h.checkDependencyAuthority(ctx, r); err != nil {
		return err
	}
	if h.sources == nil || h.sources.Revalidate == nil {
		return nil
	}
	id, ok := r.Context().Value(dependencyAuthorityKey{}).(types.SourceIdentity)
	if !ok {
		return source.ErrSuperseded
	}
	p, _ := auth.FromContext(ctx)
	resource := source.Resource{Path: path, PermissionPath: path, PermissionFolder: folder}
	err := h.sources.Revalidate(
		ctx,
		source.Request{
			Repo:     pkgroute.RequestFrom(r).Repo,
			Subject:  auth.SubjectOf(p),
			Access:   auth.DeciderFrom(ctx),
			Resource: resource,
		},
		source.Permit{Identity: id},
	)
	if err != nil {
		return fmt.Errorf("bower virtual current authority: %w", err)
	}
	return nil
}

type dependencyRead struct {
	data       []byte
	revalidate func(context.Context) error
	used       func(context.Context) error
}

// Archive cache paths are not manifest permission paths. Retain the selected
// manifest's authority separately, including across transitive rewriting.
func (h handler) dependencyVersionRead(
	r, member *http.Request,
	name, version string,
) (func(context.Context) error, error) {
	if version == "" {
		return func(context.Context) error { return nil }, nil
	}
	path := bowerfmt.ManifestPath(name, version)
	if !h.k.Can(r, path, types.ActionRead) || !h.k.Can(member, path, types.ActionRead) {
		return nil, source.ErrDenied
	}
	id, err := h.k.DB().RemoteCache().Source(r.Context(), pkgroute.RequestFrom(member).Repo.Key)
	if err != nil {
		return nil, fmt.Errorf("bower dependency member identity: %w", err)
	}
	return func(ctx context.Context) error {
		if err := h.currentDependencyRead(ctx, r, path, false); err != nil {
			return err
		}
		if h.sources == nil || h.sources.Revalidate == nil {
			return nil
		}
		p, _ := auth.FromContext(ctx)
		err := h.sources.Revalidate(ctx, source.Request{
			Repo:    pkgroute.RequestFrom(member).Repo,
			Subject: auth.SubjectOf(p),
			Access:  auth.DeciderFrom(ctx),
			Resource: source.Resource{
				Kind:   source.ResourceKindArtifact,
				Path:   path,
				Method: http.MethodGet,
			},
		}, source.Permit{Identity: id})
		if err != nil {
			return fmt.Errorf("bower dependency manifest authority: %w", err)
		}
		return nil
	}, nil
}

func (h handler) readDependency(
	r *http.Request,
	name, raw, version string,
) (dependencyRead, error) {
	if !bowerfmt.ValidName(name) || !h.k.CanFolder(r, name, types.ActionRead) {
		return dependencyRead{}, source.ErrDenied
	}
	virtual := pkgroute.RequestFrom(r).Repo
	target, err := dependencyURL(virtual, raw)
	if err != nil {
		return dependencyRead{}, err
	}
	member, err := h.dependencyCache(r)
	if err != nil {
		return dependencyRead{}, err
	}
	if err := h.checkDependencyAuthority(r.Context(), r); err != nil {
		return dependencyRead{}, err
	}
	versionRead, err := h.dependencyVersionRead(r, member, name, version)
	if err != nil {
		return dependencyRead{}, err
	}
	if err := versionRead(r.Context()); err != nil {
		return dependencyRead{}, err
	}
	cacheVersion := version
	if cacheVersion == "" {
		cacheVersion = "0.0.0"
	}
	digest := sha256.Sum256([]byte(name + "\x00" + raw + "\x00" + version))
	resource := source.Resource{
		Kind: source.ResourceKindArtifact,
		Path: fmt.Sprintf(
			"%s/%s/external/%x/archive.tar.gz",
			name,
			cacheVersion,
			digest,
		),
		ExternalURL:       target.String(),
		Method:            http.MethodGet,
		Variant:           "bower-external-archive-v2",
		NoCrossOriginAuth: true,
		CheckURL: func(ctx context.Context, u *url.URL) error {
			if err := h.checkDependencyAuthority(ctx, r); err != nil {
				return err
			}
			if target.Scheme == "https" && u.Scheme != "https" {
				return source.ErrDenied
			}
			_, err := dependencyURL(virtual, u.String())
			return err
		},
		Validate: func(ctx context.Context, body io.ReadSeeker, size int64) error {
			if size <= 0 || size > maxArchiveBytes {
				return remoteInvalid()
			}
			data, err := io.ReadAll(io.LimitReader(body, maxArchiveBytes+1))
			if err != nil || int64(len(data)) != size {
				return remoteInvalid()
			}
			if err := validateDependencyVersion(data, name, version); err != nil {
				return err
			}
			return versionRead(ctx)
		},
	}
	if !h.k.Can(r, resource.Path, types.ActionRead) {
		return dependencyRead{}, source.ErrDenied
	}
	if err := h.currentDependencyRead(r.Context(), r, resource.Path, false); err != nil {
		return dependencyRead{}, err
	}
	result, err := h.k.Read(member, resource)
	if err != nil {
		return dependencyRead{}, fmt.Errorf("bower dependency read: %w", err)
	}
	var body io.ReadCloser
	switch {
	case result.Stream != nil:
		body = result.Stream.Body
	case result.Item != nil:
		body, _, err = h.k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
	default:
		return dependencyRead{}, remoteInvalid()
	}
	if err != nil {
		return dependencyRead{}, fmt.Errorf("bower dependency blob: %w", err)
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxArchiveBytes+1))
	if err != nil || len(data) > maxArchiveBytes {
		return dependencyRead{}, remoteInvalid()
	}
	if err := validateDependencyVersion(data, name, version); err != nil {
		return dependencyRead{}, err
	}
	revalidate := func(ctx context.Context) error {
		if result.Revalidate != nil {
			if err := result.Revalidate(ctx); err != nil {
				return fmt.Errorf("bower dependency authority: %w", err)
			}
		}
		if err := versionRead(ctx); err != nil {
			return err
		}
		return h.currentDependencyRead(ctx, r, resource.Path, false)
	}
	if err := revalidate(r.Context()); err != nil {
		return dependencyRead{}, err
	}
	return dependencyRead{data: data, revalidate: revalidate, used: result.Used}, nil
}

// Versionless Git manifests are valid; a declared version must agree with the
// selected semantic tag before admission, including on a warm cache read.
func validateDependencyVersion(data []byte, name, version string) error {
	files, err := dependencyFiles(data, name)
	if err != nil {
		return err
	}
	manifest, err := bowerfmt.DecodeGitManifest(files["bower.json"])
	if err != nil || version != "" && manifest.Version != "" && manifest.Version != version {
		return remoteInvalid()
	}
	return nil
}

func (h handler) externalBinary(w http.ResponseWriter, r *http.Request) bool {
	tag := chi.URLParam(r, "tag")
	if !strings.HasPrefix(tag, dependencyTargetPrefix) {
		return false
	}
	name := strings.TrimSuffix(chi.URLParam(r, "git"), ".git")
	if chi.URLParam(r, "owner") != "external" || chi.URLParam(r, "git") != name+".git" ||
		!bowerfmt.ValidName(name) ||
		r.URL.RawQuery != "" {
		h.k.Error(w, r, apierror.New(http.StatusBadRequest, "invalid Bower dependency"))
		return true
	}
	raw, err := decodeDependencyTarget(tag)
	var read dependencyRead
	var data []byte
	if err == nil {
		read, err = h.readDependency(r, name, raw, "")
		data = read.data
	}
	if err == nil {
		data, err = h.rewriteArchive(r, data, name)
	}
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return true
	}
	h.serveRewritten(w, r, data, read)
	return true
}

type rewrittenBody struct{ *bytes.Reader }

func (rewrittenBody) Close() error { return nil }

func (h handler) serveRewritten(
	w http.ResponseWriter,
	r *http.Request,
	data []byte,
	read dependencyRead,
) {
	name := strings.TrimSuffix(chi.URLParam(r, "git"), ".git")
	path := bowerfmt.ManifestPath(name, strings.TrimPrefix(chi.URLParam(r, "tag"), "v"))
	folder := !bowerfmt.ValidVersion(strings.TrimPrefix(chi.URLParam(r, "tag"), "v"))
	if folder {
		path = name
	}
	w.Header().Set("Cache-Control", "private, no-store")
	result := source.Result{
		Stream: &source.Stream{
			Body:   rewrittenBody{bytes.NewReader(data)},
			Status: http.StatusOK,
			Representation: source.Representation{
				MediaType: "application/gzip",
				Size:      int64(len(data)),
			},
		},
		Used: read.used,
		Revalidate: func(ctx context.Context) error {
			if read.revalidate != nil {
				if err := read.revalidate(ctx); err != nil {
					return fmt.Errorf("bower rewritten source authority: %w", err)
				}
			}
			return h.currentDependencyRead(ctx, r, path, folder)
		},
	}
	h.k.ServeResult(w, r, result)
}

func (h handler) rewriteArchive(r *http.Request, data []byte, name string) ([]byte, error) {
	files, err := dependencyFiles(data, name)
	if err != nil {
		return nil, err
	}
	var manifest map[string]json.RawMessage
	if json.Unmarshal(files["bower.json"], &manifest) != nil {
		return nil, remoteInvalid()
	}
	for _, field := range []string{"dependencies", "devDependencies"} {
		raw, ok := manifest[field]
		if !ok {
			continue
		}
		var deps map[string]string
		if json.Unmarshal(raw, &deps) != nil || len(deps) > 128 {
			return nil, remoteInvalid()
		}
		for dep, value := range deps {
			if !bowerfmt.ValidName(dep) {
				return nil, remoteInvalid()
			}
			if !strings.Contains(value, "://") && !strings.Contains(value, "/") {
				continue
			}
			if strings.HasPrefix(value, "art://external/") ||
				strings.HasPrefix(value, "art://"+pkgroute.RequestFrom(r).Repo.Key+"/") {
				continue
			}
			target, err := h.externalTarget(r, dep, value)
			if err != nil {
				return nil, err
			}
			if strings.HasPrefix(target, "art://") {
				deps[dep] = target
				continue
			}
			if _, err := h.readDependency(r, dep, target, ""); err != nil {
				return nil, err
			}
			deps[dep] = "art://external/" + dep + "#" + dependencyTarget(target)
		}
		manifest[field], err = json.Marshal(deps)
		if err != nil {
			return nil, fmt.Errorf("bower dependency manifest: %w", err)
		}
	}
	files["bower.json"], err = json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("bower rewritten manifest: %w", err)
	}
	return dependencyArchive(files, name)
}

func (h handler) externalTarget(r *http.Request, name, value string) (string, error) {
	if len(value) > 4096 {
		return "", source.ErrInvalidRequest
	}
	raw, ref, hasRef := strings.Cut(value, "#")
	member, err := h.dependencyCache(r)
	if err != nil {
		return "", err
	}
	c := pkgroute.RequestFrom(member).Repo
	if strings.HasPrefix(raw, "art://") || !strings.Contains(raw, "://") {
		parts := strings.Split(strings.TrimPrefix(raw, "art://"), "/")
		if len(parts) != 2 || !bowerfmt.ValidName(parts[0]) || !bowerfmt.ValidName(parts[1]) {
			return "", source.ErrInvalidRequest
		}
		base, err := configuredURL(c.URL)
		if err != nil {
			return "", err
		}
		base.Path = strings.TrimSuffix(base.Path, "/") + "/"
		if c.VCSGitProvider == repo.Stash || c.VCSGitProvider == repo.OldStash {
			base.Path += "scm/"
		}
		base.Path += parts[0] + "/" + parts[1] + ".git"
		raw = base.String()
	}
	if strings.HasSuffix(raw, ".git") || strings.HasPrefix(raw, "ssh://") {
		pointer, err := gitPointerURL(c, name, raw)
		if err != nil {
			return "", err
		}
		if _, err := dependencyURL(pkgroute.RequestFrom(r).Repo, pointer.url.String()); err != nil {
			return "", err
		}
		if !hasRef {
			ref = "*"
		}
		// The pinned client owns npm range parsing (including partial and
		// wildcard versions) and fetches non-semantic branch targets directly.
		// Preserve the target instead of duplicating that grammar here.
		if len(ref) > 200 || strings.ContainsAny(ref, "/\\\x00\r\n") {
			return "", source.ErrInvalidRequest
		}
		return "art://" + pointer.owner + "/" + pointer.project + "#" + ref, nil
	}
	if hasRef {
		return "", source.ErrInvalidRequest
	}
	target, err := dependencyURL(pkgroute.RequestFrom(r).Repo, raw)
	if err != nil {
		return "", err
	}
	return target.String(), nil
}

// archiveCapture buffers a complete, validated member response before rewriting.
// Partial/error responses cannot be mistaken for an archive or delivered with
// stale upstream checksum, range, or content-length headers.
type archiveCapture struct {
	header http.Header
	status int
	body   bytes.Buffer
	err    error
}

func (c *archiveCapture) Header() http.Header { return c.header }
func (c *archiveCapture) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}

func (c *archiveCapture) Write(data []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if len(data) > maxArchiveBytes-c.body.Len() {
		c.err = source.ErrInvalidResult
		return 0, c.err
	}
	n, _ := c.body.Write(data)
	return n, nil
}

func (h handler) rewrittenMember(
	w http.ResponseWriter,
	r, member *http.Request,
	name, version, tag string,
) {
	identity, err := h.k.DB().
		RemoteCache().
		Source(r.Context(), pkgroute.RequestFrom(member).Repo.Key)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	capture := &archiveCapture{header: make(http.Header)}
	var read dependencyRead
	request := member.Clone(context.WithValue(member.Context(), archiveResultKey{}, &read))
	request.Method = http.MethodGet
	for _, key := range []string{"Range", "If-Range", "If-None-Match", "If-Modified-Since"} {
		request.Header.Del(key)
	}
	h.serveBinary(capture, request, name, version, tag)
	if capture.status != http.StatusOK || capture.err != nil {
		h.k.Error(w, r, apierror.New(http.StatusBadGateway, "Bower member archive unavailable"))
		return
	}
	if h.sources != nil && h.sources.Revalidate != nil &&
		pkgroute.RequestFrom(member).Repo.Class != repo.Remote {
		files, err := dependencyFiles(capture.body.Bytes(), name)
		if err != nil {
			h.k.Error(w, r, pkgroute.SourceError(err, false))
			return
		}
		read.revalidate = func(ctx context.Context) error {
			p, _ := auth.FromContext(ctx)
			for path := range files {
				q := source.Request{
					Repo:     pkgroute.RequestFrom(member).Repo,
					Subject:  auth.SubjectOf(p),
					Access:   auth.DeciderFrom(ctx),
					Resource: source.Resource{Path: name + "/" + version + "/" + path},
				}
				if err := h.sources.Revalidate(
					ctx,
					q,
					source.Permit{Identity: identity},
				); err != nil {
					return fmt.Errorf("bower member file authority: %w", err)
				}
			}
			return nil
		}
	}
	data, err := h.rewriteArchive(r, capture.body.Bytes(), name)
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	h.serveRewritten(w, r, data, read)
}

func (h handler) shorthand(r *http.Request, binary bool) (gitPointer, *http.Request, error) {
	name := chi.URLParam(r, "name")
	if binary {
		name = strings.TrimSuffix(chi.URLParam(r, "git"), ".git")
	}
	owner := chi.URLParam(r, "owner")
	if !bowerfmt.ValidName(owner) || !bowerfmt.ValidName(name) || r.URL.RawQuery != "" ||
		binary && chi.URLParam(r, "git") != name+".git" {
		return gitPointer{}, nil, source.ErrInvalidRequest
	}
	member, err := h.dependencyCache(r)
	if err != nil {
		return gitPointer{}, nil, err
	}
	c := pkgroute.RequestFrom(member).Repo
	base, err := configuredURL(c.URL)
	if err != nil {
		return gitPointer{}, nil, err
	}
	base.Path = strings.TrimSuffix(base.Path, "/") + "/"
	if c.VCSGitProvider == repo.Stash || c.VCSGitProvider == repo.OldStash {
		base.Path += "scm/"
	}
	base.Path += owner + "/" + name + ".git"
	if _, err := dependencyURL(pkgroute.RequestFrom(r).Repo, base.String()); err != nil {
		return gitPointer{}, nil, err
	}
	pointer, err := gitPointerURL(c, name, base.String())
	return pointer, member, err
}

func (h handler) shorthandRefs(w http.ResponseWriter, r *http.Request) {
	pointer, member, err := h.shorthand(r, false)
	if err == nil && !h.k.CanFolder(r, pointer.name, types.ActionRead) {
		err = source.ErrDenied
	}
	var refs []remoteRef
	var result source.Result
	if err == nil {
		guard := func(ctx context.Context, target *url.URL) error {
			if err := h.currentDependencyRead(ctx, r, pointer.name, true); err != nil {
				return err
			}
			_, err := dependencyURL(pkgroute.RequestFrom(r).Repo, target.String())
			return err
		}
		refs, result, err = h.gitRefsForPointer(member, pointer.name, pointer, guard)
	}
	if err == nil && result.Revalidate != nil {
		err = result.Revalidate(r.Context())
	}
	if err == nil {
		err = h.currentDependencyRead(r.Context(), r, pointer.name, true)
	}
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	var out strings.Builder
	for _, ref := range refs {
		path := bowerfmt.ManifestPath(pointer.name, ref.Version)
		if h.k.Can(r, path, types.ActionRead) && h.k.Can(member, path, types.ActionRead) {
			if result.CheckRead != nil {
				err = result.CheckRead(r.Context(), path)
			}
			if err == nil {
				err = h.currentDependencyRead(r.Context(), r, path, false)
			}
			if err != nil {
				h.k.Error(w, r, pkgroute.SourceError(err, false))
				return
			}
			digest := sha256.Sum256([]byte(pointer.name + "\x00" + ref.Tag))
			fmt.Fprintf(&out, "%x refs/tags/%s\n", digest[:22], ref.Tag)
		}
	}
	if out.Len() == 0 {
		textAnswer(w, r, strings.Repeat("0", 44)+" refs/heads/master\n")
		return
	}
	textAnswer(w, r, out.String())
}

func (h handler) shorthandBinary(w http.ResponseWriter, r *http.Request) {
	pointer, member, err := h.shorthand(r, true)
	var target *url.URL
	if err == nil {
		target, _, err = gitArchiveURL(
			pkgroute.RequestFrom(member).Repo,
			pointer,
			chi.URLParam(r, "tag"),
		)
	}
	var read dependencyRead
	var data []byte
	if err == nil {
		version := strings.TrimPrefix(chi.URLParam(r, "tag"), "v")
		if !bowerfmt.ValidVersion(version) {
			version = ""
		}
		read, err = h.readDependency(r, pointer.name, target.String(), version)
		data = read.data
	}
	if err == nil {
		data, err = h.rewriteArchive(r, data, pointer.name)
	}
	if err != nil {
		h.k.Error(w, r, pkgroute.SourceError(err, false))
		return
	}
	h.serveRewritten(w, r, data, read)
}

type archiveResultKey struct{}

func (h handler) serveSourceResult(w http.ResponseWriter, r *http.Request, result source.Result) {
	if captured, ok := r.Context().Value(archiveResultKey{}).(*dependencyRead); ok {
		captured.revalidate, captured.used = result.Revalidate, result.Used
		result.Used = nil
	}
	h.k.ServeResult(w, r, result)
}
