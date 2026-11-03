package postgres_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/sqlstore/postgres"
	"github.com/weaveplatform/artifactweave/internal/sqlstore/sqltest"
)

func TestAccIdentityPagingAtScale(t *testing.T) {
	s, err := postgres.Open(t.Context(), sqltest.PostgresDSN(t), postgres.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	sqltest.Reset(t, s.DB(), postgres.Dialect)
	sqltest.IdentityPaging(t, s)
}
