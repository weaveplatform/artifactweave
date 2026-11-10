package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/build"
	"github.com/weaveplatform/artifactweave/migration/kernel"
	"github.com/weaveplatform/artifactweave/migration/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitIdempotencyKey(t *testing.T) {
	for _, raw := range []string{`"key"`, `"8e03978e-40d5-43e8-bc93-6894a57f9324"`, `"A.b_c:1"`, `"` + strings.Repeat("k", 128) + `"`} {
		key, err := replayKey(raw)
		if err != nil || len(key) != 64 {
			t.Fatalf("valid key %q: %q, %v", raw, key, err)
		}
	}
	for _, raw := range []string{"", "key", `""`, `"with space"`, `"with\tescape"`, `"é"`, `"key";extra=1`, `"` + strings.Repeat("k", 129) + `"`} {
		if _, err := replayKey(raw); err == nil {
			t.Fatalf("invalid key %q accepted", raw)
		}
	}
	for _, p := range []kernel.Principal{{}, kernel.Anonymous, {Name: "registry", Registry: &types.RegistryAccess{}}} {
		if _, err := appendReplayRequest(`"key"`, p, build.Append{}); err == nil {
			t.Fatalf("unsupported principal accepted: %+v", p)
		}
	}
	if _, err := appendReplayRequest(
		`"key"`,
		kernel.Principal{Name: "user", Password: true},
		build.Append{Started: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
	); err == nil {
		t.Fatal("unencodable scope accepted")
	}
}

type replayDeadlineUsers struct{ store.UserStore }

func (u replayDeadlineUsers) Get(context.Context, string) (types.User, error) {
	return types.User{}, context.DeadlineExceeded
}

// replayDeadlineStore is a store whose users time out, for an access module
// whose authenticator cannot recheck a caller.
type replayDeadlineStore struct{ *inmem.Store }

func (s replayDeadlineStore) Users() store.UserStore {
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
	err = svc.appendOnce(
		t.Context(),
		svc.builds(w.db),
		p,
		build.Append{Name: "app", Number: "1", Modules: []byte(`[]`)},
		`"deadline"`,
	)
	rec := httptest.NewRecorder()
	apierror.Write(rec, apierror.ProfileArtifactory, err)
	wantStatus(t, rec, http.StatusServiceUnavailable)
	if rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("deadline Retry-After: %v", rec.Header())
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
	ctx := accessapi.WithCaller(t.Context(), p, decider)
	svc := newArtifactoryAPI(Config{Store: w.db, Blobs: w.blobs, Access: access})
	appendOnce := func() error {
		return svc.appendOnce(
			ctx,
			svc.builds(w.db),
			p,
			build.Append{Name: "app", Number: "1", Modules: []byte(`[]`)},
			`"replay"`,
		)
	}
	if err := appendOnce(); err != nil {
		t.Fatalf("first append: %v", err)
	}
	u, err := w.db.Users().Get(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	u.Admin = false
	if err := w.db.Users().Update(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	if err := appendOnce(); !errors.Is(err, build.ErrForbidden) {
		t.Fatalf("replay after the right was removed: %v", err)
	}
}
