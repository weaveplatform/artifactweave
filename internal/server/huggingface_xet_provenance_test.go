package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/pkg/blob"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func xetProvenanceData() []byte {
	data := make([]byte, 1280000)
	for i := range data {
		data[i] = byte(i)
	}
	return data
}

func xetProvenancePermission(t *testing.T, w *itemWorld, read bool) {
	t.Helper()
	actions := types.ActionWrite
	if read {
		actions |= types.ActionRead
	}
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "model-content-provenance",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": actions}},
			Targets: map[string]types.Target{"hf": {Include: []string{"models--acme--demo/**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
}

// Reading a different model/repository is deliberately granted in the two
// cross-boundary cases: readable content elsewhere still is not an upload
// grant for the target model.
func xetProvenanceReadableOrigin(t *testing.T, w *itemWorld, key, prefix string) {
	t.Helper()
	if err := w.db.Permissions().Create(t.Context(), types.Permission{
		Name: "read-other-model-content",
		Resources: map[types.ResourceType]types.Resource{types.ResourceArtifact: {
			Actions: types.Grants{Users: map[string]types.Actions{"user": types.ActionRead}},
			Targets: map[string]types.Target{key: {Include: []string{prefix + "/**"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	wantStatus(
		t,
		w.do(
			"GET",
			"/artifactory/"+key+"/"+prefix+"/snapshots/"+strings.Repeat("a", 40)+"/weights.bin",
			"user",
			"",
		),
		200,
	)
}

func xetProvenanceCommit(
	t *testing.T,
	w *itemWorld,
	branch, path, oid string,
	size, want int,
) string {
	t.Helper()
	body := fmt.Sprintf(
		"{\"key\":\"header\",\"value\":{\"summary\":\"verified content reuse\"}}\n{\"key\":\"lfsFile\",\"value\":{\"path\":%q,\"algo\":\"sha256\",\"oid\":%q,\"size\":%d}}\n",
		path,
		oid,
		size,
	)
	got := w.do(
		"POST",
		"/artifactory/api/huggingfaceml/hf/api/models/acme/demo/commit/"+branch,
		"user",
		body,
		"Content-Type",
		"application/x-ndjson",
	)
	wantStatus(t, got, want)
	if want != 200 {
		return ""
	}
	var response struct {
		OID string `json:"commitOid"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.OID) != 40 {
		t.Fatalf("commit did not publish a revision: %s", got.Body.String())
	}
	return response.OID
}

func xetProvenanceBatch(t *testing.T, w *itemWorld, oid string, size int) string {
	t.Helper()
	body := fmt.Sprintf(`{"operation":"upload","objects":[{"oid":%q,"size":%d}]}`, oid, size)
	got := w.do(
		"POST",
		"/artifactory/api/huggingfaceml/hf/acme/demo.git/info/lfs/objects/batch",
		"user",
		body,
		"Content-Type",
		"application/vnd.git-lfs+json",
	)
	wantStatus(t, got, 200)
	var response struct {
		Objects []struct {
			OID     string `json:"oid"`
			Size    int    `json:"size"`
			Actions map[string]struct {
				Href string `json:"href"`
			} `json:"actions"`
			Error json.RawMessage `json:"error"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Objects) != 1 || response.Objects[0].OID != oid ||
		response.Objects[0].Size != size ||
		len(response.Objects[0].Error) != 0 {
		t.Fatalf("unexpected LFS batch: %s", got.Body.String())
	}
	return response.Objects[0].Actions["upload"].Href
}

func TestUnitXetCommitRequiresCallerAndModelContentProvenance(t *testing.T) {
	for _, tc := range []struct {
		name string
		read bool
		seed func(*testing.T, *itemWorld, []byte)
	}{
		{"unreadable same-model snapshot", false, func(t *testing.T, w *itemWorld, data []byte) {
			wantStatus(t, w.do("PUT", "/artifactory/hf/models--acme--demo/snapshots/"+strings.Repeat("a", 40)+"/weights.bin", "admin", string(data)), 201)
		}},
		{"trashed same-model snapshot", true, func(t *testing.T, w *itemWorld, data []byte) {
			path := "/artifactory/hf/models--acme--demo/snapshots/" + strings.Repeat("a", 40) + "/weights.bin"
			wantStatus(t, w.do("PUT", path, "admin", string(data)), 201)
			wantStatus(t, w.do("DELETE", path, "admin", ""), 204)
		}},
		{"orphan global blob", false, func(t *testing.T, w *itemWorld, data []byte) {
			upload, err := w.blobs.BeginUpload(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = upload.Append(t.Context(), bytes.NewReader(data)); err != nil {
				t.Fatal(err)
			}
			if _, err = upload.Commit(t.Context(), blob.Digests{}); err != nil {
				t.Fatal(err)
			}
		}},
		{"other model snapshot", true, func(t *testing.T, w *itemWorld, data []byte) {
			wantStatus(t, w.do("PUT", "/artifactory/hf/models--acme--other/snapshots/"+strings.Repeat("a", 40)+"/weights.bin", "admin", string(data)), 201)
			xetProvenanceReadableOrigin(t, w, "hf", "models--acme--other")
		}},
		{"other repository snapshot", true, func(t *testing.T, w *itemWorld, data []byte) {
			wantStatus(t, w.do("PUT", "/artifactory/hf-f/models--acme--demo/snapshots/"+strings.Repeat("a", 40)+"/weights.bin", "admin", string(data)), 201)
			xetProvenanceReadableOrigin(t, w, "hf-f", "models--acme--demo")
		}},
		{"another caller private CAS", true, func(t *testing.T, w *itemWorld, _ []byte) {
			token := xetTokenFor(t, w, "hf", "acme/demo", "write", "admin")
			wantStatus(t, xetCall(w, token, "POST", "/v1/xorbs/default/"+xetFixtureHash, xetFixture(t, "large.xorb")), 200)
			wantStatus(t, xetCall(w, token, "POST", "/v2/shards", xetFixture(t, "large.shard")), 200)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newXetWorld(t)
			data := xetProvenanceData()
			sum := sha256.Sum256(data)
			oid := hex.EncodeToString(sum[:])
			tc.seed(t, w, data)
			xetProvenancePermission(t, w, tc.read)
			xetProvenanceCommit(t, w, "main", "weights.bin", oid, len(data), 403)
			href := xetProvenanceBatch(t, w, oid, len(data))
			if href == "" {
				t.Fatal("LFS deduplication accepted a digest without caller/model provenance")
			}
			// Obtaining an upload URL is not proof that this caller holds the bytes.
			xetProvenanceCommit(t, w, "main", "weights.bin", oid, len(data), 403)
			corrupt := bytes.Clone(data)
			corrupt[0] ^= 1
			wantStatus(t, w.do("PUT", href, "", string(corrupt)), 422)
			xetProvenanceCommit(t, w, "main", "weights.bin", oid, len(data), 403)
			wantStatus(t, w.do("PUT", href, "", string(data)), 200)
			first := xetProvenanceCommit(t, w, "main", "weights.bin", oid, len(data), 200)
			got := w.do(
				"GET",
				"/artifactory/api/huggingfaceml/hf/acme/demo/resolve/"+first+"/weights.bin",
				"admin",
				"",
			)
			wantStatus(t, got, 200)
			if !bytes.Equal(got.Body.Bytes(), data) {
				t.Fatal("published model differs from verified uploaded bytes")
			}
			if href = xetProvenanceBatch(t, w, oid, len(data)); href != "" {
				t.Fatal("verified caller/model upload was not reusable")
			}
			// A different branch has no parent files to inherit. In the write-only
			// cases this succeeds solely from the verified upload, not snapshot Read.
			second := xetProvenanceCommit(t, w, "reuse", "reused.bin", oid, len(data), 200)
			if second == first {
				t.Fatal("reuse did not publish a separate snapshot")
			}
		})
	}
}

func TestUnitXetReadableSameModelSnapshotCanBeReusedWithoutReupload(t *testing.T) {
	w := newXetWorld(t)
	_, revision := publishXetFixture(t, w, "hf")
	xetProvenancePermission(t, w, true)
	data := xetProvenanceData()
	sum := sha256.Sum256(data)
	oid := hex.EncodeToString(sum[:])
	if href := xetProvenanceBatch(t, w, oid, len(data)); href != "" {
		t.Fatal("readable same-model snapshot unnecessarily required upload")
	}
	reused := xetProvenanceCommit(t, w, "reuse", "reused.bin", oid, len(data), 200)
	if reused == revision {
		t.Fatal("new branch reused the old snapshot identity")
	}
	got := w.do(
		"GET",
		"/artifactory/api/huggingfaceml/hf/acme/demo/resolve/"+reused+"/reused.bin",
		"user",
		"",
	)
	wantStatus(t, got, 200)
	if !bytes.Equal(got.Body.Bytes(), data) {
		t.Fatal("readable snapshot reuse changed bytes")
	}
}
