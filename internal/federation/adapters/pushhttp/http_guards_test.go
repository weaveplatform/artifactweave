package pushhttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
)

func TestUnitTargetRejectsUnverifiedStorageMetadata(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"wrong repo", func(m map[string]any) { m["repo"] = "elsewhere" }},
		{"wrong path", func(m map[string]any) { m["path"] = "/another" }},
		{"missing modified", func(m map[string]any) { delete(m, "lastModified") }},
		{"missing updated", func(m map[string]any) { delete(m, "lastUpdated") }},
		{"missing size", func(m map[string]any) { delete(m, "size") }},
		{"negative size", func(m map[string]any) { m["size"] = "-1" }},
		{"bad size", func(m map[string]any) { m["size"] = "not a number" }},
		{"missing checksums", func(m map[string]any) { delete(m, "checksums") }},
		{"missing sha256", func(m map[string]any) { m["checksums"] = map[string]any{"sha1": strings.Repeat("0", 40)} }},
		{"bad sha256", func(m map[string]any) { m["checksums"] = map[string]any{"sha256": "invalid"} }},
		{"bad sha1", func(m map[string]any) { m["checksums"] = map[string]any{"sha256": sums(nil).SHA256, "sha1": "invalid"} }},
		{"bad md5", func(m map[string]any) { m["checksums"] = map[string]any{"sha256": sums(nil).SHA256, "md5": "invalid"} }},
		{"ambiguous folder", func(m map[string]any) { m["children"] = []any{} }},
		{"null folder", func(m map[string]any) { delete(m, "size"); delete(m, "checksums"); m["children"] = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, _, _, connection := newFixture(t)
			f.hook = func(w http.ResponseWriter, _ *http.Request) bool {
				info := map[string]any{"repo": "repo", "path": "/file", "size": "0", "checksums": sums(nil), "lastModified": time.Now(), "lastUpdated": time.Now()}
				test.edit(info)
				writeJSON(w, info)
				return true
			}
			if item, err := connection.Inspect(t.Context(), "file"); !errors.Is(err, port.PushTargetInvalid) || item.Path != "" {
				t.Fatal("unverified metadata accepted", item, err)
			}
		})
	}
}

func TestUnitTargetMetadataChangeDuringPropertyReadIsRefused(t *testing.T) {
	f, _, _, connection := newFixture(t)
	f.items["file"] = fixtureItem{body: []byte("original")}
	f.hook = func(_ http.ResponseWriter, r *http.Request) bool {
		if r.URL.Query().Has("properties") {
			item := f.items["file"]
			item.version++
			f.items["file"] = item
		}
		return false
	}
	if item, err := connection.Inspect(t.Context(), "file"); !errors.Is(err, kernel.ErrConflict) || item.Path != "" {
		t.Fatal("mixed metadata snapshot accepted", item, err)
	}
}

type failureReader struct{ err error }

func (r failureReader) Read([]byte) (int, error) { return 0, r.err }

func TestUnitTargetResponseDecodePreservesTransientCauses(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, outbound.ErrConnection, outbound.ErrSecurity, outbound.ErrClosed} {
		t.Run(cause.Error(), func(t *testing.T) {
			var document any
			if err := decodeDocument(failureReader{cause}, &document); !errors.Is(err, cause) {
				t.Fatal("transient response failure lost", err)
			}
		})
	}
	for _, raw := range []string{`{"valid":true} {"extra":true}`, `{"truncated":`, `{"huge":"` + strings.Repeat("x", metadataLimit) + `"}`} {
		var document any
		if err := decodeDocument(strings.NewReader(raw), &document); !errors.Is(err, port.PushTargetInvalid) {
			t.Fatal("unbounded or incomplete metadata admitted", err)
		}
	}
}

func TestUnitTargetBodyTimeoutAndRequestCancellationRemainRetryable(t *testing.T) {
	f, _, _, connection := newFixture(t)
	f.hook = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"repo":`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return true
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err := connection.Inspect(ctx, "file"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("timed out metadata became permanent or success", err)
	}
	cancelled, stop := context.WithCancel(t.Context())
	stop()
	if _, err := connection.Inspect(cancelled, "file"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled request lost cause", err)
	}
}

func TestUnitTargetInputAndPropertyResponseGuards(t *testing.T) {
	f, _, _, connection := newFixture(t)
	f.items["file"] = fixtureItem{body: []byte("content")}
	if err := connection.Put(t.Context(), artifactsapi.TransferItem{Path: "file", Folder: true}, bytes.NewReader(nil)); !errors.Is(err, port.PushTargetInvalid) {
		t.Fatal("folder accepted a body", err)
	}
	if err := connection.Put(t.Context(), artifactsapi.TransferItem{Path: "file"}, nil); !errors.Is(err, port.PushTargetInvalid) {
		t.Fatal("file accepted missing body and checksum", err)
	}
	if err := connection.Properties(t.Context(), "file", map[string][]string{"empty": {}}); !errors.Is(err, port.PushTargetMetadata) {
		t.Fatal("empty property value accepted", err)
	}
	if err := connection.Properties(t.Context(), "file", nil); err != nil {
		t.Fatal("already empty properties failed", err)
	}
	for _, response := range []string{`{}`, `{"properties":null}`, `{"properties":`, `{"errors":[{"status":404,"message":"Item does not exist"}]}`} {
		f.mu.Lock()
		f.hook = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Query().Has("properties") {
				if strings.Contains(response, "errors") {
					w.WriteHeader(http.StatusNotFound)
				}
				_, _ = io.WriteString(w, response)
				return true
			}
			return false
		}
		f.mu.Unlock()
		if item, err := connection.Inspect(t.Context(), "file"); err == nil || item.Path != "" {
			t.Fatal("invalid properties accepted", item, err)
		}
	}
	if err := connection.Delete(t.Context(), "../outside", model.PushTargetSnapshot{}); err == nil {
		t.Fatal("invalid deletion path admitted")
	}
}

func TestUnitTargetWriteAndListingRefusalsPreserveEffects(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		query  string
		status int
		want   error
	}{
		{"upload", http.MethodPut, "", 401, port.PushTargetUnauthorized},
		{"properties", http.MethodPatch, "", 403, port.PushTargetForbidden},
		{"delete", http.MethodDelete, "", 503, errUnavailable},
		{"search", http.MethodPost, "", 429, errUnavailable},
		{"listing", http.MethodGet, "list", 403, port.PushTargetForbidden},
		{"property read", http.MethodGet, "properties", 403, port.PushTargetForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, _, _, connection := newFixture(t)
			body := []byte("retained")
			f.items["file"] = fixtureItem{body: body, properties: map[string][]string{"old": {"keep"}}}
			expected, err := connection.Inspect(t.Context(), "file")
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.hook = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method == test.method && (test.query == "" || r.URL.Query().Has(test.query)) {
					w.WriteHeader(test.status)
					_, _ = io.WriteString(w, "fixture-secret")
					return true
				}
				return false
			}
			f.mu.Unlock()
			switch test.name {
			case "upload":
				err = connection.Put(t.Context(), artifactsapi.TransferItem{Path: "file", Size: int64(len(body)), Actual: sums(body)}, bytes.NewReader(body))
			case "properties":
				err = connection.Properties(t.Context(), "file", nil)
			case "delete":
				err = connection.Delete(t.Context(), "file", expected.Snapshot)
			case "search", "listing":
				var items []port.PushTargetItem
				var next string
				items, next, err = connection.Files(t.Context(), "", kernel.Page{Limit: 1})
				if items != nil || next != "" {
					t.Fatal("refusal returned partial page", items, next)
				}
			case "property read":
				_, err = connection.Inspect(t.Context(), "file")
			}
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal("refusal changed classification or leaked", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if !bytes.Equal(f.items["file"].body, body) || len(f.items["file"].properties) != 1 {
				t.Fatal("refused request changed target effects")
			}
		})
	}
}
