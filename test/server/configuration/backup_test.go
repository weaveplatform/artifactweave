package configuration_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// TestAccBackupExclusionsFollowRESTRepositoryDeletion deletes an excluded
// repository through the REST API on SQLite and PostgreSQL and creates it
// again under the same key: the backup no longer excludes it, in the stored
// definition as in the XML. Before, the key stayed stored and the export hid
// it only while the repository was absent.
func TestAccBackupExclusionsFollowRESTRepositoryDeletion(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{9}, 32)))
	sqltest.RunModule(t, appsql.Modules(), func(base *dialect.Store) *appsql.Store {
		return appsql.New(base.DB(), base.Dialect(), keys)
	}, func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
		backupExclusionLifetime(t, newStore(t))
	}, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}

func backupExclusionLifetime(t *testing.T, db *appsql.Store) {
	t.Helper()
	support.Seed(t, db)
	auth, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: support.TestHash})
	storekit.Must(t, err)
	h := New(Config{CompatVersion: "7.155.0", Store: db, Auth: auth, PasswordHash: support.TestHash})
	admin := basic("admin", "pw")
	call := func(method, path, contentType, body string, want int) string {
		t.Helper()
		headers := map[string]string{"Content-Type": contentType}
		for k, v := range admin {
			headers[k] = v
		}
		rec := do(t, h, method, path, headers, body)
		if rec.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	const config = "/artifactory/api/system/configuration"
	call(http.MethodPatch, config, "application/yaml", `localRepositories:
  kept-local: {type: generic}
  dropped-local: {type: generic}
backups:
  nightly:
    cronExp: "0 0 2 ? * MON-FRI"
    excludedRepositories: [kept-local, dropped-local]
`, http.StatusOK)
	exported := func() string {
		t.Helper()
		body := call(http.MethodGet, config, "", "", http.StatusOK)
		_, backups, _ := strings.Cut(body, "<backups>")
		backups, _, _ = strings.Cut(backups, "</backups>")
		return backups
	}
	if got := exported(); !strings.Contains(got, "<repositoryRef>dropped-local</repositoryRef>") {
		t.Fatal("the exclusion was not exported", got)
	}
	call(http.MethodDelete, "/artifactory/api/repositories/dropped-local", "", "", http.StatusOK)
	call(http.MethodPut, "/artifactory/api/repositories/dropped-local", "application/json",
		`{"rclass":"local","packageType":"generic"}`, http.StatusOK)
	got := exported()
	if strings.Contains(got, "dropped-local") || !strings.Contains(got, "<repositoryRef>kept-local</repositoryRef>") {
		t.Fatal("the re-created repository came back excluded, or the other exclusion went", got)
	}
	// The stored definition, not only the export, lost the key, so config
	// apply reads the backup back unchanged without it.
	stored, err := db.Configs().Get(t.Context(), "backup", "nightly")
	storekit.Must(t, err)
	if strings.Contains(string(stored.Document), "dropped-local") {
		t.Fatal("the deleted repository's key is still stored", string(stored.Document))
	}
}
