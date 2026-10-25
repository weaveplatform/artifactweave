package repo_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestUnitNIMRemoteUsesNGCPersonalKeyAsBearer(t *testing.T) {
	c := repo.Defaults(&repo.Config{Class: repo.Remote, PackageType: repo.NIMModel})
	if c.EnableTokenAuthentication == nil || !*c.EnableTokenAuthentication {
		t.Fatal("NIM remote must send its personal key as a bearer token")
	}
	if c.URL != repo.DefaultNIMModelURL {
		t.Fatalf("NIM remote URL default: %q", c.URL)
	}
	if c := repo.Defaults(
		&repo.Config{Class: repo.Remote, PackageType: repo.Docker},
	); c.EnableTokenAuthentication == nil ||
		*c.EnableTokenAuthentication {
		t.Fatal("paired Docker remote must retain its separate registry token policy")
	}
}
