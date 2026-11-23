package pushhttp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/weaveplatform/artifactweave/internal/federation/core/model"
	"github.com/weaveplatform/artifactweave/internal/federation/core/port"
	"github.com/weaveplatform/artifactweave/internal/federation/core/replication"
	"github.com/weaveplatform/artifactweave/internal/kernel/wire"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/outbound"
)

// Store reads the current proxy inside the shared metadata transaction.
type Store interface {
	Configs() configdoc.ConfigStore
	InTx(context.Context) bool
}

// Options is trusted startup policy, not data supplied by a target response.
// RevisionKey is stable purpose-specific private key material, at least 32 bytes.
type Options struct {
	RevisionKey           []byte
	RootCAsPEM            []byte
	AllowedPrivateOrigins []string
	LookupIP              func(context.Context, string) ([]net.IPAddr, error)
}

type pooled struct {
	connection *connection
	used       uint64
}

// Factory resolves credentials/proxies without network I/O and owns a bounded
// connection pool. The application closes it after cancelling active workers.
type Factory struct {
	store  Store
	key    []byte
	roots  *x509.CertPool
	policy *outbound.Policy
	inputs policyInputs
	mu     sync.Mutex
	closed bool
	clock  uint64
	pool   map[string]pooled
}

type policyInputs struct {
	Roots   []byte   `json:"roots"`
	Origins []string `json:"origins"`
}

var _ port.PushConnections = (*Factory)(nil)

// New validates and copies immutable policy without reading the store or DNS.
func New(store Store, options Options) (*Factory, error) {
	if store == nil || len(options.RevisionKey) < 32 || !slices.ContainsFunc(options.RevisionKey, func(b byte) bool { return b != 0 }) {
		return nil, outbound.ErrConfiguration
	}
	origins := slices.Clone(options.AllowedPrivateOrigins)
	policy, err := outbound.NewPolicy(origins, options.LookupIP)
	if err != nil {
		return nil, fmt.Errorf("replication destination policy: %w", err)
	}
	slices.Sort(origins)
	origins = slices.Compact(origins)
	var roots *x509.CertPool
	if len(options.RootCAsPEM) > 0 {
		roots, err = x509.SystemCertPool()
		if err != nil {
			return nil, outbound.ErrConfiguration
		}
		if !roots.AppendCertsFromPEM(options.RootCAsPEM) {
			return nil, outbound.ErrConfiguration
		}
	}
	return &Factory{
		store: store, key: slices.Clone(options.RevisionKey), roots: roots, policy: policy,
		inputs: policyInputs{Roots: slices.Clone(options.RootCAsPEM), Origins: origins}, pool: make(map[string]pooled),
	}, nil
}

// Connection joins the current metadata transaction; target is its current
// unsealed configuration, supplied by the owning service's configuration fence.
func (f *Factory) Connection(ctx context.Context, target model.Replication) (port.PushConnection, error) {
	if ctx == nil || !f.store.InTx(ctx) || target.Key == "" || target.SocketTimeout <= 0 {
		return nil, outbound.ErrConfiguration
	}
	endpoint, err := replication.NewPushEndpoint(target.URL)
	if err != nil {
		return nil, outbound.ErrConfiguration
	}
	base, err := endpoint.AQLURL()
	if err != nil {
		return nil, outbound.ErrConfiguration
	}
	proxy, err := f.proxy(ctx, target)
	if err != nil {
		return nil, err
	}
	proxyURL := ""
	if proxy.url != nil {
		proxyURL = proxy.url.String()
	}
	encoded, err := json.Marshal(f.inputs)
	if err != nil {
		return nil, outbound.ErrConfiguration
	}
	mac := hmac.New(sha256.New, f.key)
	// Length-delimited fields prevent ambiguous concatenation. Secrets go
	// directly into the private digest, never a general-purpose document.
	values := []string{target.URL, target.Username, target.Password, proxyURL, strconv.FormatInt(int64(target.SocketTimeout), 10), string(encoded)}
	if proxy.ntlm != nil {
		// A domain/workstation edit must retire tunnels authenticated as the old
		// identity. Appending only for NTLM keeps Basic/direct revisions, and
		// with them persisted run bindings, unchanged. redirectedToHosts is
		// deliberately absent: push never follows a cross-origin redirect.
		values = append(values, "ntlm", proxy.domain, proxy.workstation)
	}
	for _, value := range values {
		_, _ = fmt.Fprintf(mac, "%d:", len(value))
		_, _ = io.WriteString(mac, value)
	}
	revision := hex.EncodeToString(mac.Sum(nil))
	// Credentials influence only a keyed digest and the request itself. Pool
	// keys are public identities, never raw URLs, passwords or proxy documents.
	key := target.RepoKey + "\x00" + target.Key
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, outbound.ErrClosed
	}
	f.clock++
	if existing, ok := f.pool[key]; ok {
		if existing.connection.revision == revision {
			existing.used = f.clock
			f.pool[key] = existing
			return existing.connection, nil
		}
		existing.connection.client.Close()
		delete(f.pool, key)
	}
	client, err := outbound.NewClient(outbound.ClientOptions{
		Base: base, Proxy: proxy.url, NTLM: proxy.ntlm, Policy: f.policy, RootCAs: f.roots, SocketTimeout: target.SocketTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("construct replication connection: %w", err)
	}
	if len(f.pool) >= 256 {
		var oldest string
		used := ^uint64(0)
		for candidate, entry := range f.pool {
			if entry.used < used {
				oldest, used = candidate, entry.used
			}
		}
		f.pool[oldest].connection.client.Close()
		delete(f.pool, oldest)
	}
	c := &connection{endpoint: endpoint, client: client, inTx: f.store.InTx, revision: revision, username: target.Username, password: target.Password}
	f.pool[key] = pooled{connection: c, used: f.clock}
	return c, nil
}

// Close refuses future connections and exchanges and releases all idle sockets.
func (f *Factory) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for key, entry := range f.pool {
		entry.connection.client.Close()
		delete(f.pool, key)
	}
}

// proxySelection is the effective proxy policy. Its names are private revision
// inputs; the NTLM identity also holds the sealed password, never a pool key.
type proxySelection struct {
	url                 *url.URL
	ntlm                *outbound.NTLMProxy
	domain, workstation string
}

func (f *Factory) proxy(ctx context.Context, target model.Replication) (proxySelection, error) {
	if target.DisableProxy {
		return proxySelection{}, nil
	}
	var selected configdoc.ConfigDocument
	if target.Proxy != "" {
		doc, err := f.store.Configs().Get(ctx, configdoc.ConfigProxy, target.Proxy)
		if err != nil {
			return proxySelection{}, proxyError(err)
		}
		selected = doc
	} else {
		docs, err := f.store.Configs().List(ctx, configdoc.ConfigProxy)
		if err != nil {
			return proxySelection{}, proxyError(err)
		}
		for _, doc := range docs {
			var proxy wire.ProxyConfiguration
			if json.Unmarshal(doc.Document, &proxy) != nil {
				return proxySelection{}, outbound.ErrConfiguration
			}
			if doc.Enabled && proxy.PlatformDefault && proxyService(proxy.Services) {
				if selected.Key != "" {
					return proxySelection{}, outbound.ErrConfiguration
				}
				selected = doc
			}
		}
		if selected.Key == "" {
			// No default selects the direct connection.
			return proxySelection{}, nil
		}
	}
	return proxyURL(selected)
}

func proxyURL(doc configdoc.ConfigDocument) (proxySelection, error) {
	var proxy wire.ProxyConfiguration
	if !doc.Enabled || json.Unmarshal(doc.Document, &proxy) != nil || proxy.Port < 1 || proxy.Port > 65535 {
		return proxySelection{}, outbound.ErrConfiguration
	}
	// The list is validated like the remote resolver's, so malformed settings
	// fail truthfully. A valid list changes nothing here: the outbound client
	// refuses cross-origin and write redirects, and target credentials are
	// always the replication's own, never the proxy's.
	if _, err := outbound.ProxyRedirectHosts(proxy.RedirectedToHosts); err != nil {
		return proxySelection{}, outbound.ErrConfiguration
	}
	host := proxy.Host
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	u, err := url.Parse(host)
	if err != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.Opaque != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Port() != "" || u.Path != "" && u.Path != "/" {
		return proxySelection{}, outbound.ErrConfiguration
	}
	u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(proxy.Port))
	u.Path, u.RawPath = "", ""
	if proxy.Username != "" || doc.Secrets["password"] != "" {
		u.User = url.UserPassword(proxy.Username, doc.Secrets["password"])
	}
	selection := proxySelection{url: u}
	// As in the remote resolver (#1835), either NTLM field selects NTLMv2 with
	// the same username and sealed password; Basic is never a fallback.
	if proxy.Domain != "" || proxy.NTHost != "" {
		selection.ntlm, err = outbound.NewNTLMProxy(proxy.Username, doc.Secrets["password"], proxy.Domain, proxy.NTHost)
		if err != nil {
			return proxySelection{}, outbound.ErrConfiguration
		}
		selection.domain, selection.workstation = proxy.Domain, proxy.NTHost
	}
	return selection, nil
}

func proxyService(services []string) bool {
	return len(services) == 0 || slices.Contains(services, "jfrt")
}

func proxyError(err error) error {
	for _, known := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, known) {
			return known
		}
	}
	return outbound.ErrConfiguration
}
