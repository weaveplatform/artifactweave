package artifactshttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/build"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/gen/operations"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
)

// keyedAppend runs AppendBuild with key under the shared replay over db, as
// the router wraps it, and renders the answer as the strict server would.
func keyedAppend(
	t *testing.T,
	ctx context.Context,
	svc ArtifactoryAPI,
	db *inmem.Store,
	req artgen.AppendBuildRequestObject,
	key string,
) *httptest.ResponseRecorder {
	t.Helper()
	req.Params.IdempotencyKey = &key
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	r.Header.Set(idempotency.Header, key)
	mw := idempotency.Strict[artgen.StrictHandlerFunc](
		idempotency.Replay{Store: db.Replays(), Tx: db, Budget: time.Second},
		"artifactory",
		operations.Table,
		svc.KeyedBindings(),
	)
	handler := mw(
		func(ctx context.Context, _ http.ResponseWriter, _ *http.Request, request any) (any, error) {
			return svc.AppendBuild(
				ctx,
				request.(artgen.AppendBuildRequestObject),
			) //nolint:forcetypeassert // the strict server's own type
		},
		"AppendBuild",
	)
	rec := httptest.NewRecorder()
	out, err := handler(ctx, rec, r, req)
	switch {
	case err != nil:
		apierror.Write(rec, apierror.ProfileArtifactory, err)
	case out != nil:
		if err := out.(artgen.AppendBuildResponseObject).VisitAppendBuildResponse(
			rec,
		); err != nil { //nolint:forcetypeassert // AppendBuild's answers
			t.Fatal(err)
		}
	}
	return rec
}

func appendRequest(modules string) (artgen.AppendBuildRequestObject, context.Context) {
	return artgen.AppendBuildRequestObject{BuildName: "app", BuildNumber: "1"},
		httpkit.WithRawBody(context.Background(), []byte(modules))
}

type replayDeadlineUsers struct{ accesstest.UserStore }

func (u replayDeadlineUsers) Get(context.Context, string) (accesstest.User, error) {
	return accesstest.User{}, context.DeadlineExceeded
}

// replayDeadlineStore is a store whose users time out, for an access module
// whose authenticator cannot recheck a caller.
type replayDeadlineStore struct{ *inmem.Store }

func (s replayDeadlineStore) Users() accesstest.UserStore {
	return replayDeadlineUsers{s.Store.Users()}
}

func TestAccAppendIdempotencyRecheckDeadline(t *testing.T) {
	w := newItemWorld(t, false)
	w.publish(artifactDoc("app", "1", testStarted, "artifact"))
	p, err := accesstest.Over(t, w.db).API().Authenticator.Basic(t.Context(), "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	// The access module over the store, whose authenticator alone reads
	// users that time out.
	access := accesstest.Over(t, w.db).API()
	access.Authenticator = accesstest.Over(t, replayDeadlineStore{w.db}).API().Authenticator
	svc := newArtifactoryAPI(Config{Store: w.db, Blobs: w.blobs, Access: access})
	req, ctx := appendRequest(`[]`)
	rec := keyedAppend(t, kernel.WithPrincipal(ctx, p), svc, w.db, req, `"deadline"`)
	wantStatus(t, rec, http.StatusServiceUnavailable)
	if rec.Header().Get("Retry-After") != "1" ||
		rec.Body.String() != `{"errors":[{"message":"Protected append exceeded its wait budget; retry the same key","status":503}]}`+"\n" {
		t.Fatalf("deadline answer: %v %s", rec.Header(), rec.Body)
	}
}

// A replay is authorised by the permissions its credential holds when it
// runs, never by the decider its request was authorised with: once the
// caller has lost the right, the replay is refused even though the request's
// decider still grants it (decision 0043).
func TestAccAppendIdempotencyReplayEvaluatesCurrentPermissions(t *testing.T) {
	w := newItemWorld(t, false)
	w.publish(artifactDoc("app", "1", testStarted, "artifact"))
	access := accesstest.Over(t, w.db).API()
	p, err := access.Authenticator.Basic(t.Context(), "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	p, decider, err := accesstest.Over(t, w.db).API().Authorizer.Authorize(t.Context(), p, false)
	if err != nil || !decider.IsAdmin() {
		t.Fatalf("request decider: %v", err)
	}
	svc := newArtifactoryAPI(Config{Store: w.db, Blobs: w.blobs, Access: access})
	req, ctx := appendRequest(`[]`)
	ctx = accessapi.WithCaller(ctx, p, decider)
	wantStatus(t, keyedAppend(t, ctx, svc, w.db, req, `"replay"`), http.StatusNoContent)
	u, err := w.db.Users().Get(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	u.Admin = false
	if err := w.db.Users().Update(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, keyedAppend(t, ctx, svc, w.db, req, `"replay"`), http.StatusForbidden)
}

// The binding's checks answer as the handler's do, ahead of the key, and a
// scope that cannot be encoded is a 500 like any internal fault.
func TestUnitAppendBindingChecks(t *testing.T) {
	w := newItemWorld(t, false)
	w.publish(artifactDoc("app", "1", testStarted, "artifact"))
	access := accesstest.Over(t, w.db).API()
	p, err := access.Authenticator.Basic(t.Context(), "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	p, decider, err := access.Authorizer.Authorize(t.Context(), p, false)
	if err != nil {
		t.Fatal(err)
	}
	svc := newArtifactoryAPI(Config{Store: w.db, Blobs: w.blobs, Access: access})
	for _, c := range []struct {
		started, key string
		want         int
	}{
		{"not a time", `"key"`, http.StatusBadRequest},
		{"not a time", "unquoted", http.StatusBadRequest},
		{testStarted, "unquoted", http.StatusBadRequest},
		// 23:59 west of UTC on the last day of 9999 is a year JSON cannot
		// hold once normalised to UTC.
		{"9999-12-31T23:59:59.999-2359", `"key"`, http.StatusInternalServerError},
	} {
		req, ctx := appendRequest(`[]`)
		req.Params.Started = &c.started
		rec := keyedAppend(t, accessapi.WithCaller(ctx, p, decider), svc, w.db, req, c.key)
		wantStatus(t, rec, c.want)
	}
	b := svc.appendBinding()
	if _, err := b.Resource(t.Context(), struct{}{}); !errors.Is(err, errRequestType) {
		t.Fatalf("resource of another request: %v", err)
	}
	if _, err := b.Reload(
		t.Context(),
		struct{}{},
		idempotency.ReplayReceipt{},
	); !errors.Is(
		err,
		errRequestType,
	) {
		t.Fatalf("reload of another request: %v", err)
	}
	if _, err := b.Reload(
		t.Context(),
		artgen.AppendBuildRequestObject{},
		idempotency.ReplayReceipt{},
	); err == nil {
		t.Fatal("reload without a caller")
	}
	bad := "not a time"
	if _, err := b.Reload(
		accessapi.WithCaller(t.Context(), p, decider),
		artgen.AppendBuildRequestObject{Params: artgen.AppendBuildParams{Started: &bad}},
		idempotency.ReplayReceipt{},
	); err == nil {
		t.Fatal("reload of a bad started")
	}
	noAuth := newArtifactoryAPI(Config{Store: w.db, Blobs: w.blobs})
	if _, err := noAuth.currentBuildAccess(p)(t.Context()); !errors.Is(err, build.ErrForbidden) {
		t.Fatalf("no authenticator: %v", err)
	}
	gone := p
	gone.Name = "nobody"
	if _, err := svc.currentBuildAccess(gone)(t.Context()); !errors.Is(err, build.ErrForbidden) {
		t.Fatalf("unknown user: %v", err)
	}
}
