package access_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/apilimit"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
)

// budgets is the request budget store of the in-memory composite that
// newItemWorld builds the router over. The router's store leaves budgets out,
// because the router never reads them: the application builds the limits.
func budgets(c *Config) apilimit.Store {
	return c.Store.(*inmem.Store) //nolint:forcetypeassert // fixture
}

func TestAccAPIRestrictionRecovery(t *testing.T) {
	var limits *apilimit.Service
	w := newItemWorld(t, false, func(c *Config) {
		var err error
		c.Auth, err = authn.NewAuthenticator(
			c.Store.Users(),
			c.Store.Settings(),
			authn.Options{Hash: testHash, APIRestrictions: c.Store.APIRestrictions()},
		)
		if err != nil {
			t.Fatal(err)
		}
		o := apilimit.Default()
		o.GlobalPerMinute, o.PrincipalPerMinute = 1, 1
		o.MaxConcurrent, o.ExpensiveConcurrent, o.ExpensivePrincipalConcurrent = 1, 1, 1
		limits, err = apilimit.New(budgets(c), o, nil)
		if err != nil {
			t.Fatal(err)
		}
		c.RequestLimits = limits
	})
	wantStatus(t, w.do("GET", "/artifactory/api/system/version", "admin", ""), 200)
	wantStatus(t, w.do("GET", "/artifactory/api/system/version", "admin", ""), 429)
	// Recovery has separate rate and concurrency capacity, including self-recovery.
	release, err := limits.Enter(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantStatus(t, w.do("PUT", apiRestrictionPath+"admin", "admin", restrictionBody()), 200)
	got := w.do("GET", apiRestrictionPath+"admin", "admin", "")
	wantStatus(t, got, 200)
	if !strings.Contains(got.Body.String(), `"active":true`) ||
		!strings.Contains(got.Header().Get("RateLimit"), `"recovery"`) {
		t.Fatalf("recovery state: %s %v", got.Body, got.Header())
	}
	wantStatus(t, w.do("DELETE", apiRestrictionPath+"admin", "reader", ""), 403)
	wantStatus(t, w.do("GET", apiRestrictionPath+"admin", "", ""), 401)
	wantStatus(t, w.do("GET", "/router/api/v1/system/liveness", "", ""), 200)
	release()
	wantStatus(t, w.do("GET", "/artifactory/api/system/version", "admin", ""), 403)
	wantStatus(t, w.do("DELETE", apiRestrictionPath+"admin", "admin", ""), 204)
	// Clearing a restriction does not silently reset the normal request quota.
	wantStatus(t, w.do("GET", "/artifactory/api/system/version", "admin", ""), 429)
	got = w.do("GET", apiRestrictionAuditPath+"?user=admin&limit=1", "admin", "")
	wantStatus(t, got, 200)
	var audit artgen.APIRestrictionAudit
	if err := json.Unmarshal(got.Body.Bytes(), &audit); err != nil {
		t.Fatal(err)
	}
	if len(audit.Changes) != 1 || audit.Changes[0].Actor != "admin" || audit.Changes[0].Active ||
		audit.Cursor == nil {
		t.Fatalf("clear audit: %+v", audit)
	}
	query := "?user=admin&limit=1&cursor=" + url.QueryEscape(*audit.Cursor)
	got = w.do("GET", apiRestrictionAuditPath+query, "admin", "")
	wantStatus(t, got, 200)
	audit = artgen.APIRestrictionAudit{}
	if err := json.Unmarshal(got.Body.Bytes(), &audit); err != nil {
		t.Fatal(err)
	}
	if len(audit.Changes) != 1 || !audit.Changes[0].Active || audit.Cursor != nil {
		t.Fatalf("prior audit: %+v", audit)
	}
	wantStatus(
		t,
		w.do(
			"GET",
			apiRestrictionAuditPath+strings.Replace(query, "user=admin", "user=reader", 1),
			"admin",
			"",
		),
		400,
	)
}

func TestAccAPIRestrictionValidationAndIsolation(t *testing.T) {
	w := newItemWorld(t, false)
	wantStatus(t, w.do("GET", apiRestrictionPath+"reader", "admin", ""), 200)
	for _, body := range []string{`{}`, `{"until":"2000-01-01T00:00:00Z","reason":"x"}`, `{"until":"2099-01-01T00:00:00Z","reason":"x"}`, strings.Replace(restrictionBody(), "Stop expensive polling", " ", 1)} {
		wantStatus(t, w.do("PUT", apiRestrictionPath+"reader", "admin", body), 400)
	}
	wantStatus(t, w.do("PUT", apiRestrictionPath+"missing", "admin", restrictionBody()), 404)
	wantStatus(t, w.do("PUT", apiRestrictionPath+"anonymous", "admin", restrictionBody()), 400)
	wantStatus(t, w.do("PUT", apiRestrictionPath+"reader", "reader", restrictionBody()), 403)
	wantStatus(t, w.do("PUT", apiRestrictionPath+"reader", "admin", restrictionBody()), 200)
	wantStatus(t, w.do("GET", "/artifactory/api/system/version", "reader", ""), 403)
	wantStatus(t, w.do("GET", "/artifactory/api/system/version", "admin", ""), 200)
	wantStatus(t, w.do("DELETE", apiRestrictionPath+"reader", "admin", ""), 204)
	wantStatus(t, w.do("GET", "/artifactory/api/system/version", "reader", ""), 200)
	for _, query := range []string{"?limit=0", "?limit=1001", "?cursor=invalid!"} {
		wantStatus(t, w.do("GET", apiRestrictionAuditPath+query, "admin", ""), 400)
	}
}

func TestAccAPIProjectQuotaUsesRepositoryOwner(t *testing.T) {
	w := newItemWorld(t, false, func(c *Config) {
		o := apilimit.Default()
		o.ProjectPerMinute = 1
		var err error
		c.RequestLimits, err = apilimit.New(budgets(c), o, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, project := range []string{"alpha", "beta"} {
			if err := c.Store.Projects().
				Create(t.Context(), accesstest.Project{Key: project}); err != nil {
				t.Fatal(err)
			}
			if err := c.Store.Repos().
				Create(t.Context(), *repo.Defaults(&repo.Config{Key: project + "-local", Class: repo.Local, ProjectKey: project})); err != nil {
				t.Fatal(err)
			}
		}
	})
	wantStatus(
		t,
		w.do("GET", "/artifactory/alpha-local/missing?project=beta", "admin", ""),
		http.StatusNotFound,
	)
	wantStatus(
		t,
		w.do("GET", "/artifactory/alpha-local/missing?project=beta", "admin", ""),
		http.StatusTooManyRequests,
	)
	wantStatus(
		t,
		w.do("GET", "/artifactory/alpha-local;x=1/missing", "admin", ""),
		http.StatusTooManyRequests,
	)
	wantStatus(t, w.do("GET", "/artifactory/beta-local/missing", "admin", ""), http.StatusNotFound)
}

func TestAccAPIRecoveryRequiresMatchingEscapedRoute(t *testing.T) {
	w := newItemWorld(t, false)
	ctx := t.Context()
	if err := w.db.Repos().
		Create(ctx, *repo.Defaults(&repo.Config{Key: "api", Class: repo.Local})); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Permissions().
		Create(ctx, accesstest.Permission{Name: "encoded-api", Resources: map[api.ResourceType]accesstest.Resource{
			api.ResourceArtifact: {
				Actions: accesstest.Grants{
					Users: map[string]api.Actions{
						"deployer": api.ActionRead | api.ActionWrite,
					},
				},
				Targets: map[string]accesstest.Target{"api": {Include: []string{"**"}}},
			},
		}}); err != nil {
		t.Fatal(err)
	}
	// Prove this encoded prefix reaches the artifact route with these grants.
	wantStatus(t, w.do("PUT", "/artifactory/%61pi/proof", "deployer", "content"), 201)
	wantStatus(t, w.do("PUT", apiRestrictionPath+"deployer", "admin", restrictionBody()), 200)
	for _, path := range []string{
		"/artifactory/%61pi/artifactweave/api-restrictions/target",
		"/artifactory/api/artifactweave/api-restrictions%2ftarget",
	} {
		got := w.do("PUT", path, "deployer", "blocked")
		wantStatus(t, got, 403)
	}
	wantStatus(t, w.do("GET", "/artifactory/%61pi/system/ping", "deployer", ""), 403)
	if _, err := w.db.Items().
		Get(ctx, "api", false, "artifactweave/api-restrictions/target"); !errors.Is(
		err,
		kernel.ErrNotFound,
	) {
		t.Fatalf("restricted upload mutated state: %v", err)
	}
	wantStatus(t, w.do("DELETE", apiRestrictionPath+"deployer", "admin", ""), 204)
}
