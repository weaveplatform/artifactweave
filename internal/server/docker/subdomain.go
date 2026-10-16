package docker

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/weaveplatform/artifactweave/internal/server/baseurl"
	"github.com/weaveplatform/artifactweave/pkg/repo"
)

// subdomain returns the repository a request's host names under the
// subdomain method: the host the client addressed is <label>.<parent> and
// <label> names a Docker, OCI or Helm OCI repository. ok is false when
// subdomain matching is off or the host names none, and the request is
// then read by repository path.
//
// The host decides, as Artifactory's reverse proxy decides by server name:
// on a repository's subdomain every /v2 path names an image in that
// repository, even one whose first segment is another repository's key.
// The parent itself never matches, so the two methods share no host.
func (g *Registry) subdomain(r *http.Request) (repo.Config, bool, error) {
	if g.parent == "" {
		return repo.Config{}, false, nil
	}
	label, ok := strings.CutSuffix(addressedHost(r), "."+g.parent)
	if !ok {
		return repo.Config{}, false, nil
	}
	return g.lookup(r.Context(), label)
}

// addressedHost is the host the client named, lower-cased and without its
// port: the first X-Forwarded-Host entry, which a proxy chain appends to,
// else Host (decision 0003). The configured base URL is no evidence of it:
// it names the parent.
func addressedHost(r *http.Request) string {
	host := baseurl.ForwardedHost(r)
	if host == "" {
		host = r.Host
	}
	return stripPort(strings.ToLower(strings.TrimSpace(host)))
}

// baseHost is the lower-cased host of a base URL without its port, or ""
// when it has none.
func baseHost(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return stripPort(strings.ToLower(u.Host))
}

func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
