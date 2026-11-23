package ldap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

// Secret holds a manager password for the directory client. Every way of
// printing, logging or encoding it shows only that it is redacted, so
// logging a Connection or a Snapshot never leaks it; Reveal is the one way
// to the value, for the bind itself.
type Secret struct{ value string }

const redacted = "[redacted]"

// Reveal returns the password.
func (s Secret) Reveal() string { return s.value }

// IsZero reports whether no password is stored.
func (s Secret) IsZero() bool { return s.value == "" }

func (Secret) String() string { return redacted }

// GoString keeps %#v from printing the field.
func (Secret) GoString() string { return redacted }

// LogValue keeps slog from printing the field.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON keeps encoding/json from writing the value.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// MarshalText keeps text encoders from writing the value.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Connection is one connection as a directory client needs it: the
// settings, the manager password and the connection's revision. The
// revision changes on every write of the connection, its password included,
// and never repeats, so a client keyed by (Key, Revision) is never reused
// after the connection changed, was disabled, or was deleted and created
// again.
type Connection struct {
	accessapi.LDAPConnection
	ManagerPassword Secret
	Revision        string
}

// Snapshot is every connection, by key, read in one guarded transaction,
// with a revision that changes whenever any connection's does or one is
// added or removed.
type Snapshot struct {
	Revision    string
	Connections []Connection
}

// Connection returns the connection named key.
func (s Snapshot) Connection(key string) (Connection, bool) {
	for _, c := range s.Connections {
		if c.Key == key {
			return c, true
		}
	}
	return Connection{}, false
}

// Source is the revisioned settings port of the LDAP directory clients
// (LDAP-02 authentication and LDAP-03 group synchronisation): they read a
// Snapshot, keep what they built from it under its revisions, and drop
// whatever a later revision no longer names. The port lives in core so the
// directory adapter depends on it and not the reverse (decision 0044); a
// server restart starts with no cache at all.
type Source interface {
	Snapshot(ctx context.Context) (Snapshot, error)
}

var _ Source = Service{}

// errNoSnapshot refuses a Service built without the transaction and guard a
// consistent snapshot needs; no production binding does that.
var errNoSnapshot = errors.New("ldap: Snapshot needs Tx and Guard")

// Snapshot reads every connection in its own transaction under the
// administration guard, which every connection write also holds, so the
// documents and sealed passwords it returns belong to one committed state.
// Two plain reads could otherwise pair one write's settings with another's
// password on a database whose separate statements see separate commits.
func (s Service) Snapshot(ctx context.Context) (Snapshot, error) {
	if s.Tx == nil || s.Guard == nil {
		return Snapshot{}, fmt.Errorf("%w: %w", errNoSnapshot, kernel.ErrStore)
	}
	var all []stored
	err := s.Tx.Tx(ctx, func(ctx context.Context) error {
		if err := s.Guard(ctx); err != nil {
			return fmt.Errorf("ldap: guard snapshot: %w", err)
		}
		var err error
		all, err = s.all(ctx)
		return err
	})
	if err != nil {
		return Snapshot{}, err //nolint:wrapcheck // the guard and store errors name themselves
	}
	h := sha256.New()
	out := Snapshot{Connections: make([]Connection, 0, len(all))}
	for _, st := range all {
		// validate refuses control characters in keys, so the NUL
		// separators keep ("ab","c") and ("a","bc") apart.
		_, _ = h.Write([]byte(st.conn.Key + "\x00" + st.revision + "\x00"))
		out.Connections = append(out.Connections, Connection{
			LDAPConnection: st.conn, ManagerPassword: Secret{st.password}, Revision: st.revision,
		})
	}
	out.Revision = hex.EncodeToString(h.Sum(nil))
	return out, nil
}
