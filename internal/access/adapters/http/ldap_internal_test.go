package accesshttp

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/ldap"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	accessgen "github.com/weaveplatform/artifactweave/internal/platform/gen/access"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// testingDirectory answers Test with err.
type testingDirectory struct {
	ldap.Directory
	err error
}

func (d testingDirectory) Test(context.Context, ldap.Connection) error { return d.err }

// TestUnitLDAPConnectionTestAnswers covers what the router's real client
// does not reach: no client at all, and a failure that names no LDAP step.
func TestUnitLDAPConnectionTestAnswers(t *testing.T) {
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
	key, url, pattern := "corp", "ldap://ldap.example.com", "uid={0}"
	req := accessgen.TestLdapConnectionRequestObject{Body: &accessgen.LdapSettings{
		Key: &key, LdapUrl: &url, UserDnPattern: &pattern,
	}}
	status := func(err error) int {
		var e *apierror.Error
		if !errors.As(err, &e) {
			return 0
		}
		return e.Status
	}
	if _, err := (AccessService{}).TestLdapConnection(ctx, req); status(err) != http.StatusNotImplemented {
		t.Fatalf("without a directory client: %v", err)
	}
	for _, tc := range []struct {
		err  error
		want int
	}{
		{&ldap.DirectoryError{Op: "connect", Reason: "refused"}, http.StatusBadRequest},
		{errors.New("disk on fire"), http.StatusInternalServerError},
	} {
		s := AccessService{accessState{deps: Deps{Store: inmem.New(), LDAP: testingDirectory{err: tc.err}}}}
		if _, err := s.TestLdapConnection(ctx, req); status(err) != tc.want {
			t.Fatalf("Test answering %v: %v, want %d", tc.err, err, tc.want)
		}
	}
	s := AccessService{accessState{deps: Deps{Store: inmem.New(), LDAP: testingDirectory{}}}}
	if got, err := s.TestLdapConnection(ctx, req); err != nil || got != (accessgen.TestLdapConnection204Response{}) {
		t.Fatalf("a directory that answered: %v, %v", got, err)
	}
}
