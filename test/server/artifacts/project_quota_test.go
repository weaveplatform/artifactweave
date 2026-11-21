package artifacts_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestAccProjectQuotaRemoteReads(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := remoteFolderWorld(t, backend)
			w.Repo.RetrievalCachePeriodSecs = ptr(30)
			w.Update()
			ctx := t.Context()
			call := func(method, path, body string, want int) string {
				t.Helper()
				rec := w.Request(method, path, "admin", body, "Content-Type", "application/json")
				if rec.Code != want {
					t.Fatalf("%s %s = %d %s, want %d", method, path, rec.Code, rec.Body, want)
				}
				return rec.Body.String()
			}
			bytes := func(want int64) {
				t.Helper()
				state, initialized, err := w.Runtime.Store.Projects().Storage(ctx, "alpha")
				if err != nil || !initialized || state.Bytes != want {
					t.Fatalf("committed project storage = %d, initialized=%v, want %d: %v", state.Bytes, initialized, want, err)
				}
				usage := w.Runtime.Store.Repos().(artifactsapi.StorageUsage)
				total, err := usage.ProjectStorageBytes(ctx, "alpha")
				if err != nil || total != want {
					t.Fatalf("actual retained project bytes = %d, want %d: %v", total, want, err)
				}
			}
			read := func(path, want string) {
				t.Helper()
				rec := w.Request(http.MethodGet, "/artifactory/remote/"+path, "reader", "")
				if rec.Code != http.StatusOK || rec.Body.String() != want {
					t.Fatalf("GET %s = %d %q, want current upstream content", path, rec.Code, rec.Body.String())
				}
			}
			call(http.MethodPost, "/access/api/v1/projects", `{"project_key":"alpha","display_name":"Alpha","storage_quota_bytes":100}`, http.StatusCreated)
			call(http.MethodPost, "/artifactory/api/repositories/remote", `{"projectKey":"alpha"}`, http.StatusOK)
			call(http.MethodPut, "/artifactory/api/repositories/alpha-local", `{"rclass":"local","packageType":"generic","projectKey":"alpha"}`, http.StatusOK)
			warm := strings.Repeat("w", 20)
			w.Upstream.Set("/warm.bin", fakeupstream.Resource{Body: []byte(warm), ETag: `"old"`, Header: http.Header{"Cache-Control": {"max-age=0"}}})
			read("warm.bin", warm)
			bytes(20)
			call(http.MethodPut, "/artifactory/alpha-local/base.bin", strings.Repeat("a", 80), http.StatusCreated)
			bytes(100)
			read("warm.bin", warm)
			bytes(100)
			w.Upstream.Set("/fresh.bin", fakeupstream.Resource{Body: []byte("fresh")})
			for range 2 {
				read("fresh.bin", "fresh")
				bytes(100)
				if _, err := w.Runtime.Store.Items().Get(ctx, "remote", true, "fresh.bin"); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("quota-constrained read retained a cache item", err)
				}
			}
			// A larger current upstream response remains downloadable without
			// replacing the old retained representation or charging its bytes.
			newer := strings.Repeat("n", 40)
			w.Upstream.Set("/warm.bin", fakeupstream.Resource{Body: []byte(newer), ETag: `"new"`, Header: http.Header{"Cache-Control": {"max-age=0"}}})
			read("warm.bin", newer)
			bytes(100)
			retained, err := w.Runtime.Store.Items().Get(ctx, "remote", true, "warm.bin")
			if err != nil || retained.Size != 20 {
				t.Fatal("transient read changed the retained copy", retained.Size, err)
			}
			call(http.MethodPut, "/access/api/v1/projects/alpha", `{"storage_quota_bytes":50}`, http.StatusOK)
			read("fresh.bin", "fresh")
			bytes(100)
			asked := len(w.Upstream.Requests())
			if rec := w.Request(http.MethodGet, "/artifactory/remote/fresh.bin", "denied", ""); rec.Code != http.StatusForbidden {
				t.Fatal("quota handling changed the existing authorization refusal", rec.Code)
			}
			if len(w.Upstream.Requests()) != asked {
				t.Fatal("denied read reached the upstream")
			}
			// A successful response that cannot be retained still recovers the
			// source's connection state. There is one shared admission attempt
			// and one owned transient exchange, not another recovery probe.
			state, err := w.Runtime.Store.RemoteCache().State(ctx, "remote")
			if err != nil {
				t.Fatal(err)
			}
			state.ConnectionFailures, state.OfflineUntil = 3, time.Now().Add(-time.Second)
			if err := w.Runtime.Store.RemoteCache().PutState(ctx, "remote", state); err != nil {
				t.Fatal(err)
			}
			w.Upstream.Set("/recovery.bin", fakeupstream.Resource{Body: []byte("recovered")})
			read("recovery.bin", "recovered")
			if w.Upstream.Count(http.MethodGet, "/recovery.bin") != 2 {
				t.Fatal("quota refusal duplicated source recovery probes")
			}
			state, err = w.Runtime.Store.RemoteCache().State(ctx, "remote")
			if err != nil || state.ConnectionFailures != 0 || !state.OfflineUntil.IsZero() {
				t.Fatal("quota refusal left the healthy upstream offline", err)
			}
			// The transient path retains normal integrity failures. Quota
			// pressure must never turn bad upstream bytes into a successful read.
			w.Upstream.Set("/invalid.bin", fakeupstream.Resource{Body: []byte("invalid"), Header: http.Header{"X-Checksum-Sha256": {strings.Repeat("0", 64)}}})
			if rec := w.Request(http.MethodGet, "/artifactory/remote/invalid.bin", "reader", ""); rec.Code != http.StatusBadGateway {
				t.Fatal("quota handling bypassed checksum admission", rec.Code)
			}
			w.Upstream.Set("/parallel.bin", fakeupstream.Resource{Body: []byte(strings.Repeat("p", 64))})
			responses := make(chan *httptest.ResponseRecorder, 2)
			var readers sync.WaitGroup
			for range 2 {
				readers.Go(func() { responses <- w.Request(http.MethodGet, "/artifactory/remote/parallel.bin", "reader", "") })
			}
			readers.Wait()
			close(responses)
			for rec := range responses {
				if rec.Code != http.StatusOK || rec.Body.String() != strings.Repeat("p", 64) {
					t.Fatal("concurrent transient readers shared or truncated a body", rec.Code)
				}
			}
			bytes(100)
			call(http.MethodPut, "/access/api/v1/projects/alpha", `{"soft_limit":true}`, http.StatusOK)
			w.Upstream.Set("/soft.bin", fakeupstream.Resource{Body: []byte("soft123")})
			read("soft.bin", "soft123")
			bytes(107)
			call(http.MethodPut, "/access/api/v1/projects/alpha", `{"soft_limit":false,"storage_quota_bytes":0}`, http.StatusOK)
			w.Upstream.Set("/unlimited.bin", fakeupstream.Resource{Body: []byte("unlimited")})
			read("unlimited.bin", "unlimited")
			bytes(116)
		})
	}
}

func TestAccProjectQuotaTransformedPOMReads(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			w := remoteFolderWorld(t, backend)
			call := func(method, path, body string, want int) *httptest.ResponseRecorder {
				t.Helper()
				rec := w.Request(method, path, "admin", body, "Content-Type", "application/json")
				if rec.Code != want {
					t.Fatalf("%s %s = %d %s, want %d", method, path, rec.Code, rec.Body, want)
				}
				return rec
			}
			call(http.MethodPost, "/access/api/v1/projects", `{"project_key":"alpha","display_name":"Alpha","storage_quota_bytes":1}`, http.StatusCreated)
			call(http.MethodPut, "/artifactory/api/repositories/source-maven", `{"rclass":"local","packageType":"maven"}`, http.StatusOK)
			call(
				http.MethodPut,
				"/artifactory/api/repositories/alpha-virtual",
				`{"rclass":"virtual","packageType":"maven","repositories":["source-maven"],"projectKey":"alpha"}`,
				http.StatusOK,
			)
			const path = "org/acme/app/1.0/app-1.0.pom"
			const body = `<project><modelVersion>4.0.0</modelVersion><groupId>org.acme</groupId><artifactId>app</artifactId><version>1.0</version><repositories/><name>kept</name></project>`
			call(http.MethodPut, "/artifactory/source-maven/"+path, body, http.StatusCreated)
			var cleaned string
			var etag string
			for range 2 {
				rec := call(http.MethodGet, "/artifactory/alpha-virtual/"+path, "", http.StatusOK)
				cleaned, etag = rec.Body.String(), rec.Header().Get("ETag")
				if strings.Contains(rec.Body.String(), "<repositories") || !strings.Contains(rec.Body.String(), "<name>kept</name>") {
					t.Fatal("quota pressure changed POM cleanup")
				}
				state, initialized, err := w.Runtime.Store.Projects().Storage(t.Context(), "alpha")
				if err != nil || !initialized || state.Bytes != 0 {
					t.Fatal("transient POM read consumed quota", state.Bytes, err)
				}
				if _, err := w.Runtime.Store.VirtualCopies().Get(t.Context(), "alpha-virtual", path); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatal("quota-constrained transformed copy was retained", err)
				}
			}
			call(http.MethodHead, "/artifactory/alpha-virtual/"+path, "", http.StatusOK)
			sum := sha256.Sum256([]byte(cleaned))
			if rec := call(http.MethodGet, "/artifactory/alpha-virtual/"+path+".sha256", "", http.StatusOK); strings.TrimSpace(rec.Body.String()) != hex.EncodeToString(sum[:]) {
				t.Fatal("transient POM sidecar describes different bytes")
			}
			partial := w.Request(http.MethodGet, "/artifactory/alpha-virtual/"+path, "admin", "", "Range", "bytes=0-9")
			if partial.Code != http.StatusPartialContent || partial.Body.String() != cleaned[:10] {
				t.Fatal("transient POM did not preserve range delivery", partial.Code)
			}
			conditional := w.Request(http.MethodGet, "/artifactory/alpha-virtual/"+path, "admin", "", "If-None-Match", etag)
			if conditional.Code != http.StatusNotModified {
				t.Fatal("transient POM did not preserve conditional delivery", conditional.Code)
			}
			call(http.MethodPut, "/access/api/v1/projects/alpha", `{"storage_quota_bytes":1000000}`, http.StatusOK)
			call(http.MethodGet, "/artifactory/alpha-virtual/"+path, "", http.StatusOK)
			copy, err := w.Runtime.Store.VirtualCopies().Get(t.Context(), "alpha-virtual", path)
			if err != nil {
				t.Fatal("available quota did not retain the ordinary transformed copy", err)
			}
			state, _, err := w.Runtime.Store.Projects().Storage(t.Context(), "alpha")
			if err != nil || state.Bytes != copy.Item.Size || state.Bytes == 0 {
				t.Fatal("retained transformed bytes differ from project accounting", state.Bytes, copy.Item.Size, err)
			}
		})
	}
}
