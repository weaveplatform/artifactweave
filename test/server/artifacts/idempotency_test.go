package artifacts

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

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
