package upstream

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	"github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

// faultStore is the in-memory store with one of its reads, named by fail,
// answering err: "repository" Repos().Get, "proxy" Configs().Get,
// "proxies" Configs().List and "certificate" the certificates binding's
// Certificate.
type faultStore struct {
	*inmem.Store
	fail string
	err  error
}

func (s faultStore) Repos() port.RepoStore {
	return faultRepos{RepoStore: s.Store.Repos(), s: s}
}

func (s faultStore) Configs() configdoc.ConfigStore {
	return faultConfigs{ConfigStore: s.Store.Configs(), s: s}
}

type faultRepos struct {
	port.RepoStore
	s faultStore
}

func (r faultRepos) Get(ctx context.Context, key string) (repo.Config, error) {
	if r.s.fail == "repository" {
		return repo.Config{}, r.s.err
	}
	return r.RepoStore.Get(ctx, key) //nolint:wrapcheck // the store's own answer
}

type faultConfigs struct {
	configdoc.ConfigStore
	s faultStore
}

func (c faultConfigs) Get(
	ctx context.Context, kind configdoc.ConfigKind, key string,
) (configdoc.ConfigDocument, error) {
	if c.s.fail == "proxy" {
		return configdoc.ConfigDocument{}, c.s.err
	}
	return c.ConfigStore.Get(ctx, kind, key) //nolint:wrapcheck // the store's own answer
}

func (c faultConfigs) List(
	ctx context.Context,
	kind configdoc.ConfigKind,
) ([]configdoc.ConfigDocument, error) {
	if c.s.fail == "proxies" {
		return nil, c.s.err
	}
	return c.ConfigStore.List(ctx, kind) //nolint:wrapcheck // the store's own answer
}

type faultKeys struct {
	api.Keys
	s faultStore
}

func (k faultKeys) Certificate(ctx context.Context, alias string) (api.Certificate, error) {
	if k.s.fail == "certificate" {
		return api.Certificate{}, k.s.err
	}
	return k.Keys.Certificate(ctx, alias) //nolint:wrapcheck // the store's own answer
}

// storeFailure is a driver's error that the SQL store marks as its own
// failure, as dialect.Store.Wrap does.
type storeFailure struct{ err error }

func (e storeFailure) Error() string   { return e.err.Error() }
func (e storeFailure) Unwrap() []error { return []error{kernel.ErrStore, e.err} }

// TestUnitResolverKeepsTheStoresErrorBehindAFailedRead: a statement the
// store fails to run as Options reads the repository, its proxy (named or
// the platform default) or its client certificate fails the options with
// remote.ErrConfiguration, in its words, which remote.PolicyError answers
// as it did, and keeps the store's error for StoreFault, which the
// authority fence answers as the server's fault (#1489). Any other failed
// read keeps no error behind it.
func TestUnitResolverKeepsTheStoresErrorBehindAFailedRead(t *testing.T) {
	cause := storeFailure{errors.New("sqlite: get: disk I/O error")}
	refusal := fmt.Errorf("unseal: %w", errors.New("the key does not open the secret"))
	for _, fail := range []string{"repository", "proxy", "proxies", "certificate"} {
		for _, tc := range []struct{ err, kept error }{{cause, cause}, {refusal, nil}} {
			err := tc.err
			st := faultStore{Store: inmem.New(), fail: fail, err: err}
			c := repo.Config{Key: "fault", Class: repo.Remote, URL: "https://example.com"}
			switch fail {
			case "proxy":
				c.Proxy = "named"
			case "certificate":
				c.ClientTLSCertificate = "client"
			}
			saveRepo(t, st, c)
			r := resolverFor(t, st, ResolverOptions{
				Certificates: faultKeys{Keys: accesstest.Over(t, st).API().Keys, s: st},
			})
			_, got := r.Options(t.Context(), c)
			if !errors.Is(got, remote.ErrConfiguration) ||
				got.Error() != remote.ErrConfiguration.Error() ||
				errors.Is(got, kernel.ErrStore) {
				t.Fatalf("%s %v: %v, want remote.ErrConfiguration alone", fail, err, got)
			}
			if !reflect.DeepEqual(
				remote.PolicyError(got),
				remote.PolicyError(remote.ErrConfiguration),
			) {
				t.Fatalf("%s %v: PolicyError %v, want %v", fail, err, remote.PolicyError(got),
					remote.PolicyError(remote.ErrConfiguration))
			}
			if behind := StoreFault(got); !errors.Is(behind, tc.kept) {
				t.Fatalf("%s %v: StoreFault %v, want %v", fail, err, behind, tc.kept)
			}
		}
	}
	if StoreFault(nil) != nil || StoreFault(remote.ErrConfiguration) != nil {
		t.Fatal("StoreFault found a store's error where there is none")
	}
}
