package docker_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/auth"
	"github.com/weaveplatform/artifactweave/internal/auth/token"
	"github.com/weaveplatform/artifactweave/internal/server"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
	"github.com/weaveplatform/artifactweave/test/server/registryfixture"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

type Config = server.Config

var (
	testHash  = support.TestHash
	cheapHash = support.CheapHash
)

func basic(user, password string) map[string]string { return support.Basic(user, password) }
func bearerAuth(value string) map[string]string     { return support.Bearer(value) }

func do(
	t *testing.T,
	h http.Handler,
	method, path string,
	headers map[string]string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return support.Do(t, h, method, path, headers, body)
}

func jsonAs(headers map[string]string) map[string]string {
	out := map[string]string{"Content-Type": "application/json"}
	for key, value := range headers {
		out[key] = value
	}
	return out
}

func decode(t *testing.T, body string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return value
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d: %s", rec.Code, want, rec.Body)
	}
}

type itemWorld struct {
	t     *testing.T
	h     http.Handler
	db    *inmem.Store
	blobs blob.Store
	w     *registryfixture.World
}

func newItemWorld(t *testing.T, anonymous bool, tune ...func(*Config)) *itemWorld {
	t.Helper()
	w := registryfixture.New(t, anonymous, tune...)
	return &itemWorld{t: t, h: w.Handler, db: w.DB, blobs: w.Blobs, w: w}
}

func (w *itemWorld) do(
	method, path, as, body string,
	headers ...string,
) *httptest.ResponseRecorder {
	w.t.Helper()
	return w.w.Do(method, path, as, body, headers...)
}

func (w *itemWorld) item(key string, cached bool, path string) types.Item {
	w.t.Helper()
	return w.w.Item(key, cached, path)
}

type statusSecrets map[string]string

func statusFixture(t *testing.T, anonymous bool, tune ...func(*Config)) http.Handler {
	t.Helper()
	h, _ := newStatusFixture(t, anonymous, tune...)
	return h
}

func newStatusFixture(
	t *testing.T,
	anonymous bool,
	tune ...func(*Config),
) (http.Handler, statusSecrets) {
	t.Helper()
	secrets := statusSecrets{}
	setup := func(c *Config) {
		ctx := t.Context()
		c.BaseURL = "http://example.com"
		if err := c.Store.Instance().Init(ctx, types.InstanceIdentity{
			InstanceID: "jfrt@01234567-89ab-4cde-8f01-23456789abcd", ProtocolVersion: 1,
		}); err != nil {
			t.Fatal(err)
		}
		tokens, err := token.New(ctx, c.Store, token.Options{KeyBits: 1024})
		if err != nil {
			t.Fatal(err)
		}
		past, err := token.New(ctx, c.Store, token.Options{
			KeyBits: 1024, Now: func() time.Time { return time.Now().Add(-2 * time.Hour) },
		})
		if err != nil {
			t.Fatal(err)
		}
		hour := time.Hour
		expired, err := past.Issue(ctx, token.Request{
			Username: "admin", Refreshable: true, ExpiresIn: &hour, ByAdmin: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		secrets["expired-access"] = expired.AccessToken
		parts := strings.Split(expired.AccessToken, ".")
		if len(parts) != 3 || len(parts[2]) == 0 {
			t.Fatal("expired token has no signature")
		}
		first := byte('A')
		if parts[2][0] == 'A' {
			first = 'B'
		}
		parts[2] = string(first) + parts[2][1:]
		secrets["forged-access"] = strings.Join(parts, ".")
		c.Tokens = tokens
		c.PasswordHash = testHash
		c.Auth, err = auth.NewAuthenticator(c.Store.Users(), c.Store.Settings(), auth.Options{
			Hash: testHash, Tokens: tokens,
		})
		if err != nil {
			t.Fatal(err)
		}
		seedRegistryStatus(t, c.Store, c.Blobs, secrets)
		seedRegistryManifests(t, c.Store, c.Blobs)
		seedRegistryDeletes(t, c.Store, c.Blobs)
	}
	w := registryfixture.New(t, anonymous, append([]func(*Config){setup}, tune...)...)
	return w.Handler, secrets
}
