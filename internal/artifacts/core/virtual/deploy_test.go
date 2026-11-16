package virtual_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/virtual"
)

// deploying is the virtual repository key over every repository of
// TestUnitDeployTarget, deploying to ddr.
func deploying(key, ddr string) repo.Config {
	v := virt(key, "libs-local", "fed", "up", "inner")
	v.DefaultDeploymentRepo = ddr
	return v
}

// TestUnitDeployTarget: a virtual repository deploys into its default
// deployment repository only when that is a local or federated repository
// that exists (decision 0008, R6; UploadServiceUtils.getTargetRepository).
func TestUnitDeployTarget(t *testing.T) {
	rs := configs(
		local("libs-local", false),
		repo.Config{Key: "fed", Class: repo.Federated},
		remote("up", false),
		virt("inner", "libs-local"),
	)
	for name, tc := range map[string]struct {
		ddr, want string
	}{
		"local":     {"libs-local", "libs-local"},
		"federated": {"fed", "fed"},
		"unset":     {"", ""},
		"missing":   {"gone", ""},
		"remote":    {"up", ""},
		"virtual":   {"inner", ""},
	} {
		got, err := virtual.DeployTarget(t.Context(), rs, deploying("v", tc.ddr))
		switch {
		case tc.want == "" && !errors.Is(err, virtual.ErrNoDeployTarget):
			t.Errorf("%s: %+v, %v, want ErrNoDeployTarget", name, got, err)
		case tc.want != "" && (err != nil || got.Key != tc.want):
			t.Errorf("%s: %+v, %v, want %s", name, got, err, tc.want)
		}
	}
}

func TestUnitDeployTargetStoreError(t *testing.T) {
	rs := configs(local("libs-local", false))
	_, err := virtual.DeployTarget(t.Context(), failing{rs, "broken"}, deploying("v", "broken"))
	if !errors.Is(err, errStore) || errors.Is(err, virtual.ErrNoDeployTarget) ||
		!strings.Contains(err.Error(), "default deployment repository broken of v") {
		t.Fatalf("error %v", err)
	}
}
