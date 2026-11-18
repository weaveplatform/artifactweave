package pkgroute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/kernel/antpath"
)

// ErrExternalAuthorityStore distinguishes a failed authority-store statement
// from actual repository replacement. Legacy adapters can retain their mapping.
var ErrExternalAuthorityStore = errors.New("external dependency authority store fault")

// ErrExternalBodyRead marks a failure reading the already-served server copy.
var ErrExternalBodyRead = errors.New("external dependency served body read fault")

// ExternalEnabled reports whether the addressed virtual opted into dependency rewriting.
func ExternalEnabled(c repo.Config) bool {
	return c.Class == repo.Virtual && c.ExternalDependenciesEnabled != nil && *c.ExternalDependenciesEnabled
}

// ExternalURL checks the fetched URL against the virtual's Ant allowlist. The
// source transport separately enforces DNS/IP and credential policy on every hop.
func ExternalURL(c repo.Config, raw string) (*url.URL, error) {
	if !ExternalEnabled(c) || len(raw) > 4096 {
		return nil, source.ErrDenied
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" ||
		strings.ContainsAny(u.EscapedPath(), "%\\\x00") {
		return nil, source.ErrDenied
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return nil, source.ErrDenied
		}
	}
	for _, pattern := range c.ExternalDependenciesPatterns {
		if pattern != "" && antpath.CompilePattern(pattern).Match(u.String()) {
			return u, nil
		}
	}
	return nil, source.ErrDenied
}

// ExternalGuard binds external reads to one virtual configuration and its direct
// cache remote. Format adapters retain ownership of naming and archive admission.
type ExternalGuard struct {
	Kit     *Kit
	Sources *source.Service
	Name    string
}
type externalAuthorityKey struct{}

// Cache selects the direct cache member with the original virtual authority.
func (g ExternalGuard) Cache(r *http.Request) (*http.Request, error) {
	v := RequestFrom(r).Repo
	if !ExternalEnabled(v) || v.ExternalDependenciesRemoteRepo == "" || !slices.Contains(v.Repositories, v.ExternalDependenciesRemoteRepo) {
		return nil, source.ErrDenied
	}
	c, err := g.Kit.DB().Repos().Get(r.Context(), v.ExternalDependenciesRemoteRepo)
	if err != nil {
		return nil, fmt.Errorf("%s dependency cache repository: %w", g.Name, err)
	}
	if c.Class != repo.Remote || c.PackageType != v.PackageType || c.AllowAnyHostAuth != nil && *c.AllowAnyHostAuth {
		return nil, source.ErrDenied
	}
	member, err := g.Kit.MemberRequest(r, c)
	if err != nil {
		return nil, fmt.Errorf("%s dependency member: %w", g.Name, err)
	}
	return member, nil
}

// CacheRead also retains the virtual read's origin restriction. Deployment-style
// MemberRequest deliberately permits upstream access; a cache read must not.
func (g ExternalGuard) CacheRead(ctx context.Context, r *http.Request) (*http.Request, error) {
	member, err := g.Cache(r)
	if err != nil {
		return nil, err
	}
	if g.Kit.o.NoUpstream != nil && g.Kit.o.NoUpstream(r, RequestFrom(r).Repo) {
		q := RequestFrom(member)
		q.CacheOnly = true
		member = member.WithContext(context.WithValue(ctx, requestKey{}, q))
	}
	return member, nil
}

// Pin captures durable incarnation/generation identity in addition to fields.
func (g ExternalGuard) Pin(r *http.Request) (*http.Request, error) {
	id, err := g.Kit.DB().RemoteCache().Source(r.Context(), RequestFrom(r).Repo.Key)
	if err != nil {
		return r, fmt.Errorf("%s virtual identity: %w", g.Name, err)
	}
	return r.WithContext(context.WithValue(r.Context(), externalAuthorityKey{}, id)), nil
}

// Check distinguishes actual deletion/replacement from an unavailable store.
func (g ExternalGuard) Check(ctx context.Context, r *http.Request) error {
	before := RequestFrom(r).Repo
	current, err := g.Kit.DB().Repos().Get(ctx, before.Key)
	if errors.Is(err, kernel.ErrNotFound) {
		return source.ErrSuperseded
	}
	if err != nil {
		return fmt.Errorf("%s virtual repository authority: %w: %w", g.Name, ErrExternalAuthorityStore, err)
	}
	id, err := g.Kit.DB().RemoteCache().Source(ctx, before.Key)
	if errors.Is(err, kernel.ErrNotFound) {
		return source.ErrSuperseded
	}
	if err != nil {
		return fmt.Errorf("%s virtual identity authority: %w: %w", g.Name, ErrExternalAuthorityStore, err)
	}
	initial, ok := r.Context().Value(externalAuthorityKey{}).(model.SourceIdentity)
	if !ok || id != initial || !reflect.DeepEqual(before, current) {
		return source.ErrSuperseded
	}
	return nil
}

// CurrentRead rechecks caller and virtual authority immediately before disclosure.
func (g ExternalGuard) CurrentRead(ctx context.Context, r *http.Request, path string, folder bool) error {
	if err := g.Check(ctx, r); err != nil {
		return err
	}
	if g.Sources == nil || g.Sources.Revalidate == nil {
		return nil
	}
	id, ok := r.Context().Value(externalAuthorityKey{}).(model.SourceIdentity)
	if !ok {
		return source.ErrSuperseded
	}
	p, _ := kernel.FromContext(ctx)
	d, _ := accessapi.DeciderFrom(ctx)
	err := g.Sources.Revalidate(
		ctx,
		source.Request{
			Repo:     RequestFrom(r).Repo,
			Subject:  accessapi.SubjectOf(p),
			Access:   d,
			Resource: source.Resource{Path: path, PermissionPath: path, PermissionFolder: folder},
		},
		source.Permit{Identity: id},
	)
	if err != nil {
		return fmt.Errorf("%s virtual current authority: %w", g.Name, err)
	}
	return nil
}

// ExternalRedirect validates the URL actually fetched and disallows HTTPS downgrades.
// check also guards the virtual's current authority at each redirect.
func ExternalRedirect(c repo.Config, start *url.URL, check func(context.Context) error) func(context.Context, *url.URL) error {
	return func(ctx context.Context, u *url.URL) error {
		if err := check(ctx); err != nil {
			return err
		}
		if start.Scheme == "https" && u.Scheme != "https" {
			return source.ErrDenied
		}
		_, err := ExternalURL(c, u.String())
		return err
	}
}

// ExternalRead contains validated bytes and their late delivery fences. It owns
// no open source body, so failed render/merge paths cannot leak one.
type ExternalRead struct {
	Data       []byte
	Revalidate func(context.Context) error
	Used       func(context.Context) error
}

// ReadExternal reads only through a cache member's source boundary. Validation
// runs on warm bytes too; remote policy, credentials and offline rules stay there.
func (k *Kit) ReadExternal(
	r, member *http.Request,
	resource source.Resource,
	download bool,
	maxBytes int64,
	invalid error,
	validate func([]byte) error,
	current func(context.Context) error,
) (ExternalRead, error) {
	read := k.ReadMetadata
	if download {
		read = k.Read
	}
	result, err := read(member, resource)
	if err != nil {
		return ExternalRead{}, err
	}
	var body io.ReadCloser
	switch {
	case result.Stream != nil:
		body = result.Stream.Body
	case result.Item != nil:
		body, _, err = k.Blobs().Open(r.Context(), result.Item.Actual.SHA256)
	default:
		return ExternalRead{}, invalid
	}
	if err != nil {
		return ExternalRead{}, fmt.Errorf("external dependency blob: %w", err)
	}
	defer body.Close()
	data, err := ReadServed(r.Context(), body, maxBytes, invalid)
	if err != nil {
		return ExternalRead{}, fmt.Errorf("external served body: %w: %w", ErrExternalBodyRead, err)
	}
	if int64(len(data)) > maxBytes {
		return ExternalRead{}, invalid
	}
	if err = validate(data); err != nil {
		return ExternalRead{}, err
	}
	check := func(ctx context.Context) error {
		if result.Revalidate != nil {
			if err := result.Revalidate(ctx); err != nil {
				return fmt.Errorf("external dependency current source: %w", err)
			}
		}
		return current(ctx)
	}
	if err = check(r.Context()); err != nil {
		return ExternalRead{}, err
	}
	return ExternalRead{Data: data, Revalidate: check, Used: result.Used}, nil
}
