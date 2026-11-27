package federation

import (
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/federated"
)

// Monitoring supplies read-only federation state using current authority and
// durable delivery records. The transport is resolved when a drift read runs,
// after runtime assembly has opened the signed configuration client.
func (m *Module) Monitoring(items artifactsapi.FederatedItemReader) (federated.Monitoring, error) {
	ops, err := m.Operations()
	if err != nil {
		return federated.Monitoring{}, err
	}
	return federated.Monitoring{Operations: ops, Items: items, Remote: func() federated.ConfigurationRemote {
		m.pushMu.Lock()
		defer m.pushMu.Unlock()
		remote, _ := m.configRemote.(federated.ConfigurationRemote)
		return remote
	}}, nil
}
