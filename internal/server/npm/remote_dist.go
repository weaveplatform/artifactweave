package npm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/authz"
	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type upstreamDist struct {
	fields    remoteDocument
	target    string
	checksums types.Checksums
	integrity integritySpec
	variant   string
}

func remoteDist(
	c repo.Config,
	name, version string,
	manifest remoteDocument,
) (upstreamDist, error) {
	var d upstreamDist
	_, valid := npmfmt.CleanVersion(version)
	if !valid {
		return d, remoteProtocolError()
	}
	var gotName, gotVersion string
	if json.Unmarshal(manifest["name"], &gotName) != nil || gotName != name ||
		json.Unmarshal(manifest["version"], &gotVersion) != nil ||
		gotVersion != version {
		return d, remoteProtocolError()
	}
	if json.Unmarshal(manifest["dist"], &d.fields) != nil || d.fields == nil {
		return d, remoteProtocolError()
	}
	var tarball, integrity, shasum string
	if json.Unmarshal(d.fields["tarball"], &tarball) != nil || tarball == "" {
		return d, remoteProtocolError()
	}
	if raw, ok := d.fields["integrity"]; ok {
		if json.Unmarshal(raw, &integrity) != nil {
			return d, remoteProtocolError()
		}
	}
	if raw, ok := d.fields["shasum"]; ok {
		if json.Unmarshal(raw, &shasum) != nil {
			return d, remoteProtocolError()
		}
	}
	var err error
	d.target, err = remoteTarballURL(c, tarball)
	if err != nil {
		return d, err
	}
	d.checksums, err = distChecksums(integrity, shasum)
	if err != nil {
		return d, err
	}
	d.integrity, err = parseIntegrity(integrity)
	d.variant = "npm-tarball-v1:" + integrity
	return d, err
}

func remoteTarballURL(c repo.Config, raw string) (string, error) {
	base, err := url.Parse(strings.TrimRight(c.URL, "/") + "/")
	if err != nil {
		return "", remoteProtocolError()
	}
	ref, err := url.Parse(raw)
	if err != nil || ref.User != nil || ref.Fragment != "" || ref.Opaque != "" ||
		strings.ContainsAny(raw, "\\\r\n") {
		return "", remoteProtocolError()
	}
	u := base.ResolveReference(ref)
	if u.User != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") ||
		(base.Scheme == "https" && u.Scheme != "https") {
		return "", remoteProtocolError()
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || strings.ContainsAny(segment, "\\\x00") {
			return "", remoteProtocolError()
		}
	}
	// In-base URLs are registry artifacts. Other URLs require an administrator's
	// explicit opt-in and allow pattern; the transport still enforces SSRF and
	// origin-bound credentials after DNS and every redirect.
	internal := strings.EqualFold(u.Host, base.Host) && u.Scheme == base.Scheme &&
		strings.HasPrefix(u.Path, base.Path)
	if !internal {
		matched := false
		if c.ExternalDependenciesEnabled != nil && *c.ExternalDependenciesEnabled {
			for _, pattern := range c.ExternalDependenciesPatterns {
				if pattern != "" && authz.CompilePattern(pattern).Match(u.String()) {
					matched = true
					break
				}
			}
		}
		if !matched {
			return "", apierror.New(http.StatusBadGateway, "upstream package URL refused")
		}
	}
	return u.String(), nil
}

func remoteProtocolError() error {
	return apierror.New(http.StatusBadGateway, "invalid upstream npm metadata")
}

func remoteETag(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}
