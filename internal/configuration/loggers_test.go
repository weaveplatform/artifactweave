package configuration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/logging"
)

type loggerStore struct {
	Store
	refuseCommit bool
	inside       bool
}

func (s *loggerStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	s.inside = true
	defer func() { s.inside = false }()
	err := s.Store.Tx(ctx, fn)
	if err == nil && s.refuseCommit {
		return kernel.ErrStore
	}
	return err
}

func TestUnitLoggerPublicationRechecksAuthorityAndCommit(t *testing.T) {
	for _, stage := range []string{"allowed", "guard", "credential", "missing credential", "permissions", "demoted", "nil permissions", "commit"} {
		t.Run(stage, func(t *testing.T) {
			st := &loggerStore{Store: inmem.New(), refuseCommit: stage == "commit"}
			control := logging.New(slog.New(slog.NewJSONHandler(io.Discard, nil)))
			logger := control.Logger("artifactweave.http")
			var order []string
			record := func(label string) {
				t.Helper()
				if !st.inside {
					t.Fatal("authority checked outside transaction")
				}
				order = append(order, label)
			}
			access := accessapi.API{
				Projects: &accessapitest.Projects{GuardFunc: func(context.Context) error {
					record("guard")
					if stage == "guard" {
						return kernel.ErrStore
					}
					return nil
				}},
				Authenticator: &accessapitest.Authenticator{CurrentFunc: func(_ context.Context, p kernel.Principal) (kernel.Principal, error) {
					record("credential")
					if stage == "credential" {
						return kernel.Principal{}, kernel.ErrBadCredentials
					}
					if stage == "missing credential" {
						return kernel.Principal{}, kernel.ErrNotFound
					}
					return p, nil
				}},
				Authorizer: &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
					record("permissions")
					if stage == "permissions" {
						return nil, kernel.ErrStore
					}
					if stage == "nil permissions" {
						return nil, nil
					}
					return &accessapitest.Decider{Admin: stage != "demoted"}, nil
				}},
			}
			m, err := New(Deps{Store: st, Access: access, Logging: control})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
			err = m.API().Loggers.Set(ctx, map[string]string{"artifactweave.http": "debug"}, 60)
			if stage == "allowed" {
				if err != nil || !logger.Enabled(ctx, slog.LevelDebug) || !slices.Equal(order, []string{"guard", "credential", "permissions"}) {
					t.Fatal("guarded publication failed", order, err)
				}
				m.Close()
				if logger.Enabled(ctx, slog.LevelDebug) {
					t.Fatal("module Close retained logger override")
				}
			} else {
				if err == nil || logger.Enabled(ctx, slog.LevelDebug) {
					t.Fatal("refused publication changed actual logger", stage, err)
				}
				if stage == "missing credential" && !errors.Is(err, kernel.ErrBadCredentials) {
					t.Fatal("missing credential lost classification", err)
				}
			}
		})
	}
}

func TestUnitLoggerPublicationRequiresCallerAndBindings(t *testing.T) {
	m, err := New(Deps{Store: inmem.New(), Logging: logging.New(nil)})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, ctx := range []context.Context{t.Context(), kernel.WithPrincipal(t.Context(), kernel.Anonymous), kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "user"})} {
		if err := m.API().Loggers.Delete(ctx); err == nil {
			t.Fatal("unprivileged caller admitted")
		}
	}
	ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
	if err := m.API().Loggers.Delete(ctx); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("missing access binding admitted", err)
	}
	noMechanism, err := New(Deps{Store: inmem.New()})
	if err != nil {
		t.Fatal(err)
	}
	defer noMechanism.Close()
	if noMechanism.API().Loggers != nil {
		t.Fatal("logger API bound without actual app logging mechanism")
	}
}
