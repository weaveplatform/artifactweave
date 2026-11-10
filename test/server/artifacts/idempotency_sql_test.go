package artifacts_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/migration/access/core/authn"
	appsql "github.com/weaveplatform/artifactweave/migration/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/sql/postgres"
	"github.com/weaveplatform/artifactweave/migration/app/adapters/sql/sqlite"
	appstoretest "github.com/weaveplatform/artifactweave/migration/app/storetest"
	"github.com/weaveplatform/artifactweave/migration/platform/blob/fs"
	pgdrv "github.com/weaveplatform/artifactweave/migration/platform/sql/postgres"
	platsqltest "github.com/weaveplatform/artifactweave/migration/platform/sql/sqltest"
	artclient "github.com/weaveplatform/artifactweave/pkg/client/artifactory"
)

func TestAccAppendReplayDurability(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx := t.Context()
			path := filepath.Join(t.TempDir(), "replays.db")
			dsn := ""
			if backend == "postgres" {
				dsn = platsqltest.PostgresDSN(t)
			}
			open := func() *appsql.Store {
				t.Helper()
				var s *appsql.Store
				var err error
				if backend == "sqlite" {
					s, err = sqlite.Open(ctx, path, sqlite.Options{})
				} else {
					s, err = postgres.Open(ctx, dsn, postgres.Options{MaxOpenConns: 8})
				}
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
				return s
			}
			first := open()
			if backend == "postgres" {
				appstoretest.Reset(t, first.DB(), pgdrv.Dialect)
			}
			cheapSeed(t, first)
			blobs, err := fs.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			handler := func(s *appsql.Store) http.Handler {
				t.Helper()
				a, err := authn.NewAuthenticator(
					s.Users(),
					s.Settings(),
					authn.Options{Hash: testHash},
				)
				if err != nil {
					t.Fatal(err)
				}
				return New(Config{Store: s, Blobs: blobs, Auth: a})
			}
			h1, h2 := handler(first), handler(open())
			wantStatus(t, do(
				t,
				h1,
				http.MethodPut,
				buildsPath,
				basic("admin", "pw"),
				`{"name":"app","number":"1","started":"`+testStarted+`","modules":[]}`,
			), http.StatusNoContent)
			// The first instance commits, then the TCP connection disappears before
			// any status or body is sent. The generated client sees an error.
			lostRouter := chi.NewRouter()
			lostRouter.Post("/*", func(w http.ResponseWriter, r *http.Request) {
				rec := httptest.NewRecorder()
				h1.ServeHTTP(rec, r)
				if rec.Code != http.StatusNoContent {
					t.Errorf("request before response loss: %d %s", rec.Code, rec.Body)
				}
				conn, _, err := http.NewResponseController(w).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = conn.Close()
			})
			lost := httptest.NewServer(lostRouter)
			t.Cleanup(lost.Close)
			other := httptest.NewServer(h2)
			t.Cleanup(other.Close)
			client := func(url string) *artclient.ClientWithResponses {
				t.Helper()
				c, err := artclient.NewClientWithResponses(
					url+"/artifactory",
					artclient.WithRequestEditorFn(
						func(_ context.Context, r *http.Request) error { r.SetBasicAuth("admin", "pw"); return nil },
					),
				)
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			key := `"lost-response"`
			params := &artclient.AppendBuildParams{IdempotencyKey: &key}
			body := `[{"id":"once"}]`
			if _, err := client(
				lost.URL,
			).AppendBuildWithBodyWithResponse(ctx, "app", "1", params, "application/json", strings.NewReader(body)); err == nil {
				t.Fatal("lost response was reported as a success")
			}
			got, err := client(
				other.URL,
			).AppendBuildWithBodyWithResponse(ctx, "app", "1", params, "application/json", strings.NewReader(body))
			if err != nil || got.StatusCode() != http.StatusNoContent {
				t.Fatalf("second-instance replay: %+v, %v", got, err)
			}
			// Close the original pool and construct a fresh server/authenticator.
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := open()
			restarted := handler(reopened)
			headers := basic("admin", "pw")
			headers["Idempotency-Key"] = key
			appendEventually := func(h http.Handler, requestHeaders map[string]string, modules string) {
				t.Helper()
				// Concurrent transactions can occupy this deliberately small pool.
				// Admission then returns 503 before mutation. Retry the same intent
				// using its advertised delay, without weakening the final effect count.
				for attempt := range 5 {
					rec := do(
						t,
						h,
						http.MethodPost,
						buildsPath+"/append/app/1",
						requestHeaders,
						modules,
					)
					if rec.Code == http.StatusNoContent {
						return
					}
					if rec.Code != http.StatusServiceUnavailable ||
						rec.Header().Get("Retry-After") != "1" ||
						attempt == 4 {
						t.Errorf("append attempt %d: %d %s", attempt+1, rec.Code, rec.Body)
						return
					}
					select {
					case <-ctx.Done():
						t.Errorf("append retry: %v", ctx.Err())
						return
					case <-time.After(time.Second):
					}
				}
			}
			wantStatus(
				t,
				do(t, restarted, http.MethodPost, buildsPath+"/append/app/1", headers, body),
				http.StatusNoContent,
			)
			const workers = 12
			var wg sync.WaitGroup
			start := make(chan struct{})
			for i := range workers * 2 {
				wg.Go(func() {
					<-start
					h := h2
					if i%2 == 0 {
						h = restarted
					}
					requestHeaders := basic("admin", "pw")
					requestHeaders["Idempotency-Key"] = fmt.Sprintf(`"independent-%d"`, i)
					modules := fmt.Sprintf(
						`[{"id":"module-%d","properties":{"key":"value"},"artifacts":[{"name":"a.bin"}],"dependencies":[{"id":"d.bin"}]}]`,
						i,
					)
					if i >= workers {
						requestHeaders["Idempotency-Key"] = key
						modules = body
					}
					appendEventually(h, requestHeaders, modules)
				})
			}
			close(start)
			wg.Wait()
			b, err := reopened.Builds().Get(ctx, "", "app", "1", time.Time{})
			if err != nil || len(b.Modules) != workers+1 {
				t.Fatalf(
					"lost or duplicated effects across replicas: %d modules, %v",
					len(b.Modules),
					err,
				)
			}
			for range workers {
				wg.Go(func() {
					appendEventually(restarted, headers, body)
				})
			}
			wg.Wait()
			b, err = reopened.Builds().Get(ctx, "", "app", "1", time.Time{})
			if err != nil || len(b.Modules) != workers+1 {
				t.Fatalf("duplicate effects: %d modules, %v", len(b.Modules), err)
			}
		})
	}
}
