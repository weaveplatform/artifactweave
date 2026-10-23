package npm_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type npmStatusWorld struct {
	h       http.Handler
	secrets map[string]string
}

func newNPMStatusWorld(t *testing.T, anonymous bool) npmStatusWorld {
	t.Helper()
	h, _, tokens := npmServer(t, func(cfg *server.Config) {
		if anonymous {
			if err := cfg.Store.Settings().
				Put(t.Context(), types.SettingAnonymousAccess, true); err != nil {
				t.Fatal(err)
			}
		}
		if err := cfg.Store.Repos().
			Create(t.Context(), *repo.Defaults(&repo.Config{Key: "libs-local", Class: repo.Local})); err != nil {
			t.Fatal(err)
		}
		for _, c := range npmRepos[1:] {
			if err := cfg.Store.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
				t.Fatal(err)
			}
		}
		for _, part := range []statusPart{npmStatusPart, npmAuthStatusPart, npmLifecycleStatusPart} {
			if part.seed != nil {
				part.seed(t, cfg.Store, cfg.Blobs)
			}
		}
	})
	secrets := map[string]string{}
	for _, user := range []string{"admin", "user"} {
		out, err := tokens.Issue(t.Context(), token.Request{Username: user})
		if err != nil {
			t.Fatal(err)
		}
		secrets[user+"-token"] = out.AccessToken
	}
	return npmStatusWorld{h: h, secrets: secrets}
}

func TestNPMStatusTable(t *testing.T) {
	off := newNPMStatusWorld(t, false)
	on := newNPMStatusWorld(t, true)
	for _, part := range []statusPart{npmStatusPart, npmAuthStatusPart, npmLifecycleStatusPart} {
		for _, c := range part.rows() {
			h := off.h
			var headers map[string]string
			switch c.as {
			case "anonymous":
				h = on.h
			case "admin", "user":
				headers = basic(c.as, "pw")
			case "admin-token", "user-token":
				headers = bearerAuth(off.secrets[c.as])
			case "forged":
				headers = bearerAuth("invalid.token.signature")
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
}
