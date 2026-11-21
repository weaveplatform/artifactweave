package retention_test

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	retapi "github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccCleanupExecutorNativeReference(t *testing.T) { cleanupExecutorNative(t, inmem.New()) }

func TestAccCleanupExecutorNativeSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) { cleanupExecutorNative(t, newStore(t)) },
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}

func cleanupExecutorNative(t *testing.T, db harness.Store) {
	t.Helper()
	f := newCleanupExecutorFixture(t, db)
	storekit.Must(t, db.Repos().Create(f.ctx, *repo.Defaults(&repo.Config{Key: "execution-maven", Class: repo.Local, PackageType: repo.Maven})))
	request := func(method, path, body string, want int) string {
		t.Helper()
		r := support.Do(t, f.handler, method, path, support.Basic("admin", "pw"), body)
		if r.Code != want {
			t.Fatalf("%s %s returned %d, want %d", method, path, r.Code, want)
		}
		return r.Body.String()
	}
	const base = "/artifactory/execution-maven/com/acme/app/"
	for _, version := range []string{"1.0", "2.0"} {
		request(http.MethodPut, base+version+"/app-"+version+".pom", "<project/>", http.StatusCreated)
		request(http.MethodPut, base+version+"/app-"+version+".jar", "native-"+version, http.StatusCreated)
	}
	before := request(http.MethodGet, base+"maven-metadata.xml", "", http.StatusOK)
	if !strings.Contains(before, "<version>1.0</version>") || !strings.Contains(before, "<version>2.0</version>") {
		t.Fatal("native metadata fixture did not index both versions")
	}
	p, err := f.policies.Get(f.caller, "execution-policy", false)
	storekit.Must(t, err)
	p.Criteria.PackageTypes = []string{"maven"}
	p.Criteria.Repos = []string{"execution-maven"}
	p.Criteria.KeepLastNVersions = 1
	_, err = f.policies.Update(f.caller, "execution-policy", p)
	storekit.Must(t, err)
	r, w := f.selected(t)
	storekit.Must(t, f.executor.Execute(f.ctx, w))
	finished := storekit.Get[model.CleanupRun](t)(db.Cleanup().Run(f.ctx, r.ID))
	if finished.Status != retapi.RunCompleted || finished.CleanedItems != 2 {
		t.Fatalf("native cleanup status=%s, files=%d", finished.Status, finished.CleanedItems)
	}
	request(http.MethodGet, base+"1.0/app-1.0.jar", "", http.StatusNotFound)
	if request(http.MethodGet, base+"2.0/app-2.0.jar", "", http.StatusOK) != "native-2.0" {
		t.Fatal("protected native version changed")
	}
	after := request(http.MethodGet, base+"maven-metadata.xml", "", http.StatusOK)
	if strings.Contains(after, "<version>1.0</version>") || !strings.Contains(after, "<version>2.0</version>") {
		t.Fatal("cleanup did not rebuild native version metadata")
	}
	request(http.MethodPost, "/artifactory/api/trash/restore/execution-maven/com/acme/app/1.0?to=execution-maven", "", http.StatusAccepted)
	if request(http.MethodGet, base+"1.0/app-1.0.jar", "", http.StatusOK) != "native-1.0" {
		t.Fatal("normal trash restore lost native bytes")
	}
	restored := request(http.MethodGet, base+"maven-metadata.xml", "", http.StatusOK)
	if !strings.Contains(restored, "<version>1.0</version>") || !strings.Contains(restored, "<version>2.0</version>") {
		t.Fatal("normal restore did not rebuild native version metadata")
	}
}
