package system

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
)

// nginxProxy is a complete NGINX reverse proxy as the platform_reverse_proxy
// resource sends one, after the defaults.
func nginxProxy() configapi.ReverseProxy {
	rp := configapi.ReverseProxyDefaults()
	rp.WebServerType = WebServerNginx
	rp.ArtifactoryAppContext, rp.PublicAppContext = "artifactory", "artifactory"
	rp.ServerName, rp.ArtifactoryServerName = "reg.example.com", "localhost"
	return rp
}

func TestUnitCheckReverseProxy(t *testing.T) {
	t.Parallel()
	direct := configapi.ReverseProxyDefaults()
	direct.WebServerType, direct.PublicAppContext = WebServerDirect, "artifactory"
	if err := CheckReverseProxy(direct); err != nil {
		t.Fatalf("DIRECT without host names: %v", err)
	}
	if err := CheckReverseProxy(nginxProxy()); err != nil {
		t.Fatalf("NGINX: %v", err)
	}
	for _, c := range []struct {
		name string
		edit func(*configapi.ReverseProxy)
		want string
	}{
		{"blank key", func(r *configapi.ReverseProxy) { r.Key = "" }, "key should not be blank"},
		{"bad key", func(r *configapi.ReverseProxy) { r.Key = "a b" }, "key must be a valid XML name"},
		{"no type", func(r *configapi.ReverseProxy) { r.WebServerType = "" }, "webServerType should not be null"},
		{"haproxy", func(r *configapi.ReverseProxy) { r.WebServerType = "HAPROXY" }, "webServerType must be one of"},
		{"port per repo", func(r *configapi.ReverseProxy) { r.DockerReverseProxyMethod = DockerPortPerRepo }, "dockerReverseProxyMethod PORTPERREPO is not offered"},
		{"DIRECT method", func(r *configapi.ReverseProxy) { r.DockerReverseProxyMethod = "DIRECT" }, "dockerReverseProxyMethod must be one of"},
		{"no public context", func(r *configapi.ReverseProxy) { r.PublicAppContext = "" }, "publicAppContext should not be blank"},
		{"public context", func(r *configapi.ReverseProxy) { r.PublicAppContext = "a;b" }, "publicAppContext must be"},
		{"app context", func(r *configapi.ReverseProxy) { r.ArtifactoryAppContext = "/x" }, "artifactoryAppContext must be"},
		{"no server name", func(r *configapi.ReverseProxy) { r.ServerName = "" }, "serverName should not be blank"},
		{"server name", func(r *configapi.ReverseProxy) { r.ServerName = "reg.example.com; return 200" }, "serverName must be a host name"},
		{"long server name", func(r *configapi.ReverseProxy) { r.ServerName = strings.Repeat("a.", 127) + "aa" }, "serverName must be a host name"},
		{"expression", func(r *configapi.ReverseProxy) { r.ServerNameExpression = "*.*.example.com" }, "serverNameExpression must be"},
		{"no upstream", func(r *configapi.ReverseProxy) { r.ArtifactoryServerName = "" }, "artifactoryServerName should not be blank"},
		{"upstream", func(r *configapi.ReverseProxy) { r.ArtifactoryServerName = "[10.0.0.1]" }, "artifactoryServerName must be"},
		{"upstream bracket", func(r *configapi.ReverseProxy) { r.ArtifactoryServerName = "[::1" }, "artifactoryServerName must be"},
		{"artifactory port", func(r *configapi.ReverseProxy) { r.ArtifactoryPort = 0 }, "artifactoryPort should be > 0"},
		{"ssl port", func(r *configapi.ReverseProxy) { r.SSLPort = 65536 }, "sslPort should be > 0"},
		{"http port", func(r *configapi.ReverseProxy) { r.HTTPPort = -1 }, "httpPort should be > 0"},
		{"no listener", func(r *configapi.ReverseProxy) { r.UseHTTP = false }, "useHttp or useHttps must be set"},
		{"shared port", func(r *configapi.ReverseProxy) {
			r.UseHTTPS, r.SSLCertificate, r.SSLKey, r.SSLPort = true, "/c.pem", "/k.pem", 80
		}, "sslPort and httpPort must differ"},
		{"https without files", func(r *configapi.ReverseProxy) { r.UseHTTPS = true }, "useHttps needs sslCertificate and sslKey"},
		{"certificate", func(r *configapi.ReverseProxy) { r.SSLCertificate = "/c.pem;" }, "sslCertificate must be"},
		{"key", func(r *configapi.ReverseProxy) { r.SSLKey = "k.pem" }, "sslKey must be"},
		{"key variable", func(r *configapi.ReverseProxy) { r.SSLKey = "/$k" }, "sslKey must be"},
	} {
		rp := nginxProxy()
		c.edit(&rp)
		err := CheckReverseProxy(rp)
		if !errors.Is(err, configapi.ErrInvalid) || !strings.Contains(err.Error(), "webServer: "+c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
	ipv6 := nginxProxy()
	ipv6.ArtifactoryServerName = "[::1]"
	if err := CheckReverseProxy(ipv6); err != nil {
		t.Errorf("bracketed IPv6 upstream: %v", err)
	}
}

func TestUnitReverseProxyDocumentRoundTrip(t *testing.T) {
	t.Parallel()
	rp := nginxProxy()
	rp.UseHTTPS, rp.SSLCertificate, rp.SSLKey, rp.SSLPort = true, "/etc/c.pem", "/etc/k.pem", 8443
	// The document is stored as JSON, so numbers come back as float64.
	raw, err := json.Marshal(ReverseProxyDocument(rp))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc) != 15 || doc["sslPort"] != float64(8443) {
		t.Fatalf("document %v", doc)
	}
	got, err := ReverseProxyFromDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(rp, got); diff != "" {
		t.Errorf("round trip (-want +got):\n%s", diff)
	}
	// YAML ints and json.Number read as whole numbers too; omitted fields
	// take the defaults.
	got, err = ReverseProxyFromDocument(map[string]any{"httpPort": 8080, "artifactoryPort": json.Number("9000")})
	if err != nil || got.HTTPPort != 8080 || got.ArtifactoryPort != 9000 || got.SSLPort != 443 || got.Key != "nginx" {
		t.Errorf("partial document: %+v, %v", got, err)
	}
	for name, doc := range map[string]map[string]any{
		"unknown":  {"upStreamName": "x"},
		"string":   {"serverName": 1},
		"bool":     {"useHttp": "true"},
		"fraction": {"httpPort": 80.5},
		"large":    {"httpPort": float64(1 << 40)},
		"number":   {"httpPort": json.Number("x")},
		"text":     {"httpPort": "80"},
	} {
		if _, err := ReverseProxyFromDocument(doc); !errors.Is(err, configapi.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestUnitCheckURLBase(t *testing.T) {
	t.Parallel()
	cdn := []string{"cdn.example.com"}
	for _, ok := range []string{"https://reg.example.com", "http://10.0.0.1:8082/prefix", "https://other.example.org"} {
		if err := CheckURLBase(ok, cdn); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for raw, want := range map[string]string{
		"https://reg example.com":     "spaces or control characters",
		"https://reg.example.com\n":   "spaces or control characters",
		"ftp://reg.example.com":       "absolute http or https URL",
		"reg.example.com":             "absolute http or https URL",
		"https://":                    "absolute http or https URL",
		"https:reg":                   "absolute http or https URL",
		"https://u:p@reg.example.com": "user information, a query or a fragment",
		"https://reg.example.com/?a":  "user information, a query or a fragment",
		"https://reg.example.com/?":   "user information, a query or a fragment",
		"https://reg.example.com/#":   "user information, a query or a fragment",
		"https://CDN.example.com":     "CDN domain",
		"https://example.com":         "CDN domain",
		"https://%zz":                 "absolute http or https URL",
	} {
		err := CheckURLBase(raw, cdn)
		if !errors.Is(err, configapi.ErrInvalid) || !strings.Contains(err.Error(), "urlBase ") ||
			!strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", raw, err, want)
		}
	}
}

func TestUnitNginxSnippet(t *testing.T) {
	t.Parallel()
	rp := nginxProxy()
	rp.UseHTTPS, rp.SSLCertificate, rp.SSLKey = true, "/etc/nginx/c.pem", "/etc/nginx/k.pem"
	rp.ServerNameExpression = "*.docker.example.com"
	got, err := NginxSnippet(rp)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ssl_certificate      /etc/nginx/c.pem;\n",
		"ssl_certificate_key  /etc/nginx/k.pem;\n",
		"    listen 443 ssl;\n    listen 80;\n",
		`    server_name ~^(?<repo>[^.]+)\.docker\.example\.com$ reg.example.com;` + "\n",
		"    rewrite ^/(v1|v2)/(.*) /artifactory/api/docker/$repo/$1/$2;\n",
		"        proxy_pass          http://localhost:8081;\n",
		"X-JFrog-Override-Base-Url $http_x_forwarded_proto://$host:$server_port;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("snippet lacks %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "{") != strings.Count(got, "}") {
		t.Errorf("unbalanced braces:\n%s", got)
	}

	// The repository path method has no rewrite and plain server names; the
	// subdomain parent falls back to serverName.
	path := nginxProxy()
	path.DockerReverseProxyMethod, path.ArtifactoryServerName = DockerRepoPathPrefix, "[::1]"
	got, err = NginxSnippet(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "rewrite") || strings.Contains(got, "ssl_") ||
		!strings.Contains(got, "    server_name reg.example.com;\n") ||
		!strings.Contains(got, "proxy_pass          http://[::1]:8081;") {
		t.Errorf("path method:\n%s", got)
	}
	sub := nginxProxy()
	got, _ = NginxSnippet(sub)
	if !strings.Contains(got, `~^(?<repo>[^.]+)\.reg\.example\.com$ reg.example.com;`) {
		t.Errorf("subdomain under serverName:\n%s", got)
	}

	for name, rp := range map[string]configapi.ReverseProxy{
		"apache":  func() configapi.ReverseProxy { r := nginxProxy(); r.WebServerType = WebServerApache; return r }(),
		"invalid": func() configapi.ReverseProxy { r := nginxProxy(); r.ServerName = "a b"; return r }(),
	} {
		if _, err := NginxSnippet(rp); !errors.Is(err, configapi.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestUnitPublicURL(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	var stored string
	var ok bool
	var failure error
	reads := 0
	p := &PublicURL{
		Fallback: "https://base.example",
		Now:      func() time.Time { return now },
		Read: func(context.Context) (string, bool, error) {
			reads++
			return stored, ok, failure
		},
	}
	ctx := context.Background()
	// Invalidate before the first read changes nothing.
	p.Invalidate()
	if got := p.Get(ctx); got != "https://base.example" || reads != 1 {
		t.Fatalf("unset: %q after %d reads", got, reads)
	}
	stored, ok = "https://stored.example", true
	if got := p.Get(ctx); got != "https://base.example" || reads != 1 {
		t.Fatalf("cached: %q after %d reads", got, reads)
	}
	p.Invalidate()
	if got := p.Get(ctx); got != "https://stored.example" || reads != 2 {
		t.Fatalf("invalidated: %q after %d reads", got, reads)
	}
	// A failed read keeps the last value until the next refresh.
	failure = errors.New("store down")
	now = now.Add(PublicURLRefresh)
	if got := p.Get(ctx); got != "https://stored.example" || reads != 3 {
		t.Fatalf("failed read: %q after %d reads", got, reads)
	}
	failure, ok = nil, false
	now = now.Add(PublicURLRefresh)
	if got := p.Get(ctx); got != "https://base.example" || reads != 4 {
		t.Fatalf("cleared: %q after %d reads", got, reads)
	}

	// Before any success a failed read answers the fallback; the real clock
	// is used without Now.
	q := &PublicURL{Fallback: "https://f.example", Read: func(context.Context) (string, bool, error) {
		return "", false, errors.New("down")
	}}
	if got := q.Get(ctx); got != "https://f.example" {
		t.Errorf("first read failed: %q", got)
	}
}
