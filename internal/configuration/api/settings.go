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
}
