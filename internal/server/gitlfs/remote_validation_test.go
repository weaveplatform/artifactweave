package gitlfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/weaveplatform/artifactweave/internal/auth"
	baseformat "github.com/weaveplatform/artifactweave/internal/format"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/source"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

func TestUnitRemoteBatchRejectsInvalidMetadata(t *testing.T) {
	oid := strings.Repeat("a", 64)
	obj := batchObject{OID: oid, Size: 5}
	base := "https://registry.example/artifactory/api/lfs/origin/objects/batch"
	href := "https://registry.example/artifactory/api/lfs/origin/objects/" + oid
	for _, tc := range []struct {
		name    string
		mutate  func(map[string]any, map[string]any)
		missing bool
		valid   bool
	}{
		{name: "valid action", valid: true},
		{name: "not found", missing: true, mutate: func(_ map[string]any, item map[string]any) {
			item["error"] = map[string]any{"code": http.StatusNotFound}
		}},
		{name: "gone", missing: true, mutate: func(_ map[string]any, item map[string]any) {
			item["error"] = map[string]any{"code": http.StatusGone}
		}},
		{name: "upstream denial", mutate: func(_ map[string]any, item map[string]any) {
			item["error"] = map[string]any{"code": http.StatusForbidden}
		}},
		{name: "unknown transfer", mutate: func(batch map[string]any, _ map[string]any) {
			batch["transfer"] = "tus"
		}},
		{name: "empty objects", mutate: func(batch map[string]any, _ map[string]any) {
			batch["objects"] = []any{}
		}},
		{name: "wrong object", mutate: func(_ map[string]any, item map[string]any) {
			item["oid"] = strings.Repeat("b", 64)
		}},
		{name: "wrong size", mutate: func(_ map[string]any, item map[string]any) {
			item["size"] = 4
		}},
		{name: "missing action", mutate: func(_ map[string]any, item map[string]any) {
			delete(item, "actions")
		}},
		{name: "foreign action", mutate: func(_ map[string]any, item map[string]any) {
			item["actions"] = map[string]any{"download": map[string]any{
				"href": "https://foreign.example/objects/" + oid,
			}}
		}},
		{name: "unsafe action header", mutate: func(_ map[string]any, item map[string]any) {
			item["actions"] = map[string]any{"download": map[string]any{
				"href": href, "header": map[string]string{"Cookie": "secret=value"},
			}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := map[string]any{
				"oid": oid, "size": obj.Size,
				"actions": map[string]any{"download": map[string]any{"href": href}},
			}
			batch := map[string]any{"transfer": "basic", "objects": []any{item}}
			if tc.mutate != nil {
				tc.mutate(batch, item)
			}
			data, err := json.Marshal(batch)
			if err != nil {
				t.Fatal(err)
			}
			action, missing, err := parseRemoteBatch(
				strings.NewReader(string(data)), int64(len(data)), base, obj,
			)
			switch {
			case tc.valid && (err != nil || missing || action.Href != href):
				t.Fatalf("valid action = %+v, missing=%t, err=%v", action, missing, err)
			case tc.missing && (err != nil || !missing || action.Href != ""):
				t.Fatalf("missing upstream object = %+v, missing=%t, err=%v", action, missing, err)
			case !tc.valid && !tc.missing && !errors.Is(err, source.ErrInvalidResult):
				t.Fatalf(
					"invalid upstream metadata admitted: %+v, missing=%t, err=%v",
					action,
					missing,
					err,
				)
			}
		})
	}
	for _, tc := range []struct {
		name string
		body string
		size int64
	}{
		{name: "empty", body: "", size: 0},
		{name: "oversized declaration", body: "{}", size: maxBatchResponse + 1},
		{name: "truncated", body: "{}", size: 3},
		{name: "invalid JSON", body: "{", size: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseRemoteBatch(strings.NewReader(tc.body), tc.size, base, obj)
			if !errors.Is(err, source.ErrInvalidResult) {
				t.Fatalf("invalid upstream body admitted: %v", err)
			}
		})
	}
}

func TestUnitRemoteActionURLKeepsObjectAndAuthority(t *testing.T) {
	oid := strings.Repeat("a", 64)
	base := "https://registry.example/artifactory/api/lfs/origin/objects/batch"
	valid := "https://registry.example/artifactory/api/lfs/origin/objects/" + oid
	for _, tc := range []struct {
		name string
		base string
		href string
		ok   bool
	}{
		{name: "object action", base: base, href: valid, ok: true},
		{name: "foreign host", base: base, href: "https://foreign.example/artifactory/api/lfs/origin/objects/" + oid},
		{name: "credentialed URL", base: base, href: "https://user:secret@registry.example/artifactory/api/lfs/origin/objects/" + oid},
		{name: "URL fragment", base: base, href: valid + "#secret"},
		{name: "wrong object", base: base, href: strings.Replace(valid, oid, strings.Repeat("b", 64), 1)},
		{name: "invalid base", base: "not a URL", href: valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validActionURL(tc.base, tc.href, oid); got != tc.ok {
				t.Fatalf("action authority/object validation = %t, want %t", got, tc.ok)
			}
		})
	}
}

func TestUnitActionRedirectGuardDropsUnsafeDestinations(t *testing.T) {
	target := "https://registry.example/artifactory/api/lfs/origin/objects/a"
	guard := actionURLGuard(target)
	for _, tc := range []struct {
		name string
		href string
		ok   bool
	}{
		{name: "exact action", href: target, ok: true},
		{name: "same origin", href: "https://registry.example/storage/a", ok: true},
		{name: "cross-origin HTTPS", href: "https://cdn.example/object", ok: true},
		{name: "cross-origin HTTP", href: "http://cdn.example/object"},
		{name: "credentialed URL", href: "https://user:secret@cdn.example/object"},
		{name: "fragment", href: "https://cdn.example/object#secret"},
		{name: "unsupported scheme", href: "ftp://cdn.example/object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.href)
			if err != nil {
				t.Fatal(err)
			}
			err = guard(t.Context(), u)
			if tc.ok && err != nil || !tc.ok && !errors.Is(err, source.ErrDenied) {
				t.Fatalf("redirect %q: %v", tc.href, err)
			}
		})
	}
	if err := guard(context.Background(), nil); !errors.Is(err, source.ErrDenied) {
		t.Fatalf("nil redirect admitted: %v", err)
	}
}

func TestUnitCachedObjectIdentityRequiresDigestAndSize(t *testing.T) {
	oid := strings.Repeat("a", 64)
	obj := batchObject{OID: oid, Size: 5}
	for _, tc := range []struct {
		name   string
		result source.Result
		valid  bool
	}{
		{name: "matching cached item", result: source.Result{Item: &types.Item{
			Size: 5, Actual: types.Checksums{SHA256: oid},
		}}, valid: true},
		{name: "wrong cached digest", result: source.Result{Item: &types.Item{
			Size: 5, Actual: types.Checksums{SHA256: strings.Repeat("b", 64)},
		}}},
		{name: "folder masquerading as object", result: source.Result{Item: &types.Item{
			Folder: true, Size: 5, Actual: types.Checksums{SHA256: oid},
		}}},
		{name: "wrong stream size", result: source.Result{Stream: &source.Stream{
			Size: 4, Checksums: types.Checksums{SHA256: oid},
		}}},
		{name: "matching stream", result: source.Result{Stream: &source.Stream{
			Size: 5, Checksums: types.Checksums{SHA256: oid},
		}}, valid: true},
		{name: "missing source", result: source.Result{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := resultIdentity(tc.result, obj)
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, source.ErrInvalidResult) {
				t.Fatalf("cached object identity accepted=%t: %v", tc.valid, err)
			}
		})
	}
}

func TestUnitRemoteObjectResourceRejectsChangedBytes(t *testing.T) {
	content := []byte("verified native Git LFS bytes")
	sum := sha256.Sum256(content)
	obj := batchObject{OID: hex.EncodeToString(sum[:]), Size: int64(len(content))}
	resource := objectResource(obj)
	if resource.Validate == nil || resource.Checksums.SHA256 != obj.OID {
		t.Fatalf("remote object lacks a pinned integrity check: %+v", resource)
	}
	for _, tc := range []struct {
		name string
		body []byte
		size int64
		ok   bool
	}{
		{name: "unchanged object", body: content, size: int64(len(content)), ok: true},
		{name: "wrong declared size", body: content, size: int64(len(content) + 1)},
		{name: "same-size substituted bytes", body: bytes.Repeat([]byte("x"), len(content)), size: int64(len(content))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := resource.Validate(t.Context(), bytes.NewReader(tc.body), tc.size)
			if tc.ok && err != nil || !tc.ok && !errors.Is(err, source.ErrInvalidResult) {
				t.Fatalf("remote object validation: %v", err)
			}
		})
	}
}

func TestUnitBatchErrorStatusPreservesClientMeaning(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{name: "protocol error", err: apierror.New(http.StatusBadRequest, "invalid"), want: http.StatusBadRequest},
		{name: "format rejection", err: baseformat.Rejectf(http.StatusUnprocessableEntity, "invalid"), want: http.StatusUnprocessableEntity},
		{name: "permission denied", err: auth.ErrForbidden, want: http.StatusForbidden},
		{name: "source denied", err: source.ErrDenied, want: http.StatusForbidden},
		{name: "authentication required", err: auth.ErrUnauthenticated, want: http.StatusUnauthorized},
		{name: "missing object", err: store.ErrNotFound, want: http.StatusNotFound},
		{name: "conflicting object", err: store.ErrConflict, want: http.StatusConflict},
		{name: "upstream fault", err: errors.New("upstream fault"), want: http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusOf(tc.err); got != tc.want {
				t.Fatalf("batch error status = %d, want %d", got, tc.want)
			}
		})
	}
}
