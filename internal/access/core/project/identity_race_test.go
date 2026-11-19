package project_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	accsql "github.com/weaveplatform/artifactweave/internal/access/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/access/core/identity"
	"github.com/weaveplatform/artifactweave/internal/access/core/model"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/access/core/project"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

type identityReadGate struct {
	once    sync.Once
	typ     model.PrincipalType
	entered chan struct{}
	release chan struct{}
}

func (g *identityReadGate) wait(ctx context.Context) error {
	var err error
	g.once.Do(func() {
		close(g.entered)
		select {
		case <-g.release:
		case <-ctx.Done():
			err = ctx.Err()
		}
	})
	return err
}

type identityGateStore struct {
	identity.Store
	gate *identityReadGate
}

func (s identityGateStore) Users() port.UserStore   { return gatedUsers{s.Store.Users(), s.gate} }
func (s identityGateStore) Groups() port.GroupStore { return gatedGroups{s.Store.Groups(), s.gate} }

type gatedUsers struct {
	port.UserStore
	gate *identityReadGate
}

func (s gatedUsers) Get(ctx context.Context, name string) (model.User, error) {
	u, err := s.UserStore.Get(ctx, name)
	if err == nil && s.gate.typ == model.PrincipalUser && name == "member" {
		err = s.gate.wait(ctx)
	}
	return u, err
}

type gatedGroups struct {
	port.GroupStore
	gate *identityReadGate
}

func (s gatedGroups) Get(ctx context.Context, name string) (model.Group, error) {
	g, err := s.GroupStore.Get(ctx, name)
	if err == nil && s.gate.typ == model.PrincipalGroup && name == "member" {
		err = s.gate.wait(ctx)
	}
	return g, err
}

func TestUnitIdentityDeletionSerializesWithInFlightAssignment(t *testing.T) {
	testIdentityAssignmentRace(t, func(*testing.T) port.Store { return inmem.New() })
}

func TestAccIdentityDeletionSerializesWithInFlightAssignment(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{accsql.Module}, accsql.New,
		func(t *testing.T, newStore func(*testing.T) *accsql.Store) {
			testIdentityAssignmentRace(t, func(t *testing.T) port.Store { return newStore(t) })
		}, sqltest.WithPostgres())
}

func testIdentityAssignmentRace(t *testing.T, newStore func(*testing.T) port.Store) {
	t.Helper()
	for _, typ := range []model.PrincipalType{model.PrincipalUser, model.PrincipalGroup} {
		t.Run(string(typ), func(t *testing.T) {
			db := newStore(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			must(t, db.Projects().Create(ctx, model.Project{Key: "alpha"}))
			must(t, db.Users().Create(ctx, model.User{Name: "member"}))
			must(t, db.Groups().Create(ctx, model.Group{Name: "member"}))
			gate := &identityReadGate{typ: typ, entered: make(chan struct{}), release: make(chan struct{})}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate.release) }) }
			defer release()
			wrapped := identityGateStore{Store: lifecycleStore{db}, gate: gate}
			projects := project.Service{Store: wrapped}
			identities := identity.Service{Store: wrapped}
			results := make(chan error, 2)
			go func() {
				_, err := projects.PutMember(ctx, "alpha", typ, "member", []string{"Viewer"})
				results <- err
			}()
			select {
			case <-gate.entered:
			case <-ctx.Done():
				t.Fatal("assignment did not reach its identity read")
			}
			deleting := make(chan struct{})
			go func() {
				close(deleting)
				if typ == model.PrincipalUser {
					results <- identities.DeleteUser(ctx, "MEMBER")
				} else {
					results <- identities.DeleteGroup(ctx, "member")
				}
			}()
			<-deleting
			// The assignment already observed the old identity in a guarded
			// transaction. Deletion must run after its commit and remove the
			// assignment, rather than letting the write outlive the identity.
			release()
			for range 2 {
				select {
				case err := <-results:
					must(t, err)
				case <-ctx.Done():
					t.Fatal("assignment/deletion workers did not stop")
				}
			}
			_, err := db.Projects().Member(ctx, "alpha", typ, "member")
			if !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("assignment outlived deletion: %v", err)
			}
		})
	}
}
