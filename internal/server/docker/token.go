package docker

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The actions of the Docker token specification, and the scope of the
// catalog.
const (
	actionPull   = "pull"
	actionPush   = "push"
	actionDelete = "delete"
	actionAll    = "*"

	scopeRepository = "repository"
	scopeRegistry   = "registry"
	catalogScope    = "registry:catalog:*"
)

// TokenIssuer signs registry tokens; internal/auth/token implements it.
type TokenIssuer interface {
	IssueRegistry(ctx context.Context, r token.RegistryRequest) (token.Issued, error)
}

// registerToken registers the token endpoint, GET on the realm
// (decision 0005). Without an issuer the endpoint stays unimplemented.
func (g *Registry) registerToken() {
	if g.tokens != nil {
		g.handle(KindToken, http.MethodGet, g.serveToken)
	}
}

// tokenResponse is the token endpoint's body: Docker's token
// specification, with token and access_token equal for clients that read
// either (research 17).
type tokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	IssuedAt    string `json:"issued_at"`
}

// serveToken issues a registry token to the caller for the scopes it asks
// for, each with only the actions the caller's permissions allow; scopes
// it may not use are left out rather than refused, as Docker's
// specification has it, so the registry then answers the request that
// needed them. account, client_id, offline_token and service are ignored.
func (g *Registry) serveToken(w http.ResponseWriter, r *http.Request, q Request) {
	p, ok := auth.FromContext(r.Context())
	if !ok {
		// Only a server built without authentication gets here.
		Write(w, Errorf(http.StatusUnauthorized, CodeUnauthorized, "authentication required"))
		return
	}
	realm := q.RepoKey
	if strings.EqualFold(realm, nullRealm) {
		realm = ""
	}
	access := types.RegistryAccess{Realm: realm}
	for _, raw := range r.URL.Query()["scope"] {
		for _, s := range strings.Fields(raw) {
			grant, err := g.grant(r.Context(), p, realm, s)
			if err != nil {
				g.writeErr(w, r, err)
				return
			}
			if len(grant.Actions) > 0 {
				access.Grants = append(access.Grants, grant)
			}
		}
	}
	username := p.Name
	if p.Anonymous {
		username = auth.AnonymousUser
	}
	issued, err := g.tokens.IssueRegistry(r.Context(), token.RegistryRequest{
		Username: username, ExpiresIn: lifetime(p, time.Now()), Access: access,
	})
	if err != nil {
		g.writeErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	//nolint:gosec // G117: issuing the token to its holder is the endpoint's purpose
	_ = json.NewEncoder(w).Encode(tokenResponse{
		Token: issued.AccessToken, AccessToken: issued.AccessToken, ExpiresIn: issued.ExpiresIn,
		IssuedAt: issued.Record.IssuedAt.UTC().Format(time.RFC3339),
	})
}

// lifetime is a registry token's: an hour, or what remains of the token
// that obtained it when that is less, so a token never outlives the
// credential behind it. A token that obtained one has a second left at
// least, as it verified.
func lifetime(p auth.Principal, now time.Time) time.Duration {
	d := types.RegistryTokenExpiry
	if !p.TokenExpires.IsZero() {
		d = min(d, p.TokenExpires.Sub(now).Truncate(time.Second))
	}
	return max(d, time.Second)
}

// grant answers one requested scope: type:name:actions, the name holding
// no colon but the type maybe one, as repository(plugin) does. What the
// caller may not do is dropped; an unknown type or a malformed scope
// grants nothing.
func (g *Registry) grant(
	ctx context.Context,
	p auth.Principal,
	realm, scope string,
) (types.RegistryGrant, error) {
	rest, acts, ok := cutLast(scope, ":")
	typ, name, ok2 := cutLast(rest, ":")
	if !ok || !ok2 || name == "" {
		return types.RegistryGrant{}, nil
	}
	want := requested(acts)
	var allowed []string
	switch {
	case typ == scopeRegistry && name == "catalog":
		// The catalog lists only what the caller may read, so the scope
		// itself withholds nothing.
		allowed = []string{actionAll}
		want = allowed
	case typ == scopeRepository:
		var err error
		if allowed, err = g.allowedOn(ctx, p, realm, name); err != nil {
			return types.RegistryGrant{}, err
		}
	}
	got := types.RegistryGrant{Type: typ, Name: name}
	for _, a := range want {
		if slices.Contains(allowed, a) && heldBy(p.Registry, realm, typ, name, a) {
			got.Actions = append(got.Actions, a)
		}
	}
	return got, nil
}

// requested expands a scope's actions: * is pull, push and delete. Unknown
// actions are dropped and each is kept once, in the specification's order.
func requested(acts string) []string {
	var out []string
	for a := range strings.SplitSeq(acts, ",") {
		if a == actionAll {
			return []string{actionPull, actionPush, actionDelete}
		}
		if slices.Contains([]string{actionPull, actionPush, actionDelete}, a) &&
			!slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// allowedOn is what the caller may do with the image a scope's name
// names: pull with Read, push with Deploy, delete with Delete on the
// image's folder (research 23 section 4). Under a realm the name may be
// the image alone (the Artifactory prefix, a subdomain) or begin with the
// realm's key (the repository path, whose challenge names the realm too),
// so both readings count; the registry checks the caller's permissions on
// the image it actually addresses on every request, whichever it was.
func (g *Registry) allowedOn(
	ctx context.Context,
	p auth.Principal,
	realm, name string,
) ([]string, error) {
	type target struct{ key, image string }
	var targets []target
	if key, image, ok := strings.Cut(name, "/"); realm == "" && ok {
		targets = append(targets, target{key, image})
	} else if realm != "" {
		targets = append(targets, target{realm, name})
		if ok && strings.EqualFold(key, realm) {
			targets = append(targets, target{realm, image})
		}
	}
	var allowed []string
	for _, t := range targets {
		if !ValidName(t.image) {
			continue
		}
		c, found, err := g.lookup(ctx, t.key)
		if err != nil {
			return nil, err
		}
		if !found || p.Access() == nil {
			continue
		}
		repo := authz.Repo{Key: c.Key, Class: string(c.Class)}
		for a, need := range map[string]types.Actions{
			actionPull: types.ActionRead, actionPush: types.ActionWrite,
			actionDelete: types.ActionDelete,
		} {
			if p.Access().AllowsFolder(types.ResourceArtifact, repo, t.image, need) &&
				!slices.Contains(allowed, a) {
				allowed = append(allowed, a)
			}
		}
	}
	return allowed, nil
}

// heldBy reports whether a registry token that obtains another holds the
// action itself, so that a token never mints a broader one; any other
// credential holds every action its permissions allow.
func heldBy(have *types.RegistryAccess, realm, typ, name, action string) bool {
	if have == nil {
		return true
	}
	if !strings.EqualFold(have.Realm, realm) {
		return false
	}
	return slices.ContainsFunc(have.Grants, func(g types.RegistryGrant) bool {
		return g.Type == typ && strings.EqualFold(g.Name, name) &&
			(slices.Contains(g.Actions, action) || slices.Contains(g.Actions, actionAll))
	})
}

// cutLast is strings.Cut at the last sep.
func cutLast(s, sep string) (before, after string, ok bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}
