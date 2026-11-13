package conan_test

import (
	"encoding/base64"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/core/authn"
	"github.com/weaveplatform/artifactweave/internal/access/core/token"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

// TestAccConanAuthenticateIgnoredBasic sends Conan's login an
// X-JFrog-Art-Api key that authenticates the request beside a Basic value
// 7.x reads no credential from (#1451), beside Basic credentials, and
// alone. The key authenticates the request whatever its Authorization
// header says, as 7.x's props filter does, so the Basic password beside it
// was never verified and is not exchanged: the login answers the key
// itself, as it answers a token sent as the Basic password (#1464, [I]:
// 7.x's Conan handler is not open source). Basic credentials without a key
// are exchanged for a token as before.
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
	const login = "/artifactory/api/conan/conan-local/v1/users/authenticate"
	encoded := base64.StdEncoding.EncodeToString([]byte("deployer:pw"))
	for name, value := range map[string]string{
		"lower-case basic": "basic " + encoded,
		"Basic Og==":       "Basic Og==",
		"Basic":            "Basic " + encoded,
		"wrong Basic":      "Basic " + base64.StdEncoding.EncodeToString([]byte("deployer:wrong")),
		"no Authorization": "",
	} {
		headers := []string{"X-JFrog-Art-Api", issued.ReferenceToken}
		if value != "" {
			headers = append(headers, "Authorization", value)
		}
		rec := w.do("GET", login, "", "", headers...)
		if rec.Code != 200 || rec.Body.String() != issued.ReferenceToken ||
			rec.Header().Get("Content-Type") != "text/plain" {
			t.Errorf("%s beside a key: %d %q, want 200 and the key", name, rec.Code, rec.Body)
		}
	}
	// Kept: Basic spelled exactly so, without a key, is exchanged for a
	// token.
	rec := w.do("GET", login, "", "", "Authorization", "Basic "+encoded)
	if rec.Code != 200 || rec.Body.Len() == 0 || rec.Body.String() == "pw" ||
		rec.Body.String() == issued.ReferenceToken {
		t.Errorf("Basic alone: %d %q, want 200 and a new token", rec.Code, rec.Body)
	}
}
