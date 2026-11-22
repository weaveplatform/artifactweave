package artifacts

import (
	"context"
	"errors"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/pull"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitPullBindingRefreshesCurrentCallerAndRefusesUnusableRepositories(t *testing.T) {
	for _, mode := range []string{"remote", "unopened", "alias", "project-revoked", "credential-revoked", "anonymous", "registry caller", "missing access contracts", "authorizer failed", "missing", "denied", "local"} {
		t.Run(mode, func(t *testing.T) {
			db := inmem.New()
			config := *repo.Defaults(&repo.Config{Key: "remote", Class: repo.Remote, PackageType: repo.Generic, URL: "https://upstream.example/artifactory/source"})
			if mode == "local" {
				config.Class = repo.Local
			}
			if err := db.Repos().Create(t.Context(), config); err != nil {
				t.Fatal(err)
			}
			projects, authn := &accessapitest.Projects{}, &accessapitest.Authenticator{}
			authorizer := &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
				return &accessapitest.Decider{Admin: mode != "denied"}, nil
			}}
			m := &Module{store: db, remoteSources: &source.Service{}, access: accessapi.API{Projects: projects, Authenticator: authn, Authorizer: authorizer}}
			ctx := accessapi.WithCaller(t.Context(), kernel.Principal{Name: "current-reader", TokenID: "verified-reference"}, nil)
			key, want := "remote", error(nil)
			switch mode {
			case "anonymous":
				ctx, want = t.Context(), kernel.ErrUnauthenticated
			case "registry caller":
				ctx = accessapi.WithCaller(t.Context(), kernel.Principal{Name: "registry", TokenID: "verified-reference", Registry: &kernel.RegistryAccess{}}, nil)
				want = kernel.ErrForbidden
			case "missing access contracts":
				m.access.Authenticator, want = nil, kernel.ErrStore
			case "authorizer failed":
				authorizer.DecideFunc = func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) { return nil, kernel.ErrStore }
				want = kernel.ErrStore
			case "unopened":
				m.remoteSources, want = nil, source.ErrUnavailable
			case "alias":
				key, want = "remote-cache", source.ErrInvalidRequest
			case "project-revoked":
				projects.Err, want = kernel.ErrForbidden, kernel.ErrForbidden
			case "credential-revoked":
				authn.CurrentFunc = func(context.Context, kernel.Principal) (kernel.Principal, error) {
					return kernel.Principal{}, kernel.ErrBadCredentials
				}
				want = kernel.ErrBadCredentials
			case "missing":
				key, want = "missing", kernel.ErrNotFound
			case "denied":
				want = kernel.ErrForbidden
			case "local":
				want = source.ErrInvalidRequest
			}
			svc, request, err := m.currentPullSource(ctx, key, "selected")
			if want == nil {
				if err != nil || svc != m.remoteSources || request.Repo.Key != key || request.Subject.Name != "current-reader" || request.Access == nil {
					t.Fatal("current caller/configuration were not bound", err)
				}
			} else if !errors.Is(err, want) || svc != nil || request.Repo.Key != "" {
				t.Fatal("refused owner binding exposed usable configuration", err)
			}
		})
	}
}

type pullPresencePeer struct {
	pull.Peer
	present bool
	err     error
}

func (p pullPresencePeer) Absent(context.Context, string) (bool, error) { return !p.present, p.err }

func TestUnitNativePullCannotInferRegistryAbsenceFromUnavailableStorageOrRuntime(t *testing.T) {
	for _, mode := range []string{"storage-error", "present", "no-registry-runtime", "wrong-registry-backend", "registry probe refused", "transaction"} {
		t.Run(mode, func(t *testing.T) {
			db := inmem.New()
			m := &Module{store: db}
			peer := pullPresencePeer{present: mode == "present"}
			if mode == "storage-error" {
				peer.err = kernel.ErrStore
			}
			if mode == "wrong-registry-backend" || mode == "transaction" {
				m.registrySources = &source.Service{}
			}
			if mode == "registry probe refused" {
				m.registrySources = &source.Service{HTTP: &docker.RegistryContent{}}
			}
			p := nativePullPeer{Peer: peer, module: m}
			check := func(ctx context.Context) error {
				absent, err := p.Absent(ctx, "image/latest/manifest.json")
				if mode == "present" {
					if absent || err != nil {
						t.Fatal("storage presence became registry absence", err)
					}
				} else if absent || err == nil {
					t.Fatal("unavailable registry authority became confirmed absence", err)
				}
				return nil
			}
			if mode == "transaction" {
				if err := db.Tx(t.Context(), check); err != nil {
					t.Fatal(err)
				}
			} else if err := check(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnitPullRuntimeRefusesUnsupportedNativeFilesAndInvalidPeerBindings(t *testing.T) {
	m := &Module{}
	if _, err := m.readPullContent(t.Context(), source.Request{}); !errors.Is(err, source.ErrUnavailable) {
		t.Fatal(err)
	}
	q := source.Request{Repo: repo.Config{Class: repo.Remote, PackageType: repo.Docker}}
	q.Resource.Path = "unknown-file"
	if _, err := m.readPullContent(t.Context(), q); !errors.Is(err, source.ErrUnsupported) {
		t.Fatal(err)
	}
	q.Resource.Path = "image/latest/manifest.json"
	if _, err := m.readPullContent(t.Context(), q); !errors.Is(err, source.ErrUnavailable) {
		t.Fatal(err)
	}
	for _, kind := range []repo.PackageType{repo.Generic, repo.Docker} {
		q.Repo.PackageType = kind
		q.Repo.URL = "https://invalid.example/artifactory/source?foreign=operation"
		if _, err := m.openPullPeer(t.Context(), &source.Service{}, q, "", remote.DiscoveryScope{}); err == nil {
			t.Fatal("ambiguous peer was bound")
		}
	}
}
