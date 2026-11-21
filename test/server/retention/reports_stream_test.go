package retention_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	artifactmodel "github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pkgindex"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
	"github.com/weaveplatform/artifactweave/internal/retention/core/model"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

var errReportClientClosed = errors.New("fixture client closed")

type cleanupReportWriter struct {
	*httptest.ResponseRecorder
	first  func() error
	status int
}

func (w *cleanupReportWriter) WriteHeader(status int) {
	w.status++
	w.ResponseRecorder.WriteHeader(status)
}

func (w *cleanupReportWriter) Write(data []byte) (int, error) {
	if w.first != nil {
		first := w.first
		w.first = nil
		if err := first(); err != nil {
			return 0, err
		}
	}
	return w.ResponseRecorder.Write(data) //nolint:wrapcheck // response writers preserve client I/O errors
}

func cleanupReportServe(handler http.Handler, writer http.ResponseWriter, request *http.Request) (recovered any) {
	defer func() { recovered = recover() }()
	handler.ServeHTTP(writer, request)
	return nil
}

func TestAccCleanupReportStreamingReference(t *testing.T) {
	cleanupReportStreaming(t, inmem.New())
}

func TestAccCleanupReportStreamingSQL(t *testing.T) {
	keys := storekit.Get[*crypt.Keyring](t)(crypt.New(bytes.Repeat([]byte{7}, 32)))
	sqltest.RunModule(
		t, appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) { cleanupReportStreaming(t, newStore(t)) },
		sqltest.WithPostgres(), sqltest.WithKeyring(keys),
	)
}

func cleanupReportStreaming(t *testing.T, db harness.Store) {
	t.Helper()
	f := newCleanupExecutorFixture(t, db)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	f.ctx = ctx
	const quotedPath = "package/1/a,\"quoted\".bin"
	for i := range 103 {
		path := fmt.Sprintf("package/1/report-%03d.bin", i)
		if i == 0 {
			path = quotedPath
		}
		target := &url.URL{Path: "/artifactory/execution/" + path}
		response := support.Do(t, f.handler, http.MethodPut, target.EscapedPath(), support.Basic("admin", "pw"), "report-shared")
		if response.Code != http.StatusCreated {
			t.Fatal("large report upload failed", response.Code, response.Body.String())
		}
		item := storekit.Get[artifactmodel.Item](t)(db.Items().Get(ctx, "execution", false, path))
		storekit.Must(t, pkgindex.Link(ctx, db, pkgindex.Version{RepoKey: "execution", PackageType: "generic", Package: "package", Version: "1"}, item.ID, time.Now()))
	}
	for _, tc := range []struct {
		name   string
		abort  bool
		marker bool
	}{
		{"complete", false, true},
		{"client-write-error", false, false},
		{"canceled", true, false},
		{"revoked", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := cleanupReportRun(t, f, true)
			if run.TotalItems != 105 || run.CleanedItems != 0 {
				t.Fatal("large real dry-run fixture lost selected items", run.TotalItems)
			}
			user := "report-admin-" + tc.name
			storekit.Must(t, db.Users().Create(ctx, accesstest.User{Name: user, PasswordHash: support.CheapHash, Admin: true}))
			requestContext, cancelRequest := context.WithCancel(t.Context())
			defer cancelRequest()
			request := httptest.NewRequestWithContext(
				requestContext,
				http.MethodGet,
				cleanupReports+"downloadReport?policyKey=execution-policy&runId="+strconv.FormatInt(run.ID, 10),
				nil,
			)
			request.SetBasicAuth(user, "pw")
			writer := &cleanupReportWriter{ResponseRecorder: httptest.NewRecorder()}
			var mutation <-chan error
			writer.first = func() error {
				// An independently guarded metadata change must finish while
				// the client write is blocked: no CSV transaction may hold it.
				done := make(chan error, 1)
				mutation = done
				go func() {
					done <- db.Tx(ctx, func(ctx context.Context) error {
						if err := db.Projects().Guard(ctx); err != nil {
							return err
						}
						if tc.name == "revoked" {
							return db.Users().Delete(ctx, user)
						}
						return nil
					})
				}()
				select {
				case err := <-done:
					mutation = nil
					if err != nil {
						t.Error("guarded metadata change failed", err)
						return errReportClientClosed
					}
				case <-time.After(3 * time.Second):
					t.Error("CSV held its metadata transaction across a client write")
					return errReportClientClosed
				}
				switch tc.name {
				case "client-write-error":
					return errReportClientClosed
				case "canceled":
					cancelRequest()
				}
				return nil
			}
			recovered := cleanupReportServe(f.handler, writer, request)
			if mutation != nil {
				select {
				case err := <-mutation:
					storekit.Must(t, err)
				case <-ctx.Done():
					t.Fatal("blocked metadata change did not finish after response ended")
				}
			}
			if tc.abort {
				if err, ok := recovered.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
					t.Fatal("interrupted response did not abort", recovered)
				}
			} else if recovered != nil {
				t.Fatal("unexpected response panic", recovered)
			}
			if writer.Code != http.StatusOK || writer.status != 1 {
				t.Fatal("stream attempted another HTTP status", writer.Code, writer.status)
			}
			stored := storekit.Get[model.CleanupRun](t)(db.Cleanup().Run(ctx, run.ID))
			if stored.ReportDownloaded != tc.marker || stored.CleanedItems != 0 || stored.TotalItems != 105 {
				t.Fatal("interrupted download changed completion/execution facts", stored.ReportDownloaded)
			}
			if tc.marker {
				rows, err := csv.NewReader(writer.Body).ReadAll()
				storekit.Must(t, err)
				if len(rows) != 106 || !slices.ContainsFunc(rows[1:], func(row []string) bool { return row[1] == quotedPath }) {
					t.Fatal("multi-page CSV truncated items or corrupted escaping", len(rows))
				}
			}
		})
	}
}
