package dockerhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	httpmiddleware "github.com/weaveplatform/artifactweave/internal/platform/httpkit/middleware"
)

type barrierItems struct {
	port.ItemStore
	entered chan struct{}
	release chan struct{}
	failure error
}

func (b barrierItems) Search(ctx context.Context, _ *aql.Query, _ aql.Options) ([]aql.Hit, error) {
	close(b.entered)
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("fixture blob lookup: %w", ctx.Err())
	case <-b.release:
		return nil, b.failure
	}
}

func TestAccBlobLookupCancellationClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cancel  bool
		failure error
	}{
		{"client cancellation", true, nil},
		{"live request backend cancellation", false, fmt.Errorf("backend: %w", context.Canceled)},
		{"live request backend failure", false, errBoom},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := barrierItems{entered: make(chan struct{}), release: make(chan struct{}), failure: tc.failure}
			world := newBlobWorld(t, func(o *Options) {
				lookup.ItemStore = o.Store.Items()
				o.Store = itemsFault{Store: o.Store, items: lookup}
				o.Fault = func(r *http.Request, err error) { httpkit.NoteCause(r.Context(), err) }
			})
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			h := httpmiddleware.RecoverPanic(logger, func(path string) string { return path })(
				httpmiddleware.AccessLog(logger, func(path string) string { return path })(world.h),
			)
			finished := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(finished)
				h.ServeHTTP(w, r)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			path := "/v2/docker-local/team/denied/blobs/" + digestOf("cancelled lookup")
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, server.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			type result struct {
				status int
				err    error
			}
			clientDone := make(chan result, 1)
			go func() {
				response, requestErr := server.Client().Do(req)
				got := result{err: requestErr}
				if response != nil {
					got.status = response.StatusCode
					_ = response.Body.Close()
				}
				clientDone <- got
			}()
			select {
			case <-lookup.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("blob lookup did not reach its barrier")
			}
			if tc.cancel {
				cancel()
			} else {
				close(lookup.release)
			}
			select {
			case got := <-clientDone:
				if tc.cancel {
					if !errors.Is(got.err, context.Canceled) || got.status != 0 {
						t.Fatal("client did not observe its cancellation without a response", got)
					}
				} else if got.err != nil || got.status != http.StatusInternalServerError {
					t.Fatal("live backend failure did not retain HTTP 500", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("blob request did not finish")
			}
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("blob lookup did not release after cancellation")
			}
			var record struct {
				Msg, Path, Error string
				Status, Bytes    int
				Aborted          bool
			}
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal("lookup did not produce one access record", err)
			}
			if record.Msg != "request" || record.Path != path ||
				!strings.Contains(record.Error, "docker: find blob") {
				t.Fatal("blob lookup diagnostic cause was lost", logs.String())
			}
			if tc.cancel {
				if record.Status != 0 || record.Bytes != 0 || !record.Aborted ||
					!strings.Contains(record.Error, "context canceled") {
					t.Fatal("cancelled blob lookup was not recorded as an unpublished abort", logs.String())
				}
			} else if record.Status != http.StatusInternalServerError || record.Aborted {
				t.Fatal("live backend failure lost its server-failure diagnostic", logs.String())
			}
		})
	}
}
