package remote

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	gitformat "github.com/go-git/go-git/v5/plumbing/format/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/weaveplatform/artifactweave/internal/authz"
	podfmt "github.com/weaveplatform/artifactweave/internal/format/cocoapods"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const (
	cocoaPodsGitBytes        = 256 << 20
	cocoaPodsGitObjects      = 10000
	cocoaPodsGitRepositories = 16
)

type cocoaPodsCheckout struct {
	policy           *Policy
	request          source.Request
	permit           source.Permit
	options          Options
	client           *Client
	remainingBytes   int64
	remainingObjects int
	repositories     int
	files            int
	expanded         int64
	seen             map[string]bool
	archive          *tar.Writer
}

// fetchCocoaPodsGit keeps Git transport behind the same source policy as HTTP
// archives. Only the final validated tar is published; no Git checkout or
// attacker-controlled filesystem/configuration is ever executed.
func (p *Policy) fetchCocoaPodsGit(
	ctx context.Context,
	q source.Request,
	permit source.Permit,
	opts Options,
) (*http.Response, error) {
	if q.Repo.PackageType != repo.CocoaPods || q.Resource.CocoaPodsGit == nil || p.Pool == nil ||
		!permit.UpstreamNetworkAllowed {
		return nil, source.ErrUnsupported
	}
	g := *q.Resource.CocoaPodsGit
	if g.Validate() != nil {
		return nil, source.ErrInvalidRequest
	}
	client, err := p.Pool.Client(q.Repo, opts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var output bytes.Buffer
	compressed := &cocoaPodsLimitedWriter{writer: &output, remaining: podfmt.MaxArchiveBytes}
	gz := gzip.NewWriter(compressed)
	tw := tar.NewWriter(gz)
	checkout := &cocoaPodsCheckout{
		policy:           p,
		request:          q,
		permit:           permit,
		options:          opts,
		client:           client,
		remainingBytes:   cocoaPodsGitBytes,
		remainingObjects: cocoaPodsGitObjects,
		seen:             map[string]bool{},
		archive:          tw,
	}
	if err := checkout.repository(ctx, g, "", 0); err != nil {
		return nil, err
	}
	if checkout.files == 0 {
		return nil, source.ErrInvalidResult
	}
	if err := tw.Close(); err != nil {
		return nil, source.ErrInvalidResult
	}
	if err := gz.Close(); err != nil {
		return nil, source.ErrInvalidResult
	}
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": {"application/gzip"}},
		ContentLength: int64(output.Len()),
		Body:          io.NopCloser(bytes.NewReader(output.Bytes())),
	}, nil
}

func (g *cocoaPodsCheckout) repository(
	ctx context.Context,
	spec source.CocoaPodsGitSource,
	prefix string,
	depth int,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("CocoaPods Git context: %w", err)
	}
	g.repositories++
	if depth > 8 || g.repositories > cocoaPodsGitRepositories || spec.Validate() != nil {
		return source.ErrInvalidResult
	}
	endpoint, err := transport.NewEndpoint(strings.TrimSuffix(spec.URL, "/"))
	if err != nil {
		return source.ErrInvalidResult
	}
	httpClient := &http.Client{
		Transport: cocoaPodsGitTransport{
			checkout:   g,
			repository: strings.TrimSuffix(spec.URL, "/"),
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return ErrRedirect },
	}
	session, err := githttp.NewClient(httpClient).NewUploadPackSession(endpoint, nil)
	if err != nil {
		return source.ErrInvalidResult
	}
	defer session.Close()
	refs, err := session.AdvertisedReferencesContext(ctx)
	if err != nil {
		return cocoaPodsGitError(err)
	}
	want, err := cocoaPodsGitWant(spec, refs)
	if err != nil {
		return err
	}
	request := packp.NewUploadPackRequest()
	request.Wants = []plumbing.Hash{want}
	if refs.Capabilities.Supports(capability.Shallow) {
		_ = request.Capabilities.Set(capability.Shallow)
		request.Depth = packp.DepthCommits(1)
	}
	if refs.Capabilities.Supports(capability.OFSDelta) {
		_ = request.Capabilities.Set(capability.OFSDelta)
	}
	if refs.Capabilities.Supports(capability.NoProgress) {
		_ = request.Capabilities.Set(capability.NoProgress)
	}
	response, err := session.UploadPack(ctx, request)
	if err != nil {
		return cocoaPodsGitError(err)
	}
	data, err := io.ReadAll(io.LimitReader(response, podfmt.MaxArchiveBytes+1))
	closeErr := response.Close()
	if err != nil || closeErr != nil || len(data) > podfmt.MaxArchiveBytes {
		return source.ErrInvalidResult
	}
	objects, err := decodeCocoaPodsPack(ctx, data, g.remainingBytes, g.remainingObjects)
	if err != nil {
		return err
	}
	for _, o := range objects.Objects {
		g.remainingBytes -= o.Size()
		g.remainingObjects--
	}
	commit, err := cocoaPodsGitCommit(objects, want)
	if err != nil {
		return err
	}
	tree, err := commit.Tree()
	if err != nil {
		return source.ErrInvalidResult
	}
	modules, err := cocoaPodsGitModules(tree, spec)
	if err != nil {
		return err
	}
	return g.tree(ctx, objects, tree, spec, prefix, "", modules, depth, 0)
}

func cocoaPodsGitWant(spec source.CocoaPodsGitSource, refs *packp.AdvRefs) (plumbing.Hash, error) {
	if spec.Commit != "" {
		return plumbing.NewHash(spec.Commit), nil
	}
	var hash plumbing.Hash
	switch {
	case spec.Tag != "":
		hash = refs.References["refs/tags/"+spec.Tag]
	case spec.Branch != "":
		hash = refs.References["refs/heads/"+spec.Branch]
		if hash.IsZero() {
			hash = refs.References["refs/tags/"+spec.Branch]
		}
	case refs.Head != nil:
		hash = *refs.Head
	}
	if hash.IsZero() {
		return plumbing.ZeroHash, store.ErrNotFound
	}
	return hash, nil
}

func cocoaPodsGitCommit(objects *memory.Storage, hash plumbing.Hash) (*object.Commit, error) {
	for range 8 {
		item, err := objects.EncodedObject(plumbing.AnyObject, hash)
		if err != nil {
			return nil, source.ErrInvalidResult
		}
		if item.Type() == plumbing.CommitObject {
			commit, err := object.DecodeCommit(objects, item)
			if err != nil {
				return nil, source.ErrInvalidResult
			}
			return commit, nil
		}
		if item.Type() != plumbing.TagObject {
			return nil, source.ErrInvalidResult
		}
		tag, err := object.DecodeTag(objects, item)
		if err != nil {
			return nil, source.ErrInvalidResult
		}
		hash = tag.Target
	}
	return nil, source.ErrInvalidResult
}

func cocoaPodsGitModules(
	tree *object.Tree,
	spec source.CocoaPodsGitSource,
) (map[string]string, error) {
	out := map[string]string{}
	if !spec.Submodules {
		return out, nil
	}
	file, err := tree.File(".gitmodules")
	if errors.Is(err, object.ErrFileNotFound) {
		return out, nil
	}
	if err != nil || file.Size > 64<<10 || file.Mode != filemode.Regular {
		return nil, source.ErrInvalidResult
	}
	content, err := file.Contents()
	if err != nil {
		return nil, source.ErrInvalidResult
	}
	modules := gitformat.New()
	if gitformat.NewDecoder(strings.NewReader(content)).Decode(modules) != nil {
		return nil, source.ErrInvalidResult
	}
	rows := modules.Section("submodule").Subsections
	if len(rows) > cocoaPodsGitRepositories {
		return nil, source.ErrInvalidResult
	}
	for _, row := range rows {
		module := gitconfig.Submodule{
			Name: row.Name,
			Path: row.Option("path"),
			URL:  row.Option("url"),
		}
		if module.Validate() != nil || !cocoaPodsGitPath(module.Path) || out[module.Path] != "" {
			return nil, source.ErrInvalidResult
		}
		raw := module.URL
		if strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") {
			base, _ := url.Parse(strings.TrimSuffix(spec.URL, "/") + "/")
			relative, err := url.Parse(raw)
			if err != nil {
				return nil, source.ErrInvalidResult
			}
			raw = base.ResolveReference(relative).String()
		}
		child := source.CocoaPodsGitSource{URL: raw}
		if child.Validate() != nil {
			return nil, source.ErrInvalidResult
		}
		out[module.Path] = raw
	}
	return out, nil
}

func cocoaPodsGitPath(path string) bool {
	if path == "" || len(path) > 2048 || strings.ContainsAny(path, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".git") ||
			strings.Contains(part, ":") {
			return false
		}
	}
	return true
}

func (g *cocoaPodsCheckout) tree(
	ctx context.Context,
	objects *memory.Storage,
	tree *object.Tree,
	spec source.CocoaPodsGitSource,
	prefix, relative string,
	modules map[string]string,
	depth, treeDepth int,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("CocoaPods Git context: %w", err)
	}
	if treeDepth > 64 {
		return source.ErrInvalidResult
	}
	names := map[string]bool{}
	for _, entry := range tree.Entries {
		if !cocoaPodsGitPath(entry.Name) || strings.Contains(entry.Name, "/") || names[entry.Name] {
			return source.ErrInvalidResult
		}
		names[entry.Name] = true
		local := relative + entry.Name
		path := prefix + local
		if !cocoaPodsGitPath(path) {
			return source.ErrInvalidResult
		}
		switch entry.Mode {
		case filemode.Dir:
			child, err := object.GetTree(objects, entry.Hash)
			if err != nil {
				return source.ErrInvalidResult
			}
			if err = g.tree(
				ctx,
				objects,
				child,
				spec,
				prefix,
				local+"/",
				modules,
				depth,
				treeDepth+1,
			); err != nil {
				return err
			}
		case filemode.Submodule:
			if !spec.Submodules {
				continue
			}
			raw, ok := modules[local]
			if !ok {
				return source.ErrInvalidResult
			}
			child := source.CocoaPodsGitSource{
				URL:        raw,
				Commit:     entry.Hash.String(),
				Submodules: true,
			}
			if err := g.repository(ctx, child, path+"/", depth+1); err != nil {
				return err
			}
		case filemode.Regular, filemode.Executable:
			if err := g.file(objects, entry, path); err != nil {
				return err
			}
		default:
			// Match the existing CocoaPods archive contract: links and special
			// entries are rejected before publication, rather than followed.
			return source.ErrInvalidResult
		}
	}
	return nil
}

func (g *cocoaPodsCheckout) file(
	objects *memory.Storage,
	entry object.TreeEntry,
	path string,
) error {
	if g.seen[path] {
		return source.ErrInvalidResult
	}
	g.seen[path] = true
	blob, err := object.GetBlob(objects, entry.Hash)
	if err != nil || blob.Size < 0 || blob.Size > podfmt.MaxArchiveBytes {
		return source.ErrInvalidResult
	}
	g.files++
	g.expanded += blob.Size
	if g.files > cocoaPodsGitObjects || g.expanded > cocoaPodsGitBytes {
		return source.ErrInvalidResult
	}
	mode := int64(0o644)
	if entry.Mode == filemode.Executable {
		mode = 0o755
	}
	if err = g.archive.WriteHeader(
		&tar.Header{Name: path, Mode: mode, Size: blob.Size, Typeflag: tar.TypeReg},
	); err != nil {
		return source.ErrInvalidResult
	}
	body, err := blob.Reader()
	if err != nil {
		return source.ErrInvalidResult
	}
	_, copyErr := io.CopyN(g.archive, body, blob.Size)
	closeErr := body.Close()
	if copyErr != nil || closeErr != nil {
		return source.ErrInvalidResult
	}
	return nil
}

type cocoaPodsGitTransport struct {
	checkout   *cocoaPodsCheckout
	repository string
}

func (t cocoaPodsGitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	g := t.checkout
	if g.policy.CheckCurrent != nil {
		if err := g.policy.CheckCurrent(
			req.Context(),
			g.policy.Store,
			g.request,
			g.permit,
			g.options.Revision,
		); err != nil {
			return nil, err
		}
	}
	if err := cocoaPodsGitAllowed(g.request.Repo, t.repository); err != nil {
		return nil, err
	}
	target := t.repository + "/info/refs?service=git-upload-pack"
	limit := int64(8 << 20)
	var body []byte
	if req.Method == http.MethodPost {
		target = t.repository + "/git-upload-pack"
		limit = podfmt.MaxArchiveBytes
		var err error
		body, err = io.ReadAll(io.LimitReader(req.Body, 4097))
		if err != nil || len(body) > 4096 {
			return nil, source.ErrInvalidRequest
		}
	} else if req.Method != http.MethodGet {
		return nil, source.ErrInvalidRequest
	}
	if req.URL.String() != target {
		return nil, source.ErrDenied
	}
	guard := func(ctx context.Context, next *url.URL) error {
		if next.String() != target {
			return source.ErrDenied
		}
		return cocoaPodsGitAllowed(g.request.Repo, t.repository)
	}
	accept := "application/x-git-upload-pack-advertisement"
	if req.Method == http.MethodPost {
		accept = "application/x-git-upload-pack-result"
	}
	response, err := g.client.Do(
		req.Context(),
		Request{
			Method:            req.Method,
			ExternalURL:       target,
			CheckURL:          guard,
			NoCrossOriginAuth: true,
			Metadata:          true,
			Body:              body,
			gitUploadPack:     req.Method == http.MethodPost,
			Header:            http.Header{"Accept": {accept}},
		},
	)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, responseError(response, time.Now())
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, source.ErrInvalidResult
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	response.ContentLength = int64(len(data))
	return response, nil
}

func cocoaPodsGitAllowed(c repo.Config, raw string) error {
	if c.ExternalDependenciesEnabled == nil || !*c.ExternalDependenciesEnabled {
		return source.ErrDenied
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || (source.CocoaPodsGitSource{URL: raw}).Validate() != nil {
		return source.ErrDenied
	}
	base, err := url.Parse(c.URL)
	if err != nil || base == nil || base.Scheme == "https" && u.Scheme != "https" {
		return source.ErrDenied
	}
	for _, pattern := range c.ExternalDependenciesPatterns {
		if pattern != "" && authz.CompilePattern(pattern).Match(raw) {
			return nil
		}
	}
	return source.ErrDenied
}

func cocoaPodsGitError(err error) error {
	// go-git wraps HTTP transport failures in these types without implementing
	// Unwrap. Recover the cause so permission/configuration revocation cannot
	// become a transient failure eligible for stale-cache fallback.
	var unexpected *plumbing.UnexpectedError
	if errors.As(err, &unexpected) {
		return cocoaPodsGitError(unexpected.Err)
	}
	var permanent *plumbing.PermanentError
	if errors.As(err, &permanent) {
		return cocoaPodsGitError(permanent.Err)
	}
	if errors.Is(err, transport.ErrRepositoryNotFound) ||
		errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return store.ErrNotFound
	}
	for _, allowed := range []error{source.ErrDenied, source.ErrSuperseded, source.ErrInvalidResult, context.Canceled, context.DeadlineExceeded, store.ErrNotFound} {
		if errors.Is(err, allowed) {
			return allowed
		}
	}
	var failure *source.HTTPError
	if errors.As(err, &failure) {
		return failure
	}
	return source.ErrUnavailable
}

type cocoaPodsLimitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *cocoaPodsLimitedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, source.ErrInvalidResult
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	if err != nil {
		return n, fmt.Errorf("CocoaPods archive: %w", err)
	}
	return n, nil
}
