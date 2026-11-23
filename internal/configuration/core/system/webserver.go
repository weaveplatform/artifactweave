package system

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
)

// Web server types and Docker access methods, in the JSON API's spelling.
// 7.161.26's WebServerType also has HAPROXY, but neither the reference page,
// the platform_reverse_proxy resource nor artifactory.xsd (direct, nginx,
// apache) names it, so it is refused rather than stored for nothing to read.
const (
	WebServerNginx  = "NGINX"
	WebServerApache = "APACHE"
	WebServerDirect = "DIRECT"

	DockerSubdomain      = "SUBDOMAIN"
	DockerRepoPathPrefix = "REPOPATHPREFIX"
	DockerPortPerRepo    = "PORTPERREPO"
)

var (
	// key is an XML ID (the descriptor's xs:ID) without the characters
	// DescriptorUtils.assertNoSpecialCharactersAndValidXmlName refuses.
	proxyKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
	// A context is one or more URL path segments of unreserved characters,
	// which need no quoting anywhere the snippet could put them.
	appContext = regexp.MustCompile(`^[A-Za-z0-9._~-]+(/[A-Za-z0-9._~-]+)*$`)
	// hostLabel is one DNS label; dnsName rejects the empty and trailing-dot
	// forms because nginx's server_name would read them differently.
	hostLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	// certPath admits absolute POSIX paths of characters that are literal in
	// every nginx argument: no space, quote, semicolon, brace, dollar (ssl_*
	// paths expand variables since nginx 1.15.9), hash or backslash.
	certPath = regexp.MustCompile(`^/[A-Za-z0-9._@+=,~/-]*$`)
)

// CheckReverseProxy validates complete settings: the constraints 7.161.26's
// ReverseProxyDescriptor declares, with their messages, and the values the
// nginx snippet writes as configuration, so nothing stored can change the
// snippet's syntax. Local choices, marked [I] in research 06: DIRECT may
// leave both host names blank, as the provider lets it; PORTPERREPO is
// refused because artifactweave offers no port per repository (DV-009); a
// proxy must listen somewhere; HTTPS needs a certificate and key, as the
// provider requires; and the two listeners cannot share a port.
func CheckReverseProxy(rp configapi.ReverseProxy) error {
	proxied := rp.WebServerType == WebServerNginx || rp.WebServerType == WebServerApache
	checks := []struct {
		bad     bool
		message string
	}{
		{rp.Key == "", "key should not be blank"},
		{rp.Key != "" && !proxyKey.MatchString(rp.Key), "key must be a valid XML name without special characters"},
		{rp.WebServerType == "", "webServerType should not be null"},
		{rp.WebServerType != "" && !proxied && rp.WebServerType != WebServerDirect, "webServerType must be one of NGINX, APACHE, DIRECT"},
		{rp.DockerReverseProxyMethod == DockerPortPerRepo, "dockerReverseProxyMethod PORTPERREPO is not offered: artifactweave serves no port per repository"},
		{
			!slices.Contains([]string{DockerSubdomain, DockerRepoPathPrefix, DockerPortPerRepo}, rp.DockerReverseProxyMethod),
			"dockerReverseProxyMethod must be one of SUBDOMAIN, REPOPATHPREFIX",
		},
		{rp.PublicAppContext == "", "publicAppContext should not be blank"},
		{rp.PublicAppContext != "" && !appContext.MatchString(rp.PublicAppContext), "publicAppContext must be URL path segments"},
		{rp.ArtifactoryAppContext != "" && !appContext.MatchString(rp.ArtifactoryAppContext), "artifactoryAppContext must be URL path segments"},
		{proxied && rp.ServerName == "", "serverName should not be blank"},
		{rp.ServerName != "" && !dnsName(rp.ServerName), "serverName must be a host name"},
		{rp.ServerNameExpression != "" && !dnsName(strings.TrimPrefix(rp.ServerNameExpression, "*.")), "serverNameExpression must be a host name or *.<host name>"},
		{proxied && rp.ArtifactoryServerName == "", "artifactoryServerName should not be blank"},
		{rp.ArtifactoryServerName != "" && !upstreamHost(rp.ArtifactoryServerName), "artifactoryServerName must be a host name or IP address"},
		{!port(rp.ArtifactoryPort), "artifactoryPort should be > 0 and at most 65535"},
		{!port(rp.SSLPort), "sslPort should be > 0 and at most 65535"},
		{!port(rp.HTTPPort), "httpPort should be > 0 and at most 65535"},
		{proxied && !rp.UseHTTP && !rp.UseHTTPS, "useHttp or useHttps must be set"},
		{proxied && rp.UseHTTP && rp.UseHTTPS && rp.SSLPort == rp.HTTPPort, "sslPort and httpPort must differ"},
		{proxied && rp.UseHTTPS && (rp.SSLCertificate == "" || rp.SSLKey == ""), "useHttps needs sslCertificate and sslKey"},
		{rp.SSLCertificate != "" && !certPath.MatchString(rp.SSLCertificate), "sslCertificate must be an absolute path without spaces or nginx syntax"},
		{rp.SSLKey != "" && !certPath.MatchString(rp.SSLKey), "sslKey must be an absolute path without spaces or nginx syntax"},
	}
	for _, c := range checks {
		if c.bad {
			return fmt.Errorf("%w: webServer: %s", configapi.ErrInvalid, c.message)
		}
	}
	return nil
}

func port(n int) bool { return n >= 1 && n <= 65535 }

func dnsName(s string) bool {
	if len(s) > 253 {
		return false
	}
	for label := range strings.SplitSeq(s, ".") {
		if !hostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func upstreamHost(s string) bool {
	if inner, ok := strings.CutPrefix(s, "["); ok {
		ip := net.ParseIP(strings.TrimSuffix(inner, "]"))
		return strings.HasSuffix(inner, "]") && ip != nil && ip.To4() == nil
	}
	return dnsName(s)
}

// reverseProxyFields maps the stored document's names to the settings.
// sslPort is the descriptor's name; the JSON API's httpsPort is an alias
// that only the HTTP adapter reads and writes.
func reverseProxyFields(rp *configapi.ReverseProxy) (map[string]*string, map[string]*bool, map[string]*int) {
	return map[string]*string{
			"key": &rp.Key, "webServerType": &rp.WebServerType,
			"artifactoryAppContext": &rp.ArtifactoryAppContext, "publicAppContext": &rp.PublicAppContext,
			"serverName": &rp.ServerName, "serverNameExpression": &rp.ServerNameExpression,
			"artifactoryServerName": &rp.ArtifactoryServerName, "sslCertificate": &rp.SSLCertificate,
			"sslKey": &rp.SSLKey, "dockerReverseProxyMethod": &rp.DockerReverseProxyMethod,
		}, map[string]*bool{"useHttps": &rp.UseHTTPS, "useHttp": &rp.UseHTTP},
		map[string]*int{"artifactoryPort": &rp.ArtifactoryPort, "sslPort": &rp.SSLPort, "httpPort": &rp.HTTPPort}
}

// ReverseProxyDocument is the stored form of rp: every field, so merging it
// into a stored document replaces that document whole, as POST replaces
// the current reverse proxy (CentralConfigDescriptorImpl.updateReverseProxy).
func ReverseProxyDocument(rp configapi.ReverseProxy) map[string]any {
	doc := map[string]any{}
	strs, bools, ints := reverseProxyFields(&rp)
	for k, v := range strs {
		doc[k] = *v
	}
	for k, v := range bools {
		doc[k] = *v
	}
	for k, v := range ints {
		doc[k] = *v
	}
	return doc
}

// ReverseProxyFromDocument reads a stored or merged document back. A field
// of the wrong type or an unknown one is refused, so a document that did
// not come from ReverseProxyDocument is never served.
func ReverseProxyFromDocument(doc map[string]any) (configapi.ReverseProxy, error) {
	rp := configapi.ReverseProxyDefaults()
	strs, bools, ints := reverseProxyFields(&rp)
	for _, k := range slices.Sorted(maps.Keys(doc)) {
		v := doc[k]
		var ok bool
		switch {
		case strs[k] != nil:
			*strs[k], ok = v.(string)
		case bools[k] != nil:
			*bools[k], ok = v.(bool)
		case ints[k] != nil:
			*ints[k], ok = wholeNumber(v)
		}
		if !ok {
			return configapi.ReverseProxy{}, fmt.Errorf("%w: webServer: invalid field %s", configapi.ErrInvalid, k)
		}
	}
	return rp, nil
}

// wholeNumber reads an int from a YAML int, a stored JSON float64 or a
// json.Number, refusing fractions and values outside an int32, the
// descriptor's xs:int.
func wholeNumber(v any) (int, bool) {
	var f float64
	switch n := v.(type) {
	case int:
		f = float64(n)
	case float64:
		f = n
	case json.Number:
		parsed, err := n.Float64()
		if err != nil {
			return 0, false
		}
		f = parsed
	default:
		return 0, false
	}
	if math.Trunc(f) != f || f < math.MinInt32 || f > math.MaxInt32 {
		return 0, false
	}
	return int(f), true
}

// CheckURLBase validates a non-blank urlBase: an absolute http or https URL
// naming a host, as 6.20's ConfigResource.setUrlBase requires of it through
// UrlValidator, without user information, a query or a fragment, which no
// link built on it could carry, and whose host is not one cdnHosts sit under.
func CheckURLBase(raw string, cdnHosts []string) error {
	invalid := func(why string) error {
		return fmt.Errorf("%w: urlBase %s", configapi.ErrInvalid, why)
	}
	if strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return invalid("must not contain spaces or control characters")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" {
		return invalid("must be an absolute http or https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return invalid("must not carry user information, a query or a fragment")
	}
	host := strings.ToLower(u.Hostname())
	for _, cdn := range cdnHosts {
		if cdn == host || strings.HasSuffix(cdn, "."+host) {
			return invalid("must not be a CDN domain's host or a parent of it: clients would send it their credentials")
		}
	}
	return nil
}

// errNoNginxSnippet reports settings the snippet cannot be rendered from.
func errNoNginxSnippet(why string) error {
	return fmt.Errorf("%w: webServer: %s", configapi.ErrInvalid, why)
}

// NginxSnippet renders rp as an nginx fragment for the http context, after
// the shape of the Get Reverse Proxy Snippet example: the TLS settings, then
// one server block with its listeners and server names, the forwarded
// protocol and a location that proxies to artifactweave with the
// X-JFrog-Override-Base-Url header JFrog's reverse proxy guidance sets.
// artifactweave serves every product path from one listener, so there is a
// single upstream rather than the router and Artifactory ports of a JFrog
// platform. Under the subdomain method <repo>.<server name>/v1 and /v2 are
// rewritten to /artifactory/api/docker/<repo>/..., which artifactweave
// serves; the repository path method needs no rewrite because
// artifactweave serves /v2/<repo>/... itself. The settings are checked
// again, so a document that bypassed CheckReverseProxy never reaches nginx.
func NginxSnippet(rp configapi.ReverseProxy) (string, error) {
	if err := CheckReverseProxy(rp); err != nil {
		return "", err
	}
	if rp.WebServerType != WebServerNginx {
		return "", errNoNginxSnippet("the reverse proxy is not of the NGINX type")
	}
	var b strings.Builder
	if rp.UseHTTPS {
		b.WriteString("## add ssl entries when https has been set in config\n")
		fmt.Fprintf(&b, "ssl_certificate      %s;\n", rp.SSLCertificate)
		fmt.Fprintf(&b, "ssl_certificate_key  %s;\n", rp.SSLKey)
		b.WriteString("ssl_session_cache shared:SSL:1m;\n")
		b.WriteString("ssl_prefer_server_ciphers   on;\n")
	}
	b.WriteString("## server configuration\nserver {\n")
	if rp.UseHTTPS {
		fmt.Fprintf(&b, "    listen %d ssl;\n", rp.SSLPort)
	}
	if rp.UseHTTP {
		fmt.Fprintf(&b, "    listen %d;\n", rp.HTTPPort)
	}
	names := rp.ServerName
	if rp.DockerReverseProxyMethod == DockerSubdomain {
		parent := strings.TrimPrefix(rp.ServerNameExpression, "*.")
		if parent == "" {
			parent = rp.ServerName
		}
		// A label never contains a dot, so the capture is exactly one
		// repository key; the escaped dots match only literal dots.
		names = `~^(?<repo>[^.]+)\.` + strings.ReplaceAll(parent, ".", `\.`) + "$ " + rp.ServerName
	}
	fmt.Fprintf(&b, "    server_name %s;\n\n", names)
	b.WriteString("    if ($http_x_forwarded_proto = '') {\n        set $http_x_forwarded_proto  $scheme;\n    }\n")
	b.WriteString("    ## Application specific logs\n")
	fmt.Fprintf(&b, "    ## access_log /var/log/nginx/%s-access.log;\n", rp.ServerName)
	fmt.Fprintf(&b, "    ## error_log /var/log/nginx/%s-error.log;\n", rp.ServerName)
	if rp.DockerReverseProxyMethod == DockerSubdomain {
		b.WriteString("    rewrite ^/(v1|v2)/(.*) /artifactory/api/docker/$repo/$1/$2;\n")
	}
	b.WriteString("    chunked_transfer_encoding on;\n    client_max_body_size 0;\n")
	b.WriteString("    location / {\n")
	b.WriteString("        proxy_read_timeout  900;\n")
	b.WriteString("        proxy_pass_header   Server;\n")
	b.WriteString("        proxy_cookie_path   ~*^/.* /;\n")
	fmt.Fprintf(&b, "        proxy_pass          http://%s;\n",
		net.JoinHostPort(strings.Trim(rp.ArtifactoryServerName, "[]"), strconv.Itoa(rp.ArtifactoryPort)))
	b.WriteString("        proxy_set_header    X-JFrog-Override-Base-Url $http_x_forwarded_proto://$host:$server_port;\n")
	b.WriteString("        proxy_set_header    X-Forwarded-Port  $server_port;\n")
	b.WriteString("        proxy_set_header    X-Forwarded-Proto $http_x_forwarded_proto;\n")
	b.WriteString("        proxy_set_header    Host              $http_host;\n")
	b.WriteString("        proxy_set_header    X-Forwarded-For   $proxy_add_x_forwarded_for;\n")
	b.WriteString("    }\n}\n")
	return b.String(), nil
}
