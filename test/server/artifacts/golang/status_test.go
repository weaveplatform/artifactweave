package golang_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/blob"
	"github.com/weaveplatform/artifactweave/internal/platform/settings"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

const overwriter = "overwriter:pw"

type statusCase struct {
	route, path, as, body string
	want                  int
}

func (c statusCase) method() string { method, _, _ := strings.Cut(c.route, " "); return method }

type statusPart struct {
	handwritten map[string]bool
	seed        func(*testing.T, harness.Store, blob.Store)
	rows        func() []statusCase
}

func grantOverwriter(t *testing.T, db harness.Store, key, under string) {
	t.Helper()
	if err := db.Permissions().Create(t.Context(), accesstest.Permission{
		Name: "st-overwrite-" + key,
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{
				"overwriter": api.ActionRead | api.ActionWrite,
			}},
			Targets: map[string]accesstest.Target{key: {Include: []string{under + "**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

type goStatusWorld struct {
	h               http.Handler
	expired, forged string
}

func newGoStatusWorld(t *testing.T, anonymous bool) goStatusWorld {
	t.Helper()
	var tokens *token.Service
	h, db, _ := goServer(t, func(cfg *harness.Config) {
		tokens = cfg.Tokens
		if anonymous {
			if err := cfg.Store.Settings().
				Put(t.Context(), settings.SettingAnonymousAccess, true); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range append([]repo.Config{
			{Key: "libs-local", Class: repo.Local},
			{Key: "npm-local", Class: repo.Local, PackageType: repo.NPM},
		}, goRepos[1:]...) {
			if err := cfg.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
		}
		if err := cfg.Store.Users().Create(t.Context(), accesstest.User{
			Name: "overwriter", PasswordHash: cheapHash,
		}); err != nil {
			t.Fatal(err)
		}
		goStatusPart.seed(t, cfg.Store, cfg.Blobs)
	})
	past, err := token.New(t.Context(), db, token.Options{
		KeyBits: 1024, Now: func() time.Time { return time.Now().Add(-90 * time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	hour := time.Hour
	old, err := past.Issue(t.Context(), token.Request{Username: "admin", ExpiresIn: &hour})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := tokens.Issue(t.Context(), token.Request{Username: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	// Changing the final base64url character can alter only unused padding
	// bits and leave the signature bytes intact. Change the first signature
	// character so this is always a genuinely invalid token.
	dot := strings.LastIndexByte(valid.AccessToken, '.')
	if dot < 0 || dot+1 >= len(valid.AccessToken) {
		t.Fatal("issued token has no signature segment")
	}
	first := "A"
	if valid.AccessToken[dot+1] == 'A' {
		first = "B"
	}
	return goStatusWorld{
		h: h, expired: old.AccessToken,
		forged: valid.AccessToken[:dot+1] + first + valid.AccessToken[dot+2:],
	}
}

func TestAccGoStatusTable(t *testing.T) {
	off := newGoStatusWorld(t, false)
	on := newGoStatusWorld(t, true)
	for _, c := range goStatusPart.rows() {
		h := off.h
		var headers map[string]string
		switch c.as {
		case "anonymous":
			h = on.h
		case "admin", "user":
			headers = basic(c.as, "pw")
		case "expired":
			headers = bearerAuth(off.expired)
		case "forged":
			headers = bearerAuth(off.forged)
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
			t.Errorf("%s %s as %q: status %d, want %d: %s",
				c.method(), c.path, c.as, rec.Code, c.want, rec.Body)
		}
	}
}
