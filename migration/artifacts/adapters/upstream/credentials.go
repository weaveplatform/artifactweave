package upstream

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/net/http/httpguts"
	"golang.org/x/net/publicsuffix"

	"github.com/weaveplatform/artifactweave/internal/remote"
	accessapi "github.com/weaveplatform/artifactweave/migration/access/api"
	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/kernel/wire"
	"github.com/weaveplatform/artifactweave/migration/platform/crypt"
	"github.com/weaveplatform/artifactweave/pkg/store"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

// ResolverOptions is trusted application configuration, never upstream metadata.
// RootCAsPEM augments system trust. AllowedPrivateOrigins grants exact additional
// authorities private-network access without authorizing credential forwarding.
// LookupIP is injectable for deterministic DNS/rebinding tests; nil uses net.Resolver.
type ResolverOptions struct {
	// RevisionKey is trusted, stable application key material for installations
	// without a master key. It never comes from a repository or HTTP request.
	RevisionKey           []byte
	RootCAsPEM            []byte
	AllowedPrivateOrigins []string
	LookupIP              func(context.Context, string) ([]net.IPAddr, error)
	// Certificates reads the client TLS certificate, private key unsealed,
	// that a remote names in clientTlsCertificate. The resolver reads it only
	// for such a remote, so it must be set wherever a remote can name one.
	// Any error it returns fails that remote's options with ErrConfiguration,
	// except an ended context, which fails them as a request error.
	Certificates accessapi.Keys
}

// Resolver turns a stored repository configuration into the transport's
// remote.Options: its sealed credentials, headers, cookies, client
// certificate and proxy, the destination policy, and the credential
// revision that fences the remote cache against credential changes. It is
// safe for concurrent use.
type Resolver struct {
	store   store.Store
	key     []byte
	roots   *x509.CertPool
	options ResolverOptions
}

// NewResolver returns the resolver over st. The credential revision key is
// derived from keyring when there is one, else it is opts.RevisionKey, which
// must then be at least 32 bytes and not all zero; otherwise, without a
// store, or with an AllowedPrivateOrigins entry that is no checked URL, it
// answers remote.ErrConfiguration. Root certificates that cannot be loaded
// answer remote.ErrSecurity.
func NewResolver(st store.Store, keyring *crypt.Keyring, opts ResolverOptions) (*Resolver, error) {
	if st == nil || keyring == nil && len(opts.RevisionKey) < 32 {
		return nil, remote.ErrConfiguration
	}
	key := bytes.Clone(opts.RevisionKey)
	if keyring != nil {
		key = keyring.Derive("remote-source-revision")
	}
	if bytes.Equal(key, make([]byte, len(key))) {
		return nil, remote.ErrConfiguration
	}
	opts.RevisionKey = nil
	opts.RootCAsPEM = bytes.Clone(opts.RootCAsPEM)
	opts.AllowedPrivateOrigins = append([]string(nil), opts.AllowedPrivateOrigins...)
	sort.Strings(opts.AllowedPrivateOrigins)
	if opts.LookupIP == nil {
		opts.LookupIP = net.DefaultResolver.LookupIPAddr
	}
	var roots *x509.CertPool
	if len(opts.RootCAsPEM) > 0 {
		var err error
		roots, err = x509.SystemCertPool()
		if err != nil {
			return nil, remote.ErrSecurity
		}
		if !roots.AppendCertsFromPEM(opts.RootCAsPEM) {
			return nil, remote.ErrSecurity
		}
	}
	for _, raw := range opts.AllowedPrivateOrigins {
		if _, err := remote.CheckedURL(raw); err != nil {
			return nil, remote.ErrConfiguration
		}
	}
	return &Resolver{
		store:   st,
		key:     key,
		roots:   roots,
		options: opts,
	}, nil
}

// Options reloads sealed secrets through the store, rather than treating an API's
// masked view as usable credentials. A stale non-secret configuration fails closed.
// Revision is a stable HMAC of source-affecting settings, not cache/access policy.
func (r *Resolver) Options(ctx context.Context, c repo.Config) (remote.Options, error) {
	stored, err := r.store.Repos().Get(ctx, c.Key)
	if err != nil {
		return remote.Options{}, configurationError(err)
	}
	a, _ := json.Marshal(repo.Masked(c))
	b, _ := json.Marshal(repo.Masked(stored))
	if !bytes.Equal(a, b) || stored.Class != repo.Remote {
		return remote.Options{}, remote.ErrConfiguration
	}
	base, err := remote.CheckedURL(stored.URL)
	if err != nil {
		//nolint:wrapcheck // preserves upstream error identity
		return remote.Options{}, err
	}
	if err := checkHeaders(stored.CustomHTTPHeaders); err != nil {
		return remote.Options{}, err
	}
	proxy, proxyDoc, err := r.proxy(ctx, stored)
	if err != nil {
		return remote.Options{}, err
	}
	policy := &destinationPolicy{
		private: map[string]bool{remote.Origin(base): true},
		lookup:  r.options.LookupIP,
	}
	configuredOrigins := append(sourceEndpoints(stored), r.options.AllowedPrivateOrigins...)
	for _, raw := range configuredOrigins {
		u, parseErr := remote.CheckedURL(raw)
		if parseErr != nil {
			return remote.Options{}, remote.ErrConfiguration
		}
		policy.private[remote.Origin(u)] = true
	}
	if proxy != nil {
		policy.private[remote.Origin(proxy)] = true
	}
	identity, err := r.certificate(ctx, stored.ClientTLSCertificate)
	if err != nil {
		return remote.Options{}, err
	}
	var certificate tls.Certificate
	if stored.ClientTLSCertificate != "" {
		certificate, err = tls.X509KeyPair([]byte(identity.PEM), []byte(identity.PrivateKey))
		if err != nil {
			return remote.Options{}, remote.ErrSecurity
		}
	}
	secure := &sourceSecurity{
		base:        base,
		config:      stored,
		policy:      policy,
		proxy:       proxy,
		roots:       r.roots,
		certificate: certificate,
	}
	projection := sourceSettings(stored)
	inputs := []any{
		projection,
		proxyDoc.Document,
		proxyDoc.Secrets,
		identity.PEM,
		identity.PrivateKey,
		r.options.RootCAsPEM,
		r.options.AllowedPrivateOrigins,
		"destination-policy-v1",
	}
	mac := hmac.New(sha256.New, r.key)
	encoded, _ := json.Marshal(inputs)
	_, _ = mac.Write(encoded)
	opts := remote.Options{
		ProxyURL: proxy, Revision: hex.EncodeToString(mac.Sum(nil)),
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: r.roots},
		Prepare:   secure.prepare, CheckURL: secure.checkURL,
		WrapTransport: secure.transport,
	}
	if enabled(stored.EnableCookieManagement) {
		jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
		opts.Jar = jar
	}
	return opts, nil
}

func configurationError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return safeError(err)
	}
	return remote.ErrConfiguration
}

func (r *Resolver) certificate(ctx context.Context, alias string) (types.Certificate, error) {
	if alias == "" {
		return types.Certificate{}, nil
	}
	cert, err := r.options.Certificates.Certificate(ctx, alias)
	if err != nil {
		return types.Certificate{}, configurationError(err)
	}
	return cert, nil
}

func (r *Resolver) proxy(
	ctx context.Context,
	c repo.Config,
) (*url.URL, types.ConfigDocument, error) {
	if enabled(c.DisableProxy) {
		return nil, types.ConfigDocument{}, nil
	}
	var doc types.ConfigDocument
	if c.Proxy != "" {
		var err error
		doc, err = r.store.Configs().Get(ctx, types.ConfigProxy, c.Proxy)
		if err != nil {
			return nil, doc, configurationError(err)
		}
	} else {
		docs, err := r.store.Configs().List(ctx, types.ConfigProxy)
		if err != nil {
			return nil, doc, configurationError(err)
		}
		for _, candidate := range docs {
			var p wire.ProxyConfiguration
			if json.Unmarshal(candidate.Document, &p) != nil {
				return nil, doc, remote.ErrConfiguration
			}
			if candidate.Enabled && p.PlatformDefault && proxyService(p.Services) {
				if doc.Key != "" {
					return nil, doc, remote.ErrConfiguration
				}
				doc = candidate
			}
		}
		if doc.Key == "" {
			return nil, doc, nil
		}
	}
	var p wire.ProxyConfiguration
	if !doc.Enabled || json.Unmarshal(doc.Document, &p) != nil || p.Port < 1 || p.Port > 65535 ||
		p.Domain != "" ||
		p.NTHost != "" ||
		p.RedirectedToHosts != "" {
		return nil, doc, remote.ErrConfiguration
	}
	host := p.Host
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	u, err := remote.CheckedURL(host)
	if err != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Port() != "" {
		return nil, doc, remote.ErrConfiguration
	}
	u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(p.Port))
	u.Path = ""
	if p.Username != "" || doc.Secrets["password"] != "" {
		u.User = url.UserPassword(p.Username, doc.Secrets["password"])
	}
	return u, doc, nil
}

func proxyService(services []string) bool {
	if len(services) == 0 {
		return true
	}
	for _, service := range services {
		if service == "jfrt" {
			return true
		}
	}
	return false
}

func checkHeaders(headers []repo.CustomHTTPHeader) error {
	for _, header := range headers {
		name := http.CanonicalHeaderKey(header.Name)
		if !httpguts.ValidHeaderFieldName(name) || !httpguts.ValidHeaderFieldValue(header.Value) {
			return remote.ErrConfiguration
		}
		switch name {
		case "Host",
			"Proxy-Authorization",
			"Cookie",
			"Set-Cookie",
			"Connection",
			"Proxy-Connection",
			"Content-Length",
			"Transfer-Encoding",
			"Te",
			"Trailer",
			"Upgrade":
			return remote.ErrConfiguration
		}
	}
	return nil
}

// Keep cache policy and authorization out of source identity: policy changes fence
// publication separately and must not discard valid immutable bytes (#619).
func sourceSettings(c repo.Config) repo.Config {
	return repo.Config{
		URL:                       c.URL,
		Username:                  c.Username,
		Password:                  c.Password,
		Proxy:                     c.Proxy,
		DisableProxy:              c.DisableProxy,
		AllowAnyHostAuth:          c.AllowAnyHostAuth,
		EnableCookieManagement:    c.EnableCookieManagement,
		EnableTokenAuthentication: c.EnableTokenAuthentication,
		ClientTLSCertificate:      c.ClientTLSCertificate,
		CustomHTTPHeaders:         c.CustomHTTPHeaders,
		QueryParams:               c.QueryParams,
		PropagateQueryParams:      c.PropagateQueryParams,
		DisableURLNormalization:   c.DisableURLNormalization,
		PyPIRegistryURL:           c.PyPIRegistryURL,
		PyPIRepositorySuffix:      c.PyPIRepositorySuffix,
		FeedContextPath:           c.FeedContextPath,
		DownloadContextPath:       c.DownloadContextPath,
		V3FeedURL:                 c.V3FeedURL,
		SymbolServerURL:           c.SymbolServerURL,
		BowerRegistryURL:          c.BowerRegistryURL,
		GitRegistryURL:            c.GitRegistryURL,
		ComposerRegistryURL:       c.ComposerRegistryURL,
		PodsSpecsRepoURL:          c.PodsSpecsRepoURL,
		PodsCdnURL:                c.PodsCdnURL,
		P2ChildOrigins:            c.P2ChildOrigins,
		ChartsBaseURL:             c.ChartsBaseURL,
		TerraformRegistryURL:      c.TerraformRegistryURL,
		TerraformProvidersURL:     c.TerraformProvidersURL,
		VCSType:                   c.VCSType,
		VCSGitProvider:            c.VCSGitProvider,
		VCSGitDownloadURL:         c.VCSGitDownloadURL,
		DockerProjectID:           c.DockerProjectID,
	}
}

// Explicit repository endpoints are operator configuration, unlike URLs returned
// in downloaded metadata. They grant network access but not credential forwarding.
func sourceEndpoints(c repo.Config) []string {
	urls := make([]string, 0, 12+len(c.P2ChildOrigins))
	urls = append(urls,
		c.PyPIRegistryURL,
		c.V3FeedURL,
		c.SymbolServerURL,
		c.ChartsBaseURL,
		c.TerraformRegistryURL,
		c.TerraformProvidersURL,
		c.VCSGitDownloadURL,
		c.BowerRegistryURL,
		c.GitRegistryURL,
		c.ComposerRegistryURL,
		c.PodsSpecsRepoURL,
		c.PodsCdnURL,
	)
	urls = append(urls, c.P2ChildOrigins...)
	result := make([]string, 0, len(urls))
	for _, u := range urls {
		if u != "" {
			result = append(result, u)
		}
	}
	return result
}
