package accesshttp

import (
	"context"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/core/port"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/test/server/support"
)

// keysFaultStore fails the key store's read or field write with errBoom.
type keysFaultStore struct {
	*inmem.Store
	read, write bool
}

func (s keysFaultStore) Keys() port.KeyStore { return keysFault{s.Store.Keys(), s} }

type keysFault struct {
	port.KeyStore
	s keysFaultStore
}

func (k keysFault) Keypair(ctx context.Context, name string) (api.Keypair, error) {
	if k.s.read {
		return api.Keypair{}, errBoom
	}
	return k.KeyStore.Keypair(ctx, name) //nolint:wrapcheck // test double
}

func (k keysFault) SetKeypairField(ctx context.Context, name string, f port.KeypairField, v string) error {
	if k.s.write {
		return errBoom
	}
	return k.KeyStore.SetKeypairField(ctx, name, f, v) //nolint:wrapcheck // test double
}

// TestAccInstanceGPGKeyStoreFaults: a key store that cannot be read or
// written answers 500 from every /api/gpg operation, never a 404 that would
// say no key is set or a 200 that would say one was stored (#1654).
func TestAccInstanceGPGKeyStoreFaults(t *testing.T) {
	ctx := asAdmin(context.Background())
	passphrase := artgen.SetGpgPassPhraseRequestObject{Params: artgen.SetGpgPassPhraseParams{XGPGPASSPHRASE: "secret"}}
	for name, st := range map[string]keysFaultStore{
		"read":  {Store: inmem.New(), read: true},
		"write": {Store: inmem.New(), write: true},
	} {
		s := New(Deps{Store: st, Repositories: support.Repositories(st.Store)}).Security
		_, err := s.SetGpgPassPhrase(ctx, passphrase)
		if got := errStatus(err); got != http.StatusInternalServerError {
			t.Errorf("%s: setGpgPassPhrase answered %d: %v", name, got, err)
		}
		if !st.read {
			continue
		}
		_, err = s.GetGpgPublicKey(ctx, artgen.GetGpgPublicKeyRequestObject{})
		if got := errStatus(err); got != http.StatusInternalServerError {
			t.Errorf("%s: getGpgPublicKey answered %d: %v", name, got, err)
		}
	}
}
