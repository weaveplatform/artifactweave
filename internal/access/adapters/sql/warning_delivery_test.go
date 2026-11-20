package sql_test

import (
	"context"
	"errors"
	"testing"
	"time"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/mail"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	platsql "github.com/weaveplatform/artifactweave/internal/platform/store/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/work"
)

type sqlWarningStore struct {
	*accsql.Store
	queue work.WorkQueue
}

type sqlWarningMail struct{}

func (sqlWarningMail) Server(context.Context) (configapi.MailServer, error) {
	return configapi.MailServer{Enabled: true, Host: "smtp.example.invalid", Port: 25}, nil
}

type sqlWarningSender func(context.Context, mail.Server, mail.Message, func(context.Context) error) error

func (s sqlWarningSender) Send(ctx context.Context, server mail.Server, message mail.Message, before func(context.Context) error) error {
	return s(ctx, server, message, before)
}

func TestAccProjectStorageWarningDelivery(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{platsql.Module, accsql.Module}, func(s *dialect.Store) *sqlWarningStore {
		return &sqlWarningStore{Store: accsql.New(s), queue: platsql.New(s).WorkQueue()}
	}, func(t *testing.T, newStore func(*testing.T) *sqlWarningStore) {
		for _, scenario := range []string{"retry after handler rebuild", "revoke during SMTP", "expired during SMTP"} {
			t.Run(scenario, func(t *testing.T) {
				db, ctx := newStore(t), t.Context()
				now := time.Now().UTC().Truncate(time.Microsecond)
				storageMust(t, db.Projects().Create(ctx, model.Project{Key: "alpha", StorageQuotaBytes: 100, QuotaEmail: true}))
				storageMust(t, db.Users().Create(ctx, model.User{Name: "owner", Email: "owner@example.invalid", Status: model.UserEnabled}))
				storageMust(t, db.Projects().PutMember(ctx, model.ProjectMember{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "owner", Roles: []string{"Project Admin"}}))
				service := project.Service{
					Store: db, StorageBytes: func(context.Context, string) (int64, error) { return 70, nil },
					StorageWarning: func(ctx context.Context, warning model.ProjectStorageWarning) error {
						return project.EnqueueStorageWarning(ctx, db.queue, warning, now)
					},
				}
				storageMust(t, service.ChangeStorage(ctx, "alpha", 5, nil))
				authority, err := db.Projects().Authority(ctx, "alpha")
				storageMust(t, err)
				state, initialized, err := db.Projects().Storage(ctx, "alpha")
				storageMust(t, err)
				if !initialized || state.Bytes != 75 || state.WarningKey == "" {
					t.Fatal("warning did not commit with accounting")
				}
				key := "alpha/" + string(authority.Incarnation) + "/" + state.WarningKey
				lease := func(holder string) work.WorkItem {
					t.Helper()
					item, took, err := db.queue.LeaseKey(ctx, project.StorageWarningKind, key, work.WorkLease{Holder: holder, Now: now, Until: now.Add(time.Minute)})
					storageMust(t, err)
					if !took {
						t.Fatal("committed warning was not leaseable")
					}
					return item
				}
				item := lease("first worker")
				var messages []mail.Message
				sender := sqlWarningSender(func(ctx context.Context, _ mail.Server, message mail.Message, before func(context.Context) error) error {
					if db.InTx(ctx) {
						t.Fatal("SMTP retained a SQL transaction")
					}
					if scenario == "revoke during SMTP" {
						storageMust(t, db.Projects().PutMember(ctx, model.ProjectMember{ProjectKey: "alpha", Type: model.PrincipalUser, Name: "owner", Roles: []string{"Viewer"}}))
					}
					if scenario == "expired during SMTP" {
						now = now.Add(time.Minute)
					}
					if err := before(ctx); err != nil {
						return err
					}
					messages = append(messages, message)
					return mail.ErrDelivery // receiver accepted DATA; its acknowledgement was lost
				})
				worker := func() project.WarningDelivery {
					return project.WarningDelivery{Store: db, Queue: db.queue, Mail: sqlWarningMail{}, Sender: sender, Now: func() time.Time { return now }}
				}
				err = worker().Handle(ctx, item)
				switch scenario {
				case "revoke during SMTP":
					storageMust(t, err)
					if len(messages) != 0 {
						t.Fatal("revoked administrator received DATA")
					}
				case "expired during SMTP":
					if !errors.Is(err, kernel.ErrConflict) || len(messages) != 0 {
						t.Fatal("expired lease submitted DATA", err)
					}
				default:
					if !errors.Is(err, mail.ErrDelivery) || len(messages) != 1 {
						t.Fatal("uncertain send was not retryable", err)
					}
					storageMust(t, db.queue.Fail(ctx, item.Kind, item.Key, item.Generation, work.WorkFailure{Error: "fixture acknowledgement loss", RetryAt: now, Now: now}))
					second := lease("replacement worker")
					if second.Generation == item.Generation {
						t.Fatal("replacement reused the previous generation")
					}
					if err := worker().Handle(ctx, second); !errors.Is(err, mail.ErrDelivery) {
						t.Fatal("rebuilt handler lost durable retry", err)
					}
					if len(messages) != 2 || messages[0].ID != messages[1].ID || !messages[0].Date.Equal(messages[1].Date) || messages[0].Text != messages[1].Text {
						t.Fatal("durable retry changed the warning occurrence")
					}
				}
			})
		}
	}, sqltest.WithPostgres())
}

func storageMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
