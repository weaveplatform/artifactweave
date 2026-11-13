package conan_test

import (
	"encoding/base64"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestAccConanAuthenticateIgnoredBasic sends Conan's login a Basic value
// 7.x reads no credential from beside an X-JFrog-Art-Api key that
// authenticates the request (#1451). The exchange reads Basic as the
// authentication does, so it finds no Basic credential to exchange and
// answers as it does any caller without one, rather than exchanging the
// lower-case value's password, which nothing verified. A Basic credential
// the authentication read is exchanged as before.
func TestAccConanAuthenticateIgnoredBasic(t *testing.T) {
	var tokens *token.Service
	w := newItemWorld(t, false, func(cfg *Config) {
		tokens, _ = seedTokens(t, cfg.Store)
		cfg.Tokens = tokens
		var err error
		cfg.Auth, err = authn.NewAuthenticator(
			cfg.Store.Users(),
			cfg.Store.Settings(),
			authn.Options{Hash: testHash, Tokens: tokens},
		)
		if err != nil {
			t.Fatal(err)
		}
	})
	c := repo.Config{Key: "conan-local", Class: repo.Local, PackageType: repo.Conan}
	if err := w.db.Repos().Create(t.Context(), *repo.Defaults(&c)); err != nil {
		t.Fatal(err)
	}
	issued, err := tokens.Issue(t.Context(), token.Request{
		Username: "deployer", IncludeReference: true, ForceRevocable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	const (
		login    = "/artifactory/api/conan/conan-local/v1/users/authenticate"
		required = `{"errors":[{"message":"Authentication is required","status":401}]}` + "\n"
	)
	encoded := base64.StdEncoding.EncodeToString([]byte("deployer:pw"))
	for name, value := range map[string]string{
		"lower-case basic": "basic " + encoded,
		"Basic Og==":       "Basic Og==",
	} {
		rec := w.do("GET", login, "", "",
			"Authorization", value, "X-JFrog-Art-Api", issued.ReferenceToken)
		if rec.Code != 401 || rec.Body.String() != required {
			t.Errorf("%s beside a key: %d %q, want 401 %q", name, rec.Code, rec.Body, required)
		}
	}
	// Kept: Basic spelled exactly so is exchanged for a token.
	rec := w.do("GET", login, "", "",
		"Authorization", "Basic "+encoded, "X-JFrog-Art-Api", issued.ReferenceToken)
	if rec.Code != 200 || rec.Body.Len() == 0 || rec.Body.String() == "pw" {
		t.Errorf("Basic beside a key: %d %q, want 200 and a token", rec.Code, rec.Body)
	}
}
