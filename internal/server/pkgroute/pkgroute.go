// Package pkgroute is the router kit for package formats with a protocol of
// their own: npm and Go in P7, then PyPI, NuGet, Helm and Terraform
// (decision 0032). Mount claims /artifactory/api/<name>/{repoKey}/... for
// one protocol and, before the protocol's handlers run, resolves the
// repository, checks its package type and passes it through the class gate,
// so every protocol answers those the same way and in its own envelope. The
// kit's helpers then check permissions, download through the item routes'
// download path and deploy through the item service.
//
// Docker's registry predates the kit and keeps its own router (decision
// 0031). This package never imports internal/server: the server hands it
// what it needs through Options.
package pkgroute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// Options is what the server gives every protocol: the store, and the item
// routes' download, deploy and error mapping, so a protocol's files behave
// as the item routes' do.
type Options struct {
	// Store resolves repositories. Nil answers every request 501, as the
	// server's own handlers do without a store.
	Store store.Store
	// Items returns the item service a deploy goes through, with the
	// server's formats and work queue.
	Items func() *item.Service
	// ServeItem answers GET or HEAD of a file in c with the item routes'
	// download, which applies the download redirect policy (decision 0029),
	// sets the checksum headers and counts the download.
	ServeItem func(w http.ResponseWriter, r *http.Request, c repo.Config, it types.Item)
	// ItemError turns an item service error into the *apierror.Error the
	// item routes answer with, so a format's Rejection keeps its status.
	ItemError func(ctx context.Context, op string, err error) error
	// Created answers 201 to a deploy that stored it, with the item routes'
	// deploy body. Nil answers 201 with no body.
	Created func(w http.ResponseWriter, r *http.Request, it types.Item)
	// Fault records an unexpected error for the access log; nil drops it.
	Fault func(*http.Request, error)
}

// Protocol is one format's protocol.
type Protocol struct {
	// Name is the segment after /artifactory/api/, such as "npm" or "go".
	Name string
	// Types are the package types whose repositories the protocol serves.
	// A repository of another type answers 404, as a missing one does.
	Types []repo.PackageType
	// Surface is the error envelope the protocol's clients parse.
	Surface apierror.Surface
	// Reserved are the JFrog operations beside the protocol, as paths after
	// the prefix whose {...} segments match any one segment: "auth",
	// "{repoKey}/reindex". chi routes an operation's own methods to the
	// generated servers ahead of the kit, so a request for one that reaches
	// the kit used a method no route serves, and answers 405 rather than
	// being read as a package path.
	Reserved []string
	// Absent are paths below every repository key that answer 404 before
	// the request is authenticated or its repository looked up, as
	// prefixes of the escaped path after the key: Go's "sumdb/" (decision
	// 0035). The go command takes any other answer to its checksum
	// database probe, a 401 included, for an outage and fails. The server
	// asks IsAbsent before it authenticates; Mount answers them.
	Absent []string
	// Sources serves each repository class the protocol reads, the seam
	// remote (P9, decision 0007) and virtual (P10, decision 0008) classes
	// plug into. Nil serves local and federated repositories from their own
	// items. Any other class answers 501 naming it (decision 0032).
	Sources map[repo.Class]Source
	// Routes registers the protocol's handlers on the router for one
	// repository, with paths relative to /artifactory/api/<name>/{repoKey}.
	// chi matches them against the escaped path, so a parameter keeps its
	// escapes; ParsePath decodes them. What the router answers no route
	// for is 404 or 405 in the protocol's envelope.
	Routes func(r chi.Router, k *Kit)
}

// IsAbsent reports whether escaped, a request's escaped path, is one of
// p's Absent paths in any repository.
func (p Protocol) IsAbsent(escaped string) bool {
	rel, ok := strings.CutPrefix(escaped, "/artifactory/api/"+p.Name+"/")
	if !ok {
		return false
	}
	_, rest, ok := strings.Cut(rel, "/")
	return ok && slices.ContainsFunc(p.Absent, func(prefix string) bool {
		return strings.HasPrefix(rest, prefix) || rest == strings.TrimSuffix(prefix, "/")
	})
}

// Source reads a repository's items for a protocol.
type Source interface {
	// Item returns the file or folder at path as c's class finds it, with
	// the repository it lives in, or an error wrapping store.ErrNotFound.
	Item(ctx context.Context, st store.Store, c repo.Config, path string) (Found, error)
}

// Found is an item a Source found, with the repository that holds it: for
// a virtual repository, one of its members.
type Found struct {
	Repo repo.Config
	Item types.Item
}

// Local reads a local or federated repository's own items.
type Local struct{}

// Item returns the item at path in c.
func (Local) Item(ctx context.Context, st store.Store, c repo.Config, path string) (Found, error) {
	it, err := st.Items().Get(ctx, c.Key, false, path)
	if err != nil {
		return Found{}, fmt.Errorf("item %s/%s: %w", c.Key, path, err)
	}
	return Found{Repo: c, Item: it}, nil
}

// Kit is one protocol mounted on a router. Its handlers call its helpers,
// which answer in the protocol's envelope.
type Kit struct {
	o        Options
	p        Protocol
	prefix   string
	reserved [][]string
}

// Mount claims prefix /artifactory/api/<name>/{repoKey} on r for p, before
// the generated servers are mounted (decision 0032), and returns the kit
// p's routes were given.
func Mount(r chi.Router, o Options, p Protocol) *Kit {
	k := &Kit{o: o, p: p, prefix: "/artifactory/api/" + p.Name}
	if k.p.Sources == nil {
		k.p.Sources = map[repo.Class]Source{repo.Local: Local{}, repo.Federated: Local{}}
	}
	for _, path := range p.Reserved {
		k.reserved = append(k.reserved, strings.Split(path, "/"))
	}
	r.Route(k.prefix+"/{repoKey}", func(sub chi.Router) {
		sub.Use(k.resolve)
		sub.NotFound(func(w http.ResponseWriter, r *http.Request) {
			k.Error(w, r, apierror.New(http.StatusNotFound, "not found: "+r.Method+" "+r.URL.Path))
		})
		sub.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
			k.Error(w, r, methodNotAllowed(r))
		})
		if p.Routes != nil {
			p.Routes(sub, k)
		}
	})
	return k
}

func methodNotAllowed(r *http.Request) error {
	return apierror.New(http.StatusMethodNotAllowed, "method not allowed: "+r.Method+" "+r.URL.Path)
}

// Request is a protocol request whose repository is resolved and served.
type Request struct {
	// Repo is the repository the request addressed.
	Repo repo.Config
	// Source reads Repo's items.
	Source Source
	// Rest is the escaped path after the repository key, without its
	// leading slash; ParsePath parses it.
	Rest string
}

type requestKey struct{}

// RequestFrom returns the request Mount resolved; a handler registered
// through Routes always has one.
func RequestFrom(r *http.Request) Request {
	q, _ := r.Context().Value(requestKey{}).(Request)
	return q
}

// resolve answers a reserved operation's other methods, resolves the
// repository, checks its type and class, and hands the request on with
// its Request.
func (k *Kit) resolve(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.EscapedPath(), k.prefix+"/")
		if k.isReserved(rel) {
			k.Error(w, r, methodNotAllowed(r))
			return
		}
		if k.p.IsAbsent(r.URL.EscapedPath()) {
			k.Error(w, r, apierror.New(http.StatusNotFound,
				"artifactweave does not serve "+r.URL.Path+"."))
			return
		}
		escKey, rest, _ := strings.Cut(rel, "/")
		key, err := url.PathUnescape(escKey)
		if err != nil {
			k.Error(w, r, badRequest("malformed repository key %q", escKey))
			return
		}
		if k.o.Store == nil {
			k.Error(w, r, apierror.ErrNotImplemented)
			return
		}
		c, err := k.o.Store.Repos().Get(r.Context(), key)
		switch {
		case errors.Is(err, store.ErrNotFound):
			k.Error(w, r, apierror.New(http.StatusNotFound,
				fmt.Sprintf("Repository '%s' does not exist", key)))
			return
		case err != nil:
			k.Error(w, r, fmt.Errorf("get repository %s: %w", key, err))
			return
		case !slices.Contains(k.p.Types, c.PackageType):
			// [I] Artifactory's answer for another type is unknown (research
			// 26 section 6, item 6); 404 is what the registry answers.
			k.Error(w, r, apierror.New(http.StatusNotFound,
				fmt.Sprintf("Repository '%s' is not a %s repository", key, k.p.Name)))
			return
		}
		src, ok := k.p.Sources[c.Class]
		if !ok {
			k.Error(w, r, apierror.New(http.StatusNotImplemented, fmt.Sprintf(
				"'%s' is a %s repository, which artifactweave does not serve through the %s API yet",
				key,
				c.Class,
				k.p.Name,
			)))
			return
		}
		q := Request{Repo: c, Source: src, Rest: rest}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestKey{}, q)))
	})
}

// isReserved reports whether rel, an escaped path after the prefix, is one
// of the protocol's reserved operations.
func (k *Kit) isReserved(rel string) bool {
	segs := strings.Split(strings.Trim(rel, "/"), "/")
	return slices.ContainsFunc(k.reserved, func(op []string) bool {
		if len(op) != len(segs) {
			return false
		}
		for i, s := range op {
			if !strings.HasPrefix(s, "{") && s != segs[i] {
				return false
			}
		}
		return true
	})
}

// Error answers err in the protocol's envelope. An error that is not an
// *apierror.Error is a fault: it goes to the access log and the client
// gets a bare 500.
func (k *Kit) Error(w http.ResponseWriter, r *http.Request, err error) {
	var typed *apierror.Error
	if !errors.As(err, &typed) && k.o.Fault != nil {
		k.o.Fault(r, err)
	}
	apierror.Write(w, k.p.Surface, err)
}

// Can reports whether the caller holds every action in want on path in the
// request's repository, for a handler that filters what it lists by what
// the caller may read. A request without a principal holds nothing, and so
// does one whose permissions were never evaluated: the server's middleware
// evaluates them for every request.
func (k *Kit) Can(r *http.Request, path string, want types.Actions) bool {
	return can(r, RequestFrom(r).Repo, path, want)
}

func can(r *http.Request, c repo.Config, path string, want types.Actions) bool {
	p, ok := auth.FromContext(r.Context())
	if !ok || p.Access() == nil {
		return false
	}
	return p.Access().Allows(types.ResourceArtifact, authz.Repo{
		Key: c.Key, Class: string(c.Class),
	}, path, want)
}

// Require is Can, answering the refusal when it fails: 401 to an anonymous
// caller or one without credentials, so it sends them, and 403 to anyone
// else, as the item routes refuse.
func (k *Kit) Require(
	w http.ResponseWriter,
	r *http.Request,
	path string,
	want types.Actions,
) bool {
	if k.Can(r, path, want) {
		return true
	}
	k.Error(w, r, refusal(r))
	return false
}

// RequireFolder is Require for a folder, which a caller may read when it
// may read a path below it. A protocol that answers for a whole package,
// such as npm's packument, asks it of the package's folder before saying
// whether the package exists, so a refusal says nothing about what is
// there.
func (k *Kit) RequireFolder(
	w http.ResponseWriter,
	r *http.Request,
	path string,
	want types.Actions,
) bool {
	if p, ok := auth.FromContext(r.Context()); ok && p.Access() != nil {
		c := RequestFrom(r).Repo
		if p.Access().AllowsFolder(types.ResourceArtifact, authz.Repo{
			Key: c.Key, Class: string(c.Class),
		}, path, want) {
			return true
		}
	}
	k.Error(w, r, refusal(r))
	return false
}

func refusal(r *http.Request) error {
	if p, ok := auth.FromContext(r.Context()); ok && !p.Anonymous {
		return auth.ErrForbidden
	}
	return auth.ErrUnauthenticated
}

// DB is the store the kit resolves repositories in, for a handler that
// reads the package index (decision 0018) or lists a folder: Go's version
// lists and npm's packuments are built from the index.
func (k *Kit) DB() store.Store { return k.o.Store }

// Item returns the item at path in the request's repository, through its
// Source.
func (k *Kit) Item(r *http.Request, path string) (Found, error) {
	q := RequestFrom(r)
	f, err := q.Source.Item(r.Context(), k.o.Store, q.Repo, path)
	if err != nil {
		return Found{}, fmt.Errorf("%s: %w", k.p.Name, err)
	}
	return f, nil
}

// ServeItem answers GET or HEAD of the file at path in the request's
// repository through the item routes' download, so the download redirect
// policy and download counts apply. A caller that may not read path is
// refused before the item is looked up, so a refusal says nothing about
// whether it exists; a missing file, or a folder, answers 404.
func (k *Kit) ServeItem(w http.ResponseWriter, r *http.Request, path string) {
	if !k.Require(w, r, path, types.ActionRead) {
		return
	}
	f, err := k.Item(r, path)
	switch {
	case errors.Is(err, store.ErrNotFound) || err == nil && f.Item.Folder:
		k.Error(w, r, apierror.New(http.StatusNotFound, "Could not find resource"))
	case err != nil:
		k.Error(w, r, err)
	default:
		k.o.ServeItem(w, r, f.Repo, f.Item)
	}
}

// Store writes body at path in the request's repository through the item
// service, as the item routes' deploy does: the caller needs Write on
// path, and Delete too to replace a file. props are set on the file and
// declared is checked against its content under the repository's checksum
// policy. It returns the stored file, or the refusal to answer with Error.
// The request's repository is the target; the class gate admits only the
// classes a protocol serves, which in P7 hold their own items.
func (k *Kit) Store(
	r *http.Request,
	path string,
	body io.Reader,
	props types.Properties,
	declared types.Checksums,
) (types.Item, error) {
	ctx := r.Context()
	c := RequestFrom(r).Repo
	if !can(r, c, path, types.ActionWrite) {
		return types.Item{}, refusal(r)
	}
	existing, err := k.o.Store.Items().Get(ctx, c.Key, false, path)
	switch {
	case err == nil && !existing.Folder && !can(r, c, path, types.ActionDelete):
		// Overwriting replaces content, which needs the right to delete it.
		return types.Item{}, refusal(r)
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return types.Item{}, fmt.Errorf("get item %s/%s: %w", c.Key, path, err)
	}
	p, _ := auth.FromContext(ctx)
	it, err := k.o.Items().Deploy(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Body: body, Declared: declared,
		Properties: props, By: p.Name,
	})
	if err != nil {
		return types.Item{}, k.o.ItemError(ctx, "deploy", err)
	}
	return it, nil
}

// StoreOnce is Store for a file that never changes once stored, such as a
// Go module version's .zip or .mod (decision 0035). The caller needs Write
// on path and never Delete, since nothing is replaced: the same content
// again only merges props into the stored file's properties, and other
// content is not written but refused with what differs returns for the
// stored file. Only the body's digest tells the two apart, so the body is
// committed to the blob store before the stored file is read; content left
// unreferenced is collected as an orphan.
func (k *Kit) StoreOnce(
	r *http.Request,
	path string,
	body io.Reader,
	props types.Properties,
	declared types.Checksums,
	differs func(stored types.Item) error,
) (types.Item, error) {
	ctx := r.Context()
	c := RequestFrom(r).Repo
	if !can(r, c, path, types.ActionWrite) {
		return types.Item{}, refusal(r)
	}
	p, _ := auth.FromContext(ctx)
	d := item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Body: body, Declared: declared,
		Properties: props, By: p.Name,
	}
	svc := k.o.Items()
	info, err := svc.Upload(ctx, d)
	if err != nil {
		return types.Item{}, k.o.ItemError(ctx, "deploy", err)
	}
	stored, err := k.o.Store.Items().Get(ctx, c.Key, false, path)
	switch {
	case errors.Is(err, store.ErrNotFound) || err == nil && stored.Folder:
		// The item service refuses a file over a folder, as on any deploy.
		it, err := svc.DeployCommitted(ctx, d, info)
		if err != nil {
			return types.Item{}, k.o.ItemError(ctx, "deploy", err)
		}
		return it, nil
	case err != nil:
		return types.Item{}, fmt.Errorf("get item %s/%s: %w", c.Key, path, err)
	case stored.Actual.SHA256 != info.Digests.SHA256:
		return types.Item{}, k.o.ItemError(ctx, "deploy", differs(stored))
	}
	if len(props) > 0 {
		if err := svc.Annotate(ctx, c, stored, props, nil); err != nil {
			return types.Item{}, k.o.ItemError(ctx, "deploy", err)
		}
	}
	return stored, nil
}

// Upload streams body into the blob store under the request's repository's
// checksum policy and stores no item, for a protocol that learns where
// content goes only from the content, such as an npm publish, whose
// tarball may come before the fields that name its version. StoreNew then
// stores it; content no item comes to reference is collected as an orphan.
func (k *Kit) Upload(r *http.Request, body io.Reader) (blob.Info, error) {
	ctx := r.Context()
	info, err := k.o.Items().Upload(ctx, item.Deploy{
		Target: item.Target{Repo: RequestFrom(r).Repo}, Body: body,
	})
	if err != nil {
		return blob.Info{}, k.o.ItemError(ctx, "deploy", err)
	}
	return info, nil
}

// Blobs is the blob store Upload commits to, for a handler that reads
// what it uploaded before deciding where to store it.
func (k *Kit) Blobs() blob.Store { return k.o.Items().Blobs }

// StoreNew stores content Upload committed as a new file at path in the
// request's repository, with props and the checksums the client declared
// as its originals, which the caller has checked against info, and answers
// exists() when path already
// holds an item: a file published once and never replaced, such as an npm
// version's tarball (decision 0034). The caller needs Write on path, and
// never Delete. The item service checks the path inside the write's
// transaction, so only a store of the same path committing at the same
// moment can slip past it, and then on PostgreSQL alone, whose
// transactions read what others committed.
func (k *Kit) StoreNew(
	r *http.Request,
	path string,
	info blob.Info,
	props types.Properties,
	declared types.Checksums,
	exists func() error,
) (types.Item, error) {
	ctx := r.Context()
	c := RequestFrom(r).Repo
	if !can(r, c, path, types.ActionWrite) {
		return types.Item{}, refusal(r)
	}
	p, _ := auth.FromContext(ctx)
	it, err := k.o.Items().DeployCommitted(ctx, item.Deploy{
		Target: item.Target{Repo: c, Path: path}, Properties: props, By: p.Name,
		Declared: declared, IfAbsent: true,
	}, info)
	switch {
	case errors.Is(err, item.ErrExists):
		return types.Item{}, exists()
	case err != nil:
		return types.Item{}, k.o.ItemError(ctx, "deploy", err)
	}
	return it, nil
}

// Deploy answers a PUT whose body is the file to store at path, with the
// matrix parameters props and the checksums its X-Checksum headers declare
// (DeclaredChecksums), through Store: Created's answer, or the refusal.
// It reports whether the file was stored.
func (k *Kit) Deploy(
	w http.ResponseWriter,
	r *http.Request,
	path string,
	props types.Properties,
) (types.Item, bool) {
	it, err := k.Store(r, path, r.Body, props, DeclaredChecksums(r.Header))
	if err != nil {
		k.Error(w, r, err)
		return types.Item{}, false
	}
	k.Created(w, r, it)
	return it, true
}

// Created answers a deploy that stored it: 201 with its X-Checksum-Sha1,
// -Sha256 and -Md5 headers, which jf go-publish reads (research 26 section
// 3.1), and the item routes' deploy body when the server gives one
// (Options.Created).
func (k *Kit) Created(w http.ResponseWriter, r *http.Request, it types.Item) {
	SetChecksums(w.Header(), it.Actual)
	if k.o.Created != nil {
		k.o.Created(w, r, it)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
