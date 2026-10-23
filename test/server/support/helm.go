package support

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
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
func SeedHelmCharter(t *testing.T, db store.Store, passwordHash []byte) {
	t.Helper()
	ctx := t.Context()
	if err := db.Users().
		Create(ctx, types.User{Name: "charter", PasswordHash: passwordHash}); err != nil {
		t.Fatal(err)
	}
	if err := db.Permissions().Create(ctx, types.Permission{
		Name: "helm-charter",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{
				"charter": types.ActionRead | types.ActionWrite | types.ActionDelete,
			}},
			Targets: map[string]types.Target{"helm-local": {
				Include: []string{"**"}, Exclude: []string{"**/*.prov"},
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}
