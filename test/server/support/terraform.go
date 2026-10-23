package support

import "github.com/weaveplatform/artifactweave/pkg/repo"

// TerraformRegistryRepos are the module registry fixture repositories shared
// by the root cross-format suite and the standalone Terraform route suite.
func TerraformRegistryRepos() []repo.Config {
	return []repo.Config{
		{
			Key:           "tf-registry-local",
			Class:         repo.Local,
			PackageType:   repo.Terraform,
			TerraformType: repo.TerraformModule,
		},
		{
			Key:         "tf-registry-remote",
			Class:       repo.Remote,
			PackageType: repo.Terraform,
			URL:         "https://up.example",
		},
		{Key: "tf-registry-virtual", Class: repo.Virtual, PackageType: repo.Terraform},
	}
}

// TerraformProviderRepos supplies the signing-key provider fixture.
func TerraformProviderRepos() []repo.Config {
	return []repo.Config{{
		Key: "tf-provider-local", Class: repo.Local, PackageType: repo.Terraform,
		TerraformType: repo.TerraformProvider, PrimaryKeyPairRef: "tf-provider-key",
	}}
}
