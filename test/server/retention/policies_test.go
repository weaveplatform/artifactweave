package retention_test

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/retention/api"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const policies = "/artifactory/api/cleanup/packages/policies"

type policyWorld struct {
	t     *testing.T
	store harness.Store
	h     http.Handler
}

func newWorld(t *testing.T, db harness.Store) policyWorld {
	t.Helper()
	support.Seed(t, db)
	auth, err := authn.NewAuthenticator(db.Users(), db.Settings(), authn.Options{Hash: support.TestHash, Groups: db.Groups()})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cleanup-owner", "cleanup-user"} {
		if err := db.Users().Create(t.Context(), accesstest.User{Name: name, PasswordHash: support.CheapHash}); err != nil {
			t.Fatal(err)
		}
	}
	w := policyWorld{t: t, store: db, h: harness.Router(harness.Config{Store: db, Auth: auth, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})}
	for _, key := range []string{"alpha", "beta"} {
		w.request("POST", "/access/api/v1/projects", "admin", `{"project_key":"`+key+`","display_name":"`+key+`"}`, 201)
		w.request("PUT", "/artifactory/api/repositories/"+key+"-npm", "admin", `{"rclass":"local","packageType":"npm","projectKey":"`+key+`"}`, 200)
	}
	w.request("PUT", "/access/api/v1/projects/alpha/users/cleanup-owner", "admin", `{"roles":["Project Admin"]}`, 200)
	return w
}

func (w policyWorld) request(method, path, user, body string, want int) *httptest.ResponseRecorder {
	w.t.Helper()
	headers := map[string]string{"Content-Type": "application/json"}
	if user != "" {
		for key, value := range support.Basic(user, "pw") {
			headers[key] = value
		}
	}
	r := support.Do(w.t, w.h, method, path, headers, body)
	if r.Code != want {
		w.t.Fatalf("%s %s as %q: status=%d want=%d body=%s", method, path, user, r.Code, want, r.Body.String())
	}
	return r
}

func policyBody(key, project, repository string) string {
	return fmt.Sprintf(
		`{"key":%q,"projectKey":%q,"description":"old scoped packages","searchCriteria":{"packageTypes":["npm"],"repos":[%q],"includedPackages":["@scope/*"],"createdBeforeInDays":30,"includedProperties":{"clean":["yes"]}}}`,
		key,
		project,
		repository,
	)
}

func policyRoutes(t *testing.T, db harness.Store) {
	t.Helper()
	w := newWorld(t, db)
	global := policyBody("global-policy", "", "**")
	created := w.request("POST", policies+"/global-policy", "admin", global, 201)
	if !strings.Contains(created.Body.String(), `"enabled":false`) || strings.Contains(created.Body.String(), "Revision") ||
		strings.Contains(created.Body.String(), "created_before") {
		t.Fatal("create wire state differs", created.Body.String())
	}
	w.request("POST", policies+"/global-policy", "admin", global, 409)
	w.request("POST", policies+"/active-create", "admin", strings.Replace(policyBody("active-create", "", "**"), `"description":`, `"enabled":true,"description":`, 1), 400)
	w.request("GET", policies+"/active-create", "admin", "", 404)
	w.request("POST", policies+"/bad-cron", "admin", strings.Replace(policyBody("bad-cron", "", "**"), `"description":`, `"cronExp":"0 2 * * *","description":`, 1), 400)
	w.request("POST", policies+"/missing-repo", "admin", policyBody("missing-repo", "", "missing"), 400)
	for _, project := range []string{"alpha", "beta"} {
		w.request("POST", policies+"/"+project+"-policy", "admin", policyBody(project+"-policy", project, project+"-npm"), 201)
		w.request("POST", policies+"/"+project+"-policy/enablement", "admin", `{"enabled":true}`, 200)
	}
	w.request("POST", policies+"/global-policy/enablement", "admin", `{"enabled":true}`, 200)
	own := policyBody("own-policy", "alpha", "alpha-npm")
	w.request("POST", policies+"/own-policy", "cleanup-owner", own, 201)
	w.request("POST", policies+"/foreign-policy", "cleanup-owner", policyBody("foreign-policy", "beta", "beta-npm"), 403)
	w.request("POST", policies+"/wide-policy", "cleanup-owner", policyBody("wide-policy", "alpha", "beta-npm"), 403)
	w.request("GET", policies, "cleanup-owner", "", 403)
	w.request("GET", policies+"?projectKey=beta", "cleanup-owner", "", 403)
	list := w.request("GET", policies+"?projectKey=alpha", "cleanup-owner", "", 200)
	if strings.Contains(list.Body.String(), "beta-policy") || strings.Contains(list.Body.String(), "global-policy") || !strings.Contains(list.Body.String(), "own-policy") {
		t.Fatal("project list widened or lost own policy", list.Body.String())
	}
	w.request("GET", policies+"/beta-policy", "cleanup-owner", "", 403)
	w.request("PUT", policies+"/own-policy", "cleanup-owner", own, 200)
	w.request("PUT", policies+"/own-policy", "admin", policyBody("own-policy", "beta", "beta-npm"), 400)
	w.request("PUT", "/artifactory/api/cleanup/policies/forceDisableAll?projectKey=alpha", "cleanup-owner", "", 200)
	alpha, err := db.Cleanup().Policy(t.Context(), "alpha-policy")
	if err != nil || alpha.Enabled {
		t.Fatal("force disable did not disable own scope", err)
	}
	for _, key := range []string{"beta-policy", "global-policy"} {
		p, err := db.Cleanup().Policy(t.Context(), key)
		if err != nil || !p.Enabled {
			t.Fatal("force disable crossed the requested project", key, err)
		}
	}
	w.request("PUT", "/artifactory/api/cleanup/policies/forceDisableAll", "cleanup-owner", "", 403)
	w.request("PUT", "/artifactory/api/cleanup/policies/forceDisableAll", "admin", "", 200)
	_, err = db.Cleanup().CreateRun(t.Context(), model.CleanupRun{PolicyKey: "own-policy", ItemType: model.CleanupPackage, Status: api.RunCompleted})
	if err != nil {
		t.Fatal(err)
	}
	w.request("DELETE", policies+"/own-policy", "cleanup-owner", "", 200)
	if _, err := db.Cleanup().LastRun(t.Context(), "own-policy"); err != nil {
		t.Fatal("delete lost existing run history", err)
	}
	w.request("GET", policies+"/own-policy", "cleanup-owner", "", 404)
	w.request("PUT", policies+"/global-policy/run", "admin", "", 501)
	for _, user := range []string{"", "cleanup-user"} {
		want := 403
		if user == "" {
			want = 401
		}
		w.request("GET", policies+"/global-policy", user, "", want)
		w.request("POST", policies+"/denied-policy", user, policyBody("denied-policy", "", "**"), want)
		w.request("PUT", policies+"/global-policy", user, global, want)
		w.request("DELETE", policies+"/global-policy", user, "", want)
		w.request("POST", policies+"/global-policy/enablement", user, `{"enabled":true}`, want)
	}
}

func TestAccPackageCleanupPolicyRoutes(t *testing.T) { policyRoutes(t, inmem.New()) }

func TestAccPackageCleanupPolicySQLRoutes(t *testing.T) {
	keys, err := crypt.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sqltest.RunModule(t, appsql.Modules(), func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) { policyRoutes(t, newStore(t)) }, sqltest.WithPostgres(), sqltest.WithKeyring(keys))
}
