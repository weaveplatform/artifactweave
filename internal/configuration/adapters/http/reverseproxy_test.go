package configurationhttp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	artgen "github.com/weaveplatform/artifactweave/internal/platform/gen/artifactory"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

// TestAccMethodAndPorts: a user who is no administrator keeps the Docker
// method and every port field, and loses the host names, contexts and
// certificate paths.
func TestAccMethodAndPorts(t *testing.T) {
	method := artgen.SUBDOMAIN
	full := artgen.GetReverseProxyConfiguration200JSONResponse{
		Key:                      httpkit.PtrTo("nginx"),
		WebServerType:            httpkit.PtrTo(artgen.NGINX),
		ArtifactoryAppContext:    httpkit.PtrTo("artifactory"),
		PublicAppContext:         httpkit.PtrTo("artifactory"),
		ServerName:               httpkit.PtrTo("reg.example.com"),
		ServerNameExpression:     httpkit.PtrTo("*.reg.example.com"),
		ArtifactoryServerName:    httpkit.PtrTo("localhost"),
		SslCertificate:           httpkit.PtrTo("/etc/ssl/reg.crt"),
		SslKey:                   httpkit.PtrTo("/etc/ssl/reg.key"),
		DockerReverseProxyMethod: &method,
		ArtifactoryPort:          httpkit.PtrTo(8081),
		HttpPort:                 httpkit.PtrTo(80),
		HttpsPort:                httpkit.PtrTo(443),
		SslPort:                  httpkit.PtrTo(443),
		UseHttp:                  httpkit.PtrTo(true),
		UseHttps:                 httpkit.PtrTo(true),
	}
	want := artgen.GetReverseProxyConfiguration200JSONResponse{
		DockerReverseProxyMethod: &method,
		ArtifactoryPort:          httpkit.PtrTo(8081),
		HttpPort:                 httpkit.PtrTo(80),
		HttpsPort:                httpkit.PtrTo(443),
		SslPort:                  httpkit.PtrTo(443),
		UseHttp:                  httpkit.PtrTo(true),
		UseHttps:                 httpkit.PtrTo(true),
	}
	if diff := cmp.Diff(want, methodAndPorts(full)); diff != "" {
		t.Fatalf("(-want +got):\n%s", diff)
	}
}

// fakeWebServer is configapi.WebServer with fixed answers.
type fakeWebServer struct {
	base    string
	view    configapi.ReverseProxyView
	err     error
	set     *configapi.ReverseProxy
	snippet string
}

func (f *fakeWebServer) BaseURL(context.Context) string { return f.base }

func (f *fakeWebServer) SetURLBase(_ context.Context, u string) (string, error) {
	return strings.TrimSpace(u), f.err
}

func (f *fakeWebServer) ReverseProxy(context.Context) (configapi.ReverseProxyView, error) {
	return f.view, f.err
}

func (f *fakeWebServer) SetReverseProxy(_ context.Context, rp configapi.ReverseProxy) error {
	f.set = &rp
	return f.err
}

func (f *fakeWebServer) NginxSnippet(context.Context) (string, error) { return f.snippet, f.err }

func webServerAPI(ws configapi.WebServer) SystemAPI {
	return SystemAPI{deps: Deps{
		BaseURL:       "https://install.example",
		Configuration: configapi.API{WebServer: ws},
		Logger:        slog.New(slog.DiscardHandler),
	}}
}

var (
	adminCaller = kernel.WithPrincipal(context.Background(), kernel.Principal{Name: "admin", Admin: true})
	userCaller  = kernel.WithPrincipal(context.Background(), kernel.Principal{Name: "user"})
)

func statusOf(t *testing.T, err error) int {
	t.Helper()
	var typed *apierror.Error
	if !errors.As(err, &typed) {
		t.Fatalf("untyped error %v", err)
	}
	return typed.Status
}

// TestAccGetReverseProxyConfigurationStored: stored settings are returned
// whole to an administrator, with both port spellings, and reduced to the
// method and ports for anyone the read finds not to be one.
func TestAccGetReverseProxyConfigurationStored(t *testing.T) {
	rp := configapi.ReverseProxy{
		Key: "nginx", WebServerType: "NGINX", PublicAppContext: "artifactory", ServerName: "reg.example.com",
		ArtifactoryServerName: "localhost", ArtifactoryPort: 8081, SSLCertificate: "/c.pem", SSLKey: "/k.pem",
		DockerReverseProxyMethod: "REPOPATHPREFIX", UseHTTPS: true, UseHTTP: true, SSLPort: 8443, HTTPPort: 80,
	}
	ws := &fakeWebServer{view: configapi.ReverseProxyView{Settings: rp, Stored: true, Admin: true}}
	api := webServerAPI(ws)
	got, err := api.GetReverseProxyConfiguration(adminCaller, artgen.GetReverseProxyConfigurationRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	full := got.(artgen.GetReverseProxyConfiguration200JSONResponse)
	if *full.SslPort != 8443 || *full.HttpsPort != 8443 || *full.SslKey != "/k.pem" || *full.ServerNameExpression != "" {
		t.Errorf("admin view %+v", full)
	}
	// The token says admin, the current read does not: reduced.
	ws.view.Admin = false
	got, err = api.GetReverseProxyConfiguration(adminCaller, artgen.GetReverseProxyConfigurationRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	reduced := got.(artgen.GetReverseProxyConfiguration200JSONResponse)
	if reduced.SslKey != nil || reduced.SslCertificate != nil || reduced.ServerName != nil || *reduced.HttpsPort != 8443 {
		t.Errorf("reduced view %+v", reduced)
	}
	// Without stored settings the server describes itself from the
	// configured base URL.
	ws.view, ws.base = configapi.ReverseProxyView{Admin: true}, "https://Stored.Example:8443/x"
	got, _ = api.GetReverseProxyConfiguration(adminCaller, artgen.GetReverseProxyConfigurationRequestObject{})
	if body := got.(artgen.GetReverseProxyConfiguration200JSONResponse); *body.ServerName != "stored.example" {
		t.Errorf("derived serverName %q", *body.ServerName)
	}
	ws.err = kernel.ErrBadCredentials
	if _, err := api.GetReverseProxyConfiguration(adminCaller, artgen.GetReverseProxyConfigurationRequestObject{}); !errors.Is(err, kernel.ErrBadCredentials) {
		t.Errorf("typed: %v", err)
	}
	ws.err = errBoom
	if _, err := api.GetReverseProxyConfiguration(adminCaller, artgen.GetReverseProxyConfigurationRequestObject{}); statusOf(t, err) != 500 {
		t.Errorf("untyped: %v", err)
	}
	// A router without the binding has base_url only.
	bare := SystemAPI{deps: Deps{BaseURL: "https://install.example"}}
	got, _ = bare.GetReverseProxyConfiguration(adminCaller, artgen.GetReverseProxyConfigurationRequestObject{})
	if body := got.(artgen.GetReverseProxyConfiguration200JSONResponse); *body.ServerName != "install.example" {
		t.Errorf("bare serverName %q", *body.ServerName)
	}
}

// TestAccUpdateReverseProxyConfiguration: omitted fields take the
// descriptor defaults, httpsPort is sslPort, and refusals answer 400.
func TestAccUpdateReverseProxyConfiguration(t *testing.T) {
	ws := &fakeWebServer{}
	api := webServerAPI(ws)
	kind, method := artgen.NGINX, artgen.REPOPATHPREFIX
	body := &artgen.UpdateReverseProxyConfigurationJSONRequestBody{
		WebServerType: &kind, DockerReverseProxyMethod: &method, ServerName: httpkit.PtrTo("reg.example.com"),
		HttpsPort: httpkit.PtrTo(8443), UseHttps: httpkit.PtrTo(true), UseHttp: httpkit.PtrTo(false),
		SslKey: httpkit.PtrTo("/k.pem"), ArtifactoryPort: httpkit.PtrTo(9000),
	}
	if _, err := api.UpdateReverseProxyConfiguration(adminCaller, artgen.UpdateReverseProxyConfigurationRequestObject{Body: body}); err != nil {
		t.Fatal(err)
	}
	want := configapi.ReverseProxyDefaults()
	want.WebServerType, want.DockerReverseProxyMethod, want.ServerName = "NGINX", "REPOPATHPREFIX", "reg.example.com"
	want.SSLPort, want.UseHTTPS, want.UseHTTP, want.SSLKey, want.ArtifactoryPort = 8443, true, false, "/k.pem", 9000
	if diff := cmp.Diff(want, *ws.set); diff != "" {
		t.Errorf("(-want +got):\n%s", diff)
	}
	body.SslPort = httpkit.PtrTo(443)
	for name, c := range map[string]struct {
		ctx  context.Context
		api  SystemAPI
		body *artgen.UpdateReverseProxyConfigurationJSONRequestBody
		err  error
		want int
	}{
		"disagree": {adminCaller, api, body, nil, 400},
		"no body":  {adminCaller, api, nil, nil, 400},
		"user":     {userCaller, api, body, nil, 403},
		"unbound":  {adminCaller, SystemAPI{}, body, nil, 501},
		"invalid":  {adminCaller, api, &artgen.UpdateReverseProxyConfigurationJSONRequestBody{}, fmt.Errorf("%w: webServer: bad", configapi.ErrInvalid), 400},
		"revoked":  {adminCaller, api, &artgen.UpdateReverseProxyConfigurationJSONRequestBody{}, kernel.ErrForbidden, 403},
		"store":    {adminCaller, api, &artgen.UpdateReverseProxyConfigurationJSONRequestBody{}, errBoom, 500},
	} {
		ws.err = c.err
		_, err := c.api.UpdateReverseProxyConfiguration(c.ctx, artgen.UpdateReverseProxyConfigurationRequestObject{Body: c.body})
		if got := statusOf(t, err); got != c.want {
			t.Errorf("%s: %d %v", name, got, err)
		}
		if name == "invalid" && err.Error() != "webServer: bad" {
			t.Errorf("invalid message %q", err)
		}
	}
}

func TestAccGetReverseProxySnippet(t *testing.T) {
	ws := &fakeWebServer{snippet: "server {}\n"}
	api := webServerAPI(ws)
	got, err := api.GetReverseProxySnippet(adminCaller, artgen.GetReverseProxySnippetRequestObject{})
	if err != nil || got.(artgen.GetReverseProxySnippet200TextResponse) != "server {}\n" {
		t.Fatalf("%v %v", got, err)
	}
	ws.err = configapi.ErrInvalid
	for name, c := range map[string]struct {
		ctx  context.Context
		api  SystemAPI
		want int
	}{"invalid": {adminCaller, api, 400}, "user": {userCaller, api, 403}, "unbound": {adminCaller, SystemAPI{}, 501}} {
		_, err := c.api.GetReverseProxySnippet(c.ctx, artgen.GetReverseProxySnippetRequestObject{})
		if got := statusOf(t, err); got != c.want {
			t.Errorf("%s: %d", name, got)
		}
	}
}

func TestAccPutBaseURL(t *testing.T) {
	ws := &fakeWebServer{}
	api := webServerAPI(ws)
	put := func(api SystemAPI, ctx context.Context, body io.Reader) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		api.PutBaseURL(rec, httptest.NewRequestWithContext(ctx, http.MethodPut, configPath+"/baseUrl", body))
		return rec
	}
	rec := put(api, adminCaller, strings.NewReader("https://reg.example.com\n"))
	if rec.Code != 200 || rec.Body.String() != `URL base has been successfully updated to "https://reg.example.com".` ||
		rec.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	for name, c := range map[string]struct {
		api  SystemAPI
		ctx  context.Context
		body io.Reader
		err  error
		want int
	}{
		"user":       {api, userCaller, strings.NewReader("x"), nil, 403},
		"unbound":    {SystemAPI{}, adminCaller, strings.NewReader("x"), nil, 501},
		"too large":  {api, adminCaller, strings.NewReader(strings.Repeat("a", urlBaseBodyLimit+1)), nil, 413},
		"unreadable": {api, adminCaller, failingReader{}, nil, 400},
		"invalid":    {api, adminCaller, strings.NewReader("ftp://x"), configapi.ErrInvalid, 400},
	} {
		ws.err = c.err
		if rec := put(c.api, c.ctx, c.body); rec.Code != c.want {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}
