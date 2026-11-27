package federation_test

import (
	"context"
	"testing"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type cleanupMetadataFault struct {
	artifactsapi.CleanupMetadata
	repositoryAt, downloadsAt, repositories, downloads int
}

func (m *cleanupMetadataFault) Repository(ctx context.Context, key string) (artifactsapi.CleanupRepository, error) {
	m.repositories++
	if m.repositories == m.repositoryAt {
		return artifactsapi.CleanupRepository{}, errCoordinationPersistence
	}
	return m.CleanupMetadata.Repository(ctx, key)
}

func (m *cleanupMetadataFault) Downloads(
	ctx context.Context,
	repo artifactsapi.CleanupRepository,
	versions []artifactsapi.DownloadVersion,
) ([]artifactsapi.VersionDownload, error) {
	m.downloads++
	if m.downloads == m.downloadsAt {
		return nil, errCoordinationPersistence
	}
	return m.CleanupMetadata.Downloads(ctx, repo, versions)
}

type cleanupInstanceFault struct{ instance.InstanceStore }

func (s cleanupInstanceFault) ID(context.Context) (string, error) {
	return "", errCoordinationPersistence
}

type cleanupConfigFault struct{ configdoc.ConfigStore }

func (s cleanupConfigFault) Get(context.Context, configdoc.ConfigKind, string) (configdoc.ConfigDocument, error) {
	return configdoc.ConfigDocument{}, errCoordinationPersistence
}

type cleanupMemberFault struct{ port.FederationStore }

func (s cleanupMemberFault) Members(context.Context, string) ([]model.FederationMember, error) {
	return nil, errCoordinationPersistence
}

type cleanupPeerFault struct {
	port.PeerStore
	port.PeerRevisions
	getAt, gets int
	guard       bool
}

func (s *cleanupPeerFault) Get(ctx context.Context, key string) (model.Peer, error) {
	s.gets++
	if s.gets == s.getAt {
		return model.Peer{}, errCoordinationPersistence
	}
	return s.PeerStore.Get(ctx, key)
}

func (s *cleanupPeerFault) GuardRevision(ctx context.Context, key string, rev port.PeerRevision) error {
	if s.guard {
		return errCoordinationPersistence
	}
	return s.PeerRevisions.GuardRevision(ctx, key, rev)
}

type cleanupDependencyFault struct {
	cleanupWorldStore
	mode  string
	peers *cleanupPeerFault
}

func (s cleanupDependencyFault) Instance() instance.InstanceStore {
	if s.mode == "instance" {
		return cleanupInstanceFault{s.cleanupWorldStore.Instance()}
	}
	return s.cleanupWorldStore.Instance()
}

func (s cleanupDependencyFault) Configs() configdoc.ConfigStore {
	if s.mode == "config" {
		return cleanupConfigFault{s.cleanupWorldStore.Configs()}
	}
	return s.cleanupWorldStore.Configs()
}

func (s cleanupDependencyFault) Federation() port.FederationStore {
	if s.mode == "members" {
		return cleanupMemberFault{s.cleanupWorldStore.Federation()}
	}
	return s.cleanupWorldStore.Federation()
}

func (s cleanupDependencyFault) Peers() port.PeerStore {
	if s.mode == "missing_revisions" {
		return struct{ port.PeerStore }{s.cleanupWorldStore.Peers()}
	}
	return s.peers
}

// A failed metadata/authority read is never equivalent to an authenticated
// empty observation. Every refusal returns no signed facts and leaves the real
// content and ordinary admissions available for the subsequent healthy owner.
func TestAccCleanupMetadataFailuresDoNotDiscloseOrAuthorize(t *testing.T) {
	for _, phase := range []string{"scope", "guard", "downloads", "collect", "prepare"} {
		modes := []string{"repository", "downloads"}
		switch phase {
		case "scope":
			modes = []string{"instance", "config", "members", "missing_revisions"}
		case "guard":
			modes = []string{"repository", "downloads", "peer_guard"}
		case "downloads":
			modes = []string{"peer_read", "scope_repository", "guard_repository", "repository", "downloads"}
		case "collect":
			modes = []string{"peer_read"}
		}
		for _, mode := range modes {
			t.Run(phase+"/"+mode, func(t *testing.T) {
				w := newWorld(t, 1, 1)
				ctx := t.Context()
				evidence := w.collect(t)
				caller := storekit.Get[model.Peer](t)(w.store.Peers().Get(ctx, evidence.Scope.Members[0].Instance))
				meta := &cleanupMetadataFault{CleanupMetadata: w.service.Metadata}
				peers := &cleanupPeerFault{PeerStore: w.store.Peers(), PeerRevisions: w.store.Peers().(port.PeerRevisions)}
				dependencies := cleanupDependencyFault{cleanupWorldStore: w.store, mode: mode, peers: peers}
				var id string
				if phase == "prepare" {
					storekit.Must(t, w.store.Tx(ctx, func(ctx context.Context) error {
						var err error
						id, err = w.service.Reserve(ctx, evidence, 7, w.firstItem)
						return err
					}))
				}
				if phase == "prepare" {
					w.service.Remote = coordinationOutcomeRemote{w.service.Remote, func(_ context.Context, r cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error) {
						return cleanup.CoordinationAnswer{State: "prepared", Exclusion: r.Exclusion}, nil
					}}
				}
				w.service.Store = dependencies
				w.service.Metadata = meta
				switch mode {
				case "repository":
					meta.repositoryAt = 2
					if phase == "downloads" {
						meta.repositoryAt = 3
					}
					if phase == "prepare" {
						meta.repositoryAt = 1
					}
				case "scope_repository":
					meta.repositoryAt = 1
				case "guard_repository":
					meta.repositoryAt = 2
				case "downloads":
					meta.downloadsAt = 1
				case "peer_guard":
					peers.guard = true
				case "peer_read":
					peers.getAt = 1
					if phase == "collect" {
						peers.getAt = 2
					}
				}
				var err error
				switch phase {
				case "scope":
					scope, readErr := w.service.Scope(ctx, "packages")
					err = readErr
					if scope.Repository != "" {
						t.Fatal("failed scope disclosed partially trusted identity")
					}
				case "guard":
					err = w.store.Tx(ctx, func(ctx context.Context) error { return w.service.Guard(ctx, evidence) })
				case "downloads":
					answer, readErr := w.service.Downloads(
						ctx,
						caller,
						"packages",
						cleanup.Request{Challenge: "fresh-observation-challenge", SourceRepository: "remote", PackageType: evidence.Scope.PackageType, Versions: w.versions},
					)
					err = readErr
					if answer.Instance != "" || len(answer.Versions) != 0 {
						t.Fatal("failed observation disclosed trusted metadata")
					}
				case "collect":
					proof, readErr := w.service.Collect(ctx, evidence.Scope, w.versions)
					err = readErr
					if len(proof.Pins) != 0 {
						t.Fatal("failed collection disclosed usable evidence")
					}
				case "prepare":
					_, err = w.service.Prepare(ctx, id)
				}
				expected := errCoordinationPersistence
				if mode == "missing_revisions" {
					expected = kernel.ErrStore
				}
				storekit.WantErr(t, err, expected)
				w.service.Store = w.store
				w.service.Metadata = meta.CleanupMetadata
				if phase == "prepare" {
					storekit.Must(t, w.store.Tx(ctx, func(ctx context.Context) error { return w.service.Abort(ctx, id) }))
					w.service.Remote = coordinationOutcomeRemote{w.service.Remote, func(_ context.Context, r cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error) {
						return cleanup.CoordinationAnswer{State: string(model.CleanupAborted), Exclusion: r.Exclusion}, nil
					}}
					storekit.Must(t, w.service.Finish(ctx, id))
				}
				_, err = w.artifacts.Items().GetByID(ctx, w.firstItem)
				storekit.Must(t, err)
				pin := storekit.Get[artifactsapi.CleanupAdmission](t)(w.service.Exclusions.Admit(ctx, "packages", w.firstItem))
				storekit.Must(t, w.service.Exclusions.Complete(ctx, pin))
			})
		}
	}
}
