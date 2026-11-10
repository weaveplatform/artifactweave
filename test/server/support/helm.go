package support

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"testing"

	"github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/access/core/model"
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
		Create(ctx, model.User{Name: "charter", PasswordHash: passwordHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Permissions().Create(ctx, model.Permission{
		Name: "helm-charter",
		Resources: map[api.ResourceType]model.Resource{api.ResourceArtifact: {
			Actions: model.Grants{Users: map[string]api.Actions{
				"charter": api.ActionRead | api.ActionWrite | api.ActionDelete,
			}},
			Targets: map[string]model.Target{"helm-local": {
				Include: []string{"**"}, Exclude: []string{"**/*.prov"},
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}
