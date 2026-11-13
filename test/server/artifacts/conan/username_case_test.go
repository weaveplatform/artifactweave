package conan_test

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// TestAccConanAuthenticateUsernameCase: Conan's login takes the Basic user
// name in any case, as every other route does (#1432). The Conan client
// sends the name as the user typed it, so `conan remote login aw BOB` must
// get bob's token, and a token sent back as the password is echoed under
// any spelling of its own user but never under another user's name.
func TestAccConanAuthenticateUsernameCase(t *testing.T) {
	w := newItemWorld(t, false, func(cfg *Config) {
		cfg.Tokens, _ = seedTokens(t, cfg.Store)
		var err error
		cfg.Auth, err = authn.NewAuthenticator(
			cfg.Store.Users(),
			cfg.Store.Settings(),
			authn.Options{Hash: testHash, Tokens: cfg.Tokens},
		)
		if err != nil {
			t.Fatal(err)
		}
	})
	c := repo.Config{Key: "conan-local", Class: repo.Local, PackageType: repo.Conan}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	for _, gen := range []string{"v1", "v2"} {
		base := "/artifactory/api/conan/conan-local/" + gen + "/users/"
		for _, as := range []string{"deployer", "DEPLOYER", "Deployer"} {
			t.Run(gen+"/"+as, func(t *testing.T) {
				rec := w.do("GET", base+"authenticate", as, "")
				if rec.Code != 200 {
					t.Fatalf("authenticate as %s: %d %s", as, rec.Code, rec.Body)
				}
				if got := rec.Header().Get("Content-Type"); got != "text/plain" {
					t.Errorf("Content-Type %q", got)
				}
				if got := rec.Header().Get("Cache-Control"); got != "no-store" {
					t.Errorf("Cache-Control %q", got)
				}
				token := rec.Body.String()
				if strings.TrimSpace(token) == "" || token == "pw" {
					t.Fatalf("authenticate as %s answered no token: %q", as, token)
				}
				check := support.Do(t, w.h, "GET", base+"check_credentials",
					support.Bearer(token), "")
				if check.Code != 200 || check.Body.String() != "deployer" {
					t.Errorf("the token's user: %d %q, want 200 deployer",
						check.Code, check.Body)
				}
				echo := support.Do(t, w.h, "GET", base+"authenticate",
					support.Basic(strings.ToUpper(as), token), "")
				if echo.Code != 200 || echo.Body.String() != token {
					t.Errorf("the token as the password of %s: %d %q",
						strings.ToUpper(as), echo.Code, echo.Body)
				}
				other := support.Do(t, w.h, "GET", base+"authenticate",
					support.Basic("user", token), "")
				if other.Code != 401 {
					t.Errorf("the token under another user's name: %d %s",
						other.Code, other.Body)
				}
			})
		}
	}
}
