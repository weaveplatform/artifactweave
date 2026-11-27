package federation_test

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	peeradapter "github.com/weaveplatform/artifactweave/internal/federation/adapters/peer"
	"github.com/weaveplatform/artifactweave/internal/federation/core/cleanup"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/platform/instance"
	"github.com/weaveplatform/artifactweave/internal/platform/testkit/storekit"
)

func connectCleanupMesh(t *testing.T, nodes ...*signedCleanupNode) {
	t.Helper()
	for _, local := range nodes {
		cfg := storekit.Get[repo.Config](t)(local.artifacts.Repos().Get(t.Context(), local.key))
		cfg.Members = []repo.Member{{URL: repo.SelfMemberURL(local.key), Enabled: repo.Flex(true)}}
		var members []model.FederationMember
		var origins []string
		for _, remote := range nodes {
			if remote == local {
				continue
			}
			id := storekit.Get[instance.InstanceIdentity](t)(remote.store.Instance().Get(t.Context()))
			storekit.Must(
				t,
				local.store.Peers().
					Put(t.Context(), model.Peer{InstanceID: id.InstanceID, PublicKey: id.PublicKey, BaseURL: remote.server.URL, ProtocolVersion: 1, UseCases: []model.UseCase{model.UseFederatedRepo}}),
			)
			cfg.Members = append(cfg.Members, repo.Member{URL: remote.server.URL + "/artifactory/" + remote.key, Enabled: repo.Flex(true)})
			members = append(members, model.FederationMember{PeerInstanceID: id.InstanceID, BaseURL: remote.server.URL + "/artifactory", RemoteRepoKey: remote.key, Enabled: true})
			origins = append(origins, remote.server.URL)
		}
		storekit.Must(t, local.artifacts.Repos().Update(t.Context(), cfg))
		storekit.Must(t, local.store.Federation().SetMembers(t.Context(), local.key, members))
		factory := storekit.Get[*peeradapter.Factory](t)(peeradapter.NewFactory(peeradapter.ClientOptions{Signer: local.signer, AllowedPrivateOrigins: origins, Attempts: 1}))
		local.service.Remote = peeradapter.CleanupRemote{Factory: factory}
	}
}

type releaseFaultRemote struct {
	cleanup.Remote
	coordinated  cleanup.CoordinationRemote
	offline      map[string]bool
	loseReply    map[string]bool
	releaseCalls map[string]int
}

func (r *releaseFaultRemote) Coordinate(ctx context.Context, peer model.Peer, key string, request cleanup.CoordinationRequest) (cleanup.CoordinationAnswer, error) {
	if request.Action == "release" {
		r.releaseCalls[peer.InstanceID]++
		if r.offline[peer.InstanceID] {
			return cleanup.CoordinationAnswer{}, cleanup.ErrUnavailable
		}
	}
	answer, err := r.coordinated.Coordinate(ctx, peer, key, request)
	if err == nil && request.Action == "release" && r.loseReply[peer.InstanceID] {
		delete(r.loseReply, peer.InstanceID)
		return cleanup.CoordinationAnswer{}, cleanup.ErrUnavailable
	}
	return answer, err
}
