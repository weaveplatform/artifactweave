package access_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	appsql "github.com/weaveplatform/artifactweave/internal/app/adapters/sql"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/crypt"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/dialect"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

var errNativeIssuance = errors.New("controlled token persistence failure")

type nativeFailureStore struct {
	harness.Store
	fail    *atomic.Bool
	created *atomic.Value
}

func (s nativeFailureStore) Tokens() accesstest.TokenStore {
	return nativeFailureTokens{TokenStore: s.Store.Tokens(), fail: s.fail, created: s.created}
}

type nativeFailureTokens struct {
	accesstest.TokenStore
	fail    *atomic.Bool
	created *atomic.Value
}

func (s nativeFailureTokens) Create(ctx context.Context, token accesstest.Token) error {
	if err := s.TokenStore.Create(ctx, token); err != nil {
		return err
	}
	s.created.Store(token.ID)
	if s.fail.Load() {
		return errNativeIssuance
	}
	return nil
}

func TestAccNativeLoginIssuanceFailureSQL(t *testing.T) {
	keys, err := crypt.New(bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sqltest.RunModule(
		t,
		appsql.Modules(),
		func(s *dialect.Store) *appsql.Store { return appsql.New(s.DB(), s.Dialect(), keys) },
		func(t *testing.T, newStore func(*testing.T) *appsql.Store) {
			db := newStore(t)
			var fail atomic.Bool
			var created atomic.Value
			h := nativeSQLRouter(t, nativeFailureStore{Store: db, fail: &fail, created: &created})
			b := newNativeBrowser(t, h)
			b.open()
			b.login()
			b.approve()
			fail.Store(true)
			if w := b.request(http.MethodGet, b.poll, "", nil); w.Code != 400 {
				t.Fatal("issuance failure", w.Code)
			}
			failedID, ok := created.Load().(string)
			if !ok || failedID == "" {
				t.Fatal("fault never reached real token persistence")
			}
			if _, err := db.Tokens().Get(t.Context(), failedID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatal("failed token survived rollback", err)
			}
			tokens, _, err := db.Tokens().List(t.Context(), accesstest.TokenFilter{}, kernel.Page{Limit: 100})
			if err != nil || len(tokens) != 0 {
				t.Fatal("failed transaction retained a credential", len(tokens), err)
			}
			fail.Store(false)
			if w := b.request(http.MethodGet, b.poll, "", nil); w.Code != 400 {
				t.Fatal("failed issuance capability replayed", w.Code)
			}
			b = newNativeBrowser(t, h)
			b.open()
			b.login()
			b.approve()
			if w := b.request(http.MethodGet, b.poll, "", nil); w.Code != 200 {
				t.Fatal("fresh approval refused after repair", w.Code, w.Body)
			}
			tokens, _, err = db.Tokens().List(t.Context(), accesstest.TokenFilter{}, kernel.Page{Limit: 100})
			if err != nil || len(tokens) != 1 {
				t.Fatal("fresh approval must mint exactly once", len(tokens), err)
			}
		},
		sqltest.WithPostgres(),
		sqltest.WithKeyring(keys),
	)
}
