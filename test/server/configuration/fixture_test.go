package configuration_test

// This file holds the configuration suites' fixtures: adapters, under the
// names the tests had in internal/server, over test/server/support/harness
// and test/server/support. Every router here is harness.Router, so a change
// to the router's assembly reaches these tests without an edit here.

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/migration/platform/settings"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

// Config is the router configuration a test adjusts before the router is
// built.
type Config = harness.Config

func do(
	t *testing.T,
	h http.Handler,
	method, path string,
	headers map[string]string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return support.Do(t, h, method, path, headers, body)
}

func basic(user, password string) map[string]string { return support.Basic(user, password) }

func jsonAs(headers map[string]string) map[string]string {
	out := map[string]string{"Content-Type": "application/json"}
	for k, v := range headers {
		out[k] = v
	}
	return out
}

// statusFixture is the production router over a seeded store whose admin
// password is "pw", with the local repository libs-local and a file blob
// store: what the root status fixture gives the tests here, which deploy,
// read storage info and create a Docker repository as the admin. tune
// adjusts the configuration before the router is built.
func statusFixture(t *testing.T, anonymous bool, tune ...harness.Tune) http.Handler {
	t.Helper()
	db := inmem.New()
	support.Seed(t, db)
	if anonymous {
		if err := db.Settings().
			Put(t.Context(), settings.SettingAnonymousAccess, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Repos().Create(t.Context(), *repo.Defaults(&repo.Config{
		Key: "libs-local", Class: repo.Local,
	})); err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(
		db.Users(),
		db.Settings(),
		authn.Options{Hash: support.TestHash},
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		CompatVersion: "7.155.0", Store: db, Blobs: blobs, Auth: a, PasswordHash: support.TestHash,
	}
	for _, f := range tune {
		f(&cfg)
	}
	return harness.Router(cfg)
}
