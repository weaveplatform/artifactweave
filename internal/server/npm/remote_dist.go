package npm

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/authz"
	npmfmt "github.com/weaveplatform/artifactweave/internal/format/npm"
	"github.com/weaveplatform/artifactweave/internal/server/apierror"
	"github.com/weaveplatform/artifactweave/internal/service/item"
	"github.com/weaveplatform/artifactweave/pkg/repo"
	"github.com/weaveplatform/artifactweave/pkg/types"
)

type upstreamDist struct {
	fields    remoteDocument
	target    string
	checksums types.Checksums
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

func distChecksums(integrity, shasum string) (types.Checksums, error) {
	sums, err := item.ParseChecksums(types.Checksums{SHA1: shasum})
	if err != nil {
		return types.Checksums{}, remoteProtocolError()
	}
	if integrity == "" {
		return sums, nil
	}
	// npm currently emits SHA-512, while older metadata can contain SHA-1.
	// Preserve the original SRI string; pins validate decoded bytes without
	// replacing signature fields or manufacturing a local integrity statement.
	found := false
	for _, token := range strings.Fields(integrity) {
		alg, value, ok := strings.Cut(token, "-")
		if !ok {
			return types.Checksums{}, remoteProtocolError()
		}
		value, _, _ = strings.Cut(value, "?")
		raw, decodeErr := base64.StdEncoding.DecodeString(value)
		if decodeErr != nil {
			return types.Checksums{}, remoteProtocolError()
		}
		var field *string
		var size int
		switch alg {
		case "sha512":
			field, size = &sums.SHA512, 64
		case "sha256":
			field, size = &sums.SHA256, 32
		case "sha1":
			field, size = &sums.SHA1, 20
		default:
			continue
		}
		text := hex.EncodeToString(raw)
		if len(raw) != size || (*field != "" && *field != text) {
			return types.Checksums{}, remoteProtocolError()
		}
		*field = text
		found = true
	}
	if !found {
		return types.Checksums{}, remoteProtocolError()
	}
	return sums, nil
}

func remoteProtocolError() error {
	return apierror.New(http.StatusBadGateway, "invalid upstream npm metadata")
}

func remoteETag(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}
