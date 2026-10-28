package puppet_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/internal/format/puppet"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func TestAccFormatLifecycle(t *testing.T) {
	formattest.Run(
		t,
		formattest.Format{Type: repo.Puppet, Version: func(p, v string, n int) formattest.Version {
			name, version := "acme-"+p, v+".0"
			var out bytes.Buffer
			gz := gzip.NewWriter(&out)
			tw := tar.NewWriter(gz)
			files := []struct{ name, body string }{
				{
					"metadata.json",
					fmt.Sprintf(`{"name":%q,"version":%q,"dependencies":[]}`, name, version),
				},
				{"manifests/init.pp", fmt.Sprintf("# deployment %d", n)},
			}
			for _, f := range files {
				if err := tw.WriteHeader(
					&tar.Header{
						Name: name + "-" + version + "/" + f.name,
						Mode: 0o644,
						Size: int64(len(f.body)),
					},
				); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write([]byte(f.body)); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			return formattest.Version{
				Package: name,
				Version: version,
				Files:   []formattest.File{{Path: puppet.Path(name, version), Body: out.Bytes()}},
			}
		}},
	)
}
