package configuration

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/logging"
)

type loggerStore struct {
	Store
	refuseCommit bool
	inside       bool
}

type loggerOuterStore struct {
	*inmem.Store
	failCommit bool
}

func (s *loggerOuterStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	joined := s.InTx(ctx)
	err := s.Store.Tx(ctx, fn)
	if err == nil && !joined && s.failCommit {
		return kernel.ErrStore
	}
	return err
}

type loggerAuthorityWaitKey struct{}

type loggerWaitStore struct{ *inmem.Store }

func (s loggerWaitStore) Tx(ctx context.Context, fn func(context.Context) error) error {
	if entered, ok := ctx.Value(loggerAuthorityWaitKey{}).(chan struct{}); ok && !s.InTx(ctx) {
		close(entered)
	}
	return s.Store.Tx(ctx, fn)
}

func TestUnitLoggerTransactionReadCannotDeadlockMutation(t *testing.T) {
	for _, operation := range []string{"set", "delete"} {
		t.Run(operation, func(t *testing.T) {
			st := loggerWaitStore{Store: inmem.New()}
			control := logging.New(slog.New(slog.NewJSONHandler(io.Discard, nil)))
			logger := control.Logger("artifactweave.http")
			access := accessapi.API{
				Projects: &accessapitest.Projects{GuardFunc: st.Projects().Guard},
				Authenticator: &accessapitest.Authenticator{CurrentFunc: func(_ context.Context, p kernel.Principal) (kernel.Principal, error) {
					return p, nil
				}},
				Authorizer: &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
					return &accessapitest.Decider{Admin: true}, nil
				}},
			}
			module, err := New(Deps{Store: st, Access: access, Logging: control})
			if err != nil {
				t.Fatal(err)
			}
			defer module.Close()
			ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
			classes := map[string]string{"artifactweave.http": "debug"}
			if operation == "delete" {
				if err := module.API().Loggers.Set(ctx, classes, 60); err != nil {
					t.Fatal(err)
				}
			}
			entered := make(chan struct{})
			mutationCtx, cancel := context.WithCancel(context.WithValue(ctx, loggerAuthorityWaitKey{}, entered))
			defer cancel()
			mutationDone := make(chan error, 1)
			if err := st.Tx(ctx, func(txctx context.Context) error {
				if err := st.Projects().Guard(txctx); err != nil {
					return err
				}
				go func() {
					if operation == "set" {
						mutationDone <- module.API().Loggers.Set(mutationCtx, classes, 1)
					} else {
						mutationDone <- module.API().Loggers.Delete(mutationCtx)
					}
				}()
				// The mutation owns the logger lock before entering its
				// authority Tx, whose store/project guard this caller holds.
				<-entered
				readDone := make(chan error, 1)
				go func() {
					_, err := module.API().Loggers.Level(txctx, "artifactweave.http")
					readDone <- err
				}()
				// A mutex wait is not durably blocked in synctest. Use a
				// real watchdog while channels fix the lock interleaving.
				deadline := time.NewTimer(5 * time.Second)
				var readErr error
				select {
				case readErr = <-readDone:
				case <-deadline.C:
					// Cancellation releases the old implementation's cycle,
					// so this regression fails without leaving blocked workers.
					cancel()
					readErr = <-readDone
					t.Error("transaction-scoped read blocked behind a waiting logger mutation")
				}
				deadline.Stop()
				if !errors.Is(readErr, configapi.ErrInvalidLoggerSettings) {
					t.Errorf("transaction-scoped read = %v; want standalone-only refusal", readErr)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := <-mutationDone; err != nil {
				t.Fatal("standalone mutation did not complete", err)
			}
			if logger.Enabled(ctx, slog.LevelDebug) != (operation == "set") {
				t.Error("standalone mutation did not publish its actual log decision")
			}
			deadline := time.Now().Add(5 * time.Second)
			for logger.Enabled(ctx, slog.LevelDebug) {
				if time.Now().After(deadline) {
					t.Fatal("expiration remained blocked after transaction-scoped read")
				}
				time.Sleep(10 * time.Millisecond)
			}
			module.Close()
		})
	}
}

func TestUnitLoggerMutationsRespectTransactionOutcomes(t *testing.T) {
	for _, operation := range []string{"set", "delete"} {
		for _, outcome := range []string{"rollback", "commit failure", "standalone commit failure"} {
			t.Run(operation+"/"+outcome, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					st := &loggerOuterStore{Store: inmem.New()}
					control := logging.New(slog.New(slog.NewJSONHandler(io.Discard, nil)))
					logger := control.Logger("artifactweave.http")
					access := accessapi.API{
						Projects: &accessapitest.Projects{GuardFunc: func(context.Context) error { return nil }},
						Authenticator: &accessapitest.Authenticator{CurrentFunc: func(_ context.Context, p kernel.Principal) (kernel.Principal, error) {
							return p, nil
						}},
						Authorizer: &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
							return &accessapitest.Decider{Admin: true}, nil
						}},
					}
					module, err := New(Deps{Store: st, Access: access, Logging: control})
					if err != nil {
						t.Fatal(err)
					}
					defer module.Close()
					ctx := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
					classes := map[string]string{"artifactweave.http": "debug"}
					if operation == "delete" {
						if err := module.API().Loggers.Set(ctx, classes, 1); err != nil {
							t.Fatal(err)
						}
					}
					wantDebug := operation == "delete"
					st.failCommit = outcome != "rollback"
					marker := errors.New("outer rollback")
					mutate := func(ctx context.Context) error {
						if operation == "set" {
							return module.API().Loggers.Set(ctx, classes, 1)
						}
						return module.API().Loggers.Delete(ctx)
					}
					if outcome == "standalone commit failure" {
						if err := mutate(ctx); !errors.Is(err, kernel.ErrStore) {
							t.Fatal("failed standalone authority commit was acknowledged", err)
						}
					} else {
						var mutationErr error
						outerErr := st.Tx(ctx, func(ctx context.Context) error {
							mutationErr = mutate(ctx)
							if logger.Enabled(ctx, slog.LevelDebug) != wantDebug {
								t.Error("transaction-scoped mutation changed a real logger before the outer outcome")
							}
							level, err := module.API().Loggers.Level(ctx, "artifactweave.http")
							if !errors.Is(err, configapi.ErrInvalidLoggerSettings) || level != "" {
								t.Errorf("transaction-scoped read = %q, %v; want standalone-only refusal", level, err)
							}
							if outcome == "rollback" {
								return marker
							}
							return nil
						})
						if !errors.Is(mutationErr, configapi.ErrInvalidLoggerSettings) {
							t.Errorf("transaction-scoped %s = %v; want explicit refusal", operation, mutationErr)
						}
						if outcome == "rollback" && !errors.Is(outerErr, marker) || outcome == "commit failure" && !errors.Is(outerErr, kernel.ErrStore) {
							t.Fatal("outer transaction outcome changed", outerErr)
						}
					}
					if logger.Enabled(ctx, slog.LevelDebug) != wantDebug {
						t.Error("failed enclosing transaction changed runtime levels")
					}
					time.Sleep(2 * time.Second)
					synctest.Wait()
					if logger.Enabled(ctx, slog.LevelDebug) {
						t.Fatal("refused mutation left a session or stopped the original expiry")
					}
				})
			})
		}
	}
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
