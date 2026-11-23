package api

import "context"

// ReverseProxy is the stored reverseProxies entry: the web server settings
// that GET/POST /api/system/configuration/webServer read and replace and the
// nginx snippet renders. SSLPort is the descriptor's sslPort, which the JSON
// API also spells httpsPort. Certificate and key are file paths on the proxy
// host, never key material, but they still stay out of the non-admin view.
type ReverseProxy struct {
	Key                      string
	WebServerType            string
	ArtifactoryAppContext    string
	PublicAppContext         string
	ServerName               string
	ServerNameExpression     string
	ArtifactoryServerName    string
	ArtifactoryPort          int
	SSLCertificate           string
	SSLKey                   string
	DockerReverseProxyMethod string
	UseHTTPS                 bool
	UseHTTP                  bool
	SSLPort                  int
	HTTPPort                 int
}

// ReverseProxyDefaults are the defaults of the fields a POST may omit: ReverseProxyDescriptor's field
// initialisers in 7.161.26 (key nginx, artifactoryPort 8081, useHttp true,
// useHttps false, sslPort 443, httpPort 80) and ReverseProxyMethod's
// defaultValue, SUBDOMAIN, which is also the provider's default.
func ReverseProxyDefaults() ReverseProxy {
	return ReverseProxy{
		Key: "nginx", ArtifactoryPort: 8081, DockerReverseProxyMethod: "SUBDOMAIN",
		UseHTTP: true, SSLPort: 443, HTTPPort: 80,
	}
}

// ReverseProxyView is a read of the reverse proxy settings. Stored is false
// when none are stored, and Settings is then zero. Admin is the caller's
// current administrator authority, which decides how much of Settings the
// caller may see: certificate and key paths, host names and contexts are
// for administrators only.
type ReverseProxyView struct {
	Settings ReverseProxy
	Stored   bool
	Admin    bool
}

// WebServer administers the descriptor's urlBase and reverse proxy settings.
// Every write runs through Descriptor.Apply's validation and single
// transaction, after rechecking current administrator authority under the
// shared configuration guard, so a refused or failed write changes nothing.
// Refusals wrap ErrInvalid.
type WebServer interface {
	// BaseURL is the effective configured public base URL: the stored
	// urlBase when one is set, else the installation's base_url, else "".
	// It never fails: a store error keeps the last value read. Writes made
	// through this process take effect as soon as they commit; writes from
	// another process within a bounded refresh interval.
	BaseURL(ctx context.Context) string
	// SetURLBase stores urlBase, or clears it when it is blank, and returns
	// the stored value.
	SetURLBase(ctx context.Context, urlBase string) (string, error)
	// ReverseProxy returns the stored settings and whether ctx's caller is
	// currently an administrator, checked under the configuration guard.
	ReverseProxy(ctx context.Context) (ReverseProxyView, error)
	// SetReverseProxy replaces the stored settings with rp.
	SetReverseProxy(ctx context.Context, rp ReverseProxy) error
	// NginxSnippet renders the stored settings as an nginx configuration
	// fragment. Settings that are absent or not of the NGINX type answer an
	// error wrapping ErrInvalid.
	NginxSnippet(ctx context.Context) (string, error)
}
