package npmhttp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/artifacts/api/repo"
	npmfmt "github.com/weaveplatform/artifactweave/internal/artifacts/ecosystem/npm"
	"github.com/weaveplatform/artifactweave/internal/kernel"
	"github.com/weaveplatform/artifactweave/internal/platform/httpkit/apierror"
)

type upstreamDist struct {
	fields    remoteDocument
	target    string
	checksums kernel.Checksums
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

// remoteTarballURL resolves a packument's dist.tarball against the remote's
// URL, wherever it points: under the registry, elsewhere on its host, or on
// another host. 7.x fetches a tarball from where its metadata puts it: a
// registry whose tarballs lie outside its own root stopped answering 404 in
// 7.24.1 (RTFACT-23931), and 7.161.26's RemoteRepoBase lets a package
// handler's download URL replace the configured origin because "npm tarballs
// [come] from a CDN" (mayReplaceConfiguredOrigin), with only the outbound
// SSRF guard applied to it (HttpRepo.resolveAlternativeUrl). No repository
// setting gates it: externalDependencies* are a virtual repository's
// dependency rewrite (P10), not an npm remote's.
//
// What is refused here are safety rules, not 7.x answers: userinfo, a
// fragment, an opaque or non-HTTP(S) URL, a backslash, CR or LF, a dot
// segment left after resolution, and HTTP under an HTTPS registry. The
// transport then refuses private and metadata addresses an operator has not
// allowed, after DNS and every redirect, and sends the remote's credentials
// to another origin only under allowAnyHostAuth and HTTPS.
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
	return u.String(), nil
}

func remoteProtocolError() error {
	return apierror.New(http.StatusBadGateway, "invalid upstream npm metadata")
}

func remoteETag(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}
