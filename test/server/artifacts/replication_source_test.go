package artifacts_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactsmodule "github.com/weaveplatform/artifactweave/internal/artifacts"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccReplicationSourceReference(t *testing.T) {
	replicationSourceContract(t, inmem.New())
}

func TestAccReplicationSourceSQL(t *testing.T) {
	keys, err := crypt.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sqltest.RunModule(t, appsql.Modules(), func(s *dialect.Store) *appsql.Store {
		return appsql.New(s.DB(), s.Dialect(), keys)
	}, func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
		replicationSourceContract(t, newStore(t))
	}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}

func replicationSourceContract(t *testing.T, db harness.Store) {
	t.Helper()
	ctx := t.Context()
	support.Seed(t, db)
	storekit.Must(t, db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "source", Class: repo.Local, PackageType: repo.Generic})))
	storekit.Must(t, db.Users().Create(ctx, accesstest.User{Name: "replica-reader", PasswordHash: support.CheapHash}))
	permission := accesstest.Permission{
		Name: "replica-read-selected", Resources: map[accessapi.ResourceType]accesstest.Resource{
			accessapi.ResourceArtifact: {
				Targets: map[string]accesstest.Target{"source": {Include: []string{"selected/**"}}},
				Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"replica-reader": accessapi.ActionRead}},
			},
		},
	}
	storekit.Must(t, db.Permissions().Create(ctx, permission))
	access := accesstest.Over(t, db).API()
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	moduleStore, ok := db.(artifactsmodule.Store)
	if !ok {
		t.Fatal("source fixture lacks the production artifacts store")
	}
	module, err := artifactsmodule.New(artifactsmodule.Deps{Store: moduleStore, Blobs: blobs, Access: access})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(module.Close)
	source := module.API().TransferSource
	routeAuth, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: support.TestHash, Groups: db.Groups()})
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{Store: db, Blobs: blobs, Auth: routeAuth})
	for _, path := range []string{"selected/a.bin", "private/secret.bin"} {
		wantStatus(t, do(t, h, http.MethodPut, "/artifactory/source/"+path, admin, "original"), http.StatusCreated)
	}
	wantStatus(t, do(t, h, http.MethodPatch, "/artifactory/api/metadata/source/selected/a.bin?recursiveProperties=0", admin,
		`{"props":{"quality":["verified"]},"stats":{"downloadCount":4}}`), http.StatusNoContent)
	p, err := access.Authenticator.Basic(ctx, "replica-reader", "pw")
	if err != nil {
		t.Fatal(err)
	}
	readCtx := kernel.WithPrincipal(ctx, p)
	repository, err := source.Repository(readCtx, "source")
	if err != nil {
		t.Fatal(err)
	}
	query := artifactsapi.TransferSelection{Root: "selected", Include: "**/*.bin", Properties: true, Statistics: true}
	rows, next, err := source.Items(readCtx, repository, query, kernel.Page{Limit: 1})
	if err != nil || len(rows) != 1 || next != "" || !slices.Equal(rows[0].Properties["quality"], []string{"verified"}) ||
		rows[0].Statistics == nil || rows[0].Statistics.DownloadCount != 4 {
		t.Fatal("current source projection disagrees with public writes", rows, next, err)
	}
	expected := rows[0]
	digest := sha256.Sum256([]byte("original"))
	if expected.Actual.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("source projection changed the actual content digest")
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	var restored artifactsapi.TransferItem
	storekit.Must(t, json.Unmarshal(encoded, &restored))
	storekit.Must(t, source.WithCurrent(readCtx, restored, func(ctx context.Context) error {
		body, err := source.Open(ctx, restored)
		if !errors.Is(err, kernel.ErrInvalid) || body != nil {
			t.Fatal("SQL checkpoint allowed content I/O in its transaction", body, err)
		}
		return nil
	}))
	denied, deniedNext, err := source.Items(readCtx, repository, artifactsapi.TransferSelection{}, kernel.Page{Limit: 100})
	if !errors.Is(err, kernel.ErrForbidden) || denied != nil || deniedNext != "" {
		t.Fatal("source page disclosed unreadable content", denied, deniedNext, err)
	}
	called := false
	if err := source.WithAbsent(readCtx, repository, "private/absent.bin", func(context.Context) error {
		called = true
		return nil
	}); !errors.Is(err, kernel.ErrForbidden) || called {
		t.Fatal("denied source path was treated as absent", called, err)
	}
	storekit.Must(t, source.WithAbsent(readCtx, repository, "selected/absent.bin", nil))
	body, err := source.Open(readCtx, expected)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	wantStatus(t, do(t, h, http.MethodPut, "/artifactory/source/selected/a.bin", admin, "replacement"), http.StatusCreated)
	content, err := io.ReadAll(body)
	if err != nil || string(content) != "original" {
		t.Fatal("source replacement changed an already-owned immutable body", string(content), err)
	}
	if err := source.WithCurrent(readCtx, expected, nil); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("obsolete content could receive a current success result", err)
	}
	storekit.Must(t, db.Permissions().Delete(ctx, permission.Name))
	if _, err := source.Repository(readCtx, "source"); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatal("source reused revoked native Read", err)
	}
	storekit.Must(t, db.Permissions().Create(ctx, permission))
	storekit.Must(t, source.WithAbsent(readCtx, repository, "selected/absent.bin", nil))
	user := storekit.Get[accesstest.User](t)(db.Users().Get(ctx, "replica-reader"))
	user.PasswordHash = accesstest.HashPassword(t, "rotated-fixture-password")
	storekit.Must(t, db.Users().Update(ctx, user))
	if _, err := source.Repository(readCtx, "source"); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Fatal("source reused a rotated password reference", err)
	}
}
