package api

// Settings are the configuration options that the configuration module's
// code reads from the installation's configuration. The zero value is the
// default of each.
type Settings struct {
	// DockerSubdomainMatch is docker.subdomain_match. When it is set, the
	// reverse proxy configuration names the Docker subdomain method, in which
	// <repo>.<base URL host> addresses repo, and the server name expression
	// that matches those hosts; when it is not, it names the repository path
	// method.
	DockerSubdomainMatch bool
	// BaseURL is the installation's base_url. A urlBase stored through the
	// descriptor takes precedence over it while one is set, as
	// downloadRedirectConfig does over download.redirect.min_size: the
	// descriptor is the instance's own setting, which administrators change
	// at run time (decision 0009).
	BaseURL string
	// CDNHosts are the lower-cased hosts of the hot blob tier's CDN domains.
	// A urlBase whose host is one of them, or a parent of one, is refused for
	// the reason app/config refuses such a base_url: clients would send their
	// registry credentials to the CDN (decision 0029).
	CDNHosts []string
}
