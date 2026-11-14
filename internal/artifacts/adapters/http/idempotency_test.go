package artifactshttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// serveKeyed runs handler, the partial's handler of the operation the Go
// name op names, with key under the shared replay over db, as the router
// wraps it, and renders the answer with visit as the strict server would.
func serveKeyed(
	t *testing.T,
	ctx context.Context,
	svc ArtifactoryAPI,
	db *inmem.Store,
	op, key string,
	request any,
	handler func(context.Context, any) (any, error),
	visit func(any, http.ResponseWriter) error,
) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
	r.Header.Set(idempotency.Header, key)
	mw := idempotency.Strict[artgen.StrictHandlerFunc](
		idempotency.Replay{Store: db.Replays(), Tx: db, Budget: time.Second},
		"artifactory",
		operations.Table,
		svc.KeyedBindings(),
	)
	wrapped := mw(
		func(ctx context.Context, _ http.ResponseWriter, _ *http.Request, request any) (any, error) {
			return handler(ctx, request)
		},
		op,
	)
	rec := httptest.NewRecorder()
	out, err := wrapped(ctx, rec, r, request)
	switch {
	case err != nil:
		apierror.Write(rec, apierror.ProfileArtifactory, err)
	case out != nil:
		if err := visit(out, rec); err != nil {
			t.Fatal(err)
		}
	}
	return rec
}

// adminCaller is a context carrying the seeded admin and its decider.
func adminCaller(t *testing.T, ctx context.Context, w *itemWorld) context.Context {
	t.Helper()
	access := accesstest.Over(t, w.db).API()
	p, err := access.Authenticator.Basic(t.Context(), "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	p, decider, err := access.Authorizer.Authorize(t.Context(), p, false)
	if err != nil {
		t.Fatal(err)
	}
	return accessapi.WithCaller(ctx, p, decider)
}

func runCount(t *testing.T, w *itemWorld, name string) int {
	t.Helper()
	runs, _, err := w.db.Builds().Runs(t.Context(), "", name, kernel.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return len(runs)
}

// A keyed delete deletes once in either form, answers its replays with
// the runs it deleted, and refuses its key with other effect parameters.
func TestAccDeleteBuildsIdempotency(t *testing.T) {
	w := newItemWorld(t, false)
	for i, number := range []string{"1", "1", "2", "3"} {
		w.publish(artifactDoc("app", number, fmt.Sprintf("2026-10-15T09:3%d:00.000+0000", i), "a"))
	}
	svc := newArtifactoryAPI(
		Config{Store: w.db, Blobs: w.blobs, Access: accesstest.Over(t, w.db).API()},
	)
	ctx := adminCaller(t, context.Background(), w)
	byQuery := func(numbers string, artifacts int, key string) *httptest.ResponseRecorder {
		req := artgen.DeleteBuildsRequestObject{BuildName: "app", Params: artgen.DeleteBuildsParams{
			BuildNumbers: &numbers, IdempotencyKey: &key,
		}}
		if artifacts != 0 {
			a := artgen.DeleteBuildsParamsArtifacts(artifacts)
			req.Params.Artifacts = &a
		}
		return serveKeyed(t, ctx, svc, w.db, "DeleteBuilds", key, req,
			func(ctx context.Context, r any) (any, error) {
				return svc.DeleteBuilds(
					ctx,
					r.(artgen.DeleteBuildsRequestObject),
				) //nolint:forcetypeassert // its own request
			},
			func(out any, rw http.ResponseWriter) error {
				return out.(artgen.DeleteBuildsResponseObject).VisitDeleteBuildsResponse(
					rw,
				) //nolint:forcetypeassert // its own answer
			})
	}
	const one = "The following builds has been deleted successfully: 'app#1'."
	for range 2 {
		rec := byQuery(" 1 ", 0, `"query"`)
		wantStatus(t, rec, http.StatusOK)
		if rec.Body.String() != one {
			t.Fatalf("answer %q", rec.Body)
		}
	}
	if n := runCount(t, w, "app"); n != 3 {
		t.Fatalf("runs left %d, want 3", n)
	}
	wantStatus(t, byQuery("1", 1, `"query"`), http.StatusUnprocessableEntity)

	byBody := func(body, key string) *httptest.ResponseRecorder {
		var d artgen.DeleteBuildsMultipleJSONRequestBody
		if err := json.Unmarshal([]byte(body), &d); err != nil {
			t.Fatal(err)
		}
		req := artgen.DeleteBuildsMultipleRequestObject{
			Params: artgen.DeleteBuildsMultipleParams{IdempotencyKey: &key}, Body: &d,
		}
		return serveKeyed(t, httpkit.WithRawBody(ctx, []byte(body)), svc, w.db,
			"DeleteBuildsMultiple", key, req,
			func(ctx context.Context, r any) (any, error) {
				return svc.DeleteBuildsMultiple(
					ctx,
					r.(artgen.DeleteBuildsMultipleRequestObject),
				) //nolint:forcetypeassert // its own request
			},
			func(out any, rw http.ResponseWriter) error {
				return out.(artgen.DeleteBuildsMultipleResponseObject).VisitDeleteBuildsMultipleResponse(
					rw,
				) //nolint:forcetypeassert // its own answer
			})
	}
	for range 2 {
		rec := byBody(`{"buildName":"app","buildNumbers":["2"]}`, `"body"`)
		wantStatus(t, rec, http.StatusOK)
		if rec.Body.String() != "The following builds has been deleted successfully: 'app#2'." {
			t.Fatalf("answer %q", rec.Body)
		}
	}
	if n := runCount(t, w, "app"); n != 2 {
		t.Fatalf("runs left %d, want 2", n)
	}
	wantStatus(t, byBody(`{"buildName":"app","buildNumbers":["3"]}`, `"body"`),
		http.StatusUnprocessableEntity)
	wantStatus(t, byBody(`{"buildName":"absent","deleteAll":true}`, `"absent"`),
		http.StatusNotFound)
	wantStatus(t, byQuery("", 0, `"none"`), http.StatusBadRequest)
}

// A keyed promotion promotes once and answers its replays with its report;
// a dry run keeps no receipt, so a retry runs it again.
func TestAccPromoteBuildIdempotency(t *testing.T) {
	w := newItemWorld(t, false)
	w.publish(artifactDoc("app", "1", testStarted, "a"))
	svc := newArtifactoryAPI(
		Config{Store: w.db, Blobs: w.blobs, Access: accesstest.Over(t, w.db).API()},
	)
	ctx := adminCaller(t, context.Background(), w)
	promote := func(body, key string) *httptest.ResponseRecorder {
		var b artgen.BuildPromotionRequest
		if err := json.Unmarshal([]byte(body), &b); err != nil {
			t.Fatal(err)
		}
		req := artgen.PromoteBuildRequestObject{
			BuildName: "app", BuildNumber: "1",
			Params:   artgen.PromoteBuildParams{IdempotencyKey: &key},
			JSONBody: &b,
		}
		return serveKeyed(
			t,
			httpkit.WithRawBody(ctx, []byte(body)),
			svc,
			w.db,
			"PromoteBuild",
			key,
			req,
			func(ctx context.Context, r any) (any, error) {
				return svc.PromoteBuild(
					ctx,
					r.(artgen.PromoteBuildRequestObject),
				) //nolint:forcetypeassert // its own request
			},
			func(out any, rw http.ResponseWriter) error {
				return out.(artgen.PromoteBuildResponseObject).VisitPromoteBuildResponse(
					rw,
				) //nolint:forcetypeassert // its own answer
			},
		)
	}
	promotions := func() int {
		b, err := w.db.Builds().Get(t.Context(), "", "app", "1", time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := w.db.Builds().Promotions(t.Context(), b.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}
	var first string
	for i := range 2 {
		rec := promote(`{"status":"released","artifacts":false}`, `"promote"`)
		wantStatus(t, rec, http.StatusOK)
		if i == 0 {
			first = rec.Body.String()
		}
		if rec.Body.String() != first {
			t.Fatalf("replay answered %s, want %s", rec.Body, first)
		}
	}
	if n := promotions(); n != 1 {
		t.Fatalf("promotions %d, want 1", n)
	}
	wantStatus(t, promote(`{"status":"other","artifacts":false}`, `"promote"`),
		http.StatusUnprocessableEntity)
	for range 2 {
		wantStatus(t, promote(`{"status":"dry","artifacts":false,"dryRun":true}`, `"dry"`),
			http.StatusOK)
	}
	wantStatus(t, promote(`{"targetRepo":"absent"}`, `"absent"`), http.StatusNotFound)
	wantStatus(t, promote(`{"timestamp":"never"}`, `"refused"`), http.StatusBadRequest)
	if n := promotions(); n != 1 {
		t.Fatalf("promotions after refusals %d, want 1", n)
	}
}

// The delete and promotion bindings refuse another operation's request,
// check the caller as their handlers do, and map errors as they do.
func TestUnitBuildActionBindingChecks(t *testing.T) {
	w := newItemWorld(t, false)
	svc := newArtifactoryAPI(
		Config{Store: w.db, Blobs: w.blobs, Access: accesstest.Over(t, w.db).API()},
	)
	ctx := t.Context()
	for name, b := range map[string]idempotency.Binding{
		"delete":         svc.deleteBinding(),
		"deleteMultiple": svc.deleteMultipleBinding(),
		"promote":        svc.promoteBinding(),
	} {
		if _, err := b.Resource(ctx, struct{}{}); !errors.Is(err, errRequestType) {
			t.Errorf("%s: resource of another request: %v", name, err)
		}
		if _, err := b.Reload(
			ctx,
			struct{}{},
			idempotency.ReplayReceipt{},
		); !errors.Is(
			err,
			errRequestType,
		) {
			t.Errorf("%s: reload of another request: %v", name, err)
		}
		var api *apierror.Error
		if err := b.Error(ctx, struct{}{}, build.ErrNotFound); !errors.As(err, &api) ||
			api.Status != http.StatusNotFound {
			t.Errorf("%s: error of another request: %v", name, err)
		}
	}
	if _, err := svc.deleteBinding().Fingerprint(ctx, struct{}{}); !errors.Is(err, errRequestType) {
		t.Errorf("fingerprint of another request: %v", err)
	}
	body := artgen.BuildPromotionRequest{}
	for name, call := range map[string]func() (any, error){
		"delete resource": func() (any, error) {
			return svc.deleteBinding().Resource(ctx, artgen.DeleteBuildsRequestObject{})
		},
		"delete reload": func() (any, error) {
			return svc.deleteBinding().Reload(ctx, artgen.DeleteBuildsRequestObject{},
				idempotency.ReplayReceipt{})
		},
		"multiple resource": func() (any, error) {
			return svc.deleteMultipleBinding().Resource(ctx, artgen.DeleteBuildsMultipleRequestObject{})
		},
		"multiple reload": func() (any, error) {
			return svc.deleteMultipleBinding().Reload(ctx, artgen.DeleteBuildsMultipleRequestObject{
				Body: &artgen.DeleteBuildsMultipleJSONRequestBody{},
			}, idempotency.ReplayReceipt{})
		},
		"promote resource": func() (any, error) {
			return svc.promoteBinding().Resource(ctx, artgen.PromoteBuildRequestObject{JSONBody: &body})
		},
		"promote reload": func() (any, error) {
			return svc.promoteBinding().Reload(ctx, artgen.PromoteBuildRequestObject{},
				idempotency.ReplayReceipt{})
		},
	} {
		if _, err := call(); err == nil {
			t.Errorf("%s without a caller: no error", name)
		}
	}
	// A promotion that is not JSON goes on to its handler, which refuses it.
	if _, err := svc.promoteBinding().
		Resource(ctx, artgen.PromoteBuildRequestObject{}); err != nil {
		t.Errorf("promotion without a body: %v", err)
	}
}
