package terraform_test

import (
	"bytes"
	"sync"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/terraform/terraformtest"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

const (
	tfProviderVersions       = "GET /artifactory/api/terraform/v1/providers/{repo}-rt-ns-{namespace}/{type}/versions"
	tfProviderDownload       = "GET /artifactory/api/terraform/v1/providers/{repo}-rt-ns-{namespace}/{type}/{version}/download/{os}/{arch}"
	tfProviderScopedVersions = "GET /artifactory/api/terraform/{repo}/v1/providers/{namespace}/{type}/versions"
	tfProviderScopedDownload = "GET /artifactory/api/terraform/{repo}/v1/providers/{namespace}/{type}/{version}/download/{os}/{arch}"
	tfMirrorIndex            = "GET /artifactory/api/terraform/{repo}/providers/{hostname}/{namespace}/{type}/index.json"
	tfMirrorVersion          = "GET /artifactory/api/terraform/{repo}/providers/{hostname}/{namespace}/{type}/{version}.json"
	tfProviderSigned         = "GET /artifactory/api/terraform/{repo}/providers/{file}?sig={token}"
	tfProviderRouteBase      = "/artifactory/api/terraform/v1/providers/tf-provider-local-rt-ns-hashicorp/null/"
	tfProviderScopedBase     = "/artifactory/api/terraform/tf-provider-local/v1/providers/hashicorp/null/"
	tfMirrorRouteBase        = "/artifactory/api/terraform/tf-provider-local/providers/registry.terraform.io/hashicorp/null/"
)

var providerTestKey = sync.OnceValues(terraformtest.NewKey)

func seedRegistryProvider(t *testing.T, db store.Store, blobs blob.Store) {
	t.Helper()
	k, err := providerTestKey()
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Keys().
		CreateKeypair(t.Context(), types.Keypair{Name: "tf-provider-key", Type: types.KeypairGPG, PublicKey: k.Public}); err != nil {
		t.Fatal(err)
	}
	c, err := db.Repos().Get(t.Context(), "tf-provider-local")
	if err != nil {
		t.Fatal(err)
	}
	svc := &item.Service{Store: db, Blobs: blobs}
	for name, b := range (terraformtest.Release{Type: "null", Version: "3.3.2", Platforms: []string{"linux_amd64"}}).Files(k) {
		if _, err = svc.Deploy(
			t.Context(),
			item.Deploy{
				Target: item.Target{Repo: c, Path: "work/hashicorp/null/3.3.2/" + name},
				Body:   bytes.NewReader(b),
			},
		); err != nil {
			t.Fatal(err)
		}
	}
}

func providerRoutePaths() map[string]string {
	return map[string]string{
		tfProviderVersions:       tfProviderRouteBase + "versions",
		tfProviderDownload:       tfProviderRouteBase + "3.3.2/download/linux/amd64",
		tfProviderScopedVersions: tfProviderScopedBase + "versions",
		tfProviderScopedDownload: tfProviderScopedBase + "3.3.2/download/linux/amd64",
		tfMirrorIndex:            tfMirrorRouteBase + "index.json",
		tfMirrorVersion:          tfMirrorRouteBase + "3.3.2.json",
		tfProviderSigned:         "/artifactory/api/terraform/tf-provider-local/providers/work/hashicorp/null/3.3.2/x?sig=bad",
	}
}

var tfProviderStatusPart = statusPart{
	tune: tuneRegistry, seed: seedRegistryProvider,
	handwritten: func() map[string]bool {
		m := map[string]bool{}
		for route := range providerRoutePaths() {
			m[route] = true
			m["HEAD"+route[3:]] = true
		}
		return m
	}(),
	rows: func() []statusCase {
		rows := []statusCase{}
		for route, path := range providerRoutePaths() {
			for _, method := range []string{"GET", "HEAD"} {
				key := method + route[3:]
				status := 200
				if route == tfProviderSigned {
					status = 403
				}
				rows = append(rows, statusCase{key, path, "admin", "", status})
				if route != tfProviderSigned {
					rows = append(
						rows,
						statusCase{key, path, "", "", 401},
						statusCase{key, path, "user", "", 403},
					)
				}
			}
		}
		return rows
	},
}

var tfProviderMatrixPart = matrixPart{
	tune: tuneRegistry,
	seed: func(t *testing.T, f *matrixFixture) { t.Helper(); seedRegistryProvider(t, f.db, f.items.Blobs) },
	rows: func() []matrixRow {
		rows := []matrixRow{}
		read := holds(
			"tf-provider-local",
			"work/hashicorp/null/3.3.2/terraform-provider-null_3.3.2_linux_amd64.zip",
			types.ActionRead,
		)
		for route, path := range providerRoutePaths() {
			for _, method := range []string{"GET", "HEAD"} {
				r := row(method+route[3:], path, "", read, 200)
				if route == tfProviderSigned {
					r.who = func(matrixPrincipal) int { return 0 }
					r.ok = 403
					r.open = true
				}
				rows = append(rows, r)
			}
		}
		return rows
	},
}
