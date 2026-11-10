package api_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// The expected subjects are fixed: the evaluator starts from them, so a
// change would change every permission answer.
func TestUnitSubjectOf(t *testing.T) {
	user := kernel.UserScope()
	groups := kernel.Scope{Entries: []kernel.ScopeEntry{
		{Kind: kernel.ScopeGroups, Names: []string{"release managers", "readers"}},
	}}
	cases := []struct {
		name string
		p    kernel.Principal
		want api.Subject
	}{
		{
			name: "password",
			p: kernel.Principal{
				Name: "ann", Admin: true, Groups: []string{"writers", "readers"}, Password: true,
			},
			// Groups keep their order: only a token's subject is sorted.
			want: api.Subject{Name: "ann", Admin: true, Groups: []string{"writers", "readers"}},
		},
		{
			name: "token",
			p: kernel.Principal{
				Name: "ann", Groups: []string{"writers", "readers", "writers"}, Scope: user,
				TokenID: "t1", IsTokenAuthenticated: true,
			},
			want: api.Subject{Name: "ann", Groups: []string{"readers", "writers"}, Scope: &user},
		},
		{
			name: "token with an empty scope",
			p:    kernel.Principal{Name: "ann", TokenID: "t1", IsTokenAuthenticated: true},
			want: api.Subject{Name: "ann", Scope: &kernel.Scope{}},
		},
		{
			// A groups scope acts through the groups it names, whether or
			// not the user belongs to them.
			name: "groups scope",
			p: kernel.Principal{
				Name: "ann", Groups: []string{"writers", "readers"}, Scope: groups,
				TokenID: "t2", IsTokenAuthenticated: true,
			},
			want: api.Subject{
				Name:   "ann",
				Groups: []string{"readers", "release managers", "writers"},
				Scope:  &groups,
			},
		},
		{
			// A scope that no token authenticated is not applied.
			name: "scope without a token",
			p:    kernel.Principal{Name: "ann", Groups: []string{"writers"}, Scope: groups},
			want: api.Subject{Name: "ann", Groups: []string{"writers"}},
		},
		{
			name: "anonymous",
			p:    kernel.Anonymous,
			want: api.Subject{Name: kernel.AnonymousUser, Anonymous: true},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := slices.Clone(c.p.Groups)
			got := api.SubjectOf(c.p)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("SubjectOf = %+v (scope %v), want %+v (scope %v)",
					got, got.Scope, c.want, c.want.Scope)
			}
			if len(got.Groups) > 0 {
				got.Groups[0] = "changed"
			}
			if !slices.Equal(c.p.Groups, before) {
				t.Errorf("the subject's groups alias the principal's: %q", c.p.Groups)
			}
		})
	}
}
