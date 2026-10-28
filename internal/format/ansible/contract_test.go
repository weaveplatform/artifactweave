package ansible_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/ansible"
	"github.com/weaveplatform/artifactweave/internal/format/formattest"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

func sum(data []byte) string { s := sha256.Sum256(data); return hex.EncodeToString(s[:]) }

type entry struct {
	name string
	body []byte
	kind byte
	size int64
}

func archive(t *testing.T, entries []entry) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		size := int64(len(e.body))
		if e.size != 0 {
			size = e.size
		}
		if err := tw.WriteHeader(
			&tar.Header{Name: e.name, Typeflag: e.kind, Size: size, Mode: 0o644},
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func collectionEntries(t *testing.T, ns, name, version string, n int) []entry {
	t.Helper()
	body := []byte(fmt.Sprintf("collection %d", n))
	files, _ := json.Marshal(map[string]any{"files": []any{
		map[string]string{"name": ".", "ftype": "dir"},
		map[string]string{"name": "README.md", "ftype": "file", "chksum_sha256": sum(body)},
	}})
	manifest, _ := json.Marshal(map[string]any{
		"collection_info": map[string]any{
			"namespace":    ns,
			"name":         name,
			"version":      version,
			"dependencies": map[string]string{"weave.dependency": ">=1.0.0,<2.0.0"},
		},
		"file_manifest_file": map[string]string{"name": "FILES.json", "chksum_sha256": sum(files)},
	})
	return []entry{
		{name: "MANIFEST.json", body: manifest},
		{name: "FILES.json", body: files},
		{name: "README.md", body: body},
	}
}

func TestAccFormatContract(t *testing.T) {
	formattest.Run(t, formattest.Format{
		Type: repo.Ansible, NoRemoteCache: true,
		Version: func(name, version string, n int) formattest.Version {
			version += ".0"
			return formattest.Version{
				Package: "weave." + name,
				Version: version,
				Files: []formattest.File{
					{
						Path: ansible.Path("weave", name, version),
						Body: archive(t, collectionEntries(t, "weave", name, version, n)),
					},
				},
			}
		},
	})
}
