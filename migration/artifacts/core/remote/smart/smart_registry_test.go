package smart_test

import (
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/smart"
	"github.com/weaveplatform/artifactweave/migration/artifacts/ecosystem/docker"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccSmartRegistryGrantAndPropertyPath(t *testing.T) {
	for _, realm := range []string{"", "remote", "other"} {
		name := "image"
		if realm == "" {
			name = "remote/image"
		}
		access := &types.RegistryAccess{
			Realm: realm,
			Grants: []types.RegistryGrant{
				{Type: "repository", Name: name, Actions: []string{"pull"}},
			},
		}
		if smart.SmartRegistryGrant(
			docker.ParseRegistryPath, access, "remote", "image/manifests/tag",
		) != (realm != "other") {
			t.Fatal("realm grant", realm)
		}
		if smart.SmartRegistryGrant(docker.ParseRegistryPath, access, "remote", "bad") ||
			smart.SmartRegistryGrant(
				docker.ParseRegistryPath, access, "remote", "private/manifests/tag",
			) {
			t.Fatal("scope widened")
		}
	}
	for _, ref := range []string{"tag", "sha256:" + strings.Repeat("a", 64)} {
		resource, err := docker.RegistryResource("image", "manifests", ref)
		if err != nil {
			t.Fatal(err)
		}
		q := source.Request{Repo: repo.Config{PackageType: repo.OCI}, Resource: resource}
		if smart.SmartPropertyPath(docker.ParseRegistryPath, q) != (ref == "tag") ||
			smart.SmartStoragePath(q) != q.Resource.Path {
			t.Fatal("digest pull synchronized tag properties")
		}
	}
}

// Smart reaches the registry path grammar only through the parser the
// application hands NewSmart. Without one, a registry request must neither
// synchronise properties nor match a registry token's grant.
func TestUnitSmartRegistryPathNilParserFailsClosed(t *testing.T) {
	resource, err := docker.RegistryResource("image", "manifests", "tag")
	if err != nil {
		t.Fatal(err)
	}
	q := source.Request{Repo: repo.Config{PackageType: repo.OCI}, Resource: resource}
	if smart.SmartPropertyPath(nil, q) {
		t.Fatal("a nil parser gave a registry request a property path")
	}
	access := &types.RegistryAccess{
		Realm: "remote",
		Grants: []types.RegistryGrant{
			{Type: "repository", Name: "image", Actions: []string{"pull"}},
		},
	}
	if smart.SmartRegistryGrant(nil, access, "remote", "image/manifests/tag") {
		t.Fatal("a nil parser matched a registry grant")
	}
}
