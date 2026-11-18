package npm_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/weaveplatform/artifactweave/internal/artifacts/adapters/upstream/fakeupstream"
	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/kernel/wire"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/test/server/support/harness"
)

func TestAccNPMVirtualSearchRuntimeSourceRevision(t *testing.T) {
	w := virtualNPMWorld(t, npmfmt.Settings{})
	virtualNPMLocal(t, w, "first", "widget-local", "1.0.0", "local widget")
	target, err := url.Parse(w.Upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyRouter := chi.NewRouter()
	proxyRouter.Handle("/*", httputil.NewSingleHostReverseProxy(target))
	proxy := httptest.NewServer(proxyRouter)
	defer proxy.Close()
	address, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(address.Port())
	if err != nil {
		t.Fatal(err)
	}
	proxyDoc, _ := json.Marshal(wire.ProxyConfiguration{Host: address.Hostname(), Port: port, PlatformDefault: true})
	putProxy := func(password string) {
		t.Helper()
		if err := w.Runtime.Store.Configs().
			Put(t.Context(), configdoc.ConfigDocument{Kind: configdoc.ConfigProxy, Key: "global", Enabled: true, Document: proxyDoc, Secrets: map[string]string{"password": password}}); err != nil {
			t.Fatal(err)
		}
	}
	putProxy("first-secret")
	set := func(name string) {
		t.Helper()
		raw, _ := json.Marshal(
			map[string]any{
				"objects": []any{map[string]any{"package": map[string]string{"name": name, "version": "Unity-1.0.0.0"}, "vendor": "retain"}},
				"time":    "upstream",
				"total":   999,
			},
		)
		w.Upstream.Set("/-/v1/search", fakeupstream.Resource{Body: raw, Header: http.Header{"Content-Type": {"application/json"}}})
	}
	set("widget-remote")
	// This world explicitly selects ScratchDSN on the PostgreSQL lane. Prove
	// the same-kind direct admission fix through the actual runtime as well.
	direct := w.Request("GET", npmBase+"remote/-/v1/search?text=widget&from=0&size=20", "admin", "")
	harness.WantStatus(t, direct, 200)
	if !strings.Contains(direct.Body.String(), "Unity-1.0.0.0") || !strings.Contains(direct.Body.String(), `"time":"upstream"`) ||
		strings.Contains(direct.Body.String(), `"total"`) {
		t.Fatal("direct non-SemVer control", direct.Body.String())
	}
	path := npmBase + "virtual/-/v1/search?text=widget"
	first := w.Request("GET", path, "admin", "")
	harness.WantStatus(t, first, 200)
	if !strings.Contains(first.Body.String(), "widget-local") || !strings.Contains(first.Body.String(), "widget-remote") || !strings.Contains(first.Body.String(), "retain") ||
		strings.Contains(first.Body.String(), `"time"`) ||
		strings.Contains(first.Body.String(), `"total"`) {
		t.Fatal(first.Body.String())
	}
	before := w.Upstream.Count("GET", "/-/v1/search")
	set("widget-rotated")
	warm := w.Request("GET", path, "admin", "")
	harness.WantStatus(t, warm, 200)
	if !strings.Contains(warm.Body.String(), "widget-remote") || w.Upstream.Count("GET", "/-/v1/search") != before {
		t.Fatal("warm page missed", warm.Body.String())
	}
	identity, err := w.Runtime.Store.RemoteCache().Source(t.Context(), "remote")
	if err != nil {
		t.Fatal(err)
	}
	putProxy("second-secret")
	current, err := w.Runtime.Store.RemoteCache().Source(t.Context(), "remote")
	if err != nil || identity != current {
		t.Fatal("global options must not require a source incarnation change", err)
	}
	rotated := w.Request("GET", path, "admin", "")
	harness.WantStatus(t, rotated, 200)
	if !strings.Contains(rotated.Body.String(), "widget-rotated") || strings.Contains(rotated.Body.String(), "widget-remote") ||
		w.Upstream.Count("GET", "/-/v1/search") != before+1 {
		t.Fatal("production wrapper reused old credential answer", rotated.Body.String(), w.Upstream.Requests())
	}
	virtualNPMConfig(t, w, "virtual", func(c *repo.Config) { c.ExcludesPattern = new(".npm/widget-rotated/**") })
	hidden := w.Request("GET", path, "admin", "")
	harness.WantStatus(t, hidden, 200)
	if strings.Contains(hidden.Body.String(), "widget-rotated") || !strings.Contains(hidden.Body.String(), "widget-local") {
		t.Fatal(hidden.Body.String())
	}
	virtualNPMLocal(t, w, "second", "widget-federated", "1.0.0", "live widget")
	live := w.Request("GET", path, "admin", "")
	harness.WantStatus(t, live, 200)
	if !strings.Contains(live.Body.String(), "widget-federated") {
		t.Fatal("local search was cached", live.Body.String())
	}
	harness.WantStatus(t, w.Request("GET", npmBase+"virtual/-/v1/search?size=not-a-number", "reader", ""), 200)
}
