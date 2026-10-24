package oidc_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/service/oidc"
)

// patternCase is one mapping exchanged against patternClaims: the user and
// scope it issues, or the error it fails with.
type patternCase struct {
	name        string
	spec        oidc.TokenSpec
	user, scope string
	err         error
}

// patternClaims holds a claim for each example on the page ("Dynamic
// Mapping", "Advanced Dynamic Mappings" and "Add a Resource Scope to an
// Identity Mapping") and the values that try to break out of a pattern.
func patternClaims() map[string]any {
	many := make([]any, 101)
	for i := range many {
		many[i] = fmt.Sprint("g", i)
	}
	return map[string]any{
		"actor":       "octocat",
		"jfactor":     "jfuser-f12jf",
		"short":       "user",
		"mail":        "username@jfrog.com",
		"jfmail":      "jfuserid30@jfrog.com",
		"gitmail":     "gituserid30@jforg.com",
		"email":       "nick.v.goose@jfrog.com",
		"mac":         "Si-for-abced_mac",
		"data":        "team-project",
		"groups":      []any{"dev", "ops"},
		"n":           float64(7),
		"flag":        true,
		"path":        "a/b",
		"dollar":      "$1",
		"tab":         "a\tb",
		"newline":     "a\nb",
		"quote":       `a" applied-permissions/admin applied-permissions/groups:"b`,
		"object":      map[string]any{"k": "v"},
		"long":        strings.Repeat("a", 1025),
		"almost":      strings.Repeat("a", 1000),
		"many":        many,
		"jfrog_scope": "my-docker-repo",
		"full_scope":  "artifact:test-docker-local/image/1.0/**:r",
		"repo":        "jfrog-docker-prod",
		"image_path":  "docker-prod/viewer/1.2",
		"dotted":      "my.docker.repo",
		"email_path":  "team.a@company.com/docker-prod",
		"repos":       []any{"a-local", "b-local"},
		"wildcard":    "artifact:*:r",
		"glob_repo":   "artifact:docker-*/**:r",
		"system":      "system:metrics:r",
		"two_entries": "artifact:a/**:r applied-permissions/admin",
		"spaced":      "a b",
		"nested_path": "repo/path",
		"build":       "build:artifactory-build-info/**:r",
		"bad_scope":   "artifact:a/**:q",
	}
}

func runPatternCases(t *testing.T, cases []patternCase) {
	t.Helper()
	w := newWorld(t)
	ctx := context.Background()
	raw := w.mint(t, patternClaims())
	for i, c := range cases {
		name := fmt.Sprint("p", i)
		if _, err := w.svc.CreateMapping(ctx, "ci", mapping(name, c.spec, nil)); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, err := w.exchange(raw, func(r *oidc.ExchangeRequest) { r.MappingName = name })
		switch {
		case c.err != nil:
			if !errors.Is(err, c.err) {
				t.Errorf("%s: %v, want %v", c.name, err, c.err)
			}
		case err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.user != "" && got.Username != c.user:
			t.Errorf("%s: user %q, want %q", c.name, got.Username, c.user)
		case got.Record.Scope.String() != c.scope:
			t.Errorf("%s: scope %q, want %q", c.name, got.Record.Scope.String(), c.scope)
		}
	}
}

func userPattern(p string) oidc.TokenSpec { return oidc.TokenSpec{UsernamePattern: ptr(p)} }

// TestUnitPatterns runs the page's dynamic mapping examples (IDZ-61, IDZ-62):
// the template is matched against the claim, its place a capture group, and
// a claim that does not match fails the request.
func TestUnitPatterns(t *testing.T) {
	const user = "applied-permissions/user"
	runPatternCases(t, []patternCase{
		{"claim", userPattern("{{actor}}"), "octocat", user, nil},
		{"spaces", userPattern("{{ actor }}"), "octocat", user, nil},
		{"suffix", userPattern("{{actor}} | $0@Jfrog.com"), "octocat@Jfrog.com", user, nil},
		{"capture between literals", userPattern("jf{{jfactor}}jf|$1"), "user-f12", user, nil},
		{"prefix and suffix", userPattern("{{short}} | hub$0jfrog"), "hubuserjfrog", user, nil},
		{"group in the template", userPattern("({{mail}}) @jfrog.com|$1"), "username", user, nil},
		{
			"verified mail",
			userPattern("jf{{jfmail}}jfrog.com|$0"),
			"jfuserid30@jfrog.com",
			user,
			nil,
		},
		{
			"mail that does not start with jf",
			userPattern("jf{{gitmail}}jfrog.com|$0"),
			"",
			"",
			oidc.ErrNoMatch,
		},
		{"template alone verifies", userPattern("jf{{jfactor}}"), "jfuser-f12jf", user, nil},
		{"template alone refuses", userPattern("ci-{{actor}}"), "", "", oidc.ErrNoMatch},
		{
			"regex then replacement then operator",
			userPattern(`{{email}} | ^([^@]+)@jfrog\.com$ | $1_com | op:replaceAll(".","-")`),
			"nick-v-goose_com", user, nil,
		},
		{
			"operator then regex",
			userPattern(
				`{{mac}} | op:replaceAll("-","_") | ^([^_]+)_([^_]+)_([^_]+)_mac$ | $1_$2_$3_artifactory_cicd`,
			),
			"Si_for_abced_artifactory_cicd",
			user,
			nil,
		},
		{
			"operators between regex and replacement",
			userPattern(
				`{{data}} | ^([^-]+)-([^-]+)$ | op:replaceFirst("a","A") | op:replaceLast("t","T") | $1_$2`,
			),
			"teAm_projecT",
			user,
			nil,
		},
		{
			"replaceLast without a match",
			userPattern(`{{actor}} | op:replaceLast("z","Z")`),
			"octocat",
			user,
			nil,
		},
		{
			"named group",
			userPattern("{{actor}} | ^(?P<head>..)(.*)$ | ${head}-${2}"),
			"oc-tocat",
			user,
			nil,
		},
		{
			"group that took no part",
			userPattern("{{actor}} | ^(x)?(.*)$ | $1$2"),
			"octocat",
			user,
			nil,
		},
		{"escaped dollar", userPattern(`{{actor}} | \$$0`), "$octocat", user, nil},
		{"number", userPattern("{{n}}"), "7", user, nil},
		{"boolean", userPattern("{{flag}} | ci-$0"), "ci-true", user, nil},
		{
			"expression three times",
			userPattern("{{actor}} | ^(.)(.*)$ | $2$1 | $0x"),
			"ctocatox",
			user,
			nil,
		},
		{"missing claim", userPattern("{{nope}}"), "", "", oidc.ErrNoMatch},
		{"regex mismatch", userPattern("{{actor}} | ^x(.*)$ | $1"), "", "", oidc.ErrNoMatch},
		{
			"replacement mismatch",
			userPattern(`{{actor}} | ^(o)(.*)$ | op:replaceAll("o","x") | $1`),
			"",
			"",
			oidc.ErrNoMatch,
		},
		{"several names", userPattern("{{groups}}"), "", "", oidc.ErrNoMatch},
		{"empty name", userPattern("{{actor}} | ^(x)?.*$ | $1"), "", "", oidc.ErrNoMatch},
		{"object claim", userPattern("{{object}}"), "", "", oidc.ErrNoMatch},
		{"slash in a name", userPattern("{{path}}"), "", "", oidc.ErrInvalid},
		{"control character", userPattern("{{tab}}"), "", "", oidc.ErrInvalid},
		{"newline", userPattern("{{newline}}"), "", "", oidc.ErrNoMatch},
		// A claim is only ever input: a $ in it is never expanded.
		{"dollar in a claim", userPattern("{{dollar}} | x$0"), "x$1", user, nil},
		{"claim too long", userPattern("{{long}}"), "", "", oidc.ErrNoMatch},
		{
			"operator grows too long",
			userPattern(`{{almost}} | op:replaceAll("a","aa")`),
			"",
			"",
			oidc.ErrNoMatch,
		},
		{"replacement grows too long", userPattern("{{almost}} | $0$0"), "", "", oidc.ErrNoMatch},
	})
}

// TestUnitGroupPatterns: a groups pattern applies to every group the claim
// lists, and a group name a claim supplies cannot change the scope's
// meaning.
func TestUnitGroupPatterns(t *testing.T) {
	groups := func(p string) oidc.TokenSpec { return oidc.TokenSpec{GroupsPattern: ptr(p)} }
	runPatternCases(t, []patternCase{
		{
			"every group",
			groups("{{groups}} | $0-jfrog"),
			"",
			"applied-permissions/groups:dev-jfrog,ops-jfrog",
			nil,
		},
		{"empty groups", groups("{{groups}} | ^(z)?.*$ | $1"), "", "", oidc.ErrNoMatch},
		{"group regex mismatch", groups("{{groups}} | ^z$ | $0"), "", "", oidc.ErrNoMatch},
		{"missing group claim", groups("{{nope}}"), "", "", oidc.ErrNoMatch},
		{"too many groups", groups("{{many}}"), "", "", oidc.ErrNoMatch},
		{"quote in a group", groups("{{quote}}"), "", "", oidc.ErrInvalid},
		{"control character in a group", groups("{{tab}}"), "", "", oidc.ErrInvalid},
		// Each name fits, but together they exceed the scope's length limit.
		{
			"scope too long",
			groups("{{groups}} | $0" + strings.Repeat("x", 300)),
			"",
			"",
			oidc.ErrInvalid,
		},
	})
}

// TestUnitUserScope: a mapping that resolves a user name issues
// applied-permissions/user beside its other scopes, so the token also acts
// through the auto-join groups, except with a project roles scope (IDZ-59).
func TestUnitUserScope(t *testing.T) {
	const groups = "applied-permissions/groups:ci"
	runPatternCases(t, []patternCase{
		{"user only", oidc.TokenSpec{Username: ptr("u")}, "u", "applied-permissions/user", nil},
		{
			"user and groups",
			oidc.TokenSpec{Username: ptr("u"), Scope: ptr(groups)},
			"u", groups + " applied-permissions/user", nil,
		},
		{
			"user pattern and groups",
			oidc.TokenSpec{UsernamePattern: ptr("{{actor}}"), Scope: ptr(groups)},
			"octocat", groups + " applied-permissions/user", nil,
		},
		{
			"user pattern and groups pattern",
			oidc.TokenSpec{UsernamePattern: ptr("{{actor}}"), GroupsPattern: ptr("{{groups}}")},
			"octocat", "applied-permissions/groups:dev,ops applied-permissions/user", nil,
		},
		{
			"user and groups pattern",
			oidc.TokenSpec{Username: ptr("u"), GroupsPattern: ptr("{{groups}}")},
			"u", "applied-permissions/groups:dev,ops applied-permissions/user", nil,
		},
		{
			"explicit user scope",
			oidc.TokenSpec{Username: ptr("u"), Scope: ptr("applied-permissions/user " + groups)},
			"u", "applied-permissions/user " + groups, nil,
		},
		{
			"project roles",
			oidc.TokenSpec{
				UsernamePattern: ptr("{{actor}}"),
				Scope:           ptr(`applied-permissions/roles:p1:"Developer"`),
			},
			"octocat",
			"applied-permissions/roles:p1:Developer",
			nil,
		},
		{
			"admin",
			oidc.TokenSpec{Username: ptr("u"), Scope: ptr("applied-permissions/admin")},
			"u", "applied-permissions/admin", nil,
		},
		{"groups only", oidc.TokenSpec{Scope: ptr(groups)}, "oidc-ci-p8", groups, nil},
	})
}

// TestUnitResourcePatterns runs the page's Resource Scope examples (IDZ-63): the
// value becomes one artifact entry for one named repository, a bare
// repository key reading the whole repository, and anything broader or of
// another kind fails the request.
func TestUnitResourcePatterns(t *testing.T) {
	resource := func(p string) oidc.TokenSpec {
		return oidc.TokenSpec{Username: ptr("ci"), ResourcePattern: ptr(p)}
	}
	const user = " applied-permissions/user"
	runPatternCases(t, []patternCase{
		{
			"bare repository",
			resource("{{jfrog_scope}}"),
			"ci",
			"artifact:my-docker-repo/**:r" + user,
			nil,
		},
		{
			"full scope",
			resource("{{full_scope}}"),
			"ci",
			"artifact:test-docker-local/image/1.0/**:r" + user,
			nil,
		},
		{"rewrite", resource("jfrog-{{repo}}|$1"), "ci", "artifact:docker-prod/**:r" + user, nil},
		{
			"regex",
			resource("{{image_path}} | ^([^/]+)/.*$ | $1"),
			"ci",
			"artifact:docker-prod/**:r" + user,
			nil,
		},
		{
			"operator",
			resource(`{{dotted}} | op:replaceAll(".","-") | $1`),
			"ci",
			"artifact:my-docker-repo/**:r" + user,
			nil,
		},
		{
			"operator and regex",
			resource(`{{email_path}} | op:replaceAll(".","-") | ^([^@]+)@.*$ | $1`),
			"ci",
			"artifact:team-a/**:r" + user,
			nil,
		},
		{
			"list",
			resource("{{repos}}"),
			"ci",
			"artifact:a-local/**:r artifact:b-local/**:r" + user,
			nil,
		},
		{
			"groups and a synthetic subject",
			oidc.TokenSpec{
				Scope:           ptr("applied-permissions/groups:ci"),
				ResourcePattern: ptr("{{jfrog_scope}}"),
			},
			"oidc-ci-p7",
			"applied-permissions/groups:ci artifact:my-docker-repo/**:r",
			nil,
		},
		{"every repository", resource("{{wildcard}}"), "", "", oidc.ErrInvalid},
		{"repository wildcard", resource("{{glob_repo}}"), "", "", oidc.ErrInvalid},
		{"system scope", resource("{{system}}"), "", "", oidc.ErrInvalid},
		{"build scope", resource("{{build}}"), "", "", oidc.ErrInvalid},
		{"two entries", resource("{{two_entries}}"), "", "", oidc.ErrInvalid},
		{"space", resource("{{spaced}}"), "", "", oidc.ErrInvalid},
		{"path without a scope", resource("{{nested_path}}"), "", "", oidc.ErrInvalid},
		{"bad actions", resource("{{bad_scope}}"), "", "", oidc.ErrInvalid},
		{"quote", resource("{{quote}}"), "", "", oidc.ErrInvalid},
		{"missing claim", resource("{{nope}}"), "", "", oidc.ErrNoMatch},
	})
}
