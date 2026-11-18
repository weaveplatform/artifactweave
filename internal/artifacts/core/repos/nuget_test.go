package repos

import (
	"errors"
	"net/http"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

func TestUnitNuGetNormalizedVirtualMembers(t *testing.T) {
	for _, class := range []repo.Class{repo.Local, repo.Remote, repo.Federated, repo.Virtual} {
		for _, flag := range []string{"true", "false", "unset"} {
			t.Run(string(class)+"/"+flag, func(t *testing.T) {
				member := repo.Config{Key: "member", Class: class, PackageType: repo.NuGet}
				if flag != "unset" {
					member.EnableNormalizedVersion = new(flag == "true")
				}
				v := repo.Config{Key: "virtual", Class: repo.Virtual, PackageType: repo.NuGet, Repositories: []string{"member"}, EnableNormalizedVersion: new(true)}
				for _, update := range []bool{false, true} {
					var stored *repo.Config
					if update {
						old := v
						stored = &old
					}
					got := v
					err := CheckMembers(&got, stored, []repo.Config{member})
					if flag == "true" {
						if err != nil {
							t.Fatal(err)
						}
					} else {
						var answer *apierror.Error
						if !errors.As(err, &answer) || answer.Status != http.StatusBadRequest ||
							answer.Message != "Nuget Repository virtual must have only normalized repositories" {
							t.Fatal(err)
						}
					}
				}
				v.EnableNormalizedVersion = new(false)
				if err := CheckMembers(&v, nil, []repo.Config{member}); err != nil {
					t.Fatal("nonnormalized parent refused a member", err)
				}
			})
		}
	}
	v := repo.Config{Key: "self", Class: repo.Virtual, PackageType: repo.NuGet, Repositories: []string{"self"}, EnableNormalizedVersion: new(true)}
	if err := CheckMembers(&v, nil, nil); err != nil {
		t.Fatal("self cycle refused", err)
	}
	v.PackageType = repo.NPM
	v.Repositories = []string{"npm"}
	if err := CheckMembers(&v, nil, []repo.Config{{Key: "npm", Class: repo.Local, PackageType: repo.NPM}}); err != nil {
		t.Fatal("NuGet normalization leaked to another type", err)
	}
}
