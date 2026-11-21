package retention_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactsmodule "github.com/weaveplatform/artifactweave/internal/artifacts"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	artifactmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccCleanupRemovalReference(t *testing.T) { cleanupRemovalContract(t, inmem.New()) }

func TestAccCleanupRemovalSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(t, appsql.Modules(), func(s *dialect.Store) *appsql.Store {
		return appsql.New(s.DB(), s.Dialect(), keys)
	}, func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
		cleanupRemovalContract(t, newStore(t))
	}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}

func cleanupRemovalContract(t *testing.T, db harness.Store) {
	t.Helper()
	ctx := t.Context()
	support.Seed(t, db)
	storekit.Must(t, db.Repos().Create(ctx, *repo.Defaults(&repo.Config{Key: "removal", Class: repo.Local, PackageType: repo.Generic})))
	storekit.Must(t, db.Users().Create(ctx, accesstest.User{Name: "cleanup-worker", PasswordHash: support.CheapHash}))
	permission := accesstest.Permission{Name: "cleanup-native-delete", Resources: map[accessapi.ResourceType]accesstest.Resource{
		accessapi.ResourceArtifact: {
			Targets: map[string]accesstest.Target{"removal": {Include: []string{"selected/**"}}},
			Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"cleanup-worker": accessapi.ActionRead | accessapi.ActionDelete}},
		},
	}}
	storekit.Must(t, db.Permissions().Create(ctx, permission))
	access := accesstest.Over(t, db).API()
	blobs := storekit.Get[*fs.Store](t)(fs.New(t.TempDir()))
	moduleStore, ok := db.(artifactsmodule.Store)
	if !ok {
		t.Fatal("cleanup fixture lacks production artifacts store")
	}
	module := storekit.Get[*artifactsmodule.Module](t)(artifactsmodule.New(artifactsmodule.Deps{Store: moduleStore, Blobs: blobs, Access: access}))
	t.Cleanup(module.Close)
	artifactAPI := module.API()
	routeAuth := storekit.Get[*authn.Authenticator](t)(authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: support.TestHash, Groups: db.Groups()}))
	h := harness.Router(harness.Config{Store: db, Blobs: blobs, Auth: routeAuth})
	request := func(method, path, body string, want int) string {
		t.Helper()
		headers := support.Basic("admin", "pw")
		headers["Content-Type"] = "application/json"
		response := support.Do(t, h, method, path, headers, body)
		if response.Code != want {
			t.Fatalf("%s %s: status=%d want=%d", method, path, response.Code, want)
		}
		return response.Body.String()
	}
	const path = "selected/one.bin"
	request(http.MethodPut, "/artifactory/removal/"+path, "original", http.StatusCreated)
	request(
		http.MethodPatch,
		"/artifactory/api/metadata/removal/"+path+"?recursiveProperties=0",
		`{"props":{"quality":["eligible"]},"stats":{"downloadCount":4}}`,
		http.StatusNoContent,
	)
	it := storekit.Get[artifactmodel.Item](t)(db.Items().Get(ctx, "removal", false, path))
	storekit.Must(t, pkgindex.Link(ctx, db, pkgindex.Version{RepoKey: "removal", PackageType: repo.Generic, Package: "package", Version: "1"}, it.ID, time.Now()))
	p := storekit.Get[artifactmodel.Package](t)(db.Packages().Find(ctx, "removal", "package"))
	v := storekit.Get[artifactmodel.PackageVersion](t)(db.Packages().Version(ctx, p.ID, "1"))
	_, err := db.Packages().PutVersion(ctx, artifactmodel.PackageVersion{PackageID: p.ID, Version: "2"})
	storekit.Must(t, err)
	r := storekit.Get[artifactsapi.IndexRepository](t)(db.Repos().(port.RepositoryIndexPages).IndexRepository(ctx, "removal"))
	ref := artifactsapi.IndexVersion{Package: artifactsapi.IndexPackage{Repository: r, ID: p.ID, Name: p.Name}, ID: v.ID, Version: v.Version}
	snapshot := func() artifactsapi.IndexFile {
		t.Helper()
		var rows []artifactsapi.IndexFile
		storekit.Must(t, db.Tx(ctx, func(ctx context.Context) error {
			if err := access.Projects.Guard(ctx); err != nil {
				return err
			}
			var err error
			rows, _, err = artifactAPI.PackageIndex.Files(ctx, ref, map[string][]string{"quality": {"eligible"}}, kernel.Page{Limit: 1})
			return err
		}))
		if len(rows) != 1 {
			t.Fatal("cleanup snapshot missing")
		}
		return rows[0]
	}
	principal := storekit.Get[kernel.Principal](t)(access.Authenticator.Basic(ctx, "cleanup-worker", "pw"))
	selected := artifactsapi.RemovalRequest{Credential: principal.CredentialReference(), Item: snapshot(), PropertyNames: []string{"quality"}}
	revalidate := func(ctx context.Context) error {
		current, err := artifactAPI.PackageIndex.Version(ctx, ref, 0)
		if err != nil {
			return err
		}
		if current.Protected {
			return kernel.ErrConflict
		}
		return nil
	}
	record := func(ctx context.Context, result artifactsapi.RemovalResult) error {
		if result.Files != 1 || len(result.Items) != 1 || result.Items[0].ID != it.ID {
			return fmt.Errorf("incorrect removal effects: %w", kernel.ErrStore)
		}
		if err := db.Settings().Put(ctx, "cleanup-removal-ack", result); err != nil {
			return fmt.Errorf("persist cleanup acknowledgement: %w", err)
		}
		return nil
	}
	errInjected := errors.New("cleanup acknowledgement fault")
	result, err := artifactAPI.ItemRemoval.Remove(ctx, selected, revalidate, func(ctx context.Context, result artifactsapi.RemovalResult) error {
		if err := record(ctx, result); err != nil {
			return err
		}
		return errInjected
	})
	storekit.WantErr(t, err, errInjected)
	if result.Files != 0 || len(result.Items) != 0 {
		t.Fatal("rolled back cleanup returned success")
	}
	if request(http.MethodGet, "/artifactory/removal/"+path, "", http.StatusOK) != "original" {
		t.Fatal("rolled back cleanup changed publicly downloadable content")
	}
	trash := storekit.Get[[]artifactmodel.TrashEntry](t)(db.Trash().Find(ctx, "removal", path))
	if len(trash) != 0 {
		t.Fatal("failed progress leaked trash")
	}
	var ack artifactsapi.RemovalResult
	storekit.WantErr(t, db.Settings().Get(ctx, "cleanup-removal-ack", &ack), kernel.ErrNotFound)
	request(http.MethodPut, "/artifactory/removal/"+path, "replacement", http.StatusCreated)
	_, err = artifactAPI.ItemRemoval.Remove(ctx, selected, revalidate, record)
	storekit.WantErr(t, err, kernel.ErrConflict)
	selected.Item = snapshot()
	if selected.Item.ID != it.ID || !selected.Item.Created.Equal(it.Created) {
		t.Fatal("overwrite fixture did not preserve item identity")
	}
	storekit.Must(t, db.Permissions().Delete(ctx, permission.Name))
	_, err = artifactAPI.ItemRemoval.Remove(ctx, selected, revalidate, record)
	storekit.WantErr(t, err, kernel.ErrForbidden)
	storekit.Must(t, db.Permissions().Create(ctx, permission))
	user := storekit.Get[accesstest.User](t)(db.Users().Get(ctx, "cleanup-worker"))
	user.PasswordHash = accesstest.HashPassword(t, "rotated-fixture-password")
	storekit.Must(t, db.Users().Update(ctx, user))
	_, err = artifactAPI.ItemRemoval.Remove(ctx, selected, revalidate, record)
	storekit.WantErr(t, err, kernel.ErrBadCredentials)
	principal = storekit.Get[kernel.Principal](t)(access.Authenticator.Basic(ctx, "cleanup-worker", "rotated-fixture-password"))
	selected.Credential = principal.CredentialReference()
	result = storekit.Get[artifactsapi.RemovalResult](t)(artifactAPI.ItemRemoval.Remove(ctx, selected, revalidate, record))
	if result.Bytes != int64(len("replacement")) {
		t.Fatal("actual cleanup bytes came from obsolete selection")
	}
	storekit.Must(t, db.Settings().Get(ctx, "cleanup-removal-ack", &ack))
	storekit.Same(t, "committed cleanup acknowledgement", result, ack)
	request(http.MethodGet, "/artifactory/removal/"+path, "", http.StatusNotFound)
	trash = storekit.Get[[]artifactmodel.TrashEntry](t)(db.Trash().Find(ctx, "removal", path))
	if len(trash) != 1 || trash[0].Record.Item.Actual != selected.Item.Actual || trash[0].DeletedBy != "cleanup-worker" {
		t.Fatal("normal cleanup trash did not retain actual content and actor")
	}
}
