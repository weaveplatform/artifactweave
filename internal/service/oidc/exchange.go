package oidc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth/idtoken"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// The RFC 8693 identifiers of the exchange.
//
//nolint:gosec // G101: token type URNs, not credentials
const (
	GrantTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange"
	SubjectIDToken     = "urn:ietf:params:oauth:token-type:id_token"
	IssuedAccessToken  = "urn:ietf:params:oauth:token-type:access_token"
)

// ExchangeRequest is a token exchange. The CI fields are recorded in the
// issued token's description for auditing.
type ExchangeRequest struct {
	GrantType, SubjectTokenType, SubjectToken string
	ProviderName, ProjectKey, MappingName     string
	IncludeReference                          bool
	JobID, RunID, Repo, Revision, Branch      string
}

// Exchanged is the token an exchange issued.
type Exchanged struct {
	token.Issued
	Username string
}

// Exchange verifies the request's ID token against its provider, finds the
// first identity mapping whose claims it satisfies, and issues the access
// token that mapping describes.
func (s Service) Exchange(ctx context.Context, r ExchangeRequest) (Exchanged, error) {
	switch {
	case r.GrantType != GrantTokenExchange:
		return Exchanged{}, invalid("grant_type must be %s", GrantTokenExchange)
	case r.SubjectTokenType != SubjectIDToken:
		return Exchanged{}, invalid("subject_token_type must be %s", SubjectIDToken)
	case r.SubjectToken == "":
		return Exchanged{}, invalid("subject_token is required")
	case r.ProviderName == "":
		return Exchanged{}, invalid("provider_name is required")
	}
	p, err := s.Provider(ctx, r.ProviderName)
	if errors.Is(err, ErrNotFound) {
		return Exchanged{}, invalid("%s", err.Error())
	}
	if err != nil {
		return Exchanged{}, err
	}
	claims, err := s.verify(ctx, p, r.SubjectToken)
	if err != nil {
		return Exchanged{}, err
	}
	candidates, err := s.candidates(ctx, p, r)
	if err != nil {
		return Exchanged{}, err
	}
	for _, m := range candidates {
		if matches(m.Claims, claims) {
			return s.issue(ctx, p, m, claims, r)
		}
	}
	return Exchanged{}, ErrNoMatch
}

// verify checks the ID token against the provider. For GitHub the issuer is
// always the provider URL, because token_issuer carries the organisation,
// and the token must come from that organisation unless the provider is
// permissive.
func (s Service) verify(ctx context.Context, p Provider, raw string) (map[string]any, error) {
	want := idtoken.Provider{
		URL: str(p.IssuerURL), Audience: str(p.Audience), UseProxy: flag(p.UseDefaultProxy),
	}
	switch p.Type() {
	case TypeGeneric:
		want.Issuer = str(p.TokenIssuer)
	case TypeAzure:
		want.Issuer, want.AppID = str(p.TokenIssuer), str(p.AzureAppID)
	}
	claims, err := s.Keys.Verify(ctx, raw, want)
	if err != nil {
		return nil, failure{kind: ErrUnverified, msg: err.Error(), cause: err}
	}
	if p.Type() == TypeGitHub && !flag(p.EnablePermissiveConfiguration) {
		owner, _ := claims["repository_owner"].(string)
		if owner != str(p.Organization) {
			return nil, failure{kind: ErrUnverified, msg: fmt.Sprintf(
				"the subject token was issued for organization %q, not %q",
				owner,
				str(p.Organization),
			)}
		}
	}
	return claims, nil
}

// candidates are the mappings to try, in order. A named mapping is the only
// candidate. Otherwise the project's mappings come first when the request
// names a project, then the global ones; within each, lower priorities
// first, a mapping without one last, and the first created among equals.
// Another project's mappings never apply.
func (s Service) candidates(ctx context.Context, p Provider, r ExchangeRequest) ([]Mapping, error) {
	all, err := s.stored(ctx, str(p.Name))
	if err != nil {
		return nil, err
	}
	if r.MappingName != "" {
		for _, m := range all {
			if str(m.Mapping.Name) == r.MappingName {
				return []Mapping{m.Mapping}, nil
			}
		}
		return nil, invalid(
			"OIDC provider '%s' has no identity mapping '%s'",
			str(p.Name),
			r.MappingName,
		)
	}
	rank := func(m stored) int {
		switch str(m.Mapping.ProjectKey) {
		case "":
			return 1
		case r.ProjectKey:
			return 0
		default:
			return 2
		}
	}
	all = slices.DeleteFunc(all, func(m stored) bool { return rank(m) == 2 })
	slices.SortStableFunc(all, func(a, b stored) int {
		return cmp.Or(
			cmp.Compare(rank(a), rank(b)),
			cmp.Compare(priority(a.Mapping), priority(b.Mapping)),
			cmp.Compare(a.Created, b.Created),
		)
	})
	out := make([]Mapping, 0, len(all))
	for _, m := range all {
		out = append(out, m.Mapping)
	}
	return out, nil
}

func priority(m Mapping) int {
	if m.Priority == nil {
		return int(^uint(0) >> 1)
	}
	return *m.Priority
}

// issue issues the token m describes for the verified claims.
func (s Service) issue(
	ctx context.Context,
	p Provider,
	m Mapping,
	claims map[string]any,
	r ExchangeRequest,
) (Exchanged, error) {
	ts := m.TokenSpec
	username, identity, err := mappedUser(p, m, claims)
	if err != nil {
		return Exchanged{}, err
	}
	sc, err := mappedScope(ts, claims)
	if err != nil {
		return Exchanged{}, err
	}
	expires := DefaultExpiry
	if ts.ExpiresIn != nil {
		expires = time.Duration(*ts.ExpiresIn) * time.Second
	}
	expires = min(expires, MaxExpiry)
	project := ""
	if str(m.ProjectKey) != "" {
		project = r.ProjectKey
	}
	issued, err := s.Tokens.Issue(ctx, token.Request{
		Username: username, Scope: sc, ExpiresIn: &expires, Audience: str(ts.Audience),
		Description: description(p, m, r), ProjectKey: project,
		IncludeReference: r.IncludeReference, Identity: identity,
	})
	if errors.Is(err, token.ErrInvalidRequest) {
		return Exchanged{}, invalid("%v", err)
	}
	if err != nil {
		return Exchanged{}, fmt.Errorf("oidc: issue token: %w", err)
	}
	return Exchanged{Issued: issued, Username: username}, nil
}

// mappedUser is the user a mapping's token acts as. Access issues it without
// checking that the user exists (a transient user); a mapping without a user
// name gets a synthetic subject named after the provider and the mapping, so
// that its token acts through the groups of its scope alone.
func mappedUser(p Provider, m Mapping, claims map[string]any) (string, types.TokenIdentity, error) {
	ts := m.TokenSpec
	switch {
	case str(ts.Username) != "":
		return *ts.Username, types.IdentityTransient, nil
	case str(ts.UsernamePattern) != "":
		pat, err := parsePattern(*ts.UsernamePattern)
		if err != nil {
			return "", "", err
		}
		names, err := pat.apply(claims)
		if err != nil {
			return "", "", err
		}
		if len(names) != 1 || names[0] == "" {
			return "", "", fmt.Errorf(
				"%w: username_pattern yields %d names",
				ErrNoMatch,
				len(names),
			)
		}
		return names[0], types.IdentityTransient, nil
	default:
		return "oidc-" + str(p.Name) + "-" + str(m.Name), types.IdentitySynthetic, nil
	}
}

// mappedScope is the scope of a mapping's token: its token_spec.scope, the
// groups its groups_pattern yields, or the user's own permissions.
func mappedScope(ts *TokenSpec, claims map[string]any) (types.Scope, error) {
	var sc types.Scope
	if str(ts.Scope) != "" {
		parsed, err := types.ParseScope(*ts.Scope)
		if err != nil {
			return types.Scope{}, invalid("token_spec.scope: %v", err)
		}
		sc = parsed
	}
	if str(ts.GroupsPattern) != "" {
		pat, err := parsePattern(*ts.GroupsPattern)
		if err != nil {
			return types.Scope{}, err
		}
		groups, err := pat.apply(claims)
		if err != nil {
			return types.Scope{}, err
		}
		groups = slices.DeleteFunc(groups, func(g string) bool { return g == "" })
		if len(groups) == 0 {
			return types.Scope{}, fmt.Errorf("%w: groups_pattern yields no group", ErrNoMatch)
		}
		sc.Entries = append(sc.Entries, types.ScopeEntry{Kind: types.ScopeGroups, Names: groups})
	}
	if len(sc.Entries) == 0 {
		sc = types.UserScope()
	}
	return sc, nil
}

// description records where the token came from, for the token list.
func description(p Provider, m Mapping, r ExchangeRequest) string {
	d := "OIDC token exchange through " + str(p.Name) + "/" + str(m.Name)
	var ci []string
	for _, f := range []struct{ name, value string }{
		{"repo", r.Repo},
		{"revision", r.Revision},
		{"branch", r.Branch},
		{"run_id", r.RunID},
		{"job_id", r.JobID},
	} {
		if f.value != "" {
			ci = append(ci, f.name+"="+f.value)
		}
	}
	if len(ci) > 0 {
		d += " (" + strings.Join(ci, ", ") + ")"
	}
	return d
}
