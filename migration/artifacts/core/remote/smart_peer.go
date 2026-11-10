package remote

import (
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/migration/artifacts/api/repo"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/model"
	"github.com/weaveplatform/artifactweave/migration/artifacts/core/remote/source"
	"github.com/weaveplatform/artifactweave/migration/kernel"
)

// SmartPeer identifies only the configured peer. Parsing it does not negotiate
// capabilities or grant network authority; callers use source.Service.Read.
// Class and Version are filled only by authenticated discovery.
type SmartPeer struct {
	BaseURL    string
	Repository string
	Class      repo.Class
	Version    string
	revision   string
}

// Revision returns the credential revision the peer was discovered under,
// empty for a peer that ParseSmartPeer returned. Smart Remote refuses a peer
// whose revision is not the repository's current one with
// source.ErrSuperseded, so a credential change cannot reuse a discovery made
// with the old credentials.
func (p SmartPeer) Revision() string { return p.revision }

// WithRevision returns a copy of p marked as discovered under the
// credential revision r, which Smart's discovery calls. The revision is no
// field, so neither a SmartPeer literal nor ParseSmartPeer carries one.
func (p SmartPeer) WithRevision(r string) SmartPeer {
	p.revision = r
	return p
}

// ParseSmartPeer accepts repository endpoints, including the documented
// api/<package>/<repository> forms. Query-bearing and ambiguous endpoint paths
// cannot safely identify a different management API and are refused.
func ParseSmartPeer(c repo.Config) (SmartPeer, error) {
	u, err := CheckedURL(c.URL)
	if err != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.ContainsAny(
			u.Path,
			"%?#",
		) || strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") {
		return SmartPeer{}, source.ErrInvalidRequest
	}
	path := strings.Trim(u.Path, "/")
	if _, _, _, err = kernel.SplitItemPath(path); err != nil {
		return SmartPeer{}, source.ErrInvalidRequest
	}
	parts := smartRepositoryParts(c, strings.Split(path, "/"))
	key := parts[len(parts)-1]
	if strings.ContainsAny(key, ":;\\") || strings.HasSuffix(key, model.CacheSuffix) {
		return SmartPeer{}, source.ErrInvalidRequest
	}
	base := parts[:len(parts)-1]
	if len(base) >= 2 && base[len(base)-2] == "api" {
		base = base[:len(base)-2]
	}
	for _, part := range base {
		if part == "api" {
			return SmartPeer{}, source.ErrInvalidRequest
		}
	}
	u.Path, u.RawPath = "/"+strings.Join(base, "/"), ""
	return SmartPeer{BaseURL: strings.TrimRight(u.String(), "/"), Repository: key}, nil
}

// StorageURL constructs a peer storage endpoint without accepting another
// authority, repository, or a preescaped traversal. It never performs I/O.
func (p SmartPeer) StorageURL(path string, query url.Values) (string, error) {
	if strings.ContainsAny(path, "%\r\n?#") {
		return "", source.ErrInvalidRequest
	}
	if path != "" {
		if _, _, _, err := kernel.SplitItemPath(
			path,
		); err != nil ||
			path != strings.Trim(path, "/") {
			return "", source.ErrInvalidRequest
		}
	}
	u, err := CheckedURL(p.BaseURL)
	if err != nil || u.RawQuery != "" || u.ForceQuery || p.Repository == "" ||
		p.Repository == "." || p.Repository == ".." || strings.ContainsAny(p.Repository, "/\\%?#:;\r\n") {
		return "", source.ErrInvalidRequest
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/storage/" + p.Repository
	if path != "" {
		u.Path += "/" + path
	}
	u.RawPath, u.RawQuery = "", query.Encode()
	return u.String(), nil
}

// These protocol roots carry a suffix after the repository key. Match their
// complete known grammar; a bare trailing "simple" or "index.json" is a key.
func smartRepositoryParts(c repo.Config, parts []string) []string {
	n := len(parts)
	if c.PackageType == repo.NuGet && n >= 5 && parts[n-5] == "api" && parts[n-4] == "nuget" &&
		parts[n-3] == "v3" &&
		parts[n-1] == "index.json" {
		return append(append([]string(nil), parts[:n-5]...), "api", "nuget", parts[n-2])
	}
	if c.PackageType == repo.PyPI && n >= 4 && parts[n-4] == "api" && parts[n-3] == "pypi" &&
		parts[n-1] == "simple" {
		return parts[:n-1]
	}
	return parts
}
