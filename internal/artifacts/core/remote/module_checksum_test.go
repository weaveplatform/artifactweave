package remote_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
)

func TestUnitModuleChecksumProjectionMIMECompleteBytesAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name, mime, body string
		status           int
		allowed          bool
	}{
		{"text", "text/plain", "abc\n", 200, true},
		{"utf8", "text/plain; charset=UTF-8", "abc\n", 200, true},
		{"ascii", "text/plain; charset=US-ASCII", "abc\r\n", 200, true},
		{"binary", "application/octet-stream", "abc\n", 200, true},
		{"html", "text/html", "abc\n", 200, false},
		{"json", "application/json", "abc\n", 200, false},
		{"missing-mime", "", "abc\n", 200, false},
		{"wrong-status", "text/plain", "abc\n", 204, false},
		{"unsupported-charset", "text/plain; charset=utf-16", "abc\n", 200, false},
		{"binary-charset", "application/octet-stream; charset=utf-8", "abc\n", 200, false},
		{"invalid-utf8", "text/plain", string([]byte{255}), 200, false},
		{"non-ascii", "text/plain; charset=us-ascii", "é", 200, false},
		{"control", "text/plain", "abc\x00\n", 200, false},
		{"oversized", "text/plain", strings.Repeat("x", source.MaxModuleChecksumDocumentBytes+1), 200, false},
		{"maximum-size", "text/plain", strings.Repeat("x", source.MaxModuleChecksumDocumentBytes), 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := &http.Response{
				StatusCode:    tc.status,
				ContentLength: int64(len(tc.body)),
				Body:          io.NopCloser(strings.NewReader(tc.body)),
				Header:        http.Header{"Content-Type": {tc.mime}, "X-Terraform-Get": {"https://never-follow.example/archive.zip"}, "ETag": {"upstream-validator"}},
			}
			err := remote.ProjectModuleResponse(source.Request{Resource: source.Resource{ModuleChecksumDocument: true}}, response)
			defer response.Body.Close()
			if (err == nil) != tc.allowed {
				t.Fatal("checksum-file projection result", err)
			}
			if tc.allowed {
				var control source.ModuleChecksumResponse
				if json.NewDecoder(response.Body).Decode(&control) != nil || !bytes.Equal(control.Content, []byte(tc.body)) || response.Header.Get("ETag") != "" ||
					response.Header.Get("X-Terraform-Get") != "" ||
					response.Header.Get("Content-Type") != "application/json" {
					t.Fatal("checksum file changed bytes or retained upstream control headers")
				}
			}
		})
	}
}

func TestAccModuleChecksumProjectionCacheRefreshOfflineAndTransient(t *testing.T) {
	for _, mode := range []string{"stored", "upstream-no-store", "no-local-store"} {
		t.Run(mode, func(t *testing.T) {
			w := policyFixture(t)
			q := vanityRequest(w)
			q.Resource.ModuleVanityDocument = false
			q.Resource.ModuleChecksumDocument = true
			q.Resource.Variant = "module-checksum-test-v1"
			body := strings.Repeat("a", 64) + "  module.zip\n"
			q.Resource.ValidateMetadata = func(_ context.Context, r io.ReadSeeker, _ int64, _ source.MetadataResponse) error {
				var control source.ModuleChecksumResponse
				if json.NewDecoder(r).Decode(&control) != nil || string(control.Content) != body {
					return source.ErrInvalidResult
				}
				return nil
			}
			if mode == "no-local-store" {
				q.Repo.StoreArtifactsLocally = ptr(false)
			}
			header := http.Header{"Content-Type": {"text/plain"}}
			if mode == "upstream-no-store" {
				header.Set("Cache-Control", "no-store")
			}
			w.up.Set("/module/vanity", fakeupstream.Resource{Body: []byte(body), Header: header, ETag: `"sum-file"`})
			result, err := w.read(q)
			if err != nil {
				t.Fatal(err)
			}
			raw := w.bytes(result)
			if mode == "no-local-store" {
				entries, _, err := w.db.RemoteCache().List(t.Context(), q.Repo.Key, "", kernel.Page{})
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Blob.Checksums.SHA256 != "" {
						t.Fatal("transient checksum control persisted")
					}
				}
				return
			}
			calls := len(w.up.Requests())
			w.mustRead(q, raw)
			if len(w.up.Requests()) != calls {
				t.Fatal("warm checksum file contacted origin")
			}
			w.clock.Advance(time.Minute)
			w.mustRead(q, raw)
			for _, request := range w.up.Requests() {
				if request.Header.Get("If-None-Match") != "" || request.Header.Get("If-Modified-Since") != "" {
					t.Fatal("projected checksum control borrowed original validators")
				}
			}
			q.Repo.Offline = ptr(true)
			w.up.Close()
			w.mustRead(q, raw)
		})
	}
}
