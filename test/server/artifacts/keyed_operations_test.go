package artifacts_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/postgres"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/sql/sqlite"
	appstoretest "github.com/weaveplatform/artifactweave/internal/app/storetest"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
	pgdrv "github.com/weaveplatform/artifactweave/internal/platform/sql/postgres"
	platsqltest "github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
)

// keyedRequest is one request of a keyed operation's row.
type keyedRequest struct{ method, path, body string }

// keyedRow is how the table test drives one served key operation: the
// request its fixture answers, the same request with other bytes or
// effect parameters, and how many times its effect has happened.
type keyedRow struct {
	request, other keyedRequest
	effects        func(t *testing.T, s *appsql.Store) int
}

// keyedFixtureRuns are the runs every row's fixture publishes: three of
// app #1, so a delete of number 1 that ran twice deletes two.
var keyedFixtureRuns = []string{
	"2026-10-15T09:30:00.000+0000",
	"2026-10-15T09:31:00.000+0000",
	"2026-10-15T09:32:00.000+0000",
}

// runsDeleted counts the fixture's runs of app #1 that are gone.
func runsDeleted(t *testing.T, s *appsql.Store) int {
	t.Helper()
	runs, _, err := s.Builds().Runs(t.Context(), "", "app", kernel.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return len(keyedFixtureRuns) - len(runs)
}

// keyedRows has one row per served operation that declares IdempotencyKey,
// by service and operation ID; TestAccKeyedOperationsReplayOnce fails for an
// operation without one, so a new key operation is added here.
var keyedRows = map[string]keyedRow{
	"artifactory appendBuild": {
		request: keyedRequest{http.MethodPost, buildsPath + "/append/app/1", `[{"id":"once"}]`},
		other:   keyedRequest{http.MethodPost, buildsPath + "/append/app/1", `[{"id":"other"}]`},
		effects: func(t *testing.T, s *appsql.Store) int {
			t.Helper()
			b, err := s.Builds().Get(t.Context(), "", "app", "1", time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			return len(b.Modules)
		},
	},
	"artifactory deleteBuilds": {
		request: keyedRequest{http.MethodDelete, buildsPath + "/app?buildNumbers=1", ""},
		other: keyedRequest{
			http.MethodDelete,
			buildsPath + "/app?buildNumbers=1&artifacts=1",
			"",
		},
		effects: runsDeleted,
	},
	"artifactory deleteBuildsMultiple": {
		request: keyedRequest{
			http.MethodPost, buildsPath + "/delete", `{"buildName":"app","buildNumbers":["1"]}`,
		},
		other: keyedRequest{
			http.MethodPost, buildsPath + "/delete", `{"buildName":"app","buildNumbers":["1","1"]}`,
		},
		effects: runsDeleted,
	},
	"artifactory promoteBuild": {
		request: keyedRequest{
			http.MethodPost,
			buildsPath + "/promote/app/1",
			`{"status":"released","artifacts":false}`,
		},
		other: keyedRequest{
			http.MethodPost,
			buildsPath + "/promote/app/1",
			`{"status":"rejected","artifacts":false}`,
		},
		effects: func(t *testing.T, s *appsql.Store) int {
			t.Helper()
			b, err := s.Builds().Get(t.Context(), "", "app", "1", time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.Builds().Promotions(t.Context(), b.ID)
			if err != nil {
				t.Fatal(err)
			}
			return len(got)
		},
	},
}

// keyedServer is a fresh store of backend, seeded with the fixture's runs,
// behind the server.
type keyedServer struct {
	store   *appsql.Store
	handler http.Handler
}

func newKeyedServer(t *testing.T, backend, dsn string) keyedServer {
	t.Helper()
	ctx := t.Context()
	var (
		s   *appsql.Store
		err error
	)
	if backend == "sqlite" {
		s, err = sqlite.Open(ctx, filepath.Join(t.TempDir(), "keyed.db"), sqlite.Options{})
	} else {
		s, err = postgres.Open(ctx, dsn, postgres.Options{MaxOpenConns: 4})
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if backend == "postgres" {
		appstoretest.Reset(t, s.DB(), pgdrv.Dialect)
	}
	cheapSeed(t, s)
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := authn.NewAuthenticator(s.Users(), s.Settings(), authn.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	h := New(Config{Store: s, Blobs: blobs, Auth: a})
	for _, started := range keyedFixtureRuns {
		wantStatus(t, do(t, h, http.MethodPut, buildsPath, basic("admin", "pw"),
			fmt.Sprintf(`{"name":"app","number":"1","started":%q,"modules":[]}`, started),
		), http.StatusNoContent)
	}
	return keyedServer{store: s, handler: h}
}

func (k keyedServer) send(t *testing.T, r keyedRequest, key string) *httptest.ResponseRecorder {
	t.Helper()
	headers := basic("admin", "pw")
	if r.body != "" {
		headers["Content-Type"] = "application/json"
	}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	return do(t, k.handler, r.method, r.path, headers, r.body)
}

// answer is what a test compares of a response: its status, its headers
// other than the per-request ones, and its body.
func answer(rec *httptest.ResponseRecorder) string {
	h := rec.Header().Clone()
	h.Del("X-Request-Id")
	h.Del("Date")
	return fmt.Sprintf("%d %v %s", rec.Code, h, rec.Body)
}

// Every served operation that declares IdempotencyKey, on SQLite and
// PostgreSQL: the same key twice has one effect and answers the same; the
// same key with other bytes or effect parameters answers 422 and has none;
// without a key the operation answers as it does with one and repeats its
// effect, as it always has.
func TestAccKeyedOperationsReplayOnce(t *testing.T) {
	index, err := operations.NewIndex(operations.Table)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, op := range operations.Table {
		if !op.IdempotencyKey {
			continue
		}
		id := op.Service + " " + op.OperationID
		declared[id] = true
		row, ok := keyedRows[id]
		if !ok {
			t.Errorf("%s declares IdempotencyKey but has no row", id)
			continue
		}
		for _, r := range []keyedRequest{row.request, row.other} {
			req := httptest.NewRequestWithContext(context.Background(), r.method, r.path, nil)
			if got, found := index.FindRequest(req); !found || got.OperationID != op.OperationID {
				t.Errorf(
					"%s: %s %s is %s, not this operation",
					id,
					r.method,
					r.path,
					got.OperationID,
				)
			}
		}
	}
	for id := range keyedRows {
		if !declared[id] {
			t.Errorf("row %s names no operation that declares IdempotencyKey", id)
		}
	}
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			dsn := ""
			if backend == "postgres" {
				dsn = platsqltest.PostgresDSN(t)
			}
			for id, row := range keyedRows {
				t.Run(id, func(t *testing.T) {
					keyed := newKeyedServer(t, backend, dsn)
					first := keyed.send(t, row.request, `"once"`)
					if first.Code/100 != 2 {
						t.Fatalf("keyed: %s", answer(first))
					}
					if again := keyed.send(
						t,
						row.request,
						`"once"`,
					); answer(
						again,
					) != answer(
						first,
					) {
						t.Errorf("replay answered\n%s\nwant\n%s", answer(again), answer(first))
					}
					if n := row.effects(t, keyed.store); n != 1 {
						t.Errorf("keyed twice: %d effects, want 1", n)
					}
					wantStatus(
						t,
						keyed.send(t, row.other, `"once"`),
						http.StatusUnprocessableEntity,
					)
					if n := row.effects(t, keyed.store); n != 1 {
						t.Errorf("after a refused reuse: %d effects, want 1", n)
					}

					plain := newKeyedServer(t, backend, dsn)
					if got := plain.send(t, row.request, ""); answer(got) != answer(first) {
						t.Errorf("without a key\n%s\nwant\n%s", answer(got), answer(first))
					}
					plain.send(t, row.request, "")
					if n := row.effects(t, plain.store); n != 2 {
						t.Errorf("without a key twice: %d effects, want 2", n)
					}
				})
			}
		})
	}
}
