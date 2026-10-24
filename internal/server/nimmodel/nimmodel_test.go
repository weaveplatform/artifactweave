package nimmodel

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestAccPrepareUploadPublishesOnlyNIMMetadataIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, path, variant, model, version string
		packageType                         repo.PackageType
		kind                                types.RemoteKind
	}{
		{"tag metadata", "models/nim/meta/llama/1.3/.jfrog_nim_model_info.json", "nim-tag-metadata-v1", "llama", "1.3", repo.NIMModel, types.RemoteMetadata},
		{"repository metadata", "catalog/nim/meta/llama/repo.json", "nim-repository-v1", "llama", "", repo.NIMModel, types.RemoteMetadata},
		{"another package type", "models/nim/meta/llama/1.3/.jfrog_nim_model_info.json", "nim-tag-metadata-v1", "", "", repo.Generic, types.RemoteMetadata},
		{"model artifact", "models/nim/meta/llama/1.3/.jfrog_nim_model_info.json", "nim-tag-metadata-v1", "", "", repo.NIMModel, types.RemoteUnique},
		{"malformed metadata path", "models/nim/meta/llama/1.3/other.json", "nim-tag-metadata-v1", "", "", repo.NIMModel, types.RemoteMetadata},
		{"short tag path", "models/llama/.jfrog_nim_model_info.json", "nim-tag-metadata-v1", "", "", repo.NIMModel, types.RemoteMetadata},
		{"malformed repository path", "catalog/nim/meta/llama/other.json", "nim-repository-v1", "", "", repo.NIMModel, types.RemoteMetadata},
		{"short repository path", "catalog/llama/repo.json", "nim-repository-v1", "", "", repo.NIMModel, types.RemoteMetadata},
		{"unknown metadata variant", "models/nim/meta/llama/1.3/.jfrog_nim_model_info.json", "unknown", "", "", repo.NIMModel, types.RemoteMetadata},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := &item.CacheUpload{Kind: tc.kind}
			PrepareUpload(source.Request{
				Repo:     repo.Config{PackageType: tc.packageType},
				Resource: source.Resource{Path: tc.path, Variant: tc.variant},
			}, u)
			if got := u.Properties["Nimmodel.name"]; len(got) != boolCount(tc.model != "") ||
				(tc.model != "" && got[0] != tc.model) {
				t.Fatalf("name property = %v, want %q", got, tc.model)
			}
			if got := u.Properties["Nimmodel.versionId"]; len(got) != boolCount(tc.version != "") ||
				(tc.version != "" && got[0] != tc.version) {
				t.Fatalf("version property = %v, want %q", got, tc.version)
			}
			if u.PublishMetadata != (tc.model != "") {
				t.Fatalf("publish metadata = %v", u.PublishMetadata)
			}
		})
	}
}

func boolCount(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestAccDocumentedNGCPaths(t *testing.T) {
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
		{"v2/models/nim/meta/llama/versions/1/zip", "", "models/nim/meta/llama/1/model.zip", "v2/models/nim/meta/llama/versions/1/zip", source.Artifact},
		{"v2/org/nim/team/meta/models/llama/versions/1/zip", "", "models/nim/meta/llama/1/model.zip", "v2/org/nim/team/meta/models/llama/versions/1/zip", source.Artifact},
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

func TestAccRejectsUnscopedNGCRequests(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ path, query string }{
		{"v2/nim/metadata", "resourceId=nim%2Fmodel"},
		{"v2/nim/metadata", "resourceId=nim%2Fmodel&tag=one&tag=two"},
		{"v2/nim/metadata", "resourceId=nim%2F..%2Fmodel&tag=one"},
		{"v2/repos/nim/model", "next=private"},
		{"v2/models/nim/model/versions/one/files/a.bin", "next=private"},
		{"v2/models/nim/model/versions/one/files/%2e%2e", ""},
		{"v2/org/nim/team/meta/models/model/versions/one/zip/extra", ""},
		{"v2/org/nim/team/meta/models/model/versions/one/files/", ""},
	} {
		if _, err := parseResource(tc.path, tc.query); err == nil {
			t.Errorf("accepted %s?%s", tc.path, tc.query)
		}
	}
}

func TestAccUnknownNGCResourceIsNotAProxy(t *testing.T) {
	t.Parallel()
	_, err := parseResource("v2/private/other", "")
	apiErr, ok := err.(*apierror.Error)
	if !ok || apiErr.Status != http.StatusNotFound {
		t.Fatalf("unknown resource error = %v, want 404", err)
	}
}

func TestAccTagMetadataAdmission(t *testing.T) {
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
