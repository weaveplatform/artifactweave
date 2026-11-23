package ldap

import (
	"context"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
)

// testKey stands in for the key of a connection test whose body has none:
// JFrog's testLdapConnection takes the whole settings object, key included,
// but nothing is stored, so a body without one is still testable [I].
const testKey = "test"

// TestConnection is the connection that POST /access/api/v1/ldap/settings/test
// describes, merged onto Defaults and validated as a create would be. It
// stores nothing. A body that leaves the manager password out, for a key
// that is stored with the same URL and manager DN, takes the stored
// password, since no read ever returns it for the client to send back [I];
// any other server or DN is tested only with the password the body sends, so
// a test cannot send a stored password anywhere it was not meant for. It
// joins the transaction ctx carries, which the caller ends before the
// directory is asked.
func (s Service) TestConnection(ctx context.Context, ch accessapi.LDAPChange) (Connection, error) {
	if ch.Key == "" {
		ch.Key = testKey
	}
	password := ""
	if ch.ManagerPassword == nil {
		cur, found, err := s.load(ctx, ch.Key)
		if err != nil {
			return Connection{}, err
		}
		next := Merge(Defaults(ch.Key), ch)
		if found && cur.conn.URL == next.URL && cur.conn.ManagerDN == next.ManagerDN {
			password = cur.password
		}
	}
	conn, password, err := prepare(Defaults(ch.Key), ch, password)
	if err != nil {
		return Connection{}, err
	}
	return Connection{LDAPConnection: conn, ManagerPassword: Secret{password}}, nil
}
