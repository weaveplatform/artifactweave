package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/service/item"
)

// Token lifecycle tests need real identities, permissions, signing keys and one
// readable artifact. The route-status fixture also writes every package format,
// which needlessly repeats archive/signature and filesystem sync work here.
func tokenFixture(t *testing.T, tune ...func(*Config)) http.Handler {
	t.Helper()
	h, _ := newTokenFixture(t, tune...)
	return h
}

func newTokenFixture(t *testing.T, tune ...func(*Config)) (http.Handler, statusSecrets) {
	t.Helper()
	var secrets statusSecrets
	options := []func(*Config){func(c *Config) {
		c.Tokens, secrets = seedTokens(t, c.Store)
		var err error
		c.Auth, err = auth.NewAuthenticator(c.Store.Users(), c.Store.Settings(), auth.Options{
			Hash: testHash, Tokens: c.Tokens,
		})
		if err != nil {
			t.Fatal(err)
		}
		c.PasswordHash = testHash
		libs, err := c.Store.Repos().Get(t.Context(), "libs-local")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = (&item.Service{Store: c.Store, Blobs: c.Blobs}).Deploy(t.Context(), item.Deploy{
			Target: item.Target{
				Repo: libs,
				Path: "fixture/get.txt",
			},
			Body: strings.NewReader("fixture"),
		}); err != nil {
			t.Fatal(err)
		}
	}}
	options = append(options, tune...)
	w := newItemWorld(t, false, options...)
	return w.h, secrets
}
