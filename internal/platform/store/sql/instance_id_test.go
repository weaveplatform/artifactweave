package sql_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	platsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func TestAccInstanceIDDoesNotUnsealPrivateKey(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{platsql.Module}, func(db *dialect.Store) *dialect.Store { return db },
		func(t *testing.T, newStore func(*testing.T) *dialect.Store) {
			db := newStore(t)
			keyed := platsql.New(db)
			storekit.Must(t, keyed.Instance().Init(t.Context(), instance.InstanceIdentity{InstanceID: "persisted-public-id", PrivateKey: []byte("synthetic-private-material")}))
			unkeyed := platsql.New(dialect.New(db.DB(), db.Dialect(), nil))
			if _, err := unkeyed.Instance().Get(t.Context()); err == nil {
				t.Fatal("control: private key unexpectedly opened without keyring")
			}
			if id := storekit.Get[string](t)(instance.EnsureID(t.Context(), unkeyed)); id != "persisted-public-id" {
				t.Fatalf("public identity=%q", id)
			}
		}, sqltest.WithPostgres())
}
