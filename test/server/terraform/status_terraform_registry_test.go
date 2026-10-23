package terraform_test

import (
	"context"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/crypt"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
)

const (
	tfDiscovery          = "GET /.well-known/terraform.json"
	tfDiscoveryHead      = "HEAD /.well-known/terraform.json"
	tfVersions           = "GET /artifactory/api/terraform/v1/modules/{repo}__{namespace}/{name}/{system}/versions"
	tfVersionsHead       = "HEAD /artifactory/api/terraform/v1/modules/{repo}__{namespace}/{name}/{system}/versions"
	tfDownload           = "GET /artifactory/api/terraform/v1/modules/{repo}__{namespace}/{name}/{system}/{version}/download"
	tfDownloadHead       = "HEAD /artifactory/api/terraform/v1/modules/{repo}__{namespace}/{name}/{system}/{version}/download"
	tfScopedVersions     = "GET /artifactory/api/terraform/{repo}/v1/modules/{namespace}/{name}/{system}/versions"
	tfScopedVersionsHead = "HEAD /artifactory/api/terraform/{repo}/v1/modules/{namespace}/{name}/{system}/versions"
	tfScopedDownload     = "GET /artifactory/api/terraform/{repo}/v1/modules/{namespace}/{name}/{system}/{version}/download"
	tfScopedDownloadHead = "HEAD /artifactory/api/terraform/{repo}/v1/modules/{namespace}/{name}/{system}/{version}/download"
	tfSigned             = "GET /artifactory/api/terraform/{repo}/v1/modules/{file}?sig={token}"
	tfSignedHead         = "HEAD /artifactory/api/terraform/{repo}/v1/modules/{file}?sig={token}"
	tfModuleBase         = "/artifactory/api/terraform/v1/modules/tf-registry-local__ns/name/aws/"
	tfScopedBase         = "/artifactory/api/terraform/tf-registry-local/v1/modules/ns/name/aws/"
)

func seedRegistryModule(t *testing.T, db store.Store, blobs blob.Store) {
	t.Helper()
	c, err := db.Repos().Get(context.Background(), "tf-registry-local")
	if err != nil {
		t.Fatal(err)
	}
	svc := &item.Service{Store: db, Blobs: blobs}
	if _, err := svc.Deploy(
		context.Background(),
		item.Deploy{
			Target: item.Target{Repo: c, Path: "ns/name/aws/1.0.0.zip"},
			Body:   strings.NewReader("fixture"),
		},
	); err != nil {
		t.Fatal(err)
	}
}

func tuneRegistry(c *Config) {
	// Fixed fixture material lets separately constructed nodes verify URLs.
	c.Keys, _ = crypt.New([]byte(strings.Repeat("t", 32)))
}

var tfRegistryStatusPart = statusPart{
	handwritten: map[string]bool{
		tfDiscovery:          true,
		tfDiscoveryHead:      true,
		tfVersions:           true,
		tfVersionsHead:       true,
		tfDownload:           true,
		tfDownloadHead:       true,
		tfScopedVersions:     true,
		tfScopedVersionsHead: true,
		tfScopedDownload:     true,
		tfScopedDownloadHead: true,
		tfSigned:             true,
		tfSignedHead:         true,
	},
	tune: tuneRegistry,
	seed: seedRegistryModule,
	rows: func() []statusCase {
		return []statusCase{
			{tfDiscovery, "/.well-known/terraform.json", "", "", 200},
			{tfDiscovery, "/.well-known/terraform.json", "forged", "", 200},
			{tfDiscoveryHead, "/.well-known/terraform.json", "expired", "", 200},
			{tfVersions, tfModuleBase + "versions", "admin", "", 200},
			{tfVersions, tfModuleBase + "versions", "user", "", 403},
			{tfVersions, tfModuleBase + "versions", "anonymous", "", 401},
			{tfVersions, tfModuleBase + "versions", "", "", 401},
			{tfVersions, tfModuleBase + "versions", "forged", "", 401},
			{tfVersionsHead, tfModuleBase + "versions", "admin", "", 200},
			{tfDownload, tfModuleBase + "1.0.0/download", "admin", "", 204},
			{tfDownload, tfModuleBase + "2.0.0/download", "admin", "", 404},
			{tfDownloadHead, tfModuleBase + "1.0.0/download", "admin", "", 204},
			{tfScopedVersions, tfScopedBase + "versions", "admin", "", 200},
			{tfScopedVersionsHead, tfScopedBase + "versions", "admin", "", 200},
			{tfScopedDownload, tfScopedBase + "1.0.0/download", "admin", "", 204},
			{tfScopedDownloadHead, tfScopedBase + "1.0.0/download", "admin", "", 204},
			{tfSigned, tfScopedBase + "1.0.0.zip?sig=bad", "", "", 403},
			{tfSignedHead, tfScopedBase + "1.0.0.zip?sig=bad", "forged", "", 403},
			{
				tfVersions,
				"/artifactory/api/terraform/v1/modules/tf-registry-remote__ns/name/aws/versions",
				"admin",
				"",
				501,
			},
			{
				tfVersions,
				"/artifactory/api/terraform/v1/modules/tf-registry-virtual__ns/name/aws/versions",
				"admin",
				"",
				501,
			},
		}
	},
}
