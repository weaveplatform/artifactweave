package artifacts

import (
	"context"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// TestAccStorageSummaryAccess: an administrator, or a token scoped
// system:info/storage:r, reads the summary (getstoragesummaryinfo); the
// recalculation stays with administrators.
func TestAccStorageSummaryAccess(t *testing.T) {
	w := newIdentityWorld(t)
	if err := w.db.Users().Create(context.Background(), types.User{
		Name: "watcher", PasswordHash: cheapHash,
	}); err != nil {
		t.Fatal(err)
	}
	const (
		info = "/artifactory/api/storageinfo"
		calc = "/artifactory/api/storageinfo/calculate"
	)
	for _, c := range []struct {
		name     string
		creds    map[string]string
		expected int
	}{
		{"admin", admin, http.StatusOK},
		{"storage scope", w.bearer(t, "watcher", "system:info/storage:r"), http.StatusOK},
		// A scope on the whole info area covers its storage part.
		{"info scope", w.bearer(t, "watcher", "system:info:r"), http.StatusOK},
		{"other system area", w.bearer(t, "watcher", "system:metrics:r"), http.StatusForbidden},
		{"user's own token", w.bearer(t, "watcher", ""), http.StatusForbidden},
		{"password", basic("watcher", "pw"), http.StatusForbidden},
		{"no credentials", nil, http.StatusUnauthorized},
	} {
		if rec := do(t, w.h, http.MethodGet, info, c.creds, ""); rec.Code != c.expected {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
	scoped := w.bearer(t, "watcher", "system:info/storage:r")
	if rec := do(t, w.h, http.MethodPost, calc, scoped, ""); rec.Code != http.StatusForbidden {
		t.Errorf("calculate with the storage scope: %d %s", rec.Code, rec.Body)
	}
}

// A failure reading the caller's permissions is a server error, not a
// refusal.
func TestAccStorageSummaryAccessFailure(t *testing.T) {
	h := statusFixture(t, false, func(c *Config) {
		db := c.Store.(*inmem.Store) //nolint:forcetypeassert // fixture
		c.Store = faultyStore{Store: db, fail: map[string]bool{"permissions": true}}
	})
	rec := do(t, h, http.MethodGet, "/artifactory/api/storageinfo", basic("user", "pw"), "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a permission read that fails: %d %s", rec.Code, rec.Body)
	}
}
