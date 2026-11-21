package retention_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccCleanupReportProjectHistoryReference(t *testing.T) {
	cleanupReportProjectHistory(t, inmem.New())
}

func TestAccCleanupReportProjectHistorySQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t, appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			cleanupReportProjectHistory(t, newStore(t))
		},
		sqltest.WithPostgres(), sqltest.WithKeyring(keys),
	)
}

func cleanupReportProjectHistory(t *testing.T, db harness.Store) {
	t.Helper()
	f := newCleanupExecutorFixture(t, db)
	w := policyWorld{t: t, store: db, h: f.handler}
	for _, project := range []string{"alpha", "beta"} {
		user := "report-" + project
		storekit.Must(t, db.Users().Create(f.ctx, accesstest.User{Name: user, PasswordHash: support.CheapHash}))
		w.request(http.MethodPost, "/access/api/v1/projects", "admin", `{"project_key":"`+project+`","display_name":"Original `+project+`"}`, http.StatusCreated)
		w.request(http.MethodPut, "/artifactory/api/repositories/"+project+"-npm", "admin", `{"rclass":"local","packageType":"npm","projectKey":"`+project+`"}`, http.StatusOK)
		w.request(http.MethodPut, "/access/api/v1/projects/"+project+"/users/"+user, "admin", `{"roles":["Project Admin"]}`, http.StatusOK)
	}
	const key = "retained-scope"
	w.request(http.MethodPost, policies+"/"+key, "report-alpha", policyBody(key, "alpha", "alpha-npm"), http.StatusCreated)
	run := cleanupPolicyReportRun(t, f, key, true)
	id := strconv.FormatInt(run.ID, 10)
	exact := cleanupReports + key + "/" + id
	summary := cleanupReports + "summaryReports?policyKey=" + key + "&triggerId=" + id
	download := cleanupReports + "downloadReport?policyKey=" + key + "&runId=" + id
	w.request(http.MethodGet, exact, "report-alpha", "", http.StatusOK)
	w.request(http.MethodGet, summary, "report-alpha", "", http.StatusOK)
	w.request(http.MethodGet, download, "report-alpha", "", http.StatusNotFound)
	for _, path := range []string{exact, summary, download, cleanupReports + "view?projectKey=alpha", "/artifactory/api/cleanup/policies/view?itemTypes=package&projectKey=alpha"} {
		w.request(http.MethodGet, path, "report-beta", "", http.StatusForbidden)
	}
	page := func(project, user, extra string, want int64) {
		t.Helper()
		r := w.request(http.MethodGet, cleanupReports+"view?projectKey="+project+"&policyKey="+key+"&pageSize=1"+extra, user, "", http.StatusOK)
		var view artgen.CleanupPolicyRunView
		storekit.Must(t, json.Unmarshal(r.Body.Bytes(), &view))
		if view.Pagination == nil || httpkit.Deref(view.Pagination.Total) != want || view.RunViews == nil || int64(len(*view.RunViews)) != want {
			t.Fatal("history visibility was applied after count/page", want)
		}
	}
	page("alpha", "report-alpha", "", 1)
	page("alpha", "report-alpha", "&projectKeyList=beta", 0)
	w.request(http.MethodDelete, policies+"/"+key, "report-alpha", "", http.StatusOK)
	w.request(http.MethodPost, policies+"/"+key, "report-beta", policyBody(key, "beta", "beta-npm"), http.StatusCreated)
	w.request(http.MethodGet, summary, "report-alpha", "", http.StatusOK)
	w.request(http.MethodGet, exact, "report-beta", "", http.StatusForbidden)
	page("alpha", "report-alpha", "", 1)
	page("beta", "report-beta", "", 0)
	response := w.request(http.MethodGet, "/artifactory/api/cleanup/policies/view?itemTypes=package&projectKey=beta&policyKey="+key, "report-beta", "", http.StatusOK)
	var overview artgen.CleanupPolicyView
	storekit.Must(t, json.Unmarshal(response.Body.Bytes(), &overview))
	if overview.Policies == nil || len(*overview.Policies) != 1 || (*overview.Policies)[0].LastRunData != nil {
		t.Fatal("reused policy inherited another project's last run")
	}
	w.request(http.MethodDelete, "/artifactory/api/repositories/alpha-npm", "admin", "", http.StatusOK)
	w.request(http.MethodDelete, "/access/api/v1/projects/alpha", "admin", "", http.StatusNoContent)
	// Project deletion deliberately retains detached repositories. Remove
	// this fixture's old build-info namespace before creating a new lifetime.
	w.request(http.MethodDelete, "/artifactory/api/repositories/alpha-build-info", "admin", "", http.StatusOK)
	w.request(http.MethodPost, "/access/api/v1/projects", "admin", `{"project_key":"alpha","display_name":"Recreated project"}`, http.StatusCreated)
	w.request(http.MethodPut, "/access/api/v1/projects/alpha/users/report-alpha", "admin", `{"roles":["Project Admin"]}`, http.StatusOK)
	w.request(http.MethodGet, exact, "report-alpha", "", http.StatusForbidden)
	page("alpha", "report-alpha", "", 0)
	page("alpha", "admin", "", 1)
	response = w.request(http.MethodGet, exact, "admin", "", http.StatusOK)
	var history artgen.CleanupPolicyRunViewEntry
	storekit.Must(t, json.Unmarshal(response.Body.Bytes(), &history))
	if httpkit.Deref(history.DisplayProject) != "alpha" {
		t.Fatal("old run was attributed to the recreated project's display name")
	}
}
