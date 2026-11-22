package docker

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitRegistryStorageFilesReuseNativeResourceIdentity(t *testing.T) {
	hex := strings.Repeat("a", 64)
	for _, test := range []struct{ path, operation, reference string }{
		{"team/image/latest/manifest.json", "manifests", "latest"},
		{"team/image/latest/list.manifest.json", "manifests", "latest"},
		{"team/image/sha256:" + hex + "/manifest.json", "manifests", "sha256:" + hex},
		{"team/image/_uploads/sha256__" + hex, "blobs", "sha256:" + hex},
		{"team/image/latest/sha256__" + hex, "blobs", "sha256:" + hex},
	} {
		actual, ok := StorageResource(test.path)
		want, err := RegistryResource("team/image", test.operation, test.reference)
		if !ok || err != nil || actual.Path != want.Path || actual.UpstreamPath != want.UpstreamPath || actual.Variant != want.Variant || actual.Accept != want.Accept {
			t.Fatal("storage spelling changed ordinary native identity", test.path)
		}
		if StoragePath(test.path) != want.Path || StorageRoot(test.path) != "team/image" {
			t.Fatal("native path/root projection lost its canonical namespace")
		}
	}
	for _, path := range []string{"image", "UPPER/latest/manifest.json", "team/image/_uploads/manifest.json", "team/image/latest/sha256__bad", "team/image/latest/unknown.file"} {
		if _, ok := StorageResource(path); ok || StoragePath(path) != path {
			t.Fatal("unknown storage file became a registry operation", path)
		}
	}
}

func TestUnitRegistryChildKeepsExecutionFenceAndItsOwnSelectedFacts(t *testing.T) {
	hex := strings.Repeat("a", 64)
	resource, err := RegistryResource("team/image", "blobs", "sha256:"+hex)
	if err != nil {
		t.Fatal(err)
	}
	props := model.Properties{"quality": {"parent-only"}}
	parent := source.Request{
		Resource:                  source.Resource{SelectedContent: &source.ContentExpectation{Size: 123, Checksums: kernel.Checksums{SHA256: strings.Repeat("b", 64)}}},
		Publication:               &source.Publication{Check: func(context.Context) error { return nil }, Fence: func(context.Context) error { return nil }},
		SelectedProperties:        &props,
		SelectedPropertiesStarted: time.Unix(1, 0),
	}
	child := registryChild(parent, resource, Descriptor{Digest: "sha256:" + hex, Size: 4})
	if child.Publication != parent.Publication || child.Resource.SelectedContent == nil || child.Resource.SelectedContent.Size != 4 ||
		child.Resource.SelectedContent.Checksums.SHA256 != hex || !child.SelectedPropertiesStarted.IsZero() || child.SelectedProperties != nil {
		t.Fatal("native child borrowed parent facts/properties or lost execution fencing")
	}
	ordinary := registryChild(source.Request{}, resource, Descriptor{Digest: "sha256:" + hex, Size: 4})
	if ordinary.Publication != nil || ordinary.Resource.SelectedContent != nil {
		t.Fatal("ordinary native read acquired replication-only restrictions")
	}
}
