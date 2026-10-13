package repo_test

import (
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestPublic(t *testing.T) {
	inc := "**/*"
	full := repo.Config{
		Key: "r", Class: repo.Remote, PackageType: repo.Maven, Description: "d",
		RepoLayoutRef: "maven-2-default", IncludesPattern: &inc, URL: "https://up",
		Username: "u", Password: "secret", Notes: "internal",
	}
	got := repo.Public(full)
	if got.Key != "r" || got.Class != repo.Remote || got.PackageType != repo.Maven ||
		got.Description != "d" || got.RepoLayoutRef != "maven-2-default" ||
		got.IncludesPattern != &inc {
		t.Fatalf("public lost an identity field: %+v", got)
	}
	if got.URL != "" || got.Username != "" || got.Password != "" || got.Notes != "" {
		t.Fatalf("public kept a private field: %+v", got)
	}
}

func TestMaskedAndRestoreSecrets(t *testing.T) {
	stored := repo.Config{
		Key: "r", Class: repo.Remote, Password: "secret",
		CustomHTTPHeaders: []repo.CustomHTTPHeader{
			{Name: "X-Token", Value: "t0ken", Sensitive: true},
			{Name: "X-Plain", Value: "visible"},
		},
	}
	masked := repo.Masked(stored)
	if masked.Password != "" || masked.CustomHTTPHeaders[0].Value != "" ||
		masked.CustomHTTPHeaders[1].Value != "visible" {
		t.Fatalf("masked = %+v", masked)
	}
	if stored.CustomHTTPHeaders[0].Value != "t0ken" {
		t.Fatal("Masked changed the caller's headers")
	}
	// Writing the masked document back keeps the stored secret; a new value
	// replaces it.
	back := masked
	repo.RestoreSecrets(stored, &back)
	if back.CustomHTTPHeaders[0].Value != "t0ken" {
		t.Fatalf("restore = %+v", back.CustomHTTPHeaders)
	}
	changed := repo.Config{CustomHTTPHeaders: []repo.CustomHTTPHeader{
		{Name: "X-Token", Value: "new", Sensitive: true},
		{Name: "X-Other", Sensitive: true},
	}}
	repo.RestoreSecrets(stored, &changed)
	if changed.CustomHTTPHeaders[0].Value != "new" || changed.CustomHTTPHeaders[1].Value != "" {
		t.Fatalf("restore overwrote or invented a value: %+v", changed.CustomHTTPHeaders)
	}
}

func TestRelevant(t *testing.T) {
	generic := *repo.Defaults(&repo.Config{Key: "g", Class: repo.Local})
	got := repo.Relevant(generic)
	if got.HandleReleases != nil || got.MaxUniqueTags != nil || got.DockerAPIVersion != "" ||
		got.EnableFileListsIndexing != nil || got.ForceNugetAuthentication != nil {
		t.Fatalf("generic kept package-specific fields: %+v", got)
	}
	if got.IncludesPattern == nil || got.ChecksumPolicyType == "" || got.BlackedOut == nil {
		t.Fatalf("generic lost common fields: %+v", got)
	}
	maven := *repo.Defaults(&repo.Config{Key: "m", Class: repo.Local, PackageType: repo.Maven})
	got = repo.Relevant(maven)
	if got.HandleReleases == nil || got.SnapshotVersionBehavior == "" {
		t.Fatalf("maven lost its own fields: %+v", got)
	}
	if got.MaxUniqueTags != nil || got.DockerTagRetention != nil {
		t.Fatalf("maven kept docker fields: %+v", got)
	}
	docker := *repo.Defaults(&repo.Config{Key: "d", Class: repo.Local, PackageType: repo.Docker})
	if got := repo.Relevant(docker); got.DockerAPIVersion == "" || got.HandleSnapshots != nil {
		t.Fatalf("docker = %+v", got)
	}
	for _, pt := range []repo.PackageType{
		repo.RPM, repo.Debian, repo.NuGet, repo.Conan, repo.P2, repo.Helm, repo.PyPI,
		repo.Go, repo.Terraform, repo.Bower, repo.Composer,
	} {
		c := repo.Config{Key: "x", Class: repo.Remote, PackageType: pt}
		_ = repo.Relevant(*repo.Defaults(&c))
	}
}
