package nimmodel

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/source"
)

func TestDocumentedNGCPaths(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		path, query, cache, upstream string
		kind                         source.ResourceKind
	}{
		{"v2/nim/metadata", "resourceId=nim%2Fmeta%2Fllama&tag=1.3", "models/nim/meta/llama/1.3/.jfrog_nim_model_info.json", "v2/nim/metadata", source.Metadata},
		{"v2/nim/meta/llama/1.3/metadata", "", "models/nim/meta/llama/1.3/.jfrog_nim_model_info.json", "v2/nim/meta/llama/1.3/metadata", source.Metadata},
		{"v2/repos/nim/meta/llama", "", "catalog/nim/meta/llama/repo.json", "v2/repos/nim/meta/llama", source.Metadata},
		{"v2/org/nim/team/meta/repos/llama", "", "catalog/nim/meta/llama/repo.json", "v2/org/nim/team/meta/repos/llama", source.Metadata},
		{"v2/models/nim/meta/llama/versions/1/files/weights/a.bin", "", "models/nim/meta/llama/1/weights/a.bin", "v2/models/nim/meta/llama/versions/1/files/weights/a.bin", source.Artifact},
		{"v2/org/nim/team/meta/models/llama/versions/1/files/weights/a.bin", "", "models/nim/meta/llama/1/weights/a.bin", "v2/org/nim/team/meta/models/llama/versions/1/files/weights/a.bin", source.Artifact},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r, err := parseResource(tc.path, tc.query)
			if err != nil || r.Kind != tc.kind || r.Path != tc.cache ||
				r.UpstreamPath != tc.upstream {
				t.Fatalf("resource: %+v, %v", r, err)
			}
		})
	}
}

func TestRejectsUnscopedNGCRequests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ path, query string }{
		{"v2/nim/metadata", "resourceId=nim%2Fmodel"},
		{"v2/nim/metadata", "resourceId=nim%2Fmodel&tag=one&tag=two"},
		{"v2/nim/metadata", "resourceId=nim%2F..%2Fmodel&tag=one"},
		{"v2/repos/nim/model", "next=private"},
		{"v2/models/nim/model/versions/one/files/a.bin", "next=private"},
		{"v2/models/nim/model/versions/one/files/%2e%2e", ""},
		{"v2/org/nim/team/meta/models/model/versions/one/zip", ""},
		{"v2/org/nim/team/meta/models/model/versions/one/files/", ""},
	} {
		if _, err := parseResource(tc.path, tc.query); err == nil {
			t.Errorf("accepted %s?%s", tc.path, tc.query)
		}
	}
}

func TestTagMetadataAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"profiles":[{"profile_id":"h100","tested_gpu_devices":[]}],"future_field":true}`, true},
		{`{"release_details":{"container_version":"1.3"}}`, true},
		{`{"profiles":[{"profile_id":"h100"}]}`, false},
		{`{"profiles":[{"tested_gpu_devices":[]}]}`, false},
		{`{"profiles":{}}`, false},
		{`{"profiles":[]`, false},
		{`{"profiles":[]} {}`, false},
	} {
		body := bytes.NewReader([]byte(tc.body))
		err := validateTagMetadata(
			context.Background(),
			body,
			int64(len(tc.body)),
			source.MetadataResponse{},
		)
		if (err == nil) != tc.valid {
			t.Errorf("body %q: error %v", tc.body, err)
		}
	}
	large := strings.Repeat(" ", maxMetadata+1)
	if err := validateTagMetadata(
		context.Background(),
		bytes.NewReader([]byte(large)),
		int64(len(large)),
		source.MetadataResponse{},
	); err == nil {
		t.Fatal("oversized metadata accepted")
	}
}
