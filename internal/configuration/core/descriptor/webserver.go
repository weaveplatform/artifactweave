package descriptor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	configapi "github.com/weaveplatform/artifactweave/internal/configuration/api"
	"github.com/weaveplatform/artifactweave/internal/configuration/core/system"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/configdoc"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit"
)

// SectionURLBase is the descriptor's custom base URL, a top-level scalar
// documented in the YAML General settings. SectionReverseProxies names the
// reverse proxy in results; only POST /api/system/configuration/webServer
// writes it, because no YAML page documents a reverseProxies section and a
// YAML document carrying one is refused as any unapplied section is.
const (
	SectionURLBase        = "urlBase"
	SectionReverseProxies = "reverseProxies"
	// SectionFederatedRepoURLBase is 7.x's federatedRepoUrlBase, a
	// top-level scalar beside urlBase ("A hard-coded URL prefix used by
	// federated repository as the local member URL base",
	// artifactory.xsd:211). It overrides urlBase for this instance's own
	// federated repository members and for the base URL a federated pairing
	// token carries (decision 0022).
	SectionFederatedRepoURLBase = "federatedRepoUrlBase"
)

// Each is stored as one configuration document. The two base URL kinds are
// configdoc's, as the artifacts and federation modules read
// federatedRepoUrlBase and urlBase through configdoc.FederatedBase; the
// reverse proxy uses the shared reverse_proxy kind under one key, because
// Artifactory keeps a single current reverse proxy and replaces it whole
// (CentralConfigDescriptorImpl.updateReverseProxy).
const (
	urlBaseKind     = configdoc.ConfigURLBase
	urlBaseKey      = configdoc.BaseURLKey
	urlBaseField    = configdoc.URLBaseField
	reverseProxyKey = "current"
)

// baseScalar is one of the two base URL scalars of the descriptor.
type baseScalar struct {
	section string
	kind    configdoc.ConfigKind
	field   string
}

var (
	urlBaseScalar              = baseScalar{SectionURLBase, urlBaseKind, urlBaseField}
	federatedRepoURLBaseScalar = baseScalar{
		SectionFederatedRepoURLBase, configdoc.ConfigFederatedRepoURLBase, configdoc.FederatedRepoURLBaseField,
	}
)

var _ configapi.WebServer = webServerAPI{}

// singleton reports the sections whose results name the section alone.
func singleton(c ConfigChange) bool {
	return c.Kind == configdoc.ConfigMailServer || c.Section == SectionURLBase ||
		c.Section == SectionFederatedRepoURLBase || c.Section == SectionReverseProxies ||
		c.Section == SectionSystemMessage || c.Section == SectionQuota
}

// parseURLBase adds the urlBase and federatedRepoUrlBase scalars of top,
// when present, to p. A null or blank value clears one, as 6.20's
// ConfigResource allows "removing custom urlBase"; anything else must be a
// URL, checked here without the CDN hosts, which Apply adds.
func parseURLBase(top map[string]any, p *Patch) error {
	for _, b := range []baseScalar{urlBaseScalar, federatedRepoURLBaseScalar} {
		v, present := top[b.section]
		if !present {
			continue
		}
		if v != nil {
			if _, ok := v.(string); !ok {
				return fmt.Errorf("%w: %s must be a string or null", ErrInvalid, b.section)
			}
		}
		c := b.change(v)
		if !c.Delete {
			if err := system.CheckBaseURL(b.section, c.Fields[b.field].(string), nil); err != nil {
				return err //nolint:wrapcheck // the validator names the field and ErrInvalid
			}
		}
		p.Configs = append(p.Configs, c)
	}
	return nil
}

func urlBaseChange(v any) ConfigChange { return urlBaseScalar.change(v) }

func (b baseScalar) change(v any) ConfigChange {
	c := ConfigChange{Section: b.section, Kind: b.kind, Key: urlBaseKey}
	s, _ := v.(string)
	if s = strings.TrimSpace(s); s == "" {
		c.Delete = true
		return c
	}
	c.Fields = map[string]any{b.field: s}
	return c
}

// validateWebServer checks merged web server documents against the whole
// installation, which Parse cannot see: the CDN hosts a urlBase may not sit
// above.
func validateWebServer(d Deps, kind configdoc.ConfigKind, doc map[string]any) error {
	switch kind {
	case urlBaseKind:
		s, _ := doc[urlBaseField].(string)
		return system.CheckURLBase(s, d.Settings.CDNHosts) //nolint:wrapcheck // names the field and ErrInvalid
	case federatedRepoURLBaseScalar.kind:
		s, _ := doc[federatedRepoURLBaseScalar.field].(string)
		//nolint:wrapcheck // names the field and ErrInvalid
		return system.CheckBaseURL(federatedRepoURLBaseScalar.section, s, d.Settings.CDNHosts)
	case configdoc.ConfigReverseProxy:
		rp, err := system.ReverseProxyFromDocument(doc)
		if err != nil {
			return err //nolint:wrapcheck // names the field and ErrInvalid
		}
		return system.CheckReverseProxy(rp) //nolint:wrapcheck // names the field and ErrInvalid
	}
	return nil
}

// touchesURLBase reports whether p writes urlBase, whose cached effective
// value must then be dropped once the write commits.
func touchesURLBase(p *Patch) bool {
	for _, c := range p.Configs {
		if c.Kind == urlBaseKind {
			return true
		}
	}
	return false
}

// webServerAPI is configapi.WebServer over Deps. Writes build a Patch and
// apply it with Apply, so PUT baseUrl, POST webServer, PATCH, config apply
// and the bootstrap descriptor share one validation and one transaction.
type webServerAPI struct {
	d      Deps
	public *system.PublicURL
}

func newPublicURL(d Deps) *system.PublicURL {
	return &system.PublicURL{
		Fallback: d.Settings.BaseURL,
		Read: func(ctx context.Context) (string, bool, error) {
			return storedURLBase(ctx, d.Configs)
		},
	}
}

func storedURLBase(ctx context.Context, configs configdoc.ConfigStore) (string, bool, error) {
	s, ok, err := configdoc.StoredScalar(ctx, configs, urlBaseKind, urlBaseField)
	if err != nil {
		return "", false, fmt.Errorf("descriptor: %w", err)
	}
	return s, ok, nil
}

func (a webServerAPI) BaseURL(ctx context.Context) string { return a.public.Get(ctx) }

func (a webServerAPI) SetURLBase(ctx context.Context, urlBase string) (string, error) {
	c := urlBaseChange(urlBase)
	if !c.Delete {
		if err := system.CheckURLBase(c.Fields[urlBaseField].(string), a.d.Settings.CDNHosts); err != nil {
			return "", err //nolint:wrapcheck // names the field and ErrInvalid
		}
	}
	if err := a.write(ctx, &Patch{Configs: []ConfigChange{c}, Changes: 1}); err != nil {
		return "", err
	}
	stored, _ := c.Fields[urlBaseField].(string)
	return stored, nil
}

func (a webServerAPI) SetReverseProxy(ctx context.Context, rp configapi.ReverseProxy) error {
	// Checked before the write so the refusal does not wait on the guard; the
	// merged document is checked again inside Apply.
	if err := system.CheckReverseProxy(rp); err != nil {
		return err //nolint:wrapcheck // names the field and ErrInvalid
	}
	return a.write(ctx, &Patch{Configs: []ConfigChange{{
		Section: SectionReverseProxies, Kind: configdoc.ConfigReverseProxy, Key: reverseProxyKey,
		Fields: system.ReverseProxyDocument(rp),
	}}, Changes: 1})
}

// write applies p as the caller, rechecking current administrator authority
// under the configuration guard in the transaction that publishes it, as
// export does before disclosure. The cached base URL is dropped after the
// outer commit as well as inside Apply, whose transaction is nested here.
func (a webServerAPI) write(ctx context.Context, p *Patch) error {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return err //nolint:wrapcheck // the HTTP status of the refusal
	}
	err := a.d.Tx.Tx(ctx, func(ctx context.Context) error {
		if err := a.guard(ctx); err != nil {
			return err
		}
		if err := (exportAPI{d: a.d}).currentAdmin(ctx); err != nil {
			return err
		}
		_, err := Apply(ctx, a.d, p, time.Now(), false)
		return err
	})
	if touchesURLBase(p) && err == nil {
		a.public.Invalidate()
	}
	return err //nolint:wrapcheck // Apply's authored errors and the authority refusals
}

func (a webServerAPI) guard(ctx context.Context) error {
	if a.d.Projects == nil {
		return nil
	}
	if err := a.d.Projects.Guard(ctx); err != nil {
		return fmt.Errorf("guard web server configuration: %w", err)
	}
	return nil
}

// ReverseProxy reads the stored settings under the guard, and whether the
// caller is currently an administrator, which decides how much of them the
// caller may see. A caller whose administration was revoked after its token
// was issued gets the reduced view.
func (a webServerAPI) ReverseProxy(ctx context.Context) (configapi.ReverseProxyView, error) {
	var view configapi.ReverseProxyView
	err := a.d.Tx.Tx(ctx, func(ctx context.Context) error {
		if err := a.guard(ctx); err != nil {
			return err
		}
		p, _ := kernel.FromContext(ctx)
		view.Admin = p.Admin
		if p.Admin {
			err := (exportAPI{d: a.d}).currentAdmin(ctx)
			switch {
			case errors.Is(err, kernel.ErrForbidden):
				view.Admin = false
			case err != nil:
				return err
			}
		}
		var err error
		view.Settings, view.Stored, err = readReverseProxy(ctx, a.d.Configs)
		return err
	})
	if err != nil {
		return configapi.ReverseProxyView{}, fmt.Errorf("read reverse proxy configuration: %w", err)
	}
	return view, nil
}

func readReverseProxy(ctx context.Context, configs configdoc.ConfigStore) (configapi.ReverseProxy, bool, error) {
	doc, err := configs.Get(ctx, configdoc.ConfigReverseProxy, reverseProxyKey)
	if errors.Is(err, kernel.ErrNotFound) {
		return configapi.ReverseProxy{}, false, nil
	}
	if err != nil {
		return configapi.ReverseProxy{}, false, fmt.Errorf("read %s: %w", SectionReverseProxies, err)
	}
	fields, err := publicDocument(doc)
	if err != nil {
		return configapi.ReverseProxy{}, false, err
	}
	rp, err := system.ReverseProxyFromDocument(fields)
	if err != nil {
		return configapi.ReverseProxy{}, false, fmt.Errorf("invalid stored %s: %w", SectionReverseProxies, kernel.ErrStore)
	}
	return rp, true, nil
}

func (a webServerAPI) NginxSnippet(ctx context.Context) (string, error) {
	if err := httpkit.AdminOnly(ctx); err != nil {
		return "", err //nolint:wrapcheck // the HTTP status of the refusal
	}
	var rp configapi.ReverseProxy
	var stored bool
	err := a.d.Tx.Tx(ctx, func(ctx context.Context) error {
		if err := a.guard(ctx); err != nil {
			return err
		}
		if err := (exportAPI{d: a.d}).currentAdmin(ctx); err != nil {
			return err
		}
		var err error
		rp, stored, err = readReverseProxy(ctx, a.d.Configs)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("read reverse proxy configuration: %w", err)
	}
	if !stored {
		return "", fmt.Errorf("%w: webServer: no reverse proxy configuration is stored", ErrInvalid)
	}
	return system.NginxSnippet(rp) //nolint:wrapcheck // names the field and ErrInvalid
}

// readWebServerExport adds the stored reverse proxy and urlBase to the
// export, in artifactory.xsd's ReverseProxyType order and enum spellings
// (nginx, apache, direct; subDomain, path, portPerRepo). Its required
// nillable elements are written even when empty; upStreamName, which
// artifactweave does not store, is one of them.
func readWebServerExport(ctx context.Context, d Deps, out *exportDocument) error {
	rp, stored, err := readReverseProxy(ctx, d.Configs)
	if err != nil {
		return fmt.Errorf("read export %s: %w", SectionReverseProxies, err)
	}
	if stored {
		methods := map[string]string{
			system.DockerSubdomain: "subDomain", system.DockerRepoPathPrefix: "path", system.DockerPortPerRepo: "portPerRepo",
		}
		out.ReverseProxies = []exportFields{{Fields: []exportField{
			{Name: "key", Text: rp.Key},
			{Name: "webServerType", Text: strings.ToLower(rp.WebServerType)},
			{Name: "artifactoryAppContext", Text: rp.ArtifactoryAppContext},
			{Name: "publicAppContext", Text: rp.PublicAppContext},
			{Name: "serverName", Text: rp.ServerName},
			{Name: "serverNameExpression", Text: rp.ServerNameExpression},
			{Name: "sslCertificate", Text: rp.SSLCertificate},
			{Name: "sslKey", Text: rp.SSLKey},
			{Name: "dockerReverseProxyMethod", Text: methods[rp.DockerReverseProxyMethod]},
			{Name: "useHttps", Text: strconv.FormatBool(rp.UseHTTPS)},
			{Name: "useHttp", Text: strconv.FormatBool(rp.UseHTTP)},
			{Name: "sslPort", Text: strconv.Itoa(rp.SSLPort)},
			{Name: "httpPort", Text: strconv.Itoa(rp.HTTPPort)},
			{Name: "artifactoryServerName", Text: rp.ArtifactoryServerName},
			{Name: "upStreamName"},
			{Name: "artifactoryPort", Text: strconv.Itoa(rp.ArtifactoryPort)},
		}}}
	}
	urlBase, ok, err := storedURLBase(ctx, d.Configs)
	if err != nil {
		return fmt.Errorf("read export: %w", err)
	}
	if ok {
		out.URLBase = &urlBase
	}
	federated, ok, err := configdoc.StoredScalar(ctx, d.Configs,
		federatedRepoURLBaseScalar.kind, federatedRepoURLBaseScalar.field)
	if err != nil {
		return fmt.Errorf("read export: descriptor: %w", err)
	}
	if ok {
		out.FederatedRepoURLBase = &federated
	}
	return nil
}
