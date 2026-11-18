package maven_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func sourceLookupWorld(t *testing.T, kind repo.PackageType, backend string) *harness.RemoteWorld {
	t.Helper()
	var mutate []func(*config.Config)
	if backend == "postgres" {
		dsn := sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
		mutate = append(mutate, func(c *config.Config) { c.Database = config.Database{Driver: "postgres", DSN: dsn} })
	}
	w := harness.NewRemoteWorld(t, mutate...)
	w.Repo.PackageType = kind
	w.Repo.PriorityResolution = repo.Flex(true)
	w.Update()
	for _, c := range []repo.Config{
		{Key: "local", Class: repo.Local, PackageType: kind},
		{Key: "virtual", Class: repo.Virtual, PackageType: kind, Repositories: []string{"local", "remote"}},
	} {
		if err := w.Runtime.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func sourceLookupLogs(w *harness.RemoteWorld) *bytes.Buffer {
	logs := new(bytes.Buffer)
	w.H = harness.Router(
		harness.Config{
			Store:         w.Runtime.Store,
			Blobs:         w.Runtime.Blobs,
			Auth:          w.Runtime.Auth,
			Tokens:        w.Runtime.Tokens,
			Keys:          w.Runtime.Keys,
			RemoteSources: w.Runtime.Artifacts.RemoteSources(),
			RemoteHTTP:    w.Runtime.Artifacts.RemoteHTTP(),
			VirtualCache:  w.Runtime.Artifacts.VirtualCache(),
			Logger:        slog.New(slog.NewTextHandler(logs, nil)),
		},
	)
	return logs
}

func TestAccSourceLookupStatementFailuresAreInternal(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, kind := range []repo.PackageType{repo.Maven, repo.Generic, repo.NPM} {
				for _, table := range []string{"remote_cache", "remote_sources"} {
					t.Run(string(kind)+"-"+table, func(t *testing.T) {
						w := sourceLookupWorld(t, kind, backend)
						logs := sourceLookupLogs(w)
						putMetadata(t, w, "local", virtualMetadataPath, virtualMetadata("2.0"), 100)
						w.Upstream.Set("/"+virtualMetadataPath, fakeupstream.Resource{Body: []byte(virtualMetadata("1.0"))})
						w.Upstream.Set(
							"/-/npm/v1/keys",
							fakeupstream.Resource{Body: []byte(`{"keys":[{"keyid":"remote","key":"opaque"}]}`), Header: http.Header{"Content-Type": {"application/json"}}},
						)
						target := "/artifactory/remote/" + virtualMetadataPath
						if kind == repo.NPM {
							target = "/artifactory/api/npm/remote/-/npm/v1/keys"
						}
						if r := w.Request("GET", target, "admin", ""); r.Code != 200 {
							t.Fatalf("healthy direct %d %s", r.Code, r.Body.String())
						}
						virtualPath := "/artifactory/virtual/" + virtualMetadataPath
						if r := w.Request("GET", virtualPath, "admin", ""); r.Code != 200 {
							t.Fatalf("healthy virtual %d %s", r.Code, r.Body.String())
						}
						stateBefore, err := w.Runtime.Store.RemoteCache().State(t.Context(), "remote")
						if err != nil {
							t.Fatal(err)
						}
						requestsBefore := len(w.Upstream.Requests())
						db := w.Runtime.Store.DB()
						if _, err := db.ExecContext(t.Context(), "ALTER TABLE "+table+" RENAME TO "+table+"_held"); err != nil {
							t.Fatal(err)
						}
						direct := w.Request("GET", target, "admin", "")
						directLog := logs.String()
						virtual := w.Request("GET", virtualPath, "admin", "")
						if _, err := db.ExecContext(t.Context(), "ALTER TABLE "+table+"_held RENAME TO "+table); err != nil {
							t.Fatal(err)
						}
						t.Logf("%s %s: direct statement fault=%d; virtual metadata=%d", kind, table, direct.Code, virtual.Code)
						if virtual.Code != 500 || virtual.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
							t.Fatalf("virtual metadata hid the source lookup: %d %s", virtual.Code, virtual.Body.String())
						}
						want := `{"errors":[{"message":"internal error","status":500}]}` + "\n"
						if kind == repo.NPM {
							want = `{"error":"internal error"}` + "\n"
						}
						if direct.Code != 500 || direct.Body.String() != want {
							t.Errorf("own database lookup answered %d: %s", direct.Code, direct.Body.String())
						}
						if strings.Count(directLog, "level=ERROR") != 1 || !strings.Contains(directLog, table) ||
							(!strings.Contains(directLog, "no such table") && !strings.Contains(directLog, "SQLSTATE 42P01")) {
							t.Errorf("original lookup fault was not logged once: %s", directLog)
						}
						if len(w.Upstream.Requests()) != requestsBefore {
							t.Fatal("store failure asked the upstream", w.Upstream.Requests())
						}
						stateAfter, err := w.Runtime.Store.RemoteCache().State(t.Context(), "remote")
						if err != nil || !reflect.DeepEqual(stateBefore, stateAfter) {
							t.Fatal("lookup failure changed circuit state", stateBefore, stateAfter, err)
						}
					})
				}
			}
		})
	}
}

func TestAccSourceLookupFaultDoesNotOverrideReadOrPatternRefusals(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, kind := range []repo.PackageType{repo.Maven, repo.Generic, repo.NPM} {
				for _, table := range []string{"remote_cache", "remote_sources"} {
					t.Run(string(kind)+"-"+table, func(t *testing.T) {
						w := sourceLookupWorld(t, kind, backend)
						w.Upstream.Set("/"+virtualMetadataPath, fakeupstream.Resource{Body: []byte(virtualMetadata("1.0"))})
						w.Upstream.Set(
							"/-/npm/v1/keys",
							fakeupstream.Resource{Body: []byte(`{"keys":[{"keyid":"remote","key":"opaque"}]}`), Header: http.Header{"Content-Type": {"application/json"}}},
						)
						target := "/artifactory/remote/" + virtualMetadataPath
						if kind == repo.NPM {
							target = "/artifactory/api/npm/remote/-/npm/v1/keys"
						}
						for _, pattern := range []bool{false, true} {
							callers := []string{"", "denied"}
							if pattern {
								w.Repo.ExcludesPattern = new("**")
								w.Update()
								callers = []string{"admin"}
							}
							before := make([]*httptest.ResponseRecorder, len(callers))
							for i, caller := range callers {
								before[i] = w.Request("GET", target, caller, "")
								if before[i].Code < 400 || before[i].Code >= 500 {
									t.Fatalf("expected refused control %d %s", before[i].Code, before[i].Body.String())
								}
							}
							if _, err := w.Runtime.Store.DB().ExecContext(t.Context(), "ALTER TABLE "+table+" RENAME TO "+table+"_held"); err != nil {
								t.Fatal(err)
							}
							after := make([]*httptest.ResponseRecorder, len(callers))
							for i, caller := range callers {
								after[i] = w.Request("GET", target, caller, "")
							}
							if _, err := w.Runtime.Store.DB().ExecContext(t.Context(), "ALTER TABLE "+table+"_held RENAME TO "+table); err != nil {
								t.Fatal(err)
							}
							for i, control := range before {
								got := after[i]
								if got.Code != control.Code || got.Body.String() != control.Body.String() ||
									got.Header().Get("WWW-Authenticate") != control.Header().Get("WWW-Authenticate") {
									t.Fatalf("lookup fault overrode refusal: before%d %s after%d %s", control.Code, control.Body, got.Code, got.Body)
								}
								t.Logf("%s %s pattern=%t caller=%q refusal remains%d", kind, table, pattern, callers[i], got.Code)
							}
						}
					})
				}
			}
		})
	}
}

func TestAccSourceLookupMissingRowsKeepTheirVerdicts(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, kind := range []repo.PackageType{repo.Maven, repo.NPM} {
				for _, table := range []string{"remote_cache", "remote_sources"} {
					t.Run(string(kind)+"-"+table, func(t *testing.T) {
						w := sourceLookupWorld(t, kind, backend)
						logs := sourceLookupLogs(w)
						w.Upstream.Set("/"+virtualMetadataPath, fakeupstream.Resource{Body: []byte(virtualMetadata("1.0"))})
						w.Upstream.Set(
							"/-/npm/v1/keys",
							fakeupstream.Resource{Body: []byte(`{"keys":[{"keyid":"remote","key":"opaque"}]}`), Header: http.Header{"Content-Type": {"application/json"}}},
						)
						target := "/artifactory/remote/" + virtualMetadataPath
						if kind == repo.NPM {
							target = "/artifactory/api/npm/remote/-/npm/v1/keys"
						}
						if got := w.Request("GET", target, "admin", ""); got.Code != 200 {
							t.Fatal(got.Code, got.Body.String())
						}
						deleted, err := w.Runtime.Store.DB().ExecContext(t.Context(), "DELETE FROM "+table+" WHERE repo_key='remote'")
						if err != nil {
							t.Fatal(err)
						}
						n, err := deleted.RowsAffected()
						if err != nil || n == 0 {
							t.Fatal("no existing row removed", n, err)
						}
						got := w.Request("GET", target, "admin", "")
						want := 200
						if table == "remote_sources" {
							want = 404
						}
						if got.Code != want || strings.Contains(logs.String(), "level=ERROR") {
							t.Fatalf("missing row became fault: %d %s logs%s", got.Code, got.Body.String(), logs.String())
						}
					})
				}
			}
		})
	}
}
