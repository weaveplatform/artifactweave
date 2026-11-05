package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/authz"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/build"
	"github.com/weaveplatform/artifactweave/pkg/store"
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
	for _, p := range []auth.Principal{{}, auth.Anonymous, {Name: "registry", Registry: &types.RegistryAccess{}}} {
		if _, err := appendReplayRequest(`"key"`, p, build.Append{}); err == nil {
			t.Fatalf("unsupported principal accepted: %+v", p)
		}
	}
	if _, err := appendReplayRequest(
		`"key"`,
		auth.Principal{Name: "user", Password: true},
		build.Append{Started: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
	); err == nil {
		t.Fatal("unencodable scope accepted")
	}
}

func TestAccAppendIdempotencyCanonicalTimestamp(t *testing.T) {
	w := newItemWorld(t, false)
	w.publish(artifactDoc("app", "1", "2026-10-15T09:30:00.123456789Z", "artifact"))
	for _, started := range []string{"2026-10-15T09:30:00.123456789Z", "2026-10-15T09:30:00.123456Z"} {
		path := buildsPath + "/append/app/1?started=" + url.QueryEscape(started)
		wantStatus(
			t,
			w.do(
				http.MethodPost,
				path,
				"admin",
				`[{"id":"added"}]`,
				"Idempotency-Key",
				`"canonical"`,
			),
			http.StatusNoContent,
		)
	}
	b, err := w.db.Builds().Get(t.Context(), "", "app", "1", time.Time{})
	if err != nil || len(b.Modules) != 2 {
		t.Fatalf("equivalent timestamp replay: %v, %v", b.Modules, err)
	}
}

type replayDeadlineUsers struct{ store.UserStore }

func (u replayDeadlineUsers) Get(context.Context, string) (types.User, error) {
	return types.User{}, context.DeadlineExceeded
}

func TestAccAppendIdempotencyRecheckDeadline(t *testing.T) {
	w := newItemWorld(t, false)
	w.publish(artifactDoc("app", "1", testStarted, "artifact"))
	a, err := auth.NewAuthenticator(w.db.Users(), w.db.Settings(), auth.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Basic(t.Context(), "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	badAuth, err := auth.NewAuthenticator(
		replayDeadlineUsers{w.db.Users()},
		w.db.Settings(),
		auth.Options{Hash: testHash},
	)
	if err != nil {
		t.Fatal(err)
	}
	svc := artifactoryService{cfg: Config{Store: w.db, Blobs: w.blobs, Auth: badAuth}}
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
	a, err := auth.NewAuthenticator(w.db.Users(), w.db.Settings(), auth.Options{Hash: testHash})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Basic(t.Context(), "admin", "pw")
	if err != nil {
		t.Fatal(err)
	}
	p, decider, err := auth.Authorize(
		t.Context(),
		authz.Sources{Permissions: w.db.Permissions(), Groups: w.db.Groups()},
		p,
	)
	if err != nil || !decider.IsAdmin() {
		t.Fatalf("request decider: %v", err)
	}
	ctx := auth.WithAuthorized(t.Context(), p, decider)
	svc := artifactoryService{cfg: Config{Store: w.db, Blobs: w.blobs, Auth: a}}
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

func TestAccAppendIdempotency(t *testing.T) {
	w := newItemWorld(t, false)
	w.publish(artifactDoc("team/app", "1", testStarted, "artifact"))
	path := buildsPath + "/append/team%2Fapp/1"
	body := `[{"id":"added","unlisted":"preserved"}]`
	for range 3 {
		wantStatus(
			t,
			w.do(http.MethodPost, path, "admin", body, "Idempotency-Key", `"key"`),
			http.StatusNoContent,
		)
	}
	b, err := w.db.Builds().Get(t.Context(), "", "team/app", "1", time.Time{})
	if err != nil || len(b.Modules) != 2 {
		t.Fatalf("replayed append modules: %v, %v", b.Modules, err)
	}
	wantStatus(
		t,
		w.do(http.MethodPost, path, "admin", `[]`, "Idempotency-Key", `"key"`),
		http.StatusUnprocessableEntity,
	)
	wantStatus(
		t,
		w.do(http.MethodPost, path, "admin", body, "Idempotency-Key", "unquoted"),
		http.StatusBadRequest,
	)
	// The legacy unkeyed operation still appends every time.
	wantStatus(t, w.do(http.MethodPost, path, "admin", body), http.StatusNoContent)
	b, err = w.db.Builds().Get(t.Context(), "", "team/app", "1", time.Time{})
	if err != nil || len(b.Modules) != 3 {
		t.Fatalf("unkeyed append: %v, %v", b.Modules, err)
	}
	u, err := w.db.Users().Get(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	u.Admin = false
	if err := w.db.Users().Update(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	wantStatus(
		t,
		w.do(http.MethodPost, path, "admin", body, "Idempotency-Key", `"key"`),
		http.StatusForbidden,
	)
}

func TestAccUnsupportedIdempotencyKeys(t *testing.T) {
	w := newItemWorld(t, false)
	for _, path := range []string{"/access/api/v1/tokens", "/artifactory/api/security/token", buildsPath, "/unknown"} {
		wantStatus(
			t,
			w.do(http.MethodPost, path, "admin", `{}`, "Idempotency-Key", `"key"`),
			http.StatusBadRequest,
		)
	}
	req := httptest.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		buildsPath+"/append/app/1",
		strings.NewReader(`[]`),
	)
	req.Header.Add("Idempotency-Key", `"one"`)
	req.Header.Add("Idempotency-Key", `"two"`)
	rec := httptest.NewRecorder()
	w.h.ServeHTTP(rec, req)
	wantStatus(t, rec, http.StatusBadRequest)
}
