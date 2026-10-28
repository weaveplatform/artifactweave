package ansible_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/format/ansible"
)

func TestUnitReadClientBuiltArchive(t *testing.T) {
	data, err := os.ReadFile("testdata/weave-sample-1.2.3.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ansible.ReadArchive(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Namespace != "weave" || meta.Name != "sample" || meta.Version != "1.2.3" ||
		len(meta.SHA256) != 64 {
		t.Fatalf("wrong metadata: %+v", meta)
	}
}

func TestUnitRejectInvalidArchiveAndPath(t *testing.T) {
	if _, err := ansible.ReadArchive(bytes.NewReader([]byte("not a collection")), 16); err == nil {
		t.Fatal("invalid archive accepted")
	}
	if _, _, _, ok := ansible.ParsePath("collections/weave/sample/1.2.3/other.tar.gz"); ok {
		t.Fatal("wrong path accepted")
	}
	if _, _, _, ok := ansible.ParsePath(
		"collections/weave/sample/1.2.3/weave-sample-1.2.3.tar.gz",
	); !ok {
		t.Fatal("canonical path rejected")
	}
}
