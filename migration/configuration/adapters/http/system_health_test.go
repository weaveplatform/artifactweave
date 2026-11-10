package configurationhttp

import (
	"context"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store/inmem"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const healthInstanceID = "jfrt@5d0c9a3e-1b2f-4c6d-8e7f-0a1b2c3d4e5f"

// A service built only to reach the store probes it and remembers nothing.
func TestAccProbeWithoutMemory(t *testing.T) {
	db := inmem.New()
	if err := db.Instance().Init(context.Background(), types.InstanceIdentity{
		InstanceID: healthInstanceID, ProtocolVersion: 1,
	}); err != nil {
		t.Fatal(err)
	}
	s := SystemAPI{deps: Deps{Store: db}}
	id, err := s.probeDatabase(context.Background())
	if err != nil || id != healthInstanceID {
		t.Fatalf("probe: %q %v", id, err)
	}
	if got := s.lastInstanceID(); got != "" {
		t.Fatalf("remembered %q without a memory", got)
	}
}
