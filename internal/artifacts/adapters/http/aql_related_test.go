package artifactshttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/aql"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// compactAQL runs query with ?compact=true and returns the status and the
// body.
func compactAQL(
	t *testing.T,
	h http.Handler,
	as map[string]string,
	query string,
	headers ...string,
) (
	int, string,
) {
	t.Helper()
	all := map[string]string{}
	for k, v := range as {
		all[k] = v
	}
	for i := 0; i+1 < len(headers); i += 2 {
		all[headers[i]] = headers[i+1]
	}
	rec := do(t, h, http.MethodPost, aqlURL+"?compact=true", all, query)
	return rec.Code, rec.Body.String()
}

// TestAccSearchAQLTimeout: a query that outlasts the configured timeout is
// cancelled and answered with 408 (#411), the streamed timeout applying to
// a stream: true request.
func TestAccSearchAQLTimeout(t *testing.T) {
	h, _ := buildsAQLWorld(t, func(c *Config) {
		c.AQL.Timeout = time.Nanosecond
		c.Store = deadlineStore{c.Store}
	})
	admin := basic("admin", "pw")
	for _, query := range []string{`items.find()`, `builds.find()`} {
		code, body := compactAQL(t, h, admin, query)
		if code != http.StatusRequestTimeout ||
			!strings.Contains(body, "exceeded its timeout of 0.000000001 seconds") {
			t.Errorf("%s: %d %s", query, code, body)
		}
		// A streamed query has its own timeout, here none.
		if code, body := compactAQL(t, h, admin, query, "stream", "true"); code != http.StatusOK {
			t.Errorf("%s streamed: %d %s", query, code, body)
		}
	}
	h, _ = buildsAQLWorld(t, func(c *Config) {
		c.AQL.StreamTimeout = time.Nanosecond
		c.Store = deadlineStore{c.Store}
	})
	if code, _ := compactAQL(t, h, admin, `items.find()`); code != http.StatusOK {
		t.Fatalf("unstreamed query: %d", code)
	}
	if code, _ := compactAQL(t, h, admin, `items.find()`, "stream", "true"); code !=
		http.StatusRequestTimeout {
		t.Fatalf("streamed query: %d", code)
	}
	// The nesting of related rows fails the same ways: on the timeout, and
	// on an include that reaches too many related rows.
	s := newArtifactoryAPI(Config{})
	ctx := context.Background()
	var api *apierror.Error
	late := context.DeadlineExceeded
	fail := s.aqlFailure(ctx, late, time.Second, "nest", late)
	if !errors.As(fail, &api) || api.Status != http.StatusRequestTimeout ||
		api.Message != "The AQL query exceeded its timeout of 1 seconds and was cancelled" {
		t.Fatalf("nest timeout: %v", fail)
	}
	fail = s.aqlFailure(ctx, nil, 0, "nest", fmt.Errorf("%w: too many", aql.ErrInvalid))
	if !errors.As(fail, &api) || api.Status != http.StatusBadRequest {
		t.Fatalf("an include over the budget: %v", fail)
	}
	// A client that went away is no timeout.
	gone, cancel := context.WithCancel(ctx)
	cancel()
	fail = s.aqlFailure(gone, context.DeadlineExceeded, time.Second, "nest", context.Canceled)
	if errors.As(fail, &api) && api.Status == http.StatusRequestTimeout {
		t.Fatalf("a cancelled request: %v", fail)
	}
}

// deadlineStore makes an AQL search that carries a deadline wait for it, so
// the timeout tests do not race the search. Go cancels a context at once only
// when its deadline has already passed as it is made; Windows' clock is too
// coarse for a one-nanosecond deadline to have passed, and a fast in-memory
// search then finishes before the timer fires.
type deadlineStore struct{ serverStore }

func (s deadlineStore) Items() port.ItemStore { return deadlineItems{s.serverStore.Items()} }
func (s deadlineStore) Builds() port.BuildStore {
	return deadlineBuilds{s.serverStore.Builds()}
}

type deadlineItems struct{ port.ItemStore }

func (d deadlineItems) Search(ctx context.Context, q *aql.Query, o aql.Options) ([]aql.Hit, error) {
	if _, ok := ctx.Deadline(); ok {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return d.ItemStore.Search(ctx, q, o) //nolint:wrapcheck // the store's own error
}

type deadlineBuilds struct{ port.BuildStore }

func (d deadlineBuilds) Search(
	ctx context.Context,
	q *aql.Query,
	o aql.Options,
) ([]aql.BuildHit, error) {
	if _, ok := ctx.Deadline(); ok {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return d.BuildStore.Search(ctx, q, o) //nolint:wrapcheck // the store's own error
}
