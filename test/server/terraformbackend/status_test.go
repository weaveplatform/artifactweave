package terraformbackend_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type statusCase struct {
	route, path, as, body string
	want                  int
}

func (c statusCase) method() string { method, _, _ := strings.Cut(c.route, " "); return method }

type statusPart struct {
	handwritten map[string]bool
	seed        func(*testing.T, store.Store, blob.Store)
	rows        func() []statusCase
	tune        func(*Config)
}

type backendStatusWorld struct {
	h       http.Handler
	secrets map[string]string
}

func newBackendStatusWorld(
	t *testing.T,
	anonymous bool,
	tune ...func(*Config),
) backendStatusWorld {
	t.Helper()
	h, db, tokens := backendServer(t, func(cfg *Config) {
		if anonymous {
			if err := cfg.Store.Settings().
				Put(t.Context(), types.SettingAnonymousAccess, true); err != nil {
				t.Fatal(err)
			}
		}
		for _, part := range []statusPart{tfbeStatusPart} {
			if part.seed != nil {
				part.seed(t, cfg.Store, cfg.Blobs)
			}
			if part.tune != nil {
				part.tune(cfg)
			}
		}
		for _, fn := range tune {
			fn(cfg)
		}
	})
	past, err := token.New(t.Context(), db, token.Options{
		KeyBits: 1024, Now: func() time.Time { return time.Now().Add(-2 * time.Hour) },
	})
	if err != nil {
		t.Fatal(err)
	}
	hour := time.Hour
	admin, err := tokens.Issue(t.Context(), token.Request{
		Username: "admin", ByAdmin: true, ExpiresIn: &hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	expired, err := past.Issue(t.Context(), token.Request{
		Username: "admin", ByAdmin: true, Refreshable: true, ExpiresIn: &hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	forged := expired.AccessToken
	last := "A"
	if strings.HasSuffix(forged, last) {
		last = "B"
	}
	forged = forged[:len(forged)-1] + last
	return backendStatusWorld{h: h, secrets: map[string]string{
		"admin-token": admin.AccessToken, "expired": expired.AccessToken, "forged": forged,
	}}
}

func statusFixture(t *testing.T, anonymous bool, tune ...func(*Config)) http.Handler {
	t.Helper()
	return newBackendStatusWorld(t, anonymous, tune...).h
}

func TestTerraformBackendStatusTable(t *testing.T) {
	off := newBackendStatusWorld(t, false)
	on := newBackendStatusWorld(t, true)
	for _, part := range []statusPart{tfbeStatusPart} {
		for _, c := range part.rows() {
			h := off.h
			var headers map[string]string
			switch c.as {
			case "anonymous":
				h = on.h
			case "admin", "user":
				headers = basic(c.as, "pw")
			case "admin-token", "forged", "expired":
				headers = bearerAuth(off.secrets[c.as])
			default:
				if user, password, ok := strings.Cut(c.as, ":"); ok {
					headers = basic(user, password)
				}
			}
			body := c.body
			if body != "" {
				if headers == nil {
					headers = map[string]string{}
				}
				headers["Content-Type"], body = contentTypeOf(body)
			}
			rec := do(t, h, c.method(), c.path, headers, body)
			if rec.Code != c.want {
				t.Errorf(
					"%s %s as %q: status %d, want %d: %s",
					c.method(),
					c.path,
					c.as,
					rec.Code,
					c.want,
					rec.Body,
				)
			}
		}
	}
}
