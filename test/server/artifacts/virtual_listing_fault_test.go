package artifacts_test

import (
	"bytes"
	"context"
	"log/slog"
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

// A real source-authority statement failure must not be treated as an
// ordinary origin outage while composing cached children (#1723).
func TestAccVirtualFolderSourceLookupFault(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, kind := range []repo.PackageType{repo.Generic, repo.Maven} {
				t.Run(string(kind), func(t *testing.T) {
					var mutate []func(*config.Config)
					if backend == "postgres" {
						dsn := sqltest.ScratchDSN(t, sqltest.PostgresDSN(t))
						mutate = append(mutate, func(c *config.Config) { c.Database = config.Database{Driver: "postgres", DSN: dsn} })
					}
					w := harness.NewRemoteWorld(t, mutate...)
					w.Repo.PackageType = kind
					w.Repo.ListRemoteFolderItems = repo.Flex(true)
					w.Update()
					var logs bytes.Buffer
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
							Logger:        slog.New(slog.NewTextHandler(&logs, nil)),
						},
					)
					w.Upstream.Set("/dir/kept.bin", fakeupstream.Resource{Body: []byte("cached file")})
					if rec := w.Request("GET", "/artifactory/remote/dir/kept.bin", "admin", ""); rec.Code != 200 {
						t.Fatalf("seed cache:%d %s", rec.Code, rec.Body)
					}
					w.Upstream.Set("/dir/", upstreamIndex("live.bin"))
					c := *repo.Defaults(&repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: kind, Repositories: []string{"remote"}})
					if err := w.Runtime.Store.Repos().Create(t.Context(), c); err != nil {
						t.Fatal(err)
					}
					for _, key := range []string{"remote", "virtual"} {
						if r := w.Request("GET", "/artifactory/"+key+"/dir/", "admin", ""); r.Code != 200 {
							t.Fatalf("healthy %s:%d %s", key, r.Code, r.Body)
						}
					}
					stateBefore, err := w.Runtime.Store.RemoteCache().State(t.Context(), "remote")
					if err != nil {
						t.Fatal(err)
					}
					db := w.Runtime.Store.DB()
					if _, err := db.ExecContext(t.Context(), "ALTER TABLE remote_sources RENAME TO remote_sources_held"); err != nil {
						t.Fatal(err)
					}
					restored := false
					defer func() {
						if !restored {
							if _, err := db.ExecContext(context.WithoutCancel(t.Context()), "ALTER TABLE remote_sources_held RENAME TO remote_sources"); err != nil {
								t.Error(err)
							}
						}
					}()
					before := len(w.Upstream.Requests())
					logs.Reset()
					direct := w.Request("GET", "/artifactory/remote/dir/", "admin", "")
					directLog := logs.String()
					logs.Reset()
					virtual := w.Request("GET", "/artifactory/virtual/dir/", "admin", "")
					virtualLog := logs.String()
					alias := w.Request("GET", "/artifactory/remote-cache/dir/", "admin", "")
					if _, err := db.ExecContext(t.Context(), "ALTER TABLE remote_sources_held RENAME TO remote_sources"); err != nil {
						t.Fatal(err)
					}
					restored = true
					stateAfter, err := w.Runtime.Store.RemoteCache().State(t.Context(), "remote")
					if err != nil || !reflect.DeepEqual(stateBefore, stateAfter) {
						t.Fatalf("own lookup failure changed source state: before%+v after%+v err%v", stateBefore, stateAfter, err)
					}
					for name, rec := range map[string]*httptest.ResponseRecorder{"direct": direct, "virtual": virtual} {
						if rec.Body.String() != `{"errors":[{"message":"internal error","status":500}]}`+"\n" {
							t.Errorf("%s exposed an unsanitized fault: %s", name, rec.Body)
						}
					}
					if !strings.Contains(virtualLog, "remote_sources") || strings.Count(virtualLog, "level=ERROR") != 1 {
						t.Errorf("virtual cause was not logged once: %s", virtualLog)
					}
					t.Logf(
						"OBSERVED %s/%s real remote_sources statement fault: direct=%d virtual=%d alias=%d directErrors=%d originCalls=%d contextLive=%t",
						backend,
						kind,
						direct.Code,
						virtual.Code,
						alias.Code,
						strings.Count(directLog, "level=ERROR"),
						len(w.Upstream.Requests())-before,
						t.Context().Err() == nil,
					)
					if direct.Code != 500 {
						t.Errorf("direct authority phase changed:%d %s", direct.Code, direct.Body)
					}
					if virtual.Code != 500 {
						t.Errorf("virtual source fault: %d %s", virtual.Code, virtual.Body)
					}
					if alias.Code != 200 || !strings.Contains(alias.Body.String(), "kept.bin") {
						t.Errorf("alias control:%d %s", alias.Code, alias.Body)
					}
					if len(w.Upstream.Requests()) != before {
						t.Errorf("statement fault contacted origin")
					}
					if !strings.Contains(directLog, "remote_sources") || strings.Count(directLog, "level=ERROR") != 1 {
						t.Errorf("original direct SQL fault notloggedonce:%s", directLog)
					}
				})
			}
		})
	}
}
