package pushhttp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	artifactsapi "github.com/weaveplatform/artifactweave/internal/artifacts/api"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	platforminmem "github.com/weaveplatform/artifactweave/internal/platform/store/inmem"
	"github.com/weaveplatform/artifactweave/internal/platform/tx/memtx"
)

const deployment = "/custom/artifactory"

type fixtureItem struct {
	body       []byte
	properties map[string][]string
	folder     bool
	version    int
}

type fixture struct {
	mu       sync.Mutex
	items    map[string]fixtureItem
	requests []string
	queries  []string
	// Hooks run under mu and may replace one response. They model failures
	// at the HTTP seam, rather than bypassing the adapter's transport.
	hook func(http.ResponseWriter, *http.Request) bool
	url  string
}

func newFixture(t *testing.T) (*fixture, *Factory, Store, port.PushConnection) {
	t.Helper()
	f := &fixture{items: make(map[string]fixtureItem)}
	router := chi.NewRouter()
	router.HandleFunc("/*", f.serve)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	f.url = server.URL
	store := platforminmem.New(memtx.New())
	factory, err := New(store, Options{RevisionKey: bytes.Repeat([]byte{7}, 32), AllowedPrivateOrigins: []string{server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	var connection port.PushConnection
	err = store.Tx(t.Context(), func(ctx context.Context) error {
		var err error
		connection, err = factory.Connection(ctx, fixtureTarget(server.URL))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return f, factory, store, connection
}

func fixtureTarget(origin string) model.Replication {
	return model.Replication{RepoKey: "source", Key: "target", URL: origin + deployment + "/repo", Username: "worker", Password: "fixture-secret", SocketTimeout: time.Second}
}

func sums(body []byte) kernel.Checksums {
	sum := sha256.Sum256(body)
	return kernel.Checksums{SHA256: hex.EncodeToString(sum[:])}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(err)
	}
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
	username, password, ok := r.BasicAuth()
	if !ok || username != "worker" || password != "fixture-secret" || r.Header.Get("Proxy-Authorization") != "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.hook != nil && f.hook(w, r) {
		return
	}
	switch {
	case r.URL.Path == deployment+"/api/search/aql":
		body, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.queries = append(f.queries, string(body))
		writeJSON(w, map[string]any{"results": []any{}})
	case strings.HasPrefix(r.URL.Path, deployment+"/api/storage/repo"):
		f.storage(w, r)
	case strings.HasPrefix(r.URL.Path, deployment+"/api/metadata/repo/"):
		f.metadata(w, r)
	case strings.HasPrefix(r.URL.Path, deployment+"/repo/"):
		f.item(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fixture) storage(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, deployment+"/api/storage/repo")
	path = strings.TrimPrefix(path, "/")
	if r.URL.Query().Has("list") {
		rows := make([]any, 0, len(f.items))
		for name, item := range f.items {
			if item.folder || path != "" && !strings.HasPrefix(name, path+"/") {
				continue
			}
			relative := name
			if path != "" {
				relative = strings.TrimPrefix(name, path+"/")
			}
			rows = append(rows, map[string]any{"uri": "/" + relative, "folder": false})
		}
		writeJSON(w, map[string]any{"files": rows})
		return
	}
	item, exists := f.items[path]
	if !exists {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"errors": []any{map[string]any{"status": 404, "message": "Unable to find item"}}})
		return
	}
	if r.URL.Query().Has("properties") {
		if len(item.properties) == 0 {
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]any{"errors": []any{map[string]any{"status": 404, "message": "No properties could be found."}}})
			return
		}
		writeJSON(w, map[string]any{"properties": item.properties})
		return
	}
	timestamp := time.Date(2026, 11, 21, 0, 0, item.version, 0, time.UTC)
	info := map[string]any{"repo": "repo", "path": "/" + path, "lastModified": timestamp, "lastUpdated": timestamp}
	if item.folder {
		info["children"] = []any{}
	} else {
		info["size"] = strconv.Itoa(len(item.body))
		info["checksums"] = sums(item.body)
	}
	writeJSON(w, info)
}

func (f *fixture) metadata(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, deployment+"/api/metadata/repo/")
	item, exists := f.items[path]
	if !exists || r.Method != http.MethodPatch || r.URL.Query().Get("recursiveProperties") != "0" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var document struct {
		Props map[string]*[]string `json:"props"`
	}
	if json.NewDecoder(r.Body).Decode(&document) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if item.properties == nil {
		item.properties = make(map[string][]string)
	}
	for key, values := range document.Props {
		if values == nil {
			delete(item.properties, key)
		} else {
			item.properties[key] = *values
		}
	}
	item.version++
	f.items[path] = item
	w.WriteHeader(http.StatusNoContent)
}

func (f *fixture) item(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, deployment+"/repo/")
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil || r.Header.Get("X-Checksum-Deploy") != "" || r.ContentLength != int64(len(body)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		folder := strings.HasSuffix(path, "/")
		if !folder && r.Header.Get("X-Checksum-Sha256") != sums(body).SHA256 {
			w.WriteHeader(http.StatusConflict)
			return
		}
		f.items[strings.TrimSuffix(path, "/")] = fixtureItem{body: body, folder: folder, version: 1}
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		delete(f.items, path)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

type ownedBody struct {
	*bytes.Reader
	closed bool
}

func (b *ownedBody) Close() error { b.closed = true; return nil }

func TestUnitTargetNormalAdmissionMetadataAndExactDeletion(t *testing.T) {
	f, _, _, connection := newFixture(t)
	ctx := t.Context()
	path := "@scope/a;b?c#d%2F space+é.pkg"
	body := []byte("replicated package bytes")
	reader := &ownedBody{Reader: bytes.NewReader(body)}
	if err := connection.Put(ctx, artifactsapi.TransferItem{Path: path, Size: int64(len(body)), Actual: sums(body)}, reader); err != nil {
		t.Fatal(err)
	}
	if reader.closed {
		t.Fatal("transport took ownership of the source body")
	}
	initial, err := connection.Inspect(ctx, path)
	if err != nil || initial.Snapshot.Actual != sums(body) || initial.Snapshot.Size != int64(len(body)) || len(initial.Properties) != 0 {
		t.Fatal("upload metadata", initial, err)
	}
	if err := connection.Properties(ctx, path, map[string][]string{"old": {"remove"}, "keep": {"first"}}); err != nil {
		t.Fatal(err)
	}
	if err := connection.Properties(ctx, path, map[string][]string{"keep": {"second", "third"}}); err != nil {
		t.Fatal(err)
	}
	current, err := connection.Inspect(ctx, path)
	if err != nil || len(current.Properties) != 1 || !slices.Equal(current.Properties["keep"], []string{"second", "third"}) {
		t.Fatal("properties were not replaced", current, err)
	}
	if err := connection.Delete(ctx, path, initial.Snapshot); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("changed file was deleted", err)
	}
	if err := connection.Delete(ctx, path, current.Snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Inspect(ctx, path); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("deleted item survived", err)
	}
	if err := connection.Put(ctx, artifactsapi.TransferItem{Path: "folder", Folder: true}, nil); err != nil {
		t.Fatal(err)
	}
	folder, err := connection.Inspect(ctx, "folder")
	if err != nil || !folder.Snapshot.Folder {
		t.Fatal(folder, err)
	}
	if err := connection.Delete(ctx, "folder", folder.Snapshot); !errors.Is(err, kernel.ErrConflict) {
		t.Fatal("folder deletion admitted", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, request := range f.requests {
		if strings.Contains(request, "a;b") || strings.Contains(request, "fixture-secret") {
			t.Fatal("filename became a matrix parameter or secret entered URL")
		}
	}
}

func TestUnitTargetNetworkRefusesMetadataTransactions(t *testing.T) {
	f, _, store, connection := newFixture(t)
	runner := store.(*platforminmem.Store)
	err := runner.Tx(t.Context(), func(ctx context.Context) error {
		operations := []func() error{
			func() error { _, err := connection.Inspect(ctx, "file"); return err },
			func() error {
				return connection.Put(ctx, artifactsapi.TransferItem{Path: "file", Actual: sums(nil)}, bytes.NewReader(nil))
			},
			func() error { return connection.Properties(ctx, "file", nil) },
			func() error { _, _, err := connection.Files(ctx, "", kernel.Page{}); return err },
			func() error { return connection.Delete(ctx, "file", model.PushTargetSnapshot{}) },
		}
		for _, operation := range operations {
			if err := operation(); !errors.Is(err, kernel.ErrInvalid) {
				t.Fatal("transactional network operation admitted", err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 {
		t.Fatal("HTTP reached target from metadata transaction")
	}
}

func TestUnitTargetRefusalsAreClosedAndSanitized(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		want   error
	}{
		{"unauthorized", 401, port.PushTargetUnauthorized},
		{"forbidden", 403, port.PushTargetForbidden},
		{"missing", 404, kernel.ErrNotFound},
		{"throttled", 429, errUnavailable},
		{"server", 503, errUnavailable},
		{"unexpected", 200, port.PushTargetInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, _, _, connection := newFixture(t)
			f.hook = func(w http.ResponseWriter, _ *http.Request) bool {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, "fixture-secret http://private.invalid/credential")
				return true
			}
			_, err := connection.Inspect(t.Context(), "file")
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), "private.invalid") {
				t.Fatal("unsafe refusal", err)
			}
		})
	}
}

func TestUnitTargetNoPropertiesStillRequiresExistingUnchangedItem(t *testing.T) {
	f, _, _, connection := newFixture(t)
	f.items["file"] = fixtureItem{body: []byte("content")}
	// Delete after the property response, not before its handler reads it.
	var calls atomic.Int32
	f.hook = func(_ http.ResponseWriter, r *http.Request) bool {
		if !r.URL.Query().Has("properties") && calls.Add(1) == 2 {
			delete(f.items, "file")
		}
		return false
	}
	if _, err := connection.Inspect(t.Context(), "file"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("missing item accepted as no properties", err)
	}
}
