package support

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/accesstest"
	"github.com/weaveplatform/artifactweave/migration/access/api"
)

// HelmChart builds a minimal chart archive with the named version.
func HelmChart(name, version string) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := fmt.Sprintf("apiVersion: v2\nname: %s\nversion: %s\n", name, version)
	_ = tw.WriteHeader(&tar.Header{
		Name: name + "/Chart.yaml", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	})
	_, _ = io.WriteString(tw, body)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// SeedHelmCharter gives the chart-only caller rights on charts but not their
// provenance sidecars. Root and independent Helm matrices use the same grant.
func SeedHelmCharter(t *testing.T, db SeedStore, passwordHash []byte) {
	t.Helper()
	ctx := t.Context()
	if err := db.Users().
		Create(ctx, accesstest.User{Name: "charter", PasswordHash: passwordHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Permissions().Create(ctx, accesstest.Permission{
		Name: "helm-charter",
		Resources: map[api.ResourceType]accesstest.Resource{api.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]api.Actions{
				"charter": api.ActionRead | api.ActionWrite | api.ActionDelete,
			}},
			Targets: map[string]accesstest.Target{"helm-local": {
				Include: []string{"**"}, Exclude: []string{"**/*.prov"},
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}
