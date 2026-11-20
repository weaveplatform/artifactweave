package sql_test

import (
	"testing"

	ntfsql "github.com/weaveplatform/artifactweave/internal/notifications/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/notifications/adapters/storetest"
	"github.com/weaveplatform/artifactweave/internal/notifications/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

func TestAccWebhookSubscriptionFamiliesAndFutureCriteria(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{ntfsql.Module}, ntfsql.New,
		func(t *testing.T, newStore func(*testing.T) *ntfsql.Store) {
			storetest.RunSubscriptionFamilies(t, func(t *testing.T) port.Store { return newStore(t) })
		}, sqltest.WithPostgres())
}
