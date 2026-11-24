package descriptor

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	accessapi "github.com/weaveplatform/artifactweave/internal/access/api"
	"github.com/weaveplatform/artifactweave/internal/access/api/accessapitest"
	"github.com/weaveplatform/artifactweave/internal/app/adapters/inmem"
	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
)

func adminCtx(t *testing.T) context.Context {
	t.Helper()
	return kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "admin", Admin: true})
}

// webServerDeps binds the web server API as the router does: current
// authority, the configuration guard, base_url and one CDN host.
func webServerDeps(t *testing.T, db *inmem.Store) Deps {
	t.Helper()
	d := depsOf(t, db)
	d.Projects = &accessapitest.Projects{}
	d.Authenticator = &accessapitest.Authenticator{}
	d.Authorizer = &accessapitest.Authorizer{DecideFunc: func(_ context.Context, s accessapi.Subject, _ bool) (accessapi.Decider, error) {
		return &accessapitest.Decider{Admin: s.Name == "admin"}, nil
	}}
	d.Settings = configapi.Settings{BaseURL: "https://install.example", CDNHosts: []string{"cdn.example.com"}}
	return d
}

func nginx() configapi.ReverseProxy {
	rp := configapi.ReverseProxyDefaults()
	rp.WebServerType, rp.PublicAppContext, rp.ArtifactoryAppContext = system.WebServerNginx, "artifactory", "artifactory"
	rp.ServerName, rp.ArtifactoryServerName = "reg.example.com", "localhost"
	return rp
}

func TestUnitParseURLBase(t *testing.T) {
	t.Parallel()
	for doc, deleted := range map[string]bool{
		"urlBase: https://reg.example.com": false,
		"urlBase: '  '":                    true,
		"urlBase: null":                    true,
	} {
		p, err := Parse([]byte(doc), noEnv)
		if err != nil || len(p.Configs) != 1 || p.Configs[0].Delete != deleted || p.Configs[0].Section != SectionURLBase {
			t.Errorf("%s: %+v %v", doc, p, err)
		}
	}
	for _, doc := range []string{"urlBase: 1", "urlBase: [a]", "urlBase: ftp://x", "urlBase: 'https://a b'"} {
		if _, err := Parse([]byte(doc), noEnv); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", doc, err)
		}
	}
	// reverseProxies stays an unapplied section: no YAML page documents it.
	if _, err := Parse([]byte("reverseProxies: {}"), noEnv); !errors.Is(err, ErrInvalid) {
		t.Errorf("reverseProxies: %v", err)
	}
}

func TestUnitWebServerURLBase(t *testing.T) {
	db := inmem.New()
	api := NewAPI(webServerDeps(t, db))
	ws, ctx := api.WebServer, adminCtx(t)
	if got := ws.BaseURL(ctx); got != "https://install.example" {
		t.Fatalf("base_url fallback %q", got)
	}
	stored, err := ws.SetURLBase(ctx, " https://reg.example.com/prefix\n")
	if err != nil || stored != "https://reg.example.com/prefix" {
		t.Fatalf("set: %q %v", stored, err)
	}
	// The local write invalidates the cached value at once.
	if got := ws.BaseURL(ctx); got != stored {
		t.Fatalf("after set: %q", got)
	}
	// A CDN host or its parent is refused and leaves the value as it was.
	for _, bad := range []string{"https://cdn.example.com", "https://example.com", "https://reg.example.com?x"} {
		if _, err := ws.SetURLBase(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", bad, err)
		}
	}
	// The YAML patch takes the same CDN rule, from the merged document.
	plan, err := api.Descriptor.Parse([]byte("urlBase: https://example.com"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Descriptor.Apply(ctx, plan, now, false); !errors.Is(err, ErrInvalid) {
		t.Errorf("CDN parent through PATCH: %v", err)
	}
	if got := ws.BaseURL(ctx); got != stored {
		t.Fatalf("after refusals: %q", got)
	}
	// A PATCH through the descriptor also invalidates the cache.
	plan, err = api.Descriptor.Parse([]byte("urlBase: https://patched.example.org"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := api.Descriptor.Apply(ctx, plan, now, false); err != nil || !slices.Equal(res.Updated, []string{"urlBase"}) {
		t.Fatalf("patch: %+v %v", res, err)
	}
	if got := ws.BaseURL(ctx); got != "https://patched.example.org" {
		t.Fatalf("after patch: %q", got)
	}
	// Blank clears it, back to base_url.
	if stored, err := ws.SetURLBase(ctx, ""); err != nil || stored != "" {
		t.Fatalf("clear: %q %v", stored, err)
	}
	if got := ws.BaseURL(ctx); got != "https://install.example" {
		t.Fatalf("after clear: %q", got)
	}
	// Callers who are not administrators change nothing.
	user := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "user"})
	if _, err := ws.SetURLBase(user, "https://user.example"); !errors.Is(err, kernel.ErrForbidden) {
		t.Errorf("user: %v", err)
	}
	if _, err := ws.SetURLBase(t.Context(), "https://anon.example"); !errors.Is(err, kernel.ErrUnauthenticated) {
		t.Errorf("anonymous: %v", err)
	}
	if _, err := db.Configs().Get(ctx, urlBaseKind, urlBaseKey); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("refused writes stored a urlBase: %v", err)
	}
}

func TestUnitWebServerReverseProxy(t *testing.T) {
	db := inmem.New()
	d := webServerDeps(t, db)
	ws, ctx := NewAPI(d).WebServer, adminCtx(t)
	view, err := ws.ReverseProxy(ctx)
	if err != nil || view.Stored || !view.Admin {
		t.Fatalf("empty: %+v %v", view, err)
	}
	if _, err := ws.NginxSnippet(ctx); !errors.Is(err, ErrInvalid) {
		t.Fatalf("snippet without settings: %v", err)
	}
	bad := nginx()
	bad.HTTPPort = 70000
	if err := ws.SetReverseProxy(ctx, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid port: %v", err)
	}
	if _, err := db.Configs().Get(ctx, configdoc.ConfigReverseProxy, reverseProxyKey); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("refused settings were stored: %v", err)
	}
	rp := nginx()
	rp.UseHTTPS, rp.SSLCertificate, rp.SSLKey, rp.SSLPort = true, "/etc/c.pem", "/etc/k.pem", 8443
	if err := ws.SetReverseProxy(ctx, rp); err != nil {
		t.Fatal(err)
	}
	view, err = ws.ReverseProxy(ctx)
	if err != nil || !view.Stored || !view.Admin || view.Settings != rp {
		t.Fatalf("stored: %+v %v", view, err)
	}
	snippet, err := ws.NginxSnippet(ctx)
	if err != nil || !strings.Contains(snippet, "listen 8443 ssl;") {
		t.Fatalf("snippet: %q %v", snippet, err)
	}
	// A POST replaces the settings whole.
	direct := configapi.ReverseProxyDefaults()
	direct.WebServerType, direct.PublicAppContext = system.WebServerDirect, "artifactory"
	if err := ws.SetReverseProxy(ctx, direct); err != nil {
		t.Fatal(err)
	}
	if view, _ := ws.ReverseProxy(ctx); view.Settings != direct {
		t.Fatalf("replaced: %+v", view.Settings)
	}
	if _, err := ws.NginxSnippet(ctx); !errors.Is(err, ErrInvalid) {
		t.Fatalf("DIRECT snippet: %v", err)
	}

	// A signed-in user reads without administrator authority; a demoted
	// administrator is read as one.
	user := kernel.WithPrincipal(t.Context(), kernel.Principal{Name: "user"})
	if view, err := ws.ReverseProxy(user); err != nil || view.Admin || !view.Stored {
		t.Fatalf("user: %+v %v", view, err)
	}
	if err := ws.SetReverseProxy(user, nginx()); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("user write: %v", err)
	}
	if _, err := ws.NginxSnippet(user); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("user snippet: %v", err)
	}
	demoted := d
	demoted.Authorizer = &accessapitest.Authorizer{DecideFunc: func(context.Context, accessapi.Subject, bool) (accessapi.Decider, error) {
		return &accessapitest.Decider{}, nil
	}}
	dws := NewAPI(demoted).WebServer
	if view, err := dws.ReverseProxy(ctx); err != nil || view.Admin {
		t.Fatalf("demoted read: %+v %v", view, err)
	}
	if err := dws.SetReverseProxy(ctx, nginx()); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("demoted write: %v", err)
	}
	if _, err := dws.NginxSnippet(ctx); !errors.Is(err, kernel.ErrForbidden) {
		t.Fatalf("demoted snippet: %v", err)
	}
	if view, _ := ws.ReverseProxy(ctx); view.Settings != direct {
		t.Fatalf("refused writes changed the settings: %+v", view.Settings)
	}
}

func TestUnitWebServerFailures(t *testing.T) {
	failure := errors.New("web server failure")
	db := inmem.New()
	d := webServerDeps(t, db)
	ctx := adminCtx(t)
	if err := NewAPI(d).WebServer.SetReverseProxy(ctx, nginx()); err != nil {
		t.Fatal(err)
	}

	guarded := d
	guarded.Projects = &accessapitest.Projects{GuardFunc: func(context.Context) error { return failure }}
	ws := NewAPI(guarded).WebServer
	if _, err := ws.ReverseProxy(ctx); !errors.Is(err, failure) {
		t.Errorf("read guard: %v", err)
	}
	if _, err := ws.NginxSnippet(ctx); !errors.Is(err, failure) {
		t.Errorf("snippet guard: %v", err)
	}
	if _, err := ws.SetURLBase(ctx, "https://reg.example.com"); !errors.Is(err, failure) {
		t.Errorf("write guard: %v", err)
	}

	revoked := d
	revoked.Authenticator = &accessapitest.Authenticator{CurrentFunc: func(context.Context, kernel.Principal) (kernel.Principal, error) {
		return kernel.Principal{}, failure
	}}
	if _, err := NewAPI(revoked).WebServer.ReverseProxy(ctx); !errors.Is(err, failure) {
		t.Errorf("current caller: %v", err)
	}

	// An outer rollback undoes the write and the cache is not left pointing
	// at the rolled-back value.
	ws = NewAPI(d).WebServer
	err := db.Tx(ctx, func(ctx context.Context) error {
		if _, err := ws.SetURLBase(ctx, "https://rolled.example"); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := db.Configs().Get(ctx, urlBaseKind, urlBaseKey); !errors.Is(err, kernel.ErrNotFound) {
		t.Errorf("rollback kept urlBase: %v", err)
	}

	// Stored documents that did not come from this package are refused.
	for _, doc := range []configdoc.ConfigDocument{
		{Kind: configdoc.ConfigReverseProxy, Key: reverseProxyKey, Document: []byte(`{"httpPort":"80"}`)},
		{Kind: configdoc.ConfigReverseProxy, Key: reverseProxyKey, Document: []byte(`[`)},
	} {
		if err := db.Configs().Put(ctx, doc); err != nil {
			t.Fatal(err)
		}
		if _, err := ws.ReverseProxy(ctx); err == nil {
			t.Errorf("%s was served", doc.Document)
		}
		if _, err := exportAPIXML(t, d); err == nil {
			t.Errorf("%s was exported", doc.Document)
		}
	}
	for _, body := range []string{`{"urlBase":""}`, `{"urlBase":1}`, `[`} {
		if err := db.Configs().Put(ctx, configdoc.ConfigDocument{Kind: urlBaseKind, Key: urlBaseKey, Document: []byte(body)}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := storedURLBase(ctx, db.Configs()); !errors.Is(err, kernel.ErrStore) && body != `[` {
			t.Errorf("%s: %v", body, err)
		}
	}
}

func exportAPIXML(t *testing.T, d Deps) ([]byte, error) {
	t.Helper()
	return NewAPI(d).Export.XML(adminCtx(t)) //nolint:wrapcheck // test helper
}

func TestUnitWebServerExportOrder(t *testing.T) {
	db := inmem.New()
	d := webServerDeps(t, db)
	ws, ctx := NewAPI(d).WebServer, adminCtx(t)
	rp := nginx()
	rp.DockerReverseProxyMethod = system.DockerRepoPathPrefix
	if err := ws.SetReverseProxy(ctx, rp); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetURLBase(ctx, "https://reg.example.com"); err != nil {
		t.Fatal(err)
	}
	mustApply(t, db, "proxies:\n  corp:\n    host: proxy.example\n    port: 3128\n")
	node, body := exportXML(t, d)
	if got := xmlText(t, node, "urlBase"); got != "https://reg.example.com" {
		t.Errorf("urlBase %q", got)
	}
	for path, want := range map[string]string{
		"reverseProxies/reverseProxy/key":                      "nginx",
		"reverseProxies/reverseProxy/webServerType":            "nginx",
		"reverseProxies/reverseProxy/dockerReverseProxyMethod": "path",
		"reverseProxies/reverseProxy/sslPort":                  "443",
		"reverseProxies/reverseProxy/artifactoryPort":          "8081",
		"reverseProxies/reverseProxy/upStreamName":             "",
	} {
		if got := xmlText(t, node, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	// artifactory.xsd: proxies, reverseProxies, propertySets, urlBase, repoLayouts.
	order := []string{"<proxies>", "<reverseProxies>", "<urlBase>", "<repoLayouts>"}
	last := -1
	for _, tag := range order {
		i := strings.Index(body, tag)
		if i <= last {
			t.Fatalf("%s out of artifactory.xsd order in %s", tag, body)
		}
		last = i
	}
	fields := []string{
		"<key>", "<webServerType>", "<artifactoryAppContext>", "<publicAppContext>", "<serverName>",
		"<serverNameExpression>", "<sslCertificate>", "<sslKey>", "<dockerReverseProxyMethod>", "<useHttps>",
		"<useHttp>", "<sslPort>", "<httpPort>", "<artifactoryServerName>", "<upStreamName>", "<artifactoryPort>",
	}
	proxy := body[strings.Index(body, "<reverseProxy>"):]
	last = -1
	for _, tag := range fields {
		i := strings.Index(proxy, tag)
		if i <= last {
			t.Fatalf("%s out of ReverseProxyType order in %s", tag, proxy)
		}
		last = i
	}
}

// federatedRepoUrlBase (#1930) takes urlBase's rules, round-trips through
// the YAML patch and the XML export, after urlBase as in artifactory.xsd,
// and is the base the federation reads for this instance's own members.
func TestUnitFederatedRepoURLBase(t *testing.T) {
	for doc, deleted := range map[string]bool{
		"federatedRepoUrlBase: https://fed.example.com": false,
		"federatedRepoUrlBase: null":                    true,
	} {
		p, err := Parse([]byte(doc), noEnv)
		if err != nil || len(p.Configs) != 1 || p.Configs[0].Delete != deleted || p.Configs[0].Section != SectionFederatedRepoURLBase {
			t.Errorf("%s: %+v %v", doc, p, err)
		}
	}
	for _, doc := range []string{"federatedRepoUrlBase: 1", "federatedRepoUrlBase: ftp://x"} {
		if _, err := Parse([]byte(doc), noEnv); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "federatedRepoUrlBase") {
			t.Errorf("%s: %v", doc, err)
		}
	}
	db := inmem.New()
	d := webServerDeps(t, db)
	api, ctx := NewAPI(d), adminCtx(t)
	if got, err := configdoc.FederatedBase(ctx, db.Configs(), "https://install.example/"); err != nil || got != "https://install.example" {
		t.Fatalf("fallback %q %v", got, err)
	}
	plan, err := api.Descriptor.Parse([]byte("federatedRepoUrlBase: https://cdn.example.com"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Descriptor.Apply(ctx, plan, now, false); !errors.Is(err, ErrInvalid) {
		t.Errorf("CDN host: %v", err)
	}
	mustApply(t, db, "urlBase: https://reg.example.com\nfederatedRepoUrlBase: https://fed.example.com/\n")
	if got, err := configdoc.FederatedBase(ctx, db.Configs(), "https://install.example"); err != nil || got != "https://fed.example.com" {
		t.Fatalf("federated base %q %v", got, err)
	}
	node, body := exportXML(t, d)
	if got := xmlText(t, node, "federatedRepoUrlBase"); got != "https://fed.example.com/" {
		t.Errorf("federatedRepoUrlBase %q", got)
	}
	if u, f := strings.Index(body, "<urlBase>"), strings.Index(body, "<federatedRepoUrlBase>"); u < 0 || f < u {
		t.Errorf("federatedRepoUrlBase out of artifactory.xsd order in %s", body)
	}
	mustApply(t, db, "federatedRepoUrlBase: null\n")
	if got, err := configdoc.FederatedBase(ctx, db.Configs(), ""); err != nil || got != "https://reg.example.com" {
		t.Fatalf("after clearing, urlBase %q %v", got, err)
	}
	if _, body := exportXML(t, d); strings.Contains(body, "federatedRepoUrlBase") {
		t.Errorf("cleared federatedRepoUrlBase exported: %s", body)
	}
}
