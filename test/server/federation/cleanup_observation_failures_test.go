package federation_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/federation/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

type (
	noRevisions       struct{ port.PeerStore }
	peerStoreOverride struct {
		cleanup.Store
		peers port.PeerStore
	}
)

func (s peerStoreOverride) Peers() port.PeerStore { return s.peers }

type failedRevisions struct {
	port.PeerStore
	port.PeerRevisions
}

func (p failedRevisions) Revision(context.Context, string) (port.PeerRevision, error) {
	return port.PeerRevision{}, kernel.ErrStore
}

type metadataFailure struct {
	artifactsapi.CleanupMetadata
	repository, downloads error
}

func (m metadataFailure) Repository(ctx context.Context, key string) (artifactsapi.CleanupRepository, error) {
	if m.repository != nil {
		return artifactsapi.CleanupRepository{}, m.repository
	}
	return m.CleanupMetadata.Repository(ctx, key)
}

func (m metadataFailure) Downloads(ctx context.Context, scope artifactsapi.CleanupRepository, versions []artifactsapi.DownloadVersion) ([]artifactsapi.VersionDownload, error) {
	if m.downloads != nil {
		return nil, m.downloads
	}
	return m.CleanupMetadata.Downloads(ctx, scope, versions)
}

func TestUnitCleanupScopeRefusals(t *testing.T) {
	for _, name := range []string{"no_metadata", "metadata_error", "local_class", "missing_base", "self_disabled", "member_disabled", "invalid_member", "duplicate_member", "too_many_members", "missing_self", "missing_member", "missing_peer", "incompatible_peer", "no_revision_port", "revision_error", "extra_resolved_member"} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, 1, 1)
			cfg := storekit.Get[repo.Config](t)(w.artifacts.Repos().Get(t.Context(), "packages"))
			switch name {
			case "no_metadata":
				w.service.Metadata = nil
			case "metadata_error":
				w.service.Metadata = metadataFailure{CleanupMetadata: w.service.Metadata, repository: kernel.ErrStore}
			case "local_class":
				cfg.Class = repo.Local
			case "missing_base":
				w.service.BaseURL = ""
			case "self_disabled":
				cfg.Members[0].Enabled = repo.Flex(false)
			case "member_disabled":
				cfg.Members[1].Enabled = repo.Flex(false)
			case "invalid_member":
				cfg.Members[1].URL = "https://bad.example/no/repository?credential=hidden"
			case "duplicate_member":
				cfg.Members = append(cfg.Members, cfg.Members[1])
			case "too_many_members":
				cfg.Members = make([]repo.Member, repo.MaxMembers+1)
			case "missing_self":
				cfg.Members = cfg.Members[1:]
			case "missing_member":
				storekit.Must(t, w.store.Federation().SetMembers(t.Context(), cfg.Key, nil))
			case "missing_peer":
				storekit.Must(t, w.store.Peers().Delete(t.Context(), "peer-00"))
			case "incompatible_peer":
				storekit.Must(t, w.store.Peers().SetProtocolVersion(t.Context(), "peer-00", 2))
			case "no_revision_port":
				w.service.Store = peerStoreOverride{Store: w.store, peers: noRevisions{w.store.Peers()}}
			case "revision_error":
				peers := w.store.Peers()
				w.service.Store = peerStoreOverride{Store: w.store, peers: failedRevisions{peers, peers.(port.PeerRevisions)}}
			case "extra_resolved_member":
				members := storekit.Get[[]model.FederationMember](t)(w.store.Federation().Members(t.Context(), cfg.Key))
				members = append(members, model.FederationMember{PeerInstanceID: "unexpected", Enabled: true})
				storekit.Must(t, w.store.Federation().SetMembers(t.Context(), cfg.Key, members))
			}
			storekit.Must(t, w.artifacts.Repos().Update(t.Context(), cfg))
			if _, err := w.service.Scope(t.Context(), cfg.Key); err == nil {
				t.Fatal("unresolved current scope was accepted")
			}
		})
	}
	if _, err := (&cleanup.Service{}).Scope(t.Context(), "packages"); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("missing owner accepted a scope", err)
	}
}

func TestUnitCleanupCollectionRefusals(t *testing.T) {
	for _, name := range []string{"empty_batch", "excessive_batch", "duplicate_batch", "invalid_name", "inside_transaction", "no_remote", "downloads_error", "recreated_local", "absent_local", "changed_during_probe", "duplicate_remote", "unrequested_remote", "invalid_remote_ID", "future_download", "future_creation", "unknown_timestamps", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, 1, 2)
			scope := storekit.Get[api.CleanupScope](t)(w.service.Scope(t.Context(), "packages"))
			versions := w.versions
			ctx := t.Context()
			switch name {
			case "empty_batch":
				versions = nil
			case "excessive_batch":
				versions = make([]api.CleanupVersion, api.CleanupBatchSize+1)
			case "duplicate_batch":
				versions = []api.CleanupVersion{versions[0], versions[0]}
			case "invalid_name":
				versions = []api.CleanupVersion{{Package: strings.Repeat("x", 4097), Version: "1"}}
			case "inside_transaction":
				storekit.Must(t, w.store.Tx(ctx, func(ctx context.Context) error {
					_, err := w.service.Collect(ctx, scope, versions)
					storekit.WantErr(t, err, cleanup.ErrUnavailable)
					return nil
				}))
				return
			case "no_remote":
				w.service.Remote = nil
			case "downloads_error":
				w.service.Metadata = metadataFailure{CleanupMetadata: w.service.Metadata, downloads: kernel.ErrStore}
			case "recreated_local":
				scope.Incarnation = "previous-lifetime"
			case "absent_local":
				versions = []api.CleanupVersion{{Package: "absent", Version: "1"}}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			default:
				w.service.Remote = remoteFunc(func(ctx context.Context, p model.Peer, _ string, r cleanup.Request) (cleanup.Answer, error) {
					out := answer(p, r)
					switch name {
					case "changed_during_probe":
						if err := w.store.Peers().Put(ctx, p); err != nil {
							return cleanup.Answer{}, err
						}
					case "duplicate_remote":
						out.Versions[1] = out.Versions[0]
					case "unrequested_remote":
						out.Versions[0].Version = "foreign"
					case "invalid_remote_ID":
						out.Versions[0].ID = 0
					case "future_download":
						out.Versions[0].LastDownloaded = time.Now().Add(24 * time.Hour)
					case "future_creation":
						out.Versions[0].Created = time.Now().Add(24 * time.Hour)
					case "unknown_timestamps":
						out.Versions[0].Created = time.Time{}
						out.Versions[0].LastDownloaded = time.Time{}
					}
					return out, nil
				})
			}
			if _, err := w.service.Collect(ctx, scope, versions); err == nil {
				t.Fatal("invalid or unresolved observation was accepted")
			}
		})
	}
}

func TestUnitCleanupFreshnessAndGrantBoundaries(t *testing.T) {
	w := newWorld(t, 1, 1)
	evidence := w.collect(t)
	if err := w.service.Guard(t.Context(), evidence); !errors.Is(err, kernel.ErrStore) {
		t.Fatal("publication guard accepted a non-transactional caller", err)
	}
	expired := evidence
	expired.Expires = expired.Started.Add(10 * time.Second)
	storekit.WantErr(t, w.service.Fresh(t.Context(), expired), kernel.ErrConflict)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	storekit.WantErr(t, w.service.Fresh(cancelled, evidence), context.Canceled)
	empty := evidence
	empty.Pins = nil
	storekit.WantErr(t, w.service.Fresh(t.Context(), empty), kernel.ErrInvalid)
	caller := storekit.Get[model.Peer](t)(w.store.Peers().Get(t.Context(), "peer-00"))
	caller.PublicKey = []byte("different key")
	_, err := w.service.Downloads(
		t.Context(),
		caller,
		"packages",
		cleanup.Request{SourceRepository: "remote", PackageType: "npm", Challenge: "fresh-unique-challenge", Versions: w.versions},
	)
	storekit.WantErr(t, err, kernel.ErrForbidden)
	_, err = w.service.Downloads(t.Context(), caller, "packages", cleanup.Request{SourceRepository: "remote", PackageType: "npm", Challenge: "short", Versions: w.versions})
	storekit.WantErr(t, err, kernel.ErrInvalid)
}
