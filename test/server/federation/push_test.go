package federation_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/weaveplatform/artifactweave/internal/access/accesstest"
	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/app"
	"github.com/weaveplatform/artifactweave/internal/app/config"
	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/sql/sqltest"
	"github.com/weaveplatform/artifactweave/test/server/support"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

type pushWorld struct {
	t       *testing.T
	runtime *app.Runtime
	config  config.Config
	source  string
	target  string
	client  *http.Client
	server  *httptest.Server
	bridge  *pushBridge
}

func pushConfig(t *testing.T, driver, dsn string) config.Config {
	t.Helper()
	cfg := harness.AppConfig(t)
	cfg.Bootstrap.AdminPassword = "pw"
	cfg.MasterKey = strings.Repeat("test-push-private-key-", 3)
	if driver != "" {
		cfg.Database.Driver, cfg.Database.DSN = driver, dsn
	}
	return cfg
}

func newPushWorld(t *testing.T, driver, sourceDSN, targetDSN, packageType string) *pushWorld {
	t.Helper()
	target := startPushTarget(t, pushConfig(t, driver, targetDSN))
	bridge := newPushBridge(t, target)
	cfg := pushConfig(t, driver, sourceDSN)
	cfg.Remote.AllowedPrivateOrigins = []string{bridge.URL}
	runtime, err := app.OpenRuntime(t.Context(), cfg, slog.New(slog.DiscardHandler), harness.AppOptions())
	if err != nil {
		t.Fatal("open source runtime", err)
	}
	server := httptest.NewServer(pushRuntimeRouter(runtime))
	w := &pushWorld{t: t, runtime: runtime, config: cfg, source: server.URL, target: target, client: &http.Client{Timeout: 10 * time.Second}, server: server, bridge: bridge}
	t.Cleanup(func() {
		w.client.CloseIdleConnections()
		w.server.Close()
		if err := w.runtime.Close(context.Background()); err != nil {
			t.Error("close source runtime", err)
		}
	})
	repository, err := json.Marshal(map[string]string{"rclass": "local", "packageType": packageType})
	if err != nil {
		t.Fatal(err)
	}
	w.request(w.source, http.MethodPut, "/artifactory/api/repositories/source", string(repository), http.StatusOK)
	w.request(w.target, http.MethodPut, "/artifactory/api/repositories/target", string(repository), http.StatusOK)
	if err := runtime.Store.Users().Create(t.Context(), accesstest.User{Name: "replica-reader", PasswordHash: support.CheapHash}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Store.Permissions().Create(t.Context(), accesstest.Permission{Name: "replica-selected", Resources: map[accessapi.ResourceType]accesstest.Resource{
		accessapi.ResourceArtifact: {
			Targets: map[string]accesstest.Target{"source": {Include: []string{"selected/**", "com/acme/**"}}},
			Actions: accesstest.Grants{Users: map[string]accessapi.Actions{"replica-reader": accessapi.ActionRead}},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *pushWorld) request(origin, method, path, body string, want int) ([]byte, http.Header) {
	w.t.Helper()
	req, err := http.NewRequestWithContext(w.t.Context(), method, origin+path, strings.NewReader(body))
	if err != nil {
		w.t.Fatal(err)
	}
	req.SetBasicAuth("admin", "pw")
	req.Header.Set("Content-Type", "application/json")
	response, err := w.client.Do(req)
	if err != nil {
		w.t.Fatal("owned application request failed", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		w.t.Fatal(err)
	}
	if response.StatusCode != want {
		w.t.Fatalf("%s %s: got%d want%d: %s", method, path, response.StatusCode, want, data)
	}
	return data, response.Header.Clone()
}

func itemRoute(repository, path string) string {
	parts := strings.Split(path, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return "/artifactory/" + repository + "/" + strings.Join(parts, "/")
}

func (w *pushWorld) configure(include, exclude string, deletes, properties bool) model.Replication {
	w.t.Helper()
	body, err := json.Marshal(map[string]any{
		"url": w.bridge.URL + "/artifactory/target", "username": "receiver", "password": "pw", "enabled": true,
		"cronExp": "0 0 2 * * ?", "syncDeletes": deletes, "syncProperties": properties, "includePathPrefixPattern": include, "excludePathPrefixPattern": exclude,
	})
	if err != nil {
		w.t.Fatal(err)
	}
	w.request(w.source, http.MethodPut, replicationPath+"source", string(body), http.StatusCreated)
	rows, err := w.runtime.Store.Replications().ForRepo(w.t.Context(), "source")
	if err != nil || len(rows) != 1 {
		w.t.Fatal("current worker target unavailable", err)
	}
	if rows[0].IncludePathPrefix != include || rows[0].ExcludePathPrefix != exclude || rows[0].SyncDeletes != deletes || rows[0].SyncProperties != properties {
		w.t.Fatal("public fixture configuration did not retain intended selection/options")
	}
	return rows[0]
}

func (w *pushWorld) start(id, root string) (replication.Push, model.PushRun) {
	w.t.Helper()
	rows, err := w.runtime.Store.Replications().ForRepo(w.t.Context(), "source")
	if err != nil || len(rows) != 1 {
		w.t.Fatal("current worker target unavailable", err)
	}
	principal, err := w.runtime.Auth.Basic(w.t.Context(), "replica-reader", "pw")
	if err != nil {
		w.t.Fatal(err)
	}
	push, err := w.runtime.Federation.Pusher()
	if err != nil {
		w.t.Fatal(err)
	}
	ctx := accessapi.WithCaller(w.t.Context(), principal, nil)
	run, err := push.Start(ctx, replication.PushRequest{ID: id, TargetKey: rows[0].Key, TargetRevision: rows[0].Revision, Root: root}, nil)
	if err != nil {
		w.t.Fatal("admit actual single push", err)
	}
	return push, run
}

func (w *pushWorld) fence(ctx context.Context) error {
	// This fixture is the sole dispatch owner. REP-04 supplies production
	// queue-generation ownership; every local driver checkpoint must join Tx.
	if !w.runtime.Store.InTx(ctx) {
		return kernel.ErrStore
	}
	return nil
}

func (w *pushWorld) finish(push replication.Push, run model.PushRun) []model.PushItem {
	w.t.Helper()
	for attempt := 0; !run.Phase.Terminal() && attempt < 400; attempt++ {
		next, err := push.Step(w.t.Context(), run.Reference, w.fence)
		if err != nil {
			w.t.Fatal("step actual single push", err)
		}
		run = next
	}
	if run.Phase != model.PushComplete {
		w.t.Fatal("bounded push did not finish traversal", run.Phase)
	}
	items, next, err := w.runtime.Store.PushProgress().Items(w.t.Context(), run.Reference, false, kernel.Page{Limit: 1000})
	if err != nil || next != "" {
		w.t.Fatal("read complete item outcomes", err)
	}
	return items
}

func TestAccSinglePushProcessesSQLite(t *testing.T) {
	qualifySinglePushProcesses(t, newPushWorld(t, "sqlite", "", "", "generic"))
}

func TestAccSinglePushProcessesPostgres(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	source := sqltest.ScratchDSN(t, dsn)
	target := sqltest.ScratchDSN(t, dsn)
	qualifySinglePushProcesses(t, newPushWorld(t, "postgres", source, target, "generic"))
}

func TestAccSinglePushNativeTargetHooksSQLite(t *testing.T) {
	qualifyPushNativeTarget(t, newPushWorld(t, "sqlite", "", "", "maven"))
}

func TestAccSinglePushNativeTargetHooksPostgres(t *testing.T) {
	dsn := sqltest.PostgresDSN(t)
	source, target := sqltest.ScratchDSN(t, dsn), sqltest.ScratchDSN(t, dsn)
	qualifyPushNativeTarget(t, newPushWorld(t, "postgres", source, target, "maven"))
}

func qualifyPushNativeTarget(t *testing.T, w *pushWorld) {
	t.Helper()
	path := "com/acme/app/1.0/app-1.0.pom"
	pom := `<project><modelVersion>4.0.0</modelVersion><groupId>com.acme</groupId><artifactId>app</artifactId><version>1.0</version></project>`
	w.request(w.source, http.MethodPut, itemRoute("source", path), pom, http.StatusCreated)
	metadataPath := itemRoute("target", "com/acme/app/maven-metadata.xml")
	w.request(w.target, http.MethodGet, metadataPath, "", http.StatusNotFound)
	w.configure("**/*.pom", "", false, false)
	push, run := w.start("native-maven-hooks", "com/acme")
	items := w.finish(push, run)
	if len(items) != 1 || items[0].Outcome != model.PushSucceeded {
		t.Fatal("Maven package content did not verify", len(items))
	}
	data, _ := w.request(w.target, http.MethodGet, itemRoute("target", path), "", http.StatusOK)
	if string(data) != pom {
		t.Fatal("normal target admission changed POM content")
	}
	data, headers := w.request(w.target, http.MethodGet, metadataPath, "", http.StatusOK)
	if !strings.Contains(string(data), "<version>1.0</version>") {
		t.Fatal("normal target Maven metadata did not index transferred version")
	}
	sidecar, _ := w.request(w.target, http.MethodGet, metadataPath+".sha1", "", http.StatusOK)
	if headers.Get("X-Checksum-Sha1") == "" || headers.Get("X-Checksum-Sha1") != string(sidecar) {
		t.Fatal("normal target metadata/checksum companion disagreed")
	}
	t.Log("separate target process runs native Maven admission, derived metadata and checksum companions; native-client consumption remains REP-05")
}

func qualifySinglePushProcesses(t *testing.T, w *pushWorld) {
	t.Helper()
	path := "selected/a;b?c#d% space+é.bin"
	content := "source bytes through native admission"
	for name, body := range map[string]string{path: content, "selected/skip.bin": "excluded source", "selected/note.txt": "unselected source", "private/secret.bin": "private source"} {
		w.request(w.source, http.MethodPut, itemRoute("source", name), body, http.StatusCreated)
	}
	w.request(
		w.source,
		http.MethodPatch,
		"/artifactory/api/metadata/source/"+strings.TrimPrefix(itemRoute("source", path), "/artifactory/source/")+"?recursiveProperties=0",
		`{"props":{"quality":["verified"]}}`,
		http.StatusNoContent,
	)
	for name, body := range map[string]string{path: "old target", "selected/stale.bin": "delete me", "selected/skip.bin": "excluded target", "selected/note.txt": "unselected target", "private/unrelated.bin": "outside scope", "keep.bin": "outside root"} {
		w.request(w.target, http.MethodPut, itemRoute("target", name), body, http.StatusCreated)
	}
	w.request(
		w.target,
		http.MethodPatch,
		"/artifactory/api/metadata/target/"+strings.TrimPrefix(itemRoute("target", path), "/artifactory/target/")+"?recursiveProperties=0",
		`{"props":{"destinationOnly":["remove"]}}`,
		http.StatusNoContent,
	)
	w.configure("**/*.bin", "**/skip.bin", true, true)
	push, run := w.start("scoped-content-and-delete", "selected")
	items := w.finish(push, run)
	if len(items) != 2 {
		t.Fatal("expected one verified upload and one scoped deletion", len(items))
	}
	for _, item := range items {
		if item.Outcome != model.PushSucceeded || item.Phase != model.PushItemComplete {
			t.Fatal("unverified item reported as complete", item.Path, item.Outcome, item.Reason)
		}
	}
	data, headers := w.request(w.target, http.MethodGet, itemRoute("target", path), "", http.StatusOK)
	digest := sha256.Sum256([]byte(content))
	if !bytes.Equal(data, []byte(content)) || headers.Get("X-Checksum-Sha256") != hex.EncodeToString(digest[:]) {
		t.Fatal("native target did not serve source bytes/checksum")
	}
	propertyPath := "/artifactory/api/storage/target/" + strings.TrimPrefix(itemRoute("target", path), "/artifactory/target/") + "?properties"
	data, _ = w.request(w.target, http.MethodGet, propertyPath, "", http.StatusOK)
	var properties struct {
		Properties map[string][]string `json:"properties"`
	}
	if json.Unmarshal(data, &properties) != nil || len(properties.Properties) != 1 || !slices.Equal(properties.Properties["quality"], []string{"verified"}) {
		t.Fatal("native property replacement failed")
	}
	w.request(w.target, http.MethodGet, itemRoute("target", "selected/stale.bin"), "", http.StatusNotFound)
	for name, want := range map[string]string{"selected/skip.bin": "excluded target", "selected/note.txt": "unselected target", "private/unrelated.bin": "outside scope", "keep.bin": "outside root"} {
		data, _ := w.request(w.target, http.MethodGet, itemRoute("target", name), "", http.StatusOK)
		if string(data) != want {
			t.Fatal("out-of-scope destination changed", name)
		}
	}
	// The final source file disappearing must not turn empty enumeration
	// into a no-op: reconciliation still removes its selected replica.
	w.request(w.source, http.MethodDelete, itemRoute("source", path), "", http.StatusNoContent)
	push, empty := w.start("empty-selected-source", "selected")
	for _, item := range w.finish(push, empty) {
		if item.Outcome != model.PushSucceeded {
			t.Fatal("empty-source reconciliation failed", item.Path, item.Reason)
		}
	}
	w.request(w.target, http.MethodGet, itemRoute("target", path), "", http.StatusNotFound)
	w.request(w.target, http.MethodGet, itemRoute("target", "private/unrelated.bin"), "", http.StatusOK)
	t.Log("actual source and separate target process preserve scoped bytes, properties, digests, native deletion and empty-source reconciliation")
}
