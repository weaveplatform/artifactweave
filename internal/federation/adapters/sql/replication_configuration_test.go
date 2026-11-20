package sql_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	fedsql "github.com/weaveplatform/artifactweave/internal/federation/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type replicationOwner struct{}

func (replicationOwner) ProjectRepository(context.Context, string) (artifactsapi.ProjectRepository, error) {
	return artifactsapi.ProjectRepository{Key: "local", Class: repo.Local, ProjectKey: "alpha"}, nil
}

type replicationManager struct{}

func (replicationManager) Any() bool                      { return true }
func (replicationManager) Platform() bool                 { return true }
func (replicationManager) Allows(string, repo.Class) bool { return true }

var errRotationWrite = errors.New("injected failure after replication credential write")

type rotationFailureStore struct{ port.Store }

func (s rotationFailureStore) Replications() port.ReplicationStore {
	return rotationFailureTargets{s.Store.Replications()}
}

type rotationFailureTargets struct{ port.ReplicationStore }

func (s rotationFailureTargets) Put(ctx context.Context, r model.Replication) error {
	if err := s.ReplicationStore.Put(ctx, r); err != nil {
		return err
	}
	return errRotationWrite
}

func TestAccReplicationAdministrationPreservesSealedCredentialAtomicity(t *testing.T) {
	sqltest.RunModule(t, []dialect.Module{fedsql.Module}, fedsql.New, func(t *testing.T, newStore func(*testing.T) *fedsql.Store) {
		db, ctx := newStore(t), t.Context()
		service := replication.Service{Store: db, Ownership: replicationOwner{}, Authorize: func(ctx context.Context) (replication.Policy, error) {
			if !db.InTx(ctx) {
				return nil, kernel.ErrStore
			}
			return replicationManager{}, nil
		}}
		url, password := "https://target.example/repository", "fake-sealed-replication-password"
		storekit.Must(t, service.Replace(ctx, "local", replication.Input{URL: &url, Password: &password}))
		rows, err := db.Replications().ForRepo(ctx, "local")
		storekit.Must(t, err)
		if len(rows) != 1 {
			t.Fatal("create did not write a single target")
		}
		first := rows[0]
		disabled := false
		storekit.Must(t, service.Update(ctx, "local", replication.Input{Enabled: &disabled}))
		current, err := db.Replications().Get(ctx, first.Key)
		storekit.Must(t, err)
		if current.Password != password || current.Revision == first.Revision || current.Enabled {
			t.Fatal("SQL partial update lost the omitted credential or current revision")
		}
		storekit.Must(t, service.Replace(ctx, "local", replication.Input{URL: &url}))
		before, err := db.Replications().Get(ctx, first.Key)
		storekit.Must(t, err)
		if before.Password != password || !before.Enabled {
			t.Fatal("SQL replacement erased omitted credential or lost defaults")
		}
		ciphertext := replicationCiphertext(t, db, first.Key)
		if bytes.Contains(ciphertext, []byte(password)) {
			t.Fatal("SQL stored a plaintext replication password")
		}
		public, err := service.Get(ctx, "local")
		storekit.Must(t, err)
		body, err := json.Marshal(public)
		storekit.Must(t, err)
		if bytes.Contains(body, []byte(password)) || bytes.Contains(body, []byte(before.Revision)) {
			t.Fatal("public view exposed a stored credential or worker revision")
		}
		service.Store = rotationFailureStore{db}
		rotated := "new-fake-password"
		if err := service.Update(ctx, "local", replication.Input{Password: &rotated, Enabled: &disabled}); !errors.Is(err, errRotationWrite) {
			t.Fatalf("post-write rotation failure = %v", err)
		}
		after, err := db.Replications().Get(ctx, first.Key)
		storekit.Must(t, err)
		if after != before || !bytes.Equal(ciphertext, replicationCiphertext(t, db, first.Key)) {
			t.Fatal("failed SQL rotation changed config, authority or sealed credential bytes")
		}
	}, sqltest.WithPostgres())
}

func replicationCiphertext(t *testing.T, db *fedsql.Store, key string) []byte {
	t.Helper()
	var value []byte
	storekit.Must(t, db.Q(t.Context()).QueryRowContext(t.Context(), db.Dialect().Rebind("SELECT password FROM replications WHERE replication_key = ?"), key).Scan(&value))
	return value
}
