package retentionhttp

import (
	"errors"
	"testing"

	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
	"github.com/weaveplatform/artifactweave/internal/platform/idempotency"
)

func TestUnitCleanupReplayBindingRefusesMiswiringAndUnavailableModule(t *testing.T) {
	b := (PackagePoliciesAPI{}).KeyedBindings()["runPackageCleanupPolicy"]
	ctx := t.Context()
	if _, err := b.Resource(ctx, struct{}{}); !errors.Is(err, errReplayRequest) {
		t.Fatal("resource accepted a different operation", err)
	}
	if _, err := b.Fingerprint(ctx, struct{}{}); !errors.Is(err, errReplayRequest) {
		t.Fatal("fingerprint accepted a different operation", err)
	}
	if _, err := b.Reload(ctx, struct{}{}, idempotency.ReplayReceipt{}); !errors.Is(err, errReplayRequest) {
		t.Fatal("reload accepted a different operation", err)
	}
	req := artgen.RunPackageCleanupPolicyRequestObject{PolicyKey: "unavailable"}
	if _, err := b.Resource(ctx, req); !errors.Is(err, apierror.ErrNotImplemented) {
		t.Fatal("unavailable module resource lost its refusal", err)
	}
	if _, err := b.Reload(ctx, req, idempotency.ReplayReceipt{}); !errors.Is(err, apierror.ErrNotImplemented) {
		t.Fatal("unavailable module reload lost its refusal", err)
	}
}
