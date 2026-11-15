package repo_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
)

func TestUnitNIMRemoteUsesNGCPersonalKeyAsBearer(t *testing.T) {
	c := repo.Defaults(&repo.Config{Class: repo.Remote, PackageType: repo.NIMModel})
	if c.EnableTokenAuthentication == nil || !*c.EnableTokenAuthentication {
		t.Fatal("NIM remote must send its personal key as a bearer token")
	}
	// No package type has a default URL over REST, NIM's included: the
	// UI's auto-populated https://api.ngc.nvidia.com is not applied to a
	// create without one (#1584).
	if c.URL != "" {
		t.Fatalf("NIM remote URL default: %q", c.URL)
	}
	if c := repo.Defaults(
		&repo.Config{Class: repo.Remote, PackageType: repo.Docker},
	); c.EnableTokenAuthentication == nil ||
		*c.EnableTokenAuthentication {
		t.Fatal("paired Docker remote must retain its separate registry token policy")
	}
}
