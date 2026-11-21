package peerstorage_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/peerstorage"
	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/upstreamtest"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/item"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/port"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote"
	"github.com/weaveplatform/artifactweave/internal/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/blob/fs"
)

type response struct {
	body     string
	status   int
	redirect string
	entered  chan struct{}
	release  chan struct{}
}

type world struct {
	t         *testing.T
	db        *inmem.Store
	service   *source.Service
	request   source.Request
	mu        sync.Mutex
	documents map[string]response
	requests  []string
	revision  string
	revoked   atomic.Bool
}

func fixture(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, db: inmem.New(), documents: map[string]response{}, revision: "credential-1"}
	router := chi.NewRouter()
	router.Get("/*", func(writer http.ResponseWriter, request *http.Request) {
		if user, password, ok := request.BasicAuth(); !ok || user != "fixture" || password != "lab-only" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		key := strings.TrimPrefix(request.URL.Path, "/artifactory/api/storage/source")
		if request.URL.RawQuery != "" {
			key += "?" + request.URL.RawQuery
		}
		w.mu.Lock()
		w.requests = append(w.requests, key)
		document, exists := w.documents[key]
		w.mu.Unlock()
		if !exists {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if document.entered != nil {
			close(document.entered)
			select {
			case <-document.release:
			case <-request.Context().Done():
				return
			}
		}
		if document.redirect != "" {
			http.Redirect(writer, request, document.redirect, http.StatusFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if document.status != 0 {
			writer.WriteHeader(document.status)
		}
		if _, err := writer.Write([]byte(document.body)); err != nil {
			return
		}
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	config := *repo.Defaults(&repo.Config{
		Key: "remote", Class: repo.Remote, PackageType: repo.Generic,
		URL: server.URL + "/artifactory/source",
	})
	if err := w.db.Repos().Create(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	blobs, err := fs.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policy := &remote.Policy{
		Store: w.db, Blobs: blobs, Items: &item.Service{Store: w.db, Blobs: blobs},
		Pool: upstreamtest.NewPool(t), Key: []byte("private-test-only-pull-cache-key"),
		Options: func(context.Context, repo.Config) (remote.Options, error) {
			w.mu.Lock()
			revision := w.revision
			w.mu.Unlock()
			return remote.Options{
				Revision: revision,
				Prepare: func(request *http.Request) error {
					request.SetBasicAuth("fixture", "lab-only")
					return nil
				},
				CheckURL: func(_ context.Context, candidate *url.URL) error {
					if !strings.HasPrefix(candidate.String(), server.URL+"/") {
						return remote.ErrSecurity
					}
					return nil
				},
			}, nil
		},
		CheckCurrent: func(ctx context.Context, store remote.Store, q source.Request, permit source.Permit, revision string) error {
			w.mu.Lock()
			currentRevision := w.revision
			w.mu.Unlock()
			if currentRevision != revision {
				return source.ErrSuperseded
			}
			current, err := store.Repos().Get(ctx, q.Repo.Key)
			if err != nil || port.RemotePolicyChanged(q.Repo, current) {
				return source.ErrSuperseded
			}
			identity, err := store.RemoteCache().Source(ctx, q.Repo.Key)
			if err != nil || identity != permit.Identity {
				return source.ErrSuperseded
			}
			return nil
		},
	}
	w.service = &source.Service{
		HTTP: policy, Cache: policy,
		Resolve: func(ctx context.Context, c repo.Config) (model.SourceIdentity, error) {
			return w.db.RemoteCache().Source(ctx, c.Key)
		},
		Revalidate: func(context.Context, source.Request, source.Permit) error {
			if w.revoked.Load() {
				return source.ErrDenied
			}
			return nil
		},
	}
	subject := accessapi.Subject{Name: "reader", Admin: true}
	access, err := accesstest.Over(t, w.db).Decide(t.Context(), subject, false)
	if err != nil {
		t.Fatal(err)
	}
	w.request = source.Request{Repo: config, Subject: subject, Access: access}
	w.set("", `{"repo":"source","path":"/","children":[]}`)
	return w
}

func (w *world) set(path, body string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.documents[path] = response{body: body}
}

func (w *world) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.requests)
}

func (w *world) reader(root string) *peerstorage.Reader {
	w.t.Helper()
	r, err := peerstorage.New(w.t.Context(), w.service, w.request, root, remote.DiscoveryScope{Path: root})
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

func fileInfo(path string) string {
	return fmt.Sprintf(
		`{"repo":"source","path":%q,"size":"4","checksums":{"sha256":%q},"lastModified":"2026-11-20T10:00:00Z","lastUpdated":"2026-11-20T10:00:00Z","downloadUri":"https://untrusted.invalid/never-used"}`,
		"/"+path,
		strings.Repeat("a", 64),
	)
}

const listQuery = "?deep=1&list=&listFolders=0"

func TestAccFilesVerifyOrdinaryStorageMetadataAndResumeBoundedPages(t *testing.T) {
	w := fixture(t)
	w.set("/selected", `{"repo":"source","path":"/selected","children":[]}`)
	w.set("/selected"+listQuery, `{"files":[{"uri":"/d.bin","folder":false},{"uri":"/b.bin","folder":false},{"uri":"/c.bin","folder":false},{"uri":"/a.bin","folder":false}]}`)
	for _, name := range []string{"a", "b", "c", "d"} {
		w.set("/selected/"+name+".bin", fileInfo("selected/"+name+".bin"))
	}
	r := w.reader("selected")
	first, next, err := r.Files(t.Context(), kernel.Page{Limit: 2})
	if err != nil || len(first) != 2 || first[0].Path != "selected/a.bin" || first[1].Path != "selected/b.bin" || next == "" {
		t.Fatalf("first page %+v next=%q err=%v", first, next, err)
	}
	if first[0].Size != 4 || first[0].Checksums.SHA256 != strings.Repeat("a", 64) || first[0].Modified.IsZero() {
		t.Fatalf("unverified selected facts %+v", first[0])
	}
	// A fresh operation instance can continue under the same current source.
	r = w.reader("selected")
	second, final, err := r.Files(t.Context(), kernel.Page{Limit: 2, After: next})
	if err != nil || len(second) != 2 || second[0].Path != "selected/c.bin" || second[1].Path != "selected/d.bin" || final != "" {
		t.Fatalf("second page %+v next=%q err=%v", second, final, err)
	}
	children, _, err := w.db.Items().Children(t.Context(), "remote", true, "", kernel.Page{})
	if err != nil || len(children) != 0 {
		t.Fatalf("discovery manufactured cache content %+v %v", children, err)
	}
	if w.count() != 8 {
		t.Fatalf("unexpected requests %d: only anchor, complete list and selected info are allowed", w.count())
	}
}

func TestAccFilesSingleFileAndMissingRootRequireCurrentNamespace(t *testing.T) {
	w := fixture(t)
	w.set("/one.bin", fileInfo("one.bin"))
	files, next, err := w.reader("one.bin").Files(t.Context(), kernel.Page{})
	if err != nil || len(files) != 1 || files[0].Path != "one.bin" || next != "" || w.count() != 1 {
		t.Fatalf("single file %+v %q %v requests=%d", files, next, err, w.count())
	}
	files, next, err = w.reader("missing").Files(t.Context(), kernel.Page{})
	if err != nil || len(files) != 0 || next != "" || w.count() != 3 {
		t.Fatalf("missing scope %+v %q %v requests=%d", files, next, err, w.count())
	}
	w.set("", `{"repo":"other","path":"/","children":[]}`)
	if _, _, err = w.reader("missing").Files(t.Context(), kernel.Page{}); err == nil {
		t.Fatal("accepted a missing scope without a valid repository anchor")
	}
}

func TestAccPullDiscoveryAcceptsTheConfiguredUpstreamsCacheAliasMetadata(t *testing.T) {
	w := fixture(t)
	// Storage info names a cached remote by <configured-key>-cache while
	// the list and subsequent download stay under its configured key.
	w.set("", `{"repo":"source-cache","path":"/","children":[]}`)
	w.set(listQuery, `{"files":[{"uri":"/a.bin","folder":false}]}`)
	w.set("/a.bin", strings.Replace(fileInfo("a.bin"), `"repo":"source"`, `"repo":"source-cache"`, 1))
	files, next, err := w.reader("").Files(t.Context(), kernel.Page{})
	if err != nil || len(files) != 1 || next != "" || files[0].Path != "a.bin" {
		t.Fatalf("configured remote source alias %+v %q %v", files, next, err)
	}
	// The accepted alias changes no URL authority or selected repository.
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, requested := range w.requests {
		if strings.Contains(requested, "-cache") {
			t.Fatal("reported alias rewrote the configured metadata destination")
		}
	}
}

func TestAccPullMetadataRejectsMalformedOrEscapingListings(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"missing-files", `{}`},
		{"non-object", `[]`},
		{"empty-body", ``},
		{"malformed-field", `{"ignored":!}`},
		{"malformed-after-files", `{"files":[],"ignored":!}`},
		{"null-files", `{"files":null}`},
		{"duplicate-files", `{"files":[],"files":[]}`},
		{"truncated", `{"files":[`},
		{"trailing", `{"files":[]} {}`},
		{"folder", `{"files":[{"uri":"/a","folder":true}]}`},
		{"untyped", `{"files":[{"uri":"/a"}]}`},
		{"relative", `{"files":[{"uri":"a","folder":false}]}`},
		{"escape", `{"files":[{"uri":"/../a","folder":false}]}`},
		{"encoded-escape", `{"files":[{"uri":"/%2e%2e/a","folder":false}]}`},
		{"duplicate-entry", `{"files":[{"uri":"/a","folder":false},{"uri":"/a","folder":false}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := fixture(t)
			w.set(listQuery, test.body)
			files, next, err := w.reader("").Files(t.Context(), kernel.Page{Limit: 2})
			if err == nil || len(files) != 0 || next != "" || w.count() != 2 {
				t.Fatalf("malformed listing %+v %q %v requests=%d", files, next, err, w.count())
			}
		})
	}
}

func TestAccPullMetadataUsesComputedChecksumsAndSeparateProperties(t *testing.T) {
	w := fixture(t)
	info := strings.Replace(fileInfo("a.bin"), strings.Repeat("a", 64), strings.Repeat("A", 64), 1)
	info = strings.Replace(info, `"checksums":{`, `"checksums":{"sha1":"`+strings.Repeat("B", 40)+`","md5":"`+strings.Repeat("C", 32)+`",`, 1)
	w.set("/a.bin", info)
	w.set("/a.bin?properties=", `{"properties":{"build.number":["2","1"]}}`)
	r := w.reader("")
	entry, err := r.Inspect(t.Context(), "a.bin")
	if err != nil || entry.Checksums.SHA256 != strings.Repeat("a", 64) || entry.Checksums.SHA1 != strings.Repeat("b", 40) || entry.Checksums.MD5 != strings.Repeat("c", 32) {
		t.Fatalf("computed checksums %+v %v", entry, err)
	}
	properties, err := r.Properties(t.Context(), "a.bin")
	if err != nil || !reflect.DeepEqual(properties, model.Properties{"build.number": {"2", "1"}}) || w.count() != 2 {
		t.Fatalf("separate properties %+v %v requests=%d", properties, err, w.count())
	}
	for _, test := range []struct{ name, body string }{
		{"missing-properties", `{}`},
		{"null-properties", `{"properties":null}`},
		{"empty-values", `{"properties":{"key":[]}}`},
		{"invalid-key", `{"properties":{"bad/key":["a"]}}`},
		{"trailing", `{"properties":{}}{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			w.set("/a.bin?properties=", test.body)
			if _, err := r.Properties(t.Context(), "a.bin"); err == nil {
				t.Fatal("accepted invalid properties")
			}
		})
	}
}

func TestAccPullMetadataRejectsIncompleteFileFacts(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"wrong-repository", strings.Replace(fileInfo("a"), `"source"`, `"other"`, 1)},
		{"wrong-path", fileInfo("b")},
		{"absent-timestamp", strings.Replace(fileInfo("a"), `"lastUpdated":"2026-11-20T10:00:00Z",`, ``, 1)},
		{"short-digest", strings.Replace(fileInfo("a"), strings.Repeat("a", 64), "abc", 1)},
		{"bad-optional-digest", strings.Replace(fileInfo("a"), `"checksums":{`, `"checksums":{"sha1":"bad",`, 1)},
		{"negative-size", strings.Replace(fileInfo("a"), `"size":"4"`, `"size":"-1"`, 1)},
		{"invalid-digest", strings.Replace(fileInfo("a"), strings.Repeat("a", 64), strings.Repeat("z", 64), 1)},
		{"declared-only", strings.Replace(fileInfo("a"), `"checksums"`, `"originalChecksums"`, 1)},
		{"null-folder", `{"repo":"source","path":"/a","children":null}`},
		{"folder-and-file", strings.Replace(fileInfo("a"), `"size":"4"`, `"children":[],"size":"4"`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := fixture(t)
			w.set("/a", test.body)
			if _, err := w.reader("").Inspect(t.Context(), "a"); err == nil {
				t.Fatal("accepted unverifiable file info")
			}
		})
	}
}

func TestAccPullDiscoveryRejectsInvalidBindingsAndEscapingSelectedPaths(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*world) string
	}{
		{"non-remote", func(w *world) string { w.request.Repo.Class = repo.Local; return "" }},
		{"ambiguous-peer", func(w *world) string { w.request.Repo.URL += "?token=private"; return "" }},
		{"invalid-root", func(*world) string { return "../other" }},
		{"revoked", func(w *world) string { w.revoked.Store(true); return "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := fixture(t)
			root := test.edit(w)
			if _, err := peerstorage.New(t.Context(), w.service, w.request, root, remote.DiscoveryScope{}); err == nil || w.count() != 0 {
				t.Fatalf("invalid pull binding reached network: %v requests=%d", err, w.count())
			}
		})
	}
	w := fixture(t)
	r := w.reader("selected")
	for _, path := range []string{"other/file", "selected/../other", "selected/%2e%2e/file"} {
		if _, err := r.Inspect(t.Context(), path); err == nil || w.count() != 0 {
			t.Fatalf("escaping selected path %q reached network: %v requests=%d", path, err, w.count())
		}
	}
	w.set("/selected", `{"repo":"source","path":"/selected","children":[]}`)
	w.set("/selected"+listQuery, `{"ignored":{"bounded":[1,2]},"files":[]}`)
	files, next, err := r.Files(t.Context(), kernel.Page{})
	if err != nil || len(files) != 0 || next != "" {
		t.Fatalf("empty complete list %+v %q %v", files, next, err)
	}
}

func TestAccPullDiscoveryOfflineNoStoreAliasAndRedirectCannotFetch(t *testing.T) {
	for _, name := range []string{"offline", "no-store", "cache-only", "alias"} {
		t.Run(name, func(t *testing.T) {
			w := fixture(t)
			enabled, disabled := true, false
			switch name {
			case "offline":
				w.request.Repo.Offline = &enabled
				if err := w.db.Repos().Update(t.Context(), w.request.Repo); err != nil {
					t.Fatal(err)
				}
			case "no-store":
				w.request.Repo.StoreArtifactsLocally = &disabled
			case "cache-only":
				w.request.CacheOnly = true
			case "alias":
				w.request.Repo.Key += "-cache"
			}
			r, err := peerstorage.New(t.Context(), w.service, w.request, "", remote.DiscoveryScope{})
			if err == nil {
				_, _, err = r.Files(t.Context(), kernel.Page{})
			}
			if err == nil || w.count() != 0 {
				t.Fatalf("forbidden discovery err=%v requests=%d", err, w.count())
			}
		})
	}
	w := fixture(t)
	w.mu.Lock()
	w.documents[""] = response{redirect: "/artifactory/api/storage/other"}
	w.mu.Unlock()
	if _, _, err := w.reader("").Files(t.Context(), kernel.Page{}); err == nil || w.count() != 1 {
		t.Fatalf("redirect crossed metadata boundary: %v requests=%d", err, w.count())
	}
}

func TestAccPullDiscoveryFencesCredentialChangesAndForeignCursors(t *testing.T) {
	w := fixture(t)
	w.set(listQuery, `{"files":[{"uri":"/a","folder":false},{"uri":"/b","folder":false}]}`)
	w.set("/a", fileInfo("a"))
	w.set("/b", fileInfo("b"))
	r := w.reader("")
	_, next, err := r.Files(t.Context(), kernel.Page{Limit: 1})
	if err != nil || next == "" {
		t.Fatal("initial page", err)
	}
	before := w.count()
	for _, after := range []string{"!", base64.RawURLEncoding.EncodeToString([]byte(`{}`)), strings.Repeat("a", 65537)} {
		if _, _, err := r.Files(t.Context(), kernel.Page{After: after}); !errors.Is(err, kernel.ErrInvalidCursor) {
			t.Fatalf("invalid cursor accepted: %v", err)
		}
	}
	w.mu.Lock()
	w.revision = "credential-2"
	w.mu.Unlock()
	if err = r.Revalidate(t.Context()); !errors.Is(err, source.ErrSuperseded) {
		t.Fatal("old authority remained current", err)
	}
	if _, _, err = w.reader("").Files(t.Context(), kernel.Page{After: next}); !errors.Is(err, kernel.ErrInvalidCursor) || w.count() != before {
		t.Fatalf("old credential cursor fetched: %v requests=%d before=%d", err, w.count(), before)
	}
	raw, err := base64.RawURLEncoding.DecodeString(next)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if json.Unmarshal(raw, &document) != nil || strings.Contains(string(raw), "credential") {
		t.Fatalf("cursor disclosed opaque credential revision: %s", raw)
	}
}

func TestAccPullDiscoveryCannotProbeAnAssumedOfflineSource(t *testing.T) {
	w := fixture(t)
	state, err := w.db.RemoteCache().State(t.Context(), w.request.Repo.Key)
	if err != nil {
		t.Fatal(err)
	}
	state.ConnectionFailures = 10
	state.OfflineUntil = time.Now().Add(time.Hour)
	state.SettingsRevision = "credential-1"
	if err = w.db.RemoteCache().PutState(t.Context(), w.request.Repo.Key, state); err != nil {
		t.Fatal(err)
	}
	if _, _, err = w.reader("").Files(t.Context(), kernel.Page{}); err == nil || w.count() != 0 {
		t.Fatalf("discovery probed an assumed-offline source: %v requests=%d", err, w.count())
	}
}

func TestAccPullDiscoveryAbortsPageWhenListedFilesChange(t *testing.T) {
	for _, mode := range []string{"missing", "folder"} {
		t.Run(mode, func(t *testing.T) {
			w := fixture(t)
			w.set(listQuery, `{"files":[{"uri":"/a","folder":false},{"uri":"/b","folder":false}]}`)
			w.set("/a", fileInfo("a"))
			if mode == "folder" {
				w.set("/b", `{"repo":"source","path":"/b","children":[]}`)
			}
			files, next, err := w.reader("").Files(t.Context(), kernel.Page{})
			if err == nil || len(files) != 0 || next != "" {
				t.Fatalf("partially verified page escaped: %+v %q %v", files, next, err)
			}
		})
	}
}

func TestAccPullDiscoveryBlockedAnswerHonorsCurrentAuthority(t *testing.T) {
	for _, name := range []string{"caller", "credentials", "source-url"} {
		t.Run(name, func(t *testing.T) {
			w := fixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			w.mu.Lock()
			w.documents["/a"] = response{body: fileInfo("a"), entered: entered, release: release}
			w.mu.Unlock()
			r := w.reader("")
			answer := make(chan error, 1)
			go func() {
				_, err := r.Inspect(t.Context(), "a")
				answer <- err
			}()
			<-entered
			want := source.ErrSuperseded
			switch name {
			case "caller":
				w.revoked.Store(true)
				want = source.ErrDenied
			case "credentials":
				w.mu.Lock()
				w.revision = "credential-2"
				w.mu.Unlock()
			case "source-url":
				config := w.request.Repo
				config.URL += "-changed"
				if err := w.db.Repos().Update(t.Context(), config); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			if err := <-answer; !errors.Is(err, want) {
				t.Fatalf("stale answer was accepted: %v want %v", err, want)
			}
		})
	}
}

func TestAccPullDiscoveryLayoutRootScanRequiresActualRootRead(t *testing.T) {
	w := fixture(t)
	if _, err := peerstorage.New(t.Context(), w.service, w.request, "", remote.DiscoveryScope{Root: true}); err != nil || w.count() != 0 {
		t.Fatalf("authorized root binding: %v requests=%d", err, w.count())
	}
	if err := w.db.Permissions().Create(t.Context(), accesstest.Permission{Name: "narrow", Resources: map[accessapi.ResourceType]accesstest.Resource{
		accessapi.ResourceArtifact: {
			Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"limited": accessapi.ActionRead}},
			Targets: map[string]accesstest.Target{"remote": {Include: []string{"allowed/**"}}},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	w.request.Subject = accessapi.Subject{Name: "limited"}
	decider, err := accesstest.Over(t, w.db).Decide(t.Context(), w.request.Subject, false)
	if err != nil {
		t.Fatal(err)
	}
	w.request.Access = decider
	if _, err = peerstorage.New(t.Context(), w.service, w.request, "", remote.DiscoveryScope{Root: true}); !errors.Is(err, source.ErrDenied) || w.count() != 0 {
		t.Fatalf("descendant grant authorized root scan: %v requests=%d", err, w.count())
	}
	r, err := peerstorage.New(t.Context(), w.service, w.request, "allowed", remote.DiscoveryScope{Path: "allowed"})
	if err != nil {
		t.Fatal("narrow canonical selection was refused", err)
	}
	if err = r.CheckRead(t.Context(), "allowed/file"); err != nil {
		t.Fatal(err)
	}
	if err = r.CheckRead(t.Context(), "other/file"); !errors.Is(err, source.ErrDenied) {
		t.Fatal("selection escaped canonical scope", err)
	}
}
